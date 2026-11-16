package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// testStartBound is the start bound these tests give their handlers, short so
// the no-client fallback is seen quickly and long enough that a dial and a
// resize land inside it on a loaded runner.
const testStartBound = 750 * time.Millisecond

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

// sizeReporterManager serves a manager of sizeReporterCmd sessions on a mux
// with the whole session API mounted.
func sizeReporterManager(t *testing.T) (*SessionManager, *httptest.Server) {
	t.Helper()
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler(sizeReporterCmd, WithLogger(nil), WithWorkDir("/"), withStartBound(testStartBound))
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
	m, srv := sizeReporterManager(t)
	id := restCreate(t, srv)
	ws := dialSession(t, srv, id)
	t.Cleanup(func() { _ = ws.CloseNow() })
	h := sessionHandler(t, m, id)
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	awaitMainScreenText(t, h, "size=20 50")
}

// TestCreateREST_spawnsAtTheFirstResizeAfterALayoutChange pins that the size a
// socket first announces is the size the process starts at, and that a later
// change (a split, a snap, a window resize) reaches it as a resize.
func TestCreateREST_spawnsAtTheFirstResizeAfterALayoutChange(t *testing.T) {
	m, srv := sizeReporterManager(t)
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
	m, srv := sizeReporterManager(t)
	id := restCreate(t, srv)
	awaitMainScreenText(t, sessionHandler(t, m, id), "size=24 80")
}

func TestCreateREST_inputBeforeTheSpawnIsWrittenAfterItInOrder(t *testing.T) {
	m, srv := sizeReporterManager(t)
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

// bareSizeReporter serves one sizeReporterCmd handler with no manager, as a
// host serving a single terminal does, and dials it.
func bareSizeReporter(t *testing.T, cmd []string) (*Handler, *httptest.Server) {
	t.Helper()
	h := NewHandler(cmd, WithLogger(nil), WithWorkDir("/"), withStartBound(testStartBound))
	shutdownOnCleanup(t, h)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return h, srv
}

func TestHandler_inputBeforeTheFirstResizeWaitsForIt(t *testing.T) {
	h, srv := bareSizeReporter(t, sizeReporterCmd)
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	writeBinary(t, ws, []byte("one\n"))
	assertUnspawnedAfter(t, h, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 50, "rows": 20})
	awaitInOrder(t, h, "size=20 50", "got=one")
}

func TestHandler_attachedWithoutAResizeSpawnsAt80x24(t *testing.T) {
	h, srv := bareSizeReporter(t, sizeReporterCmd)
	dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	awaitMainScreenText(t, h, "size=24 80")
}

func TestHandler_closedBeforeTheSpawnSpawnsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	h, srv := bareSizeReporter(t, markerCmd(marker))
	ws := dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
	writeBinary(t, ws, []byte("x"))
	awaitHandled(t, ws)
	h.Close()
	assertNeverSpawned(t, marker)
}
