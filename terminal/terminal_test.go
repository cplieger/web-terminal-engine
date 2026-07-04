package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cplieger/web-terminal-engine/v2/vt"
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
	h.handleControl(nil, &clientState{}, payload)

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
	h.handleControl(nil, &clientState{}, payload)

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

	state := &clientState{}
	payload := mustJSON(t, controlMsg{Type: ctlTypeResume, SessionID: "sid"})
	h.handleControl(ws, state, payload)

	if state.session.Load() == nil {
		t.Errorf("handleControl(resume): clientState.session is nil; resume must resolve a session")
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

// dualConn stands up a throwaway WebSocket server, dials it, and returns BOTH
// the SERVER-side conn (to hand to dispatchFrame/handleResume) and the
// CLIENT-side conn (to read back the server→client frames a test wants to
// assert on). The server goroutine drains client→server reads so the
// connection stays healthy; nothing is sent that way in these tests.
func dualConn(t *testing.T) (server, client *websocket.Conn, cleanup func()) {
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
		t.Fatalf("dualConn dial: %v", err)
	}
	select {
	case server = <-ch:
	case <-time.After(3 * time.Second):
		_ = client.Close(websocket.StatusNormalClosure, "")
		srv.Close()
		t.Fatalf("dualConn: server side never accepted")
	}
	cleanup = func() {
		_ = client.Close(websocket.StatusNormalClosure, "")
		_ = server.CloseNow()
		srv.Close()
	}
	return server, client, cleanup
}

// readServerFrames reads every binary frame the server sent until no new frame
// arrives within idle. Frames are already buffered by the time a synchronous
// dispatchFrame/handleResume call returns, so only the final (no-more-frames)
// read waits out idle.
func readServerFrames(t *testing.T, client *websocket.Conn, idle time.Duration) [][]byte {
	t.Helper()
	var frames [][]byte
	for {
		ctx, cancel := context.WithTimeout(context.Background(), idle)
		_, msg, err := client.Read(ctx)
		cancel()
		if err != nil {
			return frames
		}
		cp := make([]byte, len(msg))
		copy(cp, msg)
		frames = append(frames, cp)
	}
}

// countFramesByType tallies frames by their leading msg_type byte, ignoring
// any zero-length frame.
func countFramesByType(frames [][]byte) map[byte]int {
	m := make(map[byte]int)
	for _, f := range frames {
		if len(f) > 0 {
			m[f[0]]++
		}
	}
	return m
}

// TestDispatchFrame_scrollOnlyEmitsScrollNotScreen verifies a frame carrying
// scroll lines but no changed rows is sent as exactly one scroll frame and no
// screen frame: the screen payload must be gated on len(changed) > 0 and the
// scroll payload must be both built (len(scrollLines) > 0) and written.
func TestDispatchFrame_scrollOnlyEmitsScrollNotScreen(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	frame := &flushFrame{
		clients:        map[*websocket.Conn]uint64{server: 0},
		scrollLines:    [][]vt.WireRun{makeLine("scrolled")},
		scrollFirstIdx: 0,
		screenHeight:   3,
	}
	h.dispatchFrame(frame)

	types := countFramesByType(readServerFrames(t, client, 300*time.Millisecond))
	if types[wireMsgScreen] != 0 {
		t.Errorf("scroll-only frame emitted %d screen frame(s); want 0 (no changed rows ⇒ no screen payload)", types[wireMsgScreen])
	}
	if types[wireMsgScroll] != 1 {
		t.Errorf("scroll-only frame emitted %d scroll frame(s); want 1", types[wireMsgScroll])
	}
}

// TestDispatchFrame_changedOnlyEmitsScreenNotScroll verifies a frame carrying
// changed rows but no scroll lines is sent as exactly one screen frame and no
// scroll frame: the scroll payload must be gated on len(scrollLines) > 0.
func TestDispatchFrame_changedOnlyEmitsScreenNotScroll(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	frame := &flushFrame{
		clients:      map[*websocket.Conn]uint64{server: 0},
		rows:         [][]vt.WireRun{makeLine("row0")},
		changed:      []int{0},
		screenHeight: 1,
	}
	h.dispatchFrame(frame)

	types := countFramesByType(readServerFrames(t, client, 300*time.Millisecond))
	if types[wireMsgScroll] != 0 {
		t.Errorf("changed-only frame emitted %d scroll frame(s); want 0 (no scroll lines ⇒ no scroll payload)", types[wireMsgScroll])
	}
	if types[wireMsgScreen] != 1 {
		t.Errorf("changed-only frame emitted %d screen frame(s); want 1", types[wireMsgScreen])
	}
}

// TestDispatchFrame_modesPayloadIsWritten verifies a non-nil modes payload is
// actually written to the client (the nil-guard must send when the payload is
// present).
func TestDispatchFrame_modesPayloadIsWritten(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	frame := &flushFrame{
		clients:      map[*websocket.Conn]uint64{server: 0},
		modesPayload: encodeModesMsg(true, false, false, false, false, false, false, 0, 0),
		screenHeight: 1,
	}
	h.dispatchFrame(frame)

	types := countFramesByType(readServerFrames(t, client, 300*time.Millisecond))
	if types[wireMsgModes] != 1 {
		t.Errorf("frame with a modes payload emitted %d modes frame(s); want 1", types[wireMsgModes])
	}
}

// TestDispatchFrame_largeScrollBurstSplitsIntoChunks pins the chunking loop in
// dispatchFrame: a drained burst larger than maxScrollLinesPerFrame is split into
// several scroll frames, each tagged with its own absolute first index
// (scrollFirstIdx + i). The other TestDispatchFrame_* cases pass a single scroll
// line, so the loop only ever iterated once and the split plus the per-chunk index
// offset were unverified. A mutant collapsing the split into one frame, or dropping
// the +i so every chunk reuses scrollFirstIdx, would survive.
func TestDispatchFrame_largeScrollBurstSplitsIntoChunks(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	const firstIdx uint64 = 7
	lines := make([][]vt.WireRun, maxScrollLinesPerFrame+500)
	for i := range lines {
		lines[i] = makeLine("x")
	}
	frame := &flushFrame{
		clients:        map[*websocket.Conn]uint64{server: 0},
		scrollLines:    lines,
		scrollFirstIdx: firstIdx,
		screenHeight:   3,
	}
	h.dispatchFrame(frame)

	var firstIdxs []uint64
	var lineCounts []uint16
	for _, f := range readServerFrames(t, client, 300*time.Millisecond) {
		if len(f) >= 19 && f[0] == wireMsgScroll {
			firstIdxs = append(firstIdxs, binary.LittleEndian.Uint64(f[9:17]))
			lineCounts = append(lineCounts, binary.LittleEndian.Uint16(f[17:19]))
		}
	}
	want := []uint64{firstIdx, firstIdx + maxScrollLinesPerFrame}
	if len(firstIdxs) != len(want) {
		t.Fatalf("burst of %d lines emitted %d scroll frame(s) (first indices %v); want %d at %v (split at maxScrollLinesPerFrame=%d)",
			len(lines), len(firstIdxs), firstIdxs, len(want), want, maxScrollLinesPerFrame)
	}
	for i, w := range want {
		if firstIdxs[i] != w {
			t.Errorf("scroll chunk %d first index = %d, want %d (chunk i must start at scrollFirstIdx+i)", i, firstIdxs[i], w)
		}
	}
	// Each chunk must be bounded by maxScrollLinesPerFrame and the chunks must
	// together carry every input line exactly once: a mutant that stops bounding
	// the slice end (shipping all lines in the first chunk and duplicating the
	// tail) keeps the frame count and first indices but breaks these counts.
	if lineCounts[0] != maxScrollLinesPerFrame || lineCounts[1] != uint16(len(lines)-maxScrollLinesPerFrame) {
		t.Errorf("scroll chunk line counts = %v, want [%d %d] (each chunk bounded by maxScrollLinesPerFrame; no dropped or duplicated lines)",
			lineCounts, maxScrollLinesPerFrame, len(lines)-maxScrollLinesPerFrame)
	}
}

// TestHandleResume_commitsScrolledLinesToHistory verifies handleResume commits
// the lines that scrolled off the screen while the client was away into the
// scrollback ring, so they can be replayed. With the commit skipped, the ring
// stays empty and that history is lost.
func TestHandleResume_commitsScrolledLinesToHistory(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, cleanup := serverSideConn(t)
	defer cleanup()

	// Tiny screen + many newlines pushes lines off the top into the pending
	// drain (not yet committed to the ring).
	h.screen = vt.New(3, 20)
	for range 20 {
		if _, err := h.screen.Write([]byte("scrolled\r\n")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
	}

	if before := h.scrollback.Committed(); before != 0 {
		t.Fatalf("precondition: committed=%d, want 0 before resume", before)
	}
	h.handleResume(server, &clientState{}, "sid", -1)

	if got := h.scrollback.Committed(); got == 0 {
		t.Errorf("handleResume committed %d lines to history; want > 0 (scrolled lines must be retained)", got)
	}
}

// TestHandleResume_altStraddleDrainNotCommitted pins the alt-straddle guard in
// handleResume (the !altTransitionPending term, the resume-side twin of Build's
// guard): drain that straddles an alt-screen transition must NOT be committed to
// main history on resume. TestHandleResume_commitsScrolledLinesToHistory covers
// only the no-transition path (fresh builder => altTransitionPending false =>
// commit). The two cases below share identical pending drain on the main screen
// and differ ONLY in the builder's last-observed alt state, so the flipped commit
// outcome proves the guard (not an empty drain) does the gating; a mutant dropping
// the !altTransitionPending term commits alt lines as main history and is caught.
func TestHandleResume_altStraddleDrainNotCommitted(t *testing.T) {
	tests := []struct {
		name          string
		prevAltValid  bool
		prevAlt       bool
		wantCommitted bool
	}{
		{"transition pending (just left alt) drops straddling drain", true, true, false},
		{"no pending transition commits drain", false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
			server, cleanup := serverSideConn(t)
			defer cleanup()

			// Tiny screen + many newlines leaves lines in the pending (uncommitted)
			// drain; the screen stays on the MAIN buffer (InAltScreen == false).
			h.screen = vt.New(3, 20)
			for range 20 {
				if _, err := h.screen.Write([]byte("scrolled\r\n")); err != nil {
					t.Fatalf("screen write: %v", err)
				}
			}
			// With prevAlt=true and the screen now on main, altTransitionPending
			// reports a not-yet-folded alt->main exit (Reset does not clear it).
			h.builder.prevAltValid = tc.prevAltValid
			h.builder.prevAlt = tc.prevAlt

			if before := h.scrollback.Committed(); before != 0 {
				t.Fatalf("precondition: committed=%d, want 0 before resume", before)
			}
			h.handleResume(server, &clientState{}, "sid", -1)

			if gotCommitted := h.scrollback.Committed() > 0; gotCommitted != tc.wantCommitted {
				t.Errorf("after resume: committed-history-present=%v, want %v (alt-straddle drain must be dropped; plain main-screen drain must commit)",
					gotCommitted, tc.wantCommitted)
			}
		})
	}
}

// TestHandleResume_replayStartsAfterHaveThrough verifies the server replays
// only the lines the client is missing: with haveThrough=0 the first replayed
// line is absolute index 1, not 0. Pins from = haveThrough + 1 (so flipping the
// +, or the haveThrough >= 0 guard, is caught — both would replay from 0 or
// replay nothing).
func TestHandleResume_replayStartsAfterHaveThrough(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	h.scrollback.Append([][]vt.WireRun{makeLine("L0"), makeLine("L1"), makeLine("L2")})

	h.handleResume(server, &clientState{}, "sid", 0) // client already holds index 0

	frames := readServerFrames(t, client, 300*time.Millisecond)
	var scroll []byte
	for _, f := range frames {
		if len(f) > 0 && f[0] == wireMsgScroll {
			scroll = f
			break
		}
	}
	if scroll == nil {
		t.Fatalf("handleResume(haveThrough=0): no replay scroll frame; want one starting at index 1")
	}
	if len(scroll) < 17 {
		t.Fatalf("scroll frame too short (%d bytes) to carry a first-index field", len(scroll))
	}
	if firstIdx := binary.LittleEndian.Uint64(scroll[9:17]); firstIdx != 1 {
		t.Errorf("replay first index = %d, want 1 (client holds index 0, replay starts at haveThrough+1)", firstIdx)
	}
}

// TestHandleResume_replayChunkBoundaryEmitsSingleFrame verifies that when the
// replay length is an exact multiple of the chunk size, the server sends one
// scroll frame and no trailing empty frame. With the loop bound off by one, an
// extra zero-line frame is emitted.
func TestHandleResume_replayChunkBoundaryEmitsSingleFrame(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	// 50 lines == replayChunk, so the chunk loop lands exactly on len(replay).
	lines := make([][]vt.WireRun, 50)
	for i := range lines {
		lines[i] = makeLine("x")
	}
	h.scrollback.Append(lines)

	h.handleResume(server, &clientState{}, "sid", -1) // -1 ⇒ replay everything from index 0

	types := countFramesByType(readServerFrames(t, client, 300*time.Millisecond))
	if types[wireMsgScroll] != 1 {
		t.Errorf("replay of exactly replayChunk lines emitted %d scroll frame(s); want 1 (no trailing empty frame)", types[wireMsgScroll])
	}
}

// TestHandleResume_replayChunksCarryAscendingAbsoluteIndices verifies a replay
// longer than one chunk tags each chunk with the correct absolute first index
// (firstAbs + i), so the client stores every line at its true index. With the
// offset computed the wrong way, later chunks carry a bogus index.
func TestHandleResume_replayChunksCarryAscendingAbsoluteIndices(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	server, client, cleanup := dualConn(t)
	defer cleanup()

	// 120 lines ⇒ three chunks of 50/50/20 at absolute indices 0, 50, 100.
	lines := make([][]vt.WireRun, 120)
	for i := range lines {
		lines[i] = makeLine("x")
	}
	h.scrollback.Append(lines)

	h.handleResume(server, &clientState{}, "sid", -1) // replay all from index 0

	var firstIdxs []uint64
	for _, f := range readServerFrames(t, client, 300*time.Millisecond) {
		if len(f) >= 17 && f[0] == wireMsgScroll {
			firstIdxs = append(firstIdxs, binary.LittleEndian.Uint64(f[9:17]))
		}
	}
	want := []uint64{0, 50, 100}
	if len(firstIdxs) != len(want) {
		t.Fatalf("replay sent %d scroll frames (first indices %v); want %d at %v", len(firstIdxs), firstIdxs, len(want), want)
	}
	for i, w := range want {
		if firstIdxs[i] != w {
			t.Errorf("scroll frame %d first index = %d, want %d (chunk %d starts at absolute index %d)", i, firstIdxs[i], w, i, w)
		}
	}
}

// TestHandleResize_belowMinimumIsFlooredUp pins the documented iPad
// keyboard-slide behavior: a near-zero resize is floored UP to the minimum
// (not dropped) and still starts the child. Existing resize tests only pass
// dimensions above the minimum, so the `cols < minResizeCols` /
// `rows < minResizeRows` floor branches were unexercised.
func TestHandleResize_belowMinimumIsFlooredUp(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	defer h.Shutdown()

	h.handleResize(1, 1)

	if !h.started.Load() {
		t.Fatalf("handleResize(1,1): process not started; a floored resize must still start the child")
	}
	h.mu.Lock()
	w, ht := h.screen.Width, h.screen.Height
	h.mu.Unlock()
	if w != minResizeCols {
		t.Errorf("handleResize(cols=1): screen.Width = %d, want %d (floored up to minResizeCols)", w, minResizeCols)
	}
	if ht != minResizeRows {
		t.Errorf("handleResize(rows=1): screen.Height = %d, want %d (floored up to minResizeRows)", ht, minResizeRows)
	}
}

// TestEnsureStarted_reaperInvokesOnProcessExitWithStatus exercises the
// process-reaper goroutine added in cycle 1 (the `go func(){ cmd.Wait();
// onProcessExit(werr); cancel() }` in ensureStarted). TestNewHandler_WithOnProcessExit
// only calls the stored callback directly and never spawns a child, so the real
// reaper path -- reap the child, forward cmd.Wait's exit status to onProcessExit --
// was unasserted. Here a real child exits non-zero and the reaper must invoke
// onProcessExit with the *exec.ExitError carrying the status. A mutant that drops
// the callback invocation, or passes nil instead of cmd.Wait's result, is caught.
func TestEnsureStarted_reaperInvokesOnProcessExitWithStatus(t *testing.T) {
	exitErr := make(chan error, 1)
	h := NewHandler([]string{"/bin/sh", "-c", "exit 7"},
		WithWorkDir("/"),
		WithLogger(nil),
		WithOnProcessExit(func(err error) { exitErr <- err }),
	)
	defer h.Shutdown()

	if err := h.ensureStarted(80, 24); err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}

	select {
	case err := <-exitErr:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("onProcessExit err = %v (%T), want *exec.ExitError from cmd.Wait", err, err)
		}
		if ee.ExitCode() != 7 {
			t.Errorf("child exit code = %d, want 7 (reaper must forward cmd.Wait's status)", ee.ExitCode())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("onProcessExit not called within 10s; the reaper goroutine must invoke it on child exit")
	}
}

// TestHandlePTYData_writesDeviceQueryResponseToPTY pins the writeback half of
// the handlePTYData refactor: a device-status query in the PTY output makes the
// VT screen produce a Response, which handlePTYData must write back to the PTY
// (outside h.mu). CSI 6 n (DSR cursor-position) on a fresh screen with the cursor
// at home yields ESC[1;1R. No terminal-level test drove this path (handlePTYData
// was 66.7%), so a mutant dropping the `resp = h.screen.Response` capture or the
// `h.ptmx.Write(resp)` writeback survived. The read deadline is required so the
// red case fails instead of blocking forever.
func TestHandlePTYData_writesDeviceQueryResponseToPTY(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	h.ptmx = pw

	// DSR cursor-position query; the fresh 30x120 screen's cursor is at home.
	h.handlePTYData([]byte("\x1b[6n"))

	if err := pr.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 64)
	n, err := pr.Read(buf)
	if err != nil {
		t.Fatalf("reading device-query response from PTY: %v (handlePTYData must write screen.Response back)", err)
	}
	if got, want := string(buf[:n]), "\x1b[1;1R"; got != want {
		t.Errorf("device-query response written to PTY = %q, want %q", got, want)
	}
}

// TestHandleResume_frameOrderByAltState pins the resume frame-ordering invariant
// behind findings l-f38 and h-f1: on resume with committed history beyond the
// client's haveThrough, the server delivers the current-window screen frame and
// the scroll replay in an order that depends on the live alt state, because the
// window frame is what sets the client's alt flag and the client drops scroll
// frames while that flag is set (store.ts applyScroll).
//   - MAIN screen (InAltScreen=false): the window frame must PRECEDE the replay,
//     so a client with a stale alt flag (disconnected in alt, app left alt while
//     away) leaves alt before the replayed history lands; otherwise it is
//     silently dropped (finding l-f38). The window must also be a full repaint at
//     the committed base (num_changed == screen_height, base == committed).
//   - ALT screen (InAltScreen=true): the replay must PRECEDE the window frame, so
//     a client not yet in alt (fresh load / second tab on an in-alt session)
//     stores the main-screen history before the window frame flips it into alt;
//     otherwise that history is dropped (the h-f1 regression).
//
// In both arms the window frame must carry the live alt state (offset-26 bit3),
// so a mutant hardcoding it false is caught.
func TestHandleResume_frameOrderByAltState(t *testing.T) {
	tests := []struct {
		name        string
		inAlt       bool
		windowFirst bool // expected: window screen frame precedes the scroll replay
	}{
		{"main screen: window frame precedes replay", false, true},
		{"in alt: replay precedes window frame", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
			server, client, cleanup := dualConn(t)
			defer cleanup()

			// Commit history the resuming client does not yet hold, so a scroll
			// replay is generated alongside the window frame.
			h.scrollback.Append([][]vt.WireRun{makeLine("L0"), makeLine("L1"), makeLine("L2")})
			// The window frame must carry this live alt state in both arms.
			h.screen.InAltScreen = tc.inAlt

			h.handleResume(server, &clientState{}, "sid", -1) // -1 ⇒ client holds nothing, replay all

			frames := readServerFrames(t, client, 300*time.Millisecond)

			firstScreen, firstScroll := -1, -1
			for i, f := range frames {
				if len(f) == 0 {
					continue
				}
				switch f[0] {
				case wireMsgScreen:
					if firstScreen == -1 {
						firstScreen = i
					}
				case wireMsgScroll:
					if firstScroll == -1 {
						firstScroll = i
					}
				}
			}

			if firstScreen == -1 {
				t.Fatalf("resume sent no screen frame; the current window must be delivered on resume")
			}
			if firstScroll == -1 {
				t.Fatalf("resume sent no scroll frame; committed history beyond haveThrough must be replayed")
			}
			if tc.windowFirst && firstScreen >= firstScroll {
				t.Errorf("window screen frame at index %d, scroll replay at index %d; on the main screen the window frame must precede the replay so the client leaves alt before the history lands",
					firstScreen, firstScroll)
			}
			if !tc.windowFirst && firstScroll >= firstScreen {
				t.Errorf("scroll replay at index %d, window screen frame at index %d; in alt the replay must precede the window frame so a not-yet-alt client stores history before the window flips it into alt",
					firstScroll, firstScreen)
			}

			// The window frame must carry the server's LIVE alt state. cursor_flags
			// is the byte at offset 26 (1 type + 8 ack + 8 base + 2 row + 2 col + 2
			// height + 2 num_changed + 1 style); bit3 (0x08) is altActive.
			screenFrame := frames[firstScreen]
			if len(screenFrame) < 27 {
				t.Fatalf("screen frame too short (%d bytes) to carry the cursor_flags byte", len(screenFrame))
			}
			if gotAlt := screenFrame[26]&0x08 != 0; gotAlt != tc.inAlt {
				t.Errorf("window frame altActive bit = %v, want %v (frame must reflect the server's live screen state)", gotAlt, tc.inAlt)
			}

			// On the main screen the resume window frame must additionally be a
			// FULL repaint at the committed base: every screen row is present
			// (num_changed == screen_height) and base equals the committed history
			// length (3 lines appended above). The ordering and alt-bit checks read
			// only the frame header, so neither pins the window's row set.
			if !tc.inAlt {
				gotHeight := binary.LittleEndian.Uint16(screenFrame[21:23])
				gotChanged := binary.LittleEndian.Uint16(screenFrame[23:25])
				if gotChanged != gotHeight {
					t.Errorf("window frame num_changed = %d, want %d (== screen_height; resume window must repaint every row)", gotChanged, gotHeight)
				}
				if gotBase := binary.LittleEndian.Uint64(screenFrame[9:17]); gotBase != 3 {
					t.Errorf("window frame base = %d, want 3 (committed history length; window sits just past committed)", gotBase)
				}
			}
		})
	}
}
