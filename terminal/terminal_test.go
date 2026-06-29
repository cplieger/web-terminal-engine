package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// dialHandler stands up the handler on an httptest server and opens
// a WebSocket client against it. Returns the open connection and a
// cleanup func. Uses /bin/cat so tests don't depend on dtach being
// installed in the workspace.
func dialHandler(t *testing.T, cmd []string) (*websocket.Conn, func()) {
	t.Helper()
	h := NewHandler(cmd, WithWorkDir("/"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	// coder/websocket's Dial nils out resp.Body on success; its
	// godoc is explicit: "You never need to close resp.Body
	// yourself." The bodyclose linter is stdlib-oriented and
	// doesn't know about that contract.
	//
	//nolint:bodyclose // library contract: Body is nil on success
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	cancel()
	if err != nil {
		srv.Close()
		t.Fatalf("ws dial: %v", err)
	}
	cleanup := func() {
		_ = ws.Close(websocket.StatusNormalClosure, "")
		srv.Close()
	}
	return ws, cleanup
}

// readUntil reads WS frames until the accumulated bytes contain
// want, or the timeout fires. Returns the concatenated bytes seen so
// far on timeout to aid debugging.
func readUntil(t *testing.T, ws *websocket.Conn, want []byte, timeout time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var got bytes.Buffer
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got so far: %q)", err, got.Bytes())
		}
		got.Write(msg)
		if bytes.Contains(got.Bytes(), want) {
			return got.Bytes()
		}
	}
}

// sendControl writes a 0x00-prefixed JSON control frame.
func sendControl(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	frame := make([]byte, len(body)+1)
	frame[0] = 0
	copy(frame[1:], body)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("control write: %v", err)
	}
}

// TestEchoThroughPTY: /bin/cat reflects input back over the PTY.
// When the PTY is in cooked mode (default), cat echoes every byte
// back once for terminal echo; we only need to confirm "hello"
// appears in the output stream to prove the bidirectional pipe is
// wired correctly.
func TestEchoThroughPTY(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/cat"})
	defer cleanup()

	sendControl(t, ws, map[string]any{
		"type": "resize", "cols": 100, "rows": 40,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	readUntil(t, ws, []byte("hello"), 2*time.Second)
}

// TestResizeControlIsAccepted: a well-formed resize frame must not
// close the WS. We send resize, then raw input, and confirm the
// pipe still works. We can't directly assert the child's window
// size without shelling out, but the happy-path ioctl is internally
// exercised.
func TestResizeControlIsAccepted(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/cat"})
	defer cleanup()

	sendControl(t, ws, map[string]any{
		"type": "resize", "cols": 100, "rows": 40,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("after-resize\n")); err != nil {
		t.Fatalf("post-resize write: %v", err)
	}
	readUntil(t, ws, []byte("after-resize"), 2*time.Second)
}

// TestBadControlMessageIgnored: a malformed JSON control frame
// must not tear down the connection — we keep the pipe open so a
// buggy client can recover.
func TestBadControlMessageIgnored(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/cat"})
	defer cleanup()

	sendControl(t, ws, map[string]any{
		"type": "resize", "cols": 100, "rows": 40,
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 0x00 prefix + garbage JSON.
	if err := ws.Write(ctx, websocket.MessageBinary, []byte{0x00, '{', 'x'}); err != nil {
		t.Fatalf("bad control write: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("still-alive\n")); err != nil {
		t.Fatalf("post-bad write: %v", err)
	}
	readUntil(t, ws, []byte("still-alive"), 2*time.Second)
}

// TestEmptyCommandFails: starting the handler with no command is a
// misconfiguration; the first WS must close cleanly with
// InternalError rather than hang or panic.
func TestEmptyCommandFails(t *testing.T) {
	h := NewHandler(nil, WithWorkDir("/"))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	//nolint:bodyclose // library contract: Body is nil on success
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		// An upgrade failure is also acceptable (some WS stacks
		// surface the server-side error at dial time). The
		// important property is the test doesn't hang.
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	// Read should return an error as the server closes.
	_, _, readErr := ws.Read(ctx)
	if readErr == nil {
		t.Fatalf("expected read error after handler rejects empty command")
	}
}

// mustJSON marshals v to JSON for use as a handleControl payload (the bytes
// after the 0x00 control prefix).
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}

// serverSideConn stands up a throwaway WebSocket server, dials it, and returns
// the SERVER-side *websocket.Conn so a test can hand a real, non-nil
// connection to handleControl/handleResume without running the full handleWS
// read loop. The server goroutine drains client reads so cleanup unblocks the
// httptest server.
func serverSideConn(t *testing.T) (*websocket.Conn, func()) {
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
		t.Fatalf("serverSideConn dial: %v", err)
	}
	var server *websocket.Conn
	select {
	case server = <-ch:
	case <-time.After(3 * time.Second):
		_ = client.Close(websocket.StatusNormalClosure, "")
		srv.Close()
		t.Fatalf("serverSideConn: server side never accepted")
	}
	cleanup := func() {
		_ = client.Close(websocket.StatusNormalClosure, "")
		_ = server.CloseNow()
		srv.Close()
	}
	return server, cleanup
}

// TestShutdown_callsCancel verifies Shutdown invokes the stored cancel func
// when the handler has been started.
func TestShutdown_callsCancel(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"})
	called := false
	h.cancel = func() { called = true }
	h.started.Store(true) // Shutdown returns early unless started

	h.Shutdown()

	if !called {
		t.Errorf("Shutdown did not invoke h.cancel; want it called")
	}
}

// TestShutdown_closesPtmx verifies Shutdown closes the PTY file. The close is
// observed by a second Close returning an already-closed error.
func TestShutdown_closesPtmx(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"})
	f, err := os.CreateTemp(t.TempDir(), "ptmx")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	h.ptmx = f
	h.cancel = func() {} // non-nil no-op so the cancel branch never panics
	h.started.Store(true)

	h.Shutdown()

	if err := h.ptmx.Close(); err == nil {
		t.Errorf("re-Close after Shutdown returned nil; Shutdown did not close ptmx")
	}
}

// TestEnsureStarted_colsNotDefaultedWhenPositive verifies a positive cols value
// below the default is kept (only cols < 1 falls back to defaultCols).
func TestEnsureStarted_colsNotDefaultedWhenPositive(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"}, WithWorkDir("/"))
	if err := h.ensureStarted(1, 24); err != nil {
		t.Fatalf("ensureStarted(1, 24): %v", err)
	}
	defer h.Shutdown()

	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 1 {
		t.Errorf("ensureStarted(cols=1): screen.Width = %d, want 1 (cols==1 must not default to %d)", w, defaultCols)
	}
}

// TestEnsureStarted_rowsNotDefaultedWhenPositive verifies a positive rows value
// below the default is kept (only rows < 1 falls back to defaultRows).
func TestEnsureStarted_rowsNotDefaultedWhenPositive(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"}, WithWorkDir("/"))
	if err := h.ensureStarted(24, 1); err != nil {
		t.Fatalf("ensureStarted(24, 1): %v", err)
	}
	defer h.Shutdown()

	h.mu.Lock()
	ht := h.screen.Height
	h.mu.Unlock()
	if ht != 1 {
		t.Errorf("ensureStarted(rows=1): screen.Height = %d, want 1 (rows==1 must not default to %d)", ht, defaultRows)
	}
}

// TestBuildFrame_appendsScrollLinesToRing verifies buildFrame appends drained
// scroll lines to the scrollback ring so they can be replayed to reconnecting
// clients.
func TestBuildFrame_appendsScrollLinesToRing(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	// Tiny screen + many newlines forces lines to scroll off into the drain.
	h.screen = vt.New(3, 20)
	for range 20 {
		if _, err := h.screen.Write([]byte("scroll line\r\n")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
	}
	h.resized = true

	frame := h.buildFrame()
	if frame == nil {
		t.Fatalf("buildFrame returned nil; expected a frame with scroll lines")
	}
	if len(frame.scrollLines) == 0 {
		t.Fatalf("precondition failed: frame has no scroll lines to append")
	}
	if got := len(h.scrollback.Lines()); got == 0 {
		t.Errorf("buildFrame appended %d scrollback lines, want > 0", got)
	}
}

// TestHandleControl_resizeStartsProcess verifies a well-formed resize control
// message starts the child process and sizes the screen.
func TestHandleControl_resizeStartsProcess(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	payload := mustJSON(t, controlMsg{Type: ctlTypeResize, Cols: 100, Rows: 40})
	h.handleControl(nil, &ClientState{}, payload)

	if !h.started.Load() {
		t.Fatalf("handleControl(valid resize): process not started")
	}
	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 100 {
		t.Errorf("handleControl(resize cols=100): screen.Width = %d, want 100", w)
	}
}

// TestHandleControl_unknownTypeDoesNotStart verifies a control message whose
// type is neither resume nor resize is ignored and does not start the process.
func TestHandleControl_unknownTypeDoesNotStart(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	payload := mustJSON(t, controlMsg{Type: "bogus"})
	h.handleControl(nil, &ClientState{}, payload)

	if h.started.Load() {
		t.Errorf("handleControl(unknown type): process started; only a resize may start it")
	}
}

// TestHandleControl_resumeResolvesSession verifies a resume control message
// carrying a session id dispatches handleResume, which resolves and registers
// the session on the client state.
func TestHandleControl_resumeResolvesSession(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()
	ws, cleanup := serverSideConn(t)
	defer cleanup()

	state := &ClientState{}
	payload := mustJSON(t, controlMsg{Type: ctlTypeResume, SessionID: "sid"})
	h.handleControl(ws, state, payload)

	if state.session.Load() == nil {
		t.Errorf("handleControl(resume): ClientState.session is nil; resume must resolve a session")
	}
	h.registry.mu.Lock()
	_, ok := h.registry.sessions["sid"]
	h.registry.mu.Unlock()
	if !ok {
		t.Errorf("handleControl(resume): registry has no session %q; resume was not dispatched", "sid")
	}
}

// TestHandleControl_pingElicitsPong verifies a client liveness ping draws a
// pong frame back. The client probes with a ping after a stretch of inbound
// silence; the returning pong (or any frame) lets it tell an idle-but-healthy
// socket from one iOS froze during sleep, so a quiet terminal is not
// reconnect-flapped (bug 2 defense-in-depth).
func TestHandleControl_pingElicitsPong(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/cat"})
	defer cleanup()

	// Deliberately send no resize: the child never starts, so no screen or
	// scroll frames race the pong. The only frame the server sends is the pong.
	sendControl(t, ws, map[string]any{"type": ctlTypePing})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("never received a pong after ping: %v", err)
		}
		if len(msg) > 0 && msg[0] == wireMsgPong {
			return // got the liveness reply
		}
	}
}

// TestHandleResize_colsAboveMinNotFloored verifies a cols value above
// minResizeCols is applied unchanged.
func TestHandleResize_colsAboveMinNotFloored(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 100 {
		t.Errorf("handleResize(cols=100): screen.Width = %d, want 100 (above minResizeCols, not floored)", w)
	}
}

// TestHandleResize_rowsAboveMinNotFloored verifies a rows value above
// minResizeRows is applied unchanged.
func TestHandleResize_rowsAboveMinNotFloored(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	ht := h.screen.Height
	h.mu.Unlock()
	if ht != 40 {
		t.Errorf("handleResize(rows=40): screen.Height = %d, want 40 (above minResizeRows, not floored)", ht)
	}
}

// TestHandleResize_holdsFlushOnSuccessfulStart verifies that after a
// successful first start, handleResize runs the post-start path including
// HoldFlush so the SIGWINCH redraw window is hidden from the client.
func TestHandleResize_holdsFlushOnSuccessfulStart(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(100, 40)

	h.mu.Lock()
	held := h.screen.IsFlushHeld()
	h.mu.Unlock()
	if !held {
		t.Errorf("handleResize: IsFlushHeld()=false after a successful start; HoldFlush must run")
	}
}
