package terminal

// gremlins_kill_vterm_u9_test.go — added by mutant-killing unit vterm-u9.
// TEST FILES ONLY; no production code is modified. Each test documents the
// exact surviving gremlins mutant(s) it kills and why the asserted value
// depends on the precise operator at that source location. Every new
// identifier is prefixed gk_vterm_u9_ / TestGkVtermU9_ so it never collides
// with a sibling unit sharing the `terminal` package.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// gk_vterm_u9_mustJSON marshals v to JSON for use as a handleControl payload.
func gk_vterm_u9_mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("gk_vterm_u9_mustJSON: %v", err)
	}
	return b
}

// gk_vterm_u9_serverConn stands up a throwaway WebSocket server, dials it,
// and returns the SERVER-side *websocket.Conn so a test can hand a real,
// non-nil connection to handleControl/handleResume without spinning up the
// full Handler.handleWS read loop. The server goroutine drains client reads
// so cleanup unblocks the httptest server. Mirrors the accept/dial pattern
// already used by other tests in this package.
func gk_vterm_u9_serverConn(t *testing.T) (*websocket.Conn, func()) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ch <- c
		for {
			if _, _, rerr := c.Read(r.Context()); rerr != nil {
				return
			}
		}
	}))
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	//nolint:bodyclose // coder/websocket Dial nils resp.Body on success
	client, _, err := websocket.Dial(dctx, wsURL, nil)
	dcancel()
	if err != nil {
		srv.Close()
		t.Fatalf("gk_vterm_u9_serverConn dial: %v", err)
	}
	var server *websocket.Conn
	select {
	case server = <-ch:
	case <-time.After(3 * time.Second):
		_ = client.Close(websocket.StatusNormalClosure, "")
		srv.Close()
		t.Fatalf("gk_vterm_u9_serverConn: server side never accepted")
	}
	cleanup := func() {
		_ = client.Close(websocket.StatusNormalClosure, "")
		_ = server.CloseNow()
		srv.Close()
	}
	return server, cleanup
}

// --- wire_binary.go --------------------------------------------------------

// Kills wire_binary.go:113:10 CONDITIONALS_BOUNDARY.
// Line: `if idx >= 0 && idx < len(rows) {` in encodeScreenMsg — the per-row
// bounds check. The `>=` at column 10 decides whether a changed row's run
// payload is encoded. For changed row index 0 against a row that has one run,
// the original `idx >= 0` is true (0 >= 0) so the run's text is appended; the
// boundary mutant `idx > 0` is false (0 > 0) so the else branch writes
// num_runs=0 and the run text is absent from the frame. The header bytes for
// this call are all 0/1/3, so the run text can only appear via appendRowRuns.
func TestGkVtermU9_EncodeScreenMsg_zeroRowIndexEncodesRunPayload(t *testing.T) {
	run := vt.WireRun{T: "gk_u9_runtext", F: -1, B: -1, Uc: -1}
	rows := [][]vt.WireRun{{run}}
	buf := encodeScreenMsg(3, 0, 0, 0, []int{0}, rows, 0, false, false, false)

	if !bytes.Contains(buf, []byte("gk_u9_runtext")) {
		t.Errorf("encodeScreenMsg(changed=[0]): row-0 run text missing; idx==0 must satisfy `idx >= 0` so rows[0] is appended (boundary `>=`->`>` would drop it)")
	}
}

// --- terminal.go: buildFrame ----------------------------------------------

// Kills terminal.go:355:44 CONDITIONALS_NEGATION.
// Line: `if frame != nil && len(frame.scrollLines) > 0 {` in buildFrame — gates
// whether drained scroll lines are appended to the scrollback ring. With a
// frame carrying real scroll lines the original `> 0` is true so the ring is
// populated; the negation `<= 0` is false so the ring stays empty.
// (The companion BOUNDARY `>=` mutant is equivalent: it differs only when the
// count is 0, where Append over an empty slice is a no-op.)
func TestGkVtermU9_BuildFrame_appendsScrollLinesToRing(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	// Tiny screen + many newlines forces lines to scroll off into the drain.
	h.screen = vt.New(3, 20)
	for range 20 {
		if _, err := h.screen.Write([]byte("gk_vterm_u9 scroll line\r\n")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
	}
	h.resized = true

	frame := h.buildFrame()
	if frame == nil {
		t.Fatalf("buildFrame returned nil; precondition failed (expected a frame with scroll lines)")
	}
	if len(frame.scrollLines) == 0 {
		t.Fatalf("precondition failed: frame has no scroll lines to append")
	}

	if got := len(h.scrollback.Lines()); got == 0 {
		t.Errorf("buildFrame appended %d scrollback lines, want > 0 (negation `<= 0` would skip Append)", got)
	}
}

// --- terminal.go: handleControl -------------------------------------------

// Kills terminal.go:504:45 CONDITIONALS_NEGATION and terminal.go:511:12
// CONDITIONALS_NEGATION.
//
//	504: `if err := json.Unmarshal(payload, &c); err != nil { return }` — with
//	     VALID json the original `err != nil` is false so dispatch proceeds; the
//	     mutant `err == nil` returns early, so the resize never runs.
//	511: `if c.Type == ctlTypeResize {` — a resize message must dispatch
//	     handleResize (which starts the child process); the negation `!=` would
//	     skip it for a genuine resize.
//
// A well-formed resize control message must start the process and size the
// screen. Either mutant leaves the process unstarted. The resize path touches
// neither ws nor state, so a nil ws is safe here.
func TestGkVtermU9_HandleControl_resizeStartsProcess(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	payload := gk_vterm_u9_mustJSON(t, controlMsg{Type: ctlTypeResize, Cols: 100, Rows: 40})
	h.handleControl(nil, &ClientState{}, payload)

	if !h.started.Load() {
		t.Fatalf("handleControl(valid resize): process not started; valid JSON must fall through and a resize must dispatch handleResize")
	}
	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 100 {
		t.Errorf("handleControl(resize cols=100): screen.Width = %d, want 100", w)
	}
}

// Also kills terminal.go:511:12 CONDITIONALS_NEGATION (false-branch side).
// A control message whose type is neither resume nor resize must NOT start the
// process. The negation `c.Type != ctlTypeResize` would treat this unknown
// type as a resize and call handleResize, starting the process. SessionID is
// empty, so the resume branch is never taken and the nil ws is never touched.
func TestGkVtermU9_HandleControl_unknownTypeDoesNotStart(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	payload := gk_vterm_u9_mustJSON(t, controlMsg{Type: "gk_vterm_u9_bogus"})
	h.handleControl(nil, &ClientState{}, payload)

	if h.started.Load() {
		t.Errorf("handleControl(unknown type): process started; only `c.Type == ctlTypeResize` may start it")
	}
}

// Kills terminal.go:507:12 and terminal.go:507:44 CONDITIONALS_NEGATION.
//
//	507:12: `c.Type == ctlTypeResume` — a resume message with a session id must
//	        dispatch handleResume; the negation `!=` makes a real resume fall
//	        through without resolving the session.
//	507:44: `c.SessionID != ""` — the same dispatch requires a non-empty id;
//	        the negation `== ""` makes a resume carrying a real id fall through.
//
// handleResume's first action is registry.ResolveSession, which stores the
// session on the ClientState and registers it in the registry — observable
// without reading the WebSocket. (This also covers 504:45: an early return on
// valid JSON would skip the resolve.)
func TestGkVtermU9_HandleControl_resumeResolvesSession(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()
	ws, cleanup := gk_vterm_u9_serverConn(t)
	defer cleanup()

	state := &ClientState{}
	payload := gk_vterm_u9_mustJSON(t, controlMsg{Type: ctlTypeResume, SessionID: "gk_vterm_u9_sid"})
	h.handleControl(ws, state, payload)

	if state.session.Load() == nil {
		t.Errorf("handleControl(resume): ClientState.session is nil; a resume with a non-empty id must dispatch handleResume->ResolveSession")
	}
	h.registry.mu.Lock()
	_, ok := h.registry.sessions["gk_vterm_u9_sid"]
	h.registry.mu.Unlock()
	if !ok {
		t.Errorf("handleControl(resume): registry has no session %q; resume was not dispatched", "gk_vterm_u9_sid")
	}
}

// --- terminal.go: handleResize --------------------------------------------

// Kills terminal.go:585:10 CONDITIONALS_NEGATION.
// Line: `if cols < minResizeCols { cols = minResizeCols }` (minResizeCols=20).
// cols=100 is comfortably above the floor: the original `100 < 20` is false so
// 100 is kept and the screen ends up 100 wide. The negation `cols >= 20` is
// true -> cols floored to 20. (The companion BOUNDARY `<=` is equivalent: it
// only differs at cols==20, where the floor sets cols back to 20.)
func TestGkVtermU9_HandleResize_colsAboveMinNotFloored(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 100 {
		t.Errorf("handleResize(cols=100): screen.Width = %d, want 100 (100 is above minResizeCols=20, must not be floored)", w)
	}
}

// Kills terminal.go:588:10 CONDITIONALS_NEGATION.
// Line: `if rows < minResizeRows { rows = minResizeRows }` (minResizeRows=5).
// rows=40 is above the floor: original `40 < 5` is false -> kept -> screen 40
// tall. The negation `rows >= 5` is true -> rows floored to 5. (BOUNDARY `<=`
// is equivalent: differs only at rows==5 where the floor re-sets it to 5.)
func TestGkVtermU9_HandleResize_rowsAboveMinNotFloored(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	ht := h.screen.Height
	h.mu.Unlock()
	if ht != 40 {
		t.Errorf("handleResize(rows=40): screen.Height = %d, want 40 (40 is above minResizeRows=5, must not be floored)", ht)
	}
}

// Kills terminal.go:594:46 CONDITIONALS_NEGATION.
// Line: `if err := h.ensureStarted(cols, rows); err != nil { ...; return }`.
// On a fresh handler ensureStarted SUCCEEDS (err == nil): the original
// `err != nil` is false so handleResize continues into the post-start work —
// including h.screen.HoldFlush(now+1s), which makes IsFlushHeld() true. The
// negation `err == nil` is true on success, so it logs and returns early and
// HoldFlush never runs, leaving IsFlushHeld() false. ensureStarted has already
// sized the screen by then, so the flush-hold — not the dimensions — is the
// discriminating observable.
func TestGkVtermU9_HandleResize_holdsFlushOnSuccessfulStart(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	held := h.screen.IsFlushHeld()
	h.mu.Unlock()
	if !held {
		t.Errorf("handleResize: IsFlushHeld()=false after a successful start; the post-ensureStarted path (HoldFlush) must run when err==nil (`err != nil` false)")
	}
}
