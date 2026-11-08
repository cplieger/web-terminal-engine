package terminal

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// gridSession dials a fresh session, announces 80x24 and resumes, declaring
// grid frames when declare is set.
func gridSession(t *testing.T, declare bool) (*websocket.Conn, <-chan []byte) {
	t.Helper()
	h := NewHandler([]string{"/bin/cat"}, WithWorkDir("/"), WithLogger(nil))
	shutdownOnCleanup(t, h)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	//nolint:bodyclose // library contract: Body is nil on success
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	frames := framePump(t, ws)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 80, "rows": 24})
	sendControl(t, ws, map[string]any{
		"type": "resume", "sessionId": "grid", "sentBytes": 0, "haveThrough": -1,
		"protocolVersion": wireProtocolVersion, "gridFrames": declare,
	})
	return ws, frames
}

func gridOf(msg []byte) (cols, rows int) {
	return int(binary.LittleEndian.Uint16(msg[9:])), int(binary.LittleEndian.Uint16(msg[11:]))
}

// TestGridFrame_announcesTheScreenGrid: a socket that declared grid frames is
// told the grid on resume, before the modes and the window, and again before
// the first screen frame after each size change.
func TestGridFrame_announcesTheScreenGrid(t *testing.T) {
	ws, frames := gridSession(t, true)

	// The attach flush can send a full window before the resume is handled;
	// only the frames after the resumeAck are the resume's.
	if pre := framesThrough(frames, typeIs(wireMsgResumeAck), 5*time.Second); !slices.ContainsFunc(pre, typeIs(wireMsgResumeAck)) {
		t.Fatalf("no resumeAck arrived (frame types %v)", frameTypes(pre))
	}
	seen := framesThrough(frames, isFullScreenFrame, 5*time.Second)
	at := slices.IndexFunc(seen, typeIs(wireMsgGrid))
	if at < 0 || at > slices.IndexFunc(seen, isFullScreenFrame) {
		t.Fatalf("resume sent no grid frame before the window (frame types %v)", frameTypes(seen))
	}
	if modes := slices.IndexFunc(seen, typeIs(wireMsgModes)); modes < at {
		t.Errorf("resume sent the modes before the grid (frame types %v)", frameTypes(seen))
	}
	if cols, rows := gridOf(seen[at]); cols != 80 || rows != 24 {
		t.Errorf("resume grid = %dx%d, want 80x24", cols, rows)
	}
	drainIdle(frames, 300*time.Millisecond)

	sendControl(t, ws, map[string]any{"type": "resize", "cols": 100, "rows": 30})
	seen = framesThrough(frames, typeIs(wireMsgScreen), 5*time.Second)
	at = slices.IndexFunc(seen, typeIs(wireMsgGrid))
	if at < 0 {
		t.Fatalf("no grid frame before the screen frame after a resize (frame types %v)", frameTypes(seen))
	}
	if cols, rows := gridOf(seen[at]); cols != 100 || rows != 30 {
		t.Errorf("grid after resize = %dx%d, want 100x30", cols, rows)
	}
}

// TestGridFrame_resumeReannouncesToAToldSocket: a second resume on a socket
// already told the grid, modes and title sends all three again before the
// window.
func TestGridFrame_resumeReannouncesToAToldSocket(t *testing.T) {
	ws, frames := gridSession(t, true)
	if pre := framesThrough(frames, typeIs(wireMsgResumeAck), 5*time.Second); !slices.ContainsFunc(pre, typeIs(wireMsgResumeAck)) {
		t.Fatalf("no first resumeAck arrived (frame types %v)", frameTypes(pre))
	}
	framesThrough(frames, isFullScreenFrame, 5*time.Second)
	drainIdle(frames, 300*time.Millisecond)

	sendControl(t, ws, map[string]any{
		"type": "resume", "sessionId": "grid", "sentBytes": 0, "haveThrough": -1,
		"protocolVersion": wireProtocolVersion, "gridFrames": true,
	})
	if pre := framesThrough(frames, typeIs(wireMsgResumeAck), 5*time.Second); !slices.ContainsFunc(pre, typeIs(wireMsgResumeAck)) {
		t.Fatalf("no second resumeAck arrived (frame types %v)", frameTypes(pre))
	}
	seen := framesThrough(frames, isFullScreenFrame, 5*time.Second)
	window := slices.IndexFunc(seen, isFullScreenFrame)
	if window < 0 {
		t.Fatalf("second resume sent no window (frame types %v)", frameTypes(seen))
	}
	for _, want := range []byte{wireMsgGrid, wireMsgModes, wireMsgTitle} {
		if at := slices.IndexFunc(seen, typeIs(want)); at < 0 || at > window {
			t.Errorf("second resume sent no frame of type %d before the window (frame types %v)", want, frameTypes(seen))
		}
	}
}

// TestGridFrame_onlyToDeclaringSockets: a client that never declared grid
// frames (every released client, whose decoder warns on an unknown type) is
// never sent one, on resume or after a size change.
func TestGridFrame_onlyToDeclaringSockets(t *testing.T) {
	ws, frames := gridSession(t, false)
	seen := framesThrough(frames, isFullScreenFrame, 5*time.Second)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": 100, "rows": 30})
	seen = append(seen, framesFor(frames, time.Second)...)
	if !slices.ContainsFunc(seen, isFullScreenFrame) {
		t.Fatal("no window frame arrived; harness broken")
	}
	if slices.ContainsFunc(seen, typeIs(wireMsgGrid)) {
		t.Errorf("a socket that did not declare gridFrames was sent a grid frame (frame types %v)", frameTypes(seen))
	}
}

func frameTypes(frames [][]byte) []byte {
	out := make([]byte, 0, len(frames))
	for _, f := range frames {
		if len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}
