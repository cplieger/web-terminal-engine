package terminal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// awaitMainScreenText polls h's main screen until its text contains want, and
// fails with the screen's rows if it never does.
func awaitMainScreenText(t *testing.T, h *Handler, want string) {
	t.Helper()
	deadline := time.Now().Add(waitPatience)
	var rows []string
	for {
		h.mu.Lock()
		rows, _ = h.screen.MainScreenText()
		h.mu.Unlock()
		if strings.Contains(strings.Join(rows, "\n"), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("main screen never showed %q within %v; rows:\n%s", want, waitPatience, strings.Join(rows, "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startEagerOnServer starts script under /bin/sh eagerly, waits until the
// screen shows ready (so whatever the script printed first was laid out before
// any client attached), then serves h and dials it.
func startEagerOnServer(t *testing.T, script, ready string, args ...string) *websocket.Conn {
	t.Helper()
	cmd := append([]string{"/bin/sh", "-c", script, "sh"}, args...)
	h := NewHandler(cmd, WithLogger(nil), WithWorkDir("/"))
	shutdownOnCleanup(t, h)
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager() = %v, want nil", err)
	}
	awaitMainScreenText(t, h, ready)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return dialWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws")
}

// TestStartEager_childReadsTheConventionalDefaultSize pins the size a program
// started before any client reports one reads at start: 80x24, the VT100
// screen and xterm's default geometry. A program that lays out by the width it
// reads at start (a menu, a column listing) never re-reads it.
func TestStartEager_childReadsTheConventionalDefaultSize(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", `printf 'size=%s\n' "$(stty size)"; exec sleep 60`},
		WithLogger(nil), WithWorkDir("/"))
	shutdownOnCleanup(t, h)
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager() = %v, want nil", err)
	}
	awaitMainScreenText(t, h, "size=24 80")
}

// TestStartEager_lineWrittenBeforeAttachReachesTheClientUnwrapped pins the
// first client's view of a long line a program printed before any client
// attached and never repainted: it arrives on one row at the client's width,
// as a terminal that re-wraps soft-wrapped lines on resize shows it. sleep
// ignores SIGWINCH, so nothing redraws the line.
func TestStartEager_lineWrittenBeforeAttachReachesTheClientUnwrapped(t *testing.T) {
	line := strings.Repeat("0123456789", 12) // 120 columns: wider than 80, narrower than the client
	ws := startEagerOnServer(t, `printf '%s\nready\n' "$1"; exec sleep 60`, "ready", line)

	sendControl(t, ws, map[string]any{"type": "resize", "cols": 153, "rows": 48})

	readUntil(t, ws, []byte(line), 5*time.Second)
}

// TestHandleResize_firstClientSizeReachesTheEagerChild pins that a program
// started before any client attached reads the default size, then gets
// SIGWINCH and reads the first client's size from its pty.
func TestHandleResize_firstClientSizeReachesTheEagerChild(t *testing.T) {
	ws := startEagerOnServer(t,
		`trap 'printf "winch=%s\n" "$(stty size)"' WINCH; printf 'start=%s\n' "$(stty size)"; while :; do sleep 0.05; done`,
		"start=24 80")

	sendControl(t, ws, map[string]any{"type": "resize", "cols": 153, "rows": 48})

	readUntil(t, ws, []byte("winch=48 153"), 5*time.Second)
}
