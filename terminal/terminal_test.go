package terminal

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
)

// dialHandler stands up the handler on an httptest server and opens
// a WebSocket client against it. Returns the open connection and a
// cleanup func. Uses /bin/cat so tests don't depend on dtach being
// installed in the workspace.
func dialHandler(t *testing.T, cmd []string) (*websocket.Conn, func()) {
	t.Helper()
	h := NewHandler(Options{Command: cmd, WorkDir: "/"})
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
	h := NewHandler(Options{Command: nil, WorkDir: "/"})
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
