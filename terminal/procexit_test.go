package terminal

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestProcessExitClosesWith4001 verifies the terminal WS closes with
// statusProcessExited (4001), not a normal closure, when the child process
// exits. The command stays alive for a short sleep so the client is fully
// attached and the read loop is blocked before the exit; then the process
// exits, procExitCh closes, cancelOnProcExit cancels the read loop, and the
// deferred close observes procExitCh and sends 4001.
func TestProcessExitClosesWith4001(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/sh", "-c", "sleep 0.2"})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Trigger the lazy process start; the byte is harmless (sh is running
	// sleep and ignores stdin) and ptmx.Write succeeds while the process is
	// alive, so there is no write-error race, the exit arrives via the sleep.
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("x")); err != nil {
		t.Fatalf("ws write: %v", err)
	}

	// Read until the server closes; the close code must be 4001.
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue // drain any screen/scroll frames the process emitted
		}
		if got := websocket.CloseStatus(err); got != statusProcessExited {
			t.Fatalf("close status = %d, want %d (statusProcessExited)", got, statusProcessExited)
		}
		return
	}
}
