package terminal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestAnnounce_reconnectTellsOnlyItsOwnSocket attaches a second socket to a
// session the first already watches, the way a reconnect opens a new socket
// and resumes on it. The new socket is told the modes and title; the first,
// which already holds them, is sent the repaint but no modes or title frame. A
// socket that never resumes is still told both.
func TestAnnounce_reconnectTellsOnlyItsOwnSocket(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithWorkDir("/"), WithLogger(nil))
	shutdownOnCleanup(t, h)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dial := func() <-chan []byte {
		t.Helper()
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
		bootstrapResume(t, ws, SessionID("s-"+time.Now().Format(time.RFC3339Nano)))
		return frames
	}

	first := dial()
	if awaitFrame(first, typeIs(wireMsgTitle), 5*time.Second) == nil {
		t.Fatal("the first socket was never told the title")
	}
	drainIdle(first, 300*time.Millisecond)

	second := dial()
	if awaitFrame(second, typeIs(wireMsgModes), 5*time.Second) == nil {
		t.Error("the reconnecting socket was never told the modes")
	}
	seen := framesThrough(first, isFullScreenFrame, 5*time.Second)
	seen = append(seen, framesFor(first, 300*time.Millisecond)...)
	if !slices.ContainsFunc(seen, isFullScreenFrame) {
		t.Fatal("the first socket never received the repaint the attach makes; harness broken")
	}
	for _, typ := range []byte{wireMsgModes, wireMsgTitle} {
		if slices.ContainsFunc(seen, typeIs(typ)) {
			t.Errorf("the first socket was re-sent a type-%d frame after another socket attached and resumed", typ)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	//nolint:bodyclose // library contract: Body is nil on success
	bare, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = bare.CloseNow() })
	reader := framesThrough(framePump(t, bare), typeIs(wireMsgTitle), 5*time.Second)
	if !slices.ContainsFunc(reader, typeIs(wireMsgModes)) || !slices.ContainsFunc(reader, typeIs(wireMsgTitle)) {
		t.Error("a socket that never resumes was not told the modes and title")
	}
}

// framesThrough returns the frames up to and including the first pred matches,
// or every frame seen when timeout passes first.
func framesThrough(frames <-chan []byte, pred func([]byte) bool, timeout time.Duration) [][]byte {
	var seen [][]byte
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-frames:
			if !ok {
				return seen
			}
			seen = append(seen, msg)
			if pred(msg) {
				return seen
			}
		case <-deadline:
			return seen
		}
	}
}

// framesFor returns every frame that arrives within d.
func framesFor(frames <-chan []byte, d time.Duration) [][]byte {
	return framesThrough(frames, func([]byte) bool { return false }, d)
}

// drainIdle consumes frames until none arrives for idle.
func drainIdle(frames <-chan []byte, idle time.Duration) {
	for {
		if len(framesFor(frames, idle)) == 0 {
			return
		}
	}
}
