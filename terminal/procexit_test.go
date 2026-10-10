package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestExitedAttachServesReplayBefore4001 pins the attach-to-dead-session
// contract: a client that connects AFTER the child process exited still gets
// its full resume exchange — resumeAck and the final-screen window frame —
// before the definitive statusProcessExited (4001) close. Before this grace
// existed, closeOnProcExit fired the 4001 immediately on attach (procExitCh
// already closed) and reliably beat handleResume's writes, so a reloading
// client saw nothing renderable — the wedge behind the "stuck loading screen
// with an endlessly flashing reconnect" report.
func TestExitedAttachServesReplayBefore4001(t *testing.T) {
	const marker = "deadwords"
	h := NewHandler([]string{"/bin/sh", "-c", "echo " + marker + "; exit 1"}, WithWorkDir("/"))
	defer h.Close()
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}
	// Poll the screen itself: it is the precondition the replay assertion needs.
	deadline := time.Now().Add(waitPatience)
	for !screenContains(h, marker) {
		if time.Now().After(deadline) {
			t.Fatalf("child output %q never reached the screen within %v (exited=%v); screen holds %q",
				marker, waitPatience, h.Exited(), screenText(h))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !h.Exited() {
		t.Fatal("child wrote its output but has not exited; the attach below would not be against a dead session")
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	//nolint:bodyclose // library contract: Body is nil on success
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "") // #nosec G104 -- best-effort test cleanup

	// Speak the resume protocol like the real client: first frame after open.
	resume, err := json.Marshal(controlMsg{Type: ctlTypeResume, SessionID: "dead-attach"})
	if err != nil {
		t.Fatalf("marshal resume: %v", err)
	}
	if err := ws.Write(ctx, websocket.MessageBinary, append([]byte{0x00}, resume...)); err != nil {
		t.Fatalf("ws write resume: %v", err)
	}

	// The replay (resumeAck + modes/title + final screen) must arrive BEFORE
	// the 4001 close: collect frames until the close error, then check both
	// that frames were delivered and that the close code is 4001.
	frames := 0
	var all []byte
	for {
		_, data, rerr := ws.Read(ctx)
		if rerr != nil {
			if got := websocket.CloseStatus(rerr); got != statusProcessExited {
				t.Fatalf("close status = %d, want %d (statusProcessExited); read err: %v", got, statusProcessExited, rerr)
			}
			break
		}
		frames++
		all = append(all, data...)
	}
	if frames == 0 {
		t.Fatal("no frames before the 4001 close; the resume exchange must be served to an attach-to-exited client")
	}
	if !strings.Contains(string(all), marker) {
		t.Errorf("final screen replay missing the child's last output %q; got %d frames, %d bytes", marker, frames, len(all))
	}
}

// TestProcessExitClosesWith4001 verifies the terminal WS closes with
// statusProcessExited (4001), not a normal closure, when the child process
// exits. The command stays alive for a short sleep so the client is fully
// attached and the read loop is blocked before the exit; then the process
// exits, procExitCh closes, cancelOnProcExit cancels the read loop, and the
// deferred close observes procExitCh and sends 4001.
func TestProcessExitClosesWith4001(t *testing.T) {
	ws, cleanup := dialHandler(t, []string{"/bin/sh", "-c", "sleep 0.2"})
	defer cleanup()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
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

// TestProcessExit_finalOutputReachesTheScreenBeforeTheCallback holds the PTY
// reader on the child's only output until the monitor starts draining, or until
// the exit callback fires if it never drains. A monitor that does not wait for
// the reader fires the callback over an empty screen.
func TestProcessExit_finalOutputReachesTheScreenBeforeTheCallback(t *testing.T) {
	const marker = "goodbye"
	draining := make(chan struct{})
	fired := make(chan struct{})
	var drainOnce, firedOnce sync.Once
	t.Cleanup(func() {
		drainOnce.Do(func() { close(draining) })
		firedOnce.Do(func() { close(fired) })
	})
	drainHold := func() { drainOnce.Do(func() { close(draining) }) }
	readHold := func() {
		select {
		case <-draining:
		case <-fired:
		}
	}
	testExitDrainHold.Store(&drainHold)
	testPTYReadHold.Store(&readHold)
	t.Cleanup(func() {
		testExitDrainHold.Store(nil)
		testPTYReadHold.Store(nil)
	})

	var h *Handler
	var atCallback string
	h = NewHandler([]string{"/bin/sh", "-c", "printf " + marker},
		WithWorkDir("/"),
		WithLogger(nil),
		WithOnProcessExit(func(error) {
			atCallback = screenText(h)
			firedOnce.Do(func() { close(fired) })
		}),
	)
	defer h.Close()
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(waitPatience):
		t.Fatalf("onProcessExit not called within %v; screen holds %q", waitPatience, screenText(h))
	}
	if !strings.Contains(atCallback, marker) {
		t.Errorf("screen at onProcessExit = %q, want it to contain the child's final output %q", atCallback, marker)
	}
}

// TestProcessExit_drainIsBoundedWhenABackgroundChildHoldsTheTerminal: a
// backgrounded process that ignores SIGHUP keeps the PTY slave open after the
// head exits, so the reader never sees EOF. The exit must still be reported.
func TestProcessExit_drainIsBoundedWhenABackgroundChildHoldsTheTerminal(t *testing.T) {
	const marker = "ready"
	fired := make(chan struct{})
	h := NewHandler([]string{"/bin/sh", "-c", `trap "" HUP; sleep 30 & printf ` + marker},
		WithWorkDir("/"),
		WithLogger(nil),
		WithOnProcessExit(func(error) { close(fired) }),
	)
	defer h.Close()
	start := time.Now()
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}

	select {
	case <-fired:
	case <-time.After(waitPatience):
		t.Fatalf("onProcessExit not called within %v while a background child held the terminal; the drain wait must be bounded", waitPatience)
	}
	if elapsed := time.Since(start); elapsed < exitDrainBound {
		t.Fatalf("onProcessExit fired %v after start, under the %v bound: either the monitor did not wait for the reader, or the background child did not hold the terminal and this fixture tests nothing", elapsed, exitDrainBound)
	}
	deadline := time.Now().Add(waitPatience)
	for !h.Exited() {
		if time.Now().After(deadline) {
			t.Fatalf("Exited() still false %v after onProcessExit fired on the bounded path", waitPatience)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !screenContains(h, marker) {
		t.Errorf("screen = %q, want the head's output %q", screenText(h), marker)
	}
}

// screenContains reports whether the parsed screen currently holds want, read
// under h.mu, the lock the parser writes the cells under.
func screenContains(h *Handler, want string) bool {
	return strings.Contains(screenText(h), want)
}

// screenText returns the parsed screen's text, one line per row with each row's
// trailing padding and the trailing blank rows dropped, so a poll that gives up
// can report what the screen held instead of only that it lacked the text: an
// empty screen means the runner was slow, and a shell diagnostic means the
// fixture is broken. Every cell grid pads to the full width, so an untrimmed dump
// is thousands of spaces around the one line that matters.
func screenText(h *Handler) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	rows := make([]string, 0, len(h.screen.Cells))
	for _, row := range h.screen.Cells {
		var b strings.Builder
		for _, c := range row {
			if c.Ch != 0 {
				b.WriteRune(c.Ch)
			}
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n") + "\n"
}
