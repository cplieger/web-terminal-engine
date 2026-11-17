package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// testStartBound is the start bound of the tests that wait out the fallback,
// short so it is seen quickly. A test whose resize must win passes noFallback,
// so a stalled runner cannot let the fallback fire first.
const (
	testStartBound = 750 * time.Millisecond
	noFallback     = time.Hour
)

// withStartBound sets the deferred start's bound.
func withStartBound(d time.Duration) Option {
	return func(c *handlerConfig) { c.startBound = d }
}

// sizeReporterCmd prints the size its pty had at start, in stty's
// rows-then-columns order, then echoes each input line as got=<line>.
var sizeReporterCmd = []string{
	"/bin/sh", "-c",
	`stty -echo; printf 'size=%s\n' "$(stty size)"; while read -r l; do printf 'got=%s\n' "$l"; done`,
}

// sizeReporterManager serves a manager of sizeReporterCmd sessions, each with
// start bound bound, on a mux with the whole session API mounted.
func sizeReporterManager(t *testing.T, bound time.Duration) (*SessionManager, *httptest.Server) {
	t.Helper()
	return commandManager(t, sizeReporterCmd, bound)
}

// commandManager serves a manager of cmd sessions, each with start bound bound,
// on a mux with the whole session API mounted.
func commandManager(t *testing.T, cmd []string, bound time.Duration) (*SessionManager, *httptest.Server) {
	t.Helper()
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler(cmd, WithLogger(nil), WithWorkDir("/"), withStartBound(bound))
	})
	t.Cleanup(func() { shutdownManager(t, m) })
	mux := http.NewServeMux()
	m.MountAPI(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv
}

// restCreate POSTs a bodiless create and returns the new session's id.
func restCreate(t *testing.T, srv *httptest.Server) SessionID {
	t.Helper()
	resp, err := http.Post(srv.URL+SessionsPath, "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST %s: %v", SessionsPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s status = %d, want 201", SessionsPath, resp.StatusCode)
	}
	var info SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return info.ID
}

// sessionHandler returns the live handler of session id.
func sessionHandler(t *testing.T, m *SessionManager, id SessionID) *Handler {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		t.Fatalf("session %s is not live", LogID(id))
	}
	return s.handler
}

// awaitInOrder polls h's main screen until it shows every want, each below the
// one before it, and fails with the screen's rows if it never does.
func awaitInOrder(t *testing.T, h *Handler, want ...string) {
	t.Helper()
	deadline := time.Now().Add(waitPatience)
	var text string
	for {
		h.mu.Lock()
		rows, _ := h.screen.MainScreenText()
		h.mu.Unlock()
		text = strings.Join(rows, "\n")
		rest, ok := text, true
		for _, w := range want {
			i := strings.Index(rest, w)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(w):]
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("main screen never showed %q in order within %v; rows:\n%s", want, waitPatience, text)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitHandled returns once the server has handled every frame sent on ws so
// far: a socket's frames are handled in order, so the pong to a ping sent last
// proves the earlier ones were read.
func awaitHandled(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	sendControl(t, ws, map[string]any{"type": ctlTypePing})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("no pong to a ping: %v", err)
		}
		if len(msg) > 0 && msg[0] == wireMsgPong {
			return
		}
	}
}

// assertUnspawnedAfter fails if h's process has started once the server has
// handled every frame sent on ws so far.
func assertUnspawnedAfter(t *testing.T, h *Handler, ws *websocket.Conn) {
	t.Helper()
	awaitHandled(t, ws)
	if h.started.Load() {
		t.Fatal("the process spawned before its first client's resize")
	}
}

// markerCmd records that its process ran by creating path, then stays alive.
func markerCmd(path string) []string {
	return []string{"/bin/sh", "-c", `: > "$1"; exec sleep 60`, "sh", path}
}

// assertNeverSpawned fails if path appears within twice the start bound.
func assertNeverSpawned(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * testStartBound)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			t.Fatal("the process spawned after its session was closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCreateREST_spawnsAtTheFirstClientResize(t *testing.T) {
	m, srv := sizeReporterManager(t, noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	awaitMainScreenText(t, h, "size=20 50")
}

// TestCreateREST_clampsAnOutOfRangeFirstResize pins that the size a process
// starts at is bounded as a resize is: 20 to 1000 columns, 5 to 1000 rows.
func TestCreateREST_clampsAnOutOfRangeFirstResize(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cols, rows int
		want       string
	}{
		{name: "below the floor", cols: 5, rows: 2, want: "size=5 20"},
		{name: "columns over the cap", cols: 1500, rows: 10, want: "size=10 1000"},
		{name: "rows over the cap", cols: 30, rows: 1500, want: "size=1000 30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := sizeReporterManager(t, noFallback)
			id := restCreate(t, srv)
			ws := dialSession(t, srv, id)
			t.Cleanup(func() { _ = ws.CloseNow() })
			sendControl(t, ws, map[string]any{"type": "resize", "cols": tc.cols, "rows": tc.rows})
			awaitMainScreenText(t, sessionHandler(t, m, id), tc.want)
		})
	}
}

// TestCreateREST_spawnsAtTheFirstResizeAfterALayoutChange pins that the size a
// socket first announces is the size the process starts at, and that a later
// change (a split, a snap, a window resize) reaches it as a resize.
func TestCreateREST_spawnsAtTheFirstResizeAfterALayoutChange(t *testing.T) {
	m, srv := sizeReporterManager(t, noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 59, "rows": 31})
	awaitMainScreenText(t, h, "size=31 59")
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 122, "rows": 31})
	awaitHandled(t, ws)
	ws2, err := pty.GetsizeFull(h.ptmx)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if ws2.Cols != 122 || ws2.Rows != 31 {
		t.Errorf("pty after the second resize = %dx%d, want 122x31", ws2.Cols, ws2.Rows)
	}
}

// TestCreateREST_withoutAClientSpawnsAt80x24 pins the fallback for a create no
// client attaches to: an API create, a smoke test's bare POST.
func TestCreateREST_withoutAClientSpawnsAt80x24(t *testing.T) {
	m, srv := sizeReporterManager(t, testStartBound)
	id := restCreate(t, srv)
	awaitMainScreenText(t, sessionHandler(t, m, id), "size=24 80")
}

func TestCreateREST_inputBeforeTheSpawnIsWrittenAfterItInOrder(t *testing.T) {
	m, srv := sizeReporterManager(t, noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	writeBinary(t, ws, []byte("one\n"))
	writeBinary(t, ws, []byte("two\n"))
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	writeBinary(t, ws, []byte("three\n"))
	awaitInOrder(t, h, "size=20 50", "got=one", "got=two", "got=three")
}

func TestCreateREST_closedBeforeTheSpawnSpawnsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler(markerCmd(marker), WithLogger(nil), WithWorkDir("/"), withStartBound(testStartBound))
	})
	t.Cleanup(func() { shutdownManager(t, m) })
	mux := http.NewServeMux()
	m.MountAPI(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	id := restCreate(t, srv)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, srv.URL+SessionsSubtreePath+string(id), http.NoBody)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("DELETE status = %d, want 2xx", resp.StatusCode)
	}
	assertNeverSpawned(t, marker)
}

// TestCreate_spawnsAtOnceAt80x24 pins the Go create a host makes with no client
// (a restored session): the process exists when Create returns.
func TestCreate_spawnsAtOnceAt80x24(t *testing.T) {
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler(sizeReporterCmd, WithLogger(nil), WithWorkDir("/"), withStartBound(time.Hour))
	})
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create() = %v, want nil", err)
	}
	h := sessionHandler(t, m, id)
	if !h.started.Load() {
		t.Fatal("Create returned before the process started")
	}
	awaitMainScreenText(t, h, "size=24 80")
}

// bareSizeReporter serves one handler of cmd with start bound bound and no
// manager, as a host serving a single terminal does.
func bareSizeReporter(t *testing.T, cmd []string, bound time.Duration) (*Handler, *httptest.Server) {
	t.Helper()
	h := NewHandler(cmd, WithLogger(nil), WithWorkDir("/"), withStartBound(bound))
	shutdownOnCleanup(t, h)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

func TestHandler_inputBeforeTheFirstResizeWaitsForIt(t *testing.T) {
	h, srv := bareSizeReporter(t, sizeReporterCmd, noFallback)
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	writeBinary(t, ws, []byte("one\n"))
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	awaitInOrder(t, h, "size=20 50", "got=one")
}

func TestHandler_attachedWithoutAResizeSpawnsAt80x24(t *testing.T) {
	h, srv := bareSizeReporter(t, sizeReporterCmd, testStartBound)
	dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	awaitMainScreenText(t, h, "size=24 80")
}

func TestHandler_closedBeforeTheSpawnSpawnsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	h, srv := bareSizeReporter(t, markerCmd(marker), testStartBound)
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	writeBinary(t, ws, []byte("x"))
	awaitHandled(t, ws)
	h.Close()
	assertNeverSpawned(t, marker)
}

// missingCmd names no executable, so its process cannot start.
var missingCmd = []string{"/nonexistent/cmd"}

// numberedLinesCmd prints its start size, then got=<n> for each input line
// <n>:<padding>. The leading newline ends the echo of the input the tty
// received before stty -echo ran, so the size line starts a row of its own.
var numberedLinesCmd = []string{
	"/bin/sh", "-c",
	`stty -echo; printf '\nsize=%s\n' "$(stty size)"; while read -r l; do printf 'got=%s\n' "${l%%:*}"; done`,
}

// queueCapLines is the number of numbered 2 KiB lines that fill the 64 KiB
// queue exactly.
const queueCapLines = 32

// writeNumberedLines sends n frames of 2 KiB, each one numbered line, and
// returns the got= lines numberedLinesCmd prints for them.
func writeNumberedLines(t *testing.T, ws *websocket.Conn, n int) []string {
	t.Helper()
	var got []string
	for i := 1; i <= n; i++ {
		head := strconv.Itoa(i) + ":"
		writeBinary(t, ws, []byte(head+strings.Repeat("a", 2048-len(head)-1)+"\n"))
		got = append(got, "got="+strconv.Itoa(i))
	}
	return got
}

// awaitClose reads ws until the server closes it and returns the close code
// and reason.
func awaitClose(t *testing.T, ws *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitPatience)
	defer cancel()
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("the socket ended without a close frame: %v", err)
		}
		return ce.Code, ce.Reason
	}
}

// assertEndedWith fails unless ws is closed as a session that ended, 4001,
// with reason.
func assertEndedWith(t *testing.T, ws *websocket.Conn, reason string) {
	t.Helper()
	code, got := awaitClose(t, ws)
	if code != statusProcessExited || got != reason {
		t.Errorf("close = %d %q, want %d %q", code, got, statusProcessExited, reason)
	}
}

// awaitUnlisted fails unless session id leaves m's list within waitPatience.
func awaitUnlisted(t *testing.T, m *SessionManager, id SessionID) {
	t.Helper()
	deadline := time.Now().Add(waitPatience)
	for {
		listed := slices.ContainsFunc(m.List(), func(s SessionInfo) bool { return s.ID == id })
		if !listed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s still listed after %v", LogID(id), waitPatience)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCreateREST_aFailedStartEndsTheSession(t *testing.T) {
	m, srv := commandManager(t, missingCmd, noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	assertEndedWith(t, ws, startFailedReason)
	awaitUnlisted(t, m, id)
}

func TestCreateREST_aFailedFallbackStartEndsTheSession(t *testing.T) {
	m, srv := commandManager(t, missingCmd, testStartBound)
	id := restCreate(t, srv)
	awaitUnlisted(t, m, id)
}

func TestCreateREST_inputUpToTheQueueCapWaitsForTheResize(t *testing.T) {
	m, srv := commandManager(t, numberedLinesCmd, noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	got := writeNumberedLines(t, ws, queueCapLines)
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 80, "rows": 50})
	awaitInOrder(t, h, append([]string{"size=50 80"}, got...)...)
}

func TestCreateREST_inputPastTheQueueCapEndsTheSession(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	m, srv := commandManager(t, markerCmd(marker), noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	writeNumberedLines(t, ws, queueCapLines)
	assertUnspawnedAfter(t, h, ws)
	writeBinary(t, ws, []byte("x"))
	assertEndedWith(t, ws, inputOverflowReason)
	awaitUnlisted(t, m, id)
	assertNeverSpawned(t, marker)
}

// removalWire returns session id's removed event from the next sweep as the
// status stream's JSON carries it.
func removalWire(t *testing.T, m *SessionManager, id SessionID) map[string]any {
	t.Helper()
	ev := eventFor(t, m.diffStatuses(), id)
	b, err := json.Marshal(&ev)
	if err != nil {
		t.Fatalf("marshal the status event: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("unmarshal the status event: %v", err)
	}
	if wire["removed"] != true {
		t.Fatalf("event for session %s = %s, want a removal", LogID(id), b)
	}
	return wire
}

// TestCreateREST_aFailedStartsRemovalCarriesTheReason pins the removal as a
// second source of the end, beside the 4001 close: a client that sees only one
// of the two learns why the session ended, including when it ended before any
// sweep had announced it.
func TestCreateREST_aFailedStartsRemovalCarriesTheReason(t *testing.T) {
	cases := []struct {
		name       string
		sweptFirst bool
	}{
		{name: "swept before the end", sweptFirst: true},
		{name: "ended before any sweep", sweptFirst: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := commandManager(t, missingCmd, noFallback)
			m.stopSweep()
			id := restCreate(t, srv)
			if tc.sweptFirst {
				eventFor(t, m.diffStatuses(), id)
			}
			ws := dialSession(t, srv, id)
			t.Cleanup(func() { _ = ws.CloseNow() })
			sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
			assertEndedWith(t, ws, startFailedReason)
			awaitUnlisted(t, m, id)
			if got := removalWire(t, m, id)["endReason"]; got != startFailedReason {
				t.Errorf("removal endReason = %v, want %q", got, startFailedReason)
			}
		})
	}
}

func TestCreateREST_anOverflowsRemovalCarriesTheReason(t *testing.T) {
	m, srv := commandManager(t, markerCmd(filepath.Join(t.TempDir(), "spawned")), noFallback)
	m.stopSweep()
	id := restCreate(t, srv)
	eventFor(t, m.diffStatuses(), id)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	writeNumberedLines(t, ws, queueCapLines)
	writeBinary(t, ws, []byte("x"))
	assertEndedWith(t, ws, inputOverflowReason)
	awaitUnlisted(t, m, id)
	if got := removalWire(t, m, id)["endReason"]; got != inputOverflowReason {
		t.Errorf("removal endReason = %v, want %q", got, inputOverflowReason)
	}
}

func TestRemovedEventOfAClosedSessionCarriesNoEndReason(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	eventFor(t, m.diffStatuses(), id)
	if !m.Close(id) {
		t.Fatalf("Close(%s) = false, want true", LogID(id))
	}
	if got, has := removalWire(t, m, id)["endReason"]; has {
		t.Errorf("removal endReason = %v, want absent", got)
	}
}

// TestHandler_aFailedStartEndsTheHandler pins a handler served on its own: the
// start error reaches the exit callback a host replaces a spent handler on, and
// input after the end is refused rather than queued.
func TestHandler_aFailedStartEndsTheHandler(t *testing.T) {
	exited := make(chan error, 1)
	h := NewHandler(missingCmd, WithLogger(nil), WithWorkDir("/"), withStartBound(noFallback),
		WithOnProcessExit(func(err error) { exited <- err }))
	shutdownOnCleanup(t, h)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	ws := dialWS(t, wsURL)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	assertEndedWith(t, ws, startFailedReason)
	select {
	case err := <-exited:
		if err == nil {
			t.Error("the exit callback got a nil error for a process that never started")
		}
	case <-time.After(waitPatience):
		t.Fatal("the exit callback never ran")
	}
	if !h.Exited() {
		t.Error("Exited() = false after the start failed")
	}

	again := dialWS(t, wsURL)
	writeBinary(t, again, bytes.Repeat([]byte("a"), 60*1024))
	assertEndedWith(t, again, startFailedReason)
	h.mu.Lock()
	queued := h.pendingInput.Len()
	h.mu.Unlock()
	if queued != 0 {
		t.Errorf("%d bytes queued after the session ended, want 0", queued)
	}
}

// TestCreateREST_aStartDuringTheOverflowIsRefused pins the overflow's latch
// against a start that waits on h.mu while the over-cap input is appended, as
// the fallback timer or a second client's first resize does.
func TestCreateREST_aStartDuringTheOverflowIsRefused(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	m, srv := commandManager(t, markerCmd(marker), noFallback)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	writeNumberedLines(t, ws, queueCapLines)
	assertUnspawnedAfter(t, h, ws)
	startErr := make(chan error, 1)
	inputOverflowHook = func() {
		done := make(chan error, 1)
		go func() { done <- h.start(50, 20) }()
		for !h.started.Load() {
			select {
			case err := <-done:
				startErr <- err
				return
			case <-time.After(time.Millisecond):
			}
		}
		startErr <- nil
	}
	t.Cleanup(func() { inputOverflowHook = func() {} })
	writeBinary(t, ws, []byte("x"))
	assertEndedWith(t, ws, inputOverflowReason)
	if err := <-startErr; !errors.Is(err, errHandlerClosed) {
		t.Errorf("the start during the overflow returned %v, want errHandlerClosed", err)
	}
	awaitUnlisted(t, m, id)
	assertNeverSpawned(t, marker)
}
