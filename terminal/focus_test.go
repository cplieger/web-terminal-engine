package terminal

// Spec-first focus tests: DEC 1004 sends CSI I on focus-in and CSI O on
// focus-out (https://invisible-island.net/xterm/ctlseqs/ctlseqs.html). Any
// focused client focuses the session, as tmux's window_pane_update_focus does,
// and enabling 1004 reports the current state, as xterm.js's _reportFocus does.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// reportingHandler returns a handler whose session already has DEC 1004 enabled,
// which is the only state in which the derivation reports anything.
func reportingHandler(t *testing.T, opts ...Option) *Handler {
	t.Helper()
	h := NewHandler([]string{"true"}, opts...)
	h.screen.FocusReporting = true
	return h
}

func TestFocusReport_derivation(t *testing.T) {
	t.Run("no client attached answers focus-out", func(t *testing.T) {
		// An unattached session has nobody to be focused, and the application
		// must not be left believing it still holds focus after every viewer
		// left.
		h := reportingHandler(t)
		if got := h.focusReportLocked(false); !bytes.Equal(got, focusOutSeq) {
			t.Errorf("focusReportLocked(false) with no client = %q, want ESC[O", got)
		}
	})

	t.Run("an attached but unfocused client answers focus-out", func(t *testing.T) {
		h := reportingHandler(t)
		state := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(state, false)
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusOutSeq) {
			t.Errorf("focusReportLocked with one unfocused client = %q, want ESC[O", got)
		}
	})

	t.Run("an attached focused client answers focus-in", func(t *testing.T) {
		h := reportingHandler(t)
		state := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(state, true)
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked with one focused client = %q, want ESC[I", got)
		}
	})

	t.Run("two clients, one focused, answers focus-in (the tmux rule)", func(t *testing.T) {
		// Driven through the registry rather than by passing the bool, so the
		// aggregate itself is pinned: this is the value the derivation reads.
		h := reportingHandler(t)
		unfocused := h.registry.Add(&websocket.Conn{})
		focused := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(unfocused, false)
		h.registry.SetClientFocus(focused, true)
		if !h.registry.AnyClientFocused() {
			t.Fatalf("AnyClientFocused() = false with one focused of two clients, want true")
		}
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked with one focused of two clients = %q, want ESC[I", got)
		}
	})

	t.Run("keepUnfocused answers focus-out even for a focused client", func(t *testing.T) {
		// The consumer's declaration is one clause of the derivation, and it wins:
		// a notifier that gates its OSC 9 on focus keeps emitting.
		h := reportingHandler(t, WithKeepUnfocused(true))
		state := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(state, true)
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusOutSeq) {
			t.Errorf("focusReportLocked under keepUnfocused with a focused client = %q, want ESC[O", got)
		}
	})

	t.Run("reporting disabled answers nothing", func(t *testing.T) {
		h := NewHandler([]string{"true"})
		if got := h.focusReportLocked(true); got != nil {
			t.Errorf("focusReportLocked with 1004 off = %q, want nil", got)
		}
	})
}

func TestFocusReport_transitions(t *testing.T) {
	t.Run("the enable edge reports the current derived state", func(t *testing.T) {
		// Not nothing (which leaves an application that enabled 1004 while
		// already focused to guess) and not ESC[O (which is a lie).
		h := NewHandler([]string{"true"})
		state := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(state, true)
		if got := h.focusReportLocked(true); got != nil {
			t.Fatalf("focusReportLocked before the enable = %q, want nil", got)
		}
		h.screen.FocusReporting = true
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked on the 1004 enable edge with a focused client = %q, want ESC[I", got)
		}
	})

	t.Run("the steady level reports nothing", func(t *testing.T) {
		h := reportingHandler(t)
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Fatalf("focusReportLocked first call = %q, want ESC[I", got)
		}
		if got := h.focusReportLocked(true); got != nil {
			t.Errorf("focusReportLocked with no change = %q, want nil", got)
		}
	})

	t.Run("a focus change reports exactly one sequence", func(t *testing.T) {
		h := reportingHandler(t)
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Fatalf("focusReportLocked focused = %q, want ESC[I", got)
		}
		if got := h.focusReportLocked(false); !bytes.Equal(got, focusOutSeq) {
			t.Errorf("focusReportLocked after losing focus = %q, want ESC[O", got)
		}
		if got := h.focusReportLocked(false); got != nil {
			t.Errorf("focusReportLocked repeating the unfocused state = %q, want nil", got)
		}
	})

	t.Run("a 1004 disable then enable re-reports", func(t *testing.T) {
		h := reportingHandler(t)
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Fatalf("focusReportLocked focused = %q, want ESC[I", got)
		}
		writeScreen(t, h, "\x1b[?1004l")
		if got := h.focusReportLocked(true); got != nil {
			t.Fatalf("focusReportLocked on the disable = %q, want nil", got)
		}
		writeScreen(t, h, "\x1b[?1004h")
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked on re-enable = %q, want ESC[I (the state is re-reported)", got)
		}
	})
}

func TestFocusReport_reEnableInsideOneRead(t *testing.T) {
	tests := []struct {
		name   string
		reread string
	}{
		{"a 1004 disable and enable", "\x1b[?1004l\x1b[?1004h"},
		{"a soft reset and an enable", "\x1b[!p\x1b[?1004h"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, pr := focusPipeHandler(t)
			h.handlePTYData([]byte("\x1b[?1004h"))
			if got := readPipe(t, pr, len(focusOutSeq)); got != string(focusOutSeq) {
				t.Fatalf("report on the first enable = %q, want ESC[O", got)
			}
			h.handlePTYData([]byte(tc.reread))
			if got := readPipe(t, pr, len(focusOutSeq)); got != string(focusOutSeq) {
				t.Errorf("report after %q in one read = %q, want ESC[O (every enable re-reports)", tc.reread, got)
			}
		})
	}
}

// TestFocusReport_writeOrderFollowsAssignment_Concurrent holds one report's PTY
// write pending (the pipe is full) while a second trigger runs: focusTold must
// not move until the first is written, so the child reads the reports in the
// order focusTold took them.
func TestFocusReport_writeOrderFollowsAssignment_Concurrent(t *testing.T) {
	h, pr := focusPipeHandler(t)
	h.screen.FocusReporting = true
	a := h.registry.Add(&websocket.Conn{})
	b := h.registry.Add(&websocket.Conn{})
	h.registry.SetClientFocus(a, true)
	h.reportFocus()
	if got := readPipe(t, pr, len(focusInSeq)); got != string(focusInSeq) {
		t.Fatalf("first report = %q, want ESC[I", got)
	}
	fillPipe(t, h.ptmx)

	var wg sync.WaitGroup
	wg.Go(func() { // a's tab closes: nobody is focused
		h.registry.SetClientFocus(a, false)
		h.reportFocus()
	})
	awaitFocusTold(t, h, focusToldOut)
	wg.Go(func() { // the user clicks into b
		h.registry.SetClientFocus(b, true)
		h.reportFocus()
	})
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if told := lockedFocusTold(h); told != focusToldOut {
			t.Errorf("focusTold = %v while ESC[O is still unwritten, want it held at focus-out", told)
			break
		}
		time.Sleep(time.Millisecond)
	}

	wg.Wait()
	want := string(focusOutSeq) + string(focusInSeq)
	if tail := readPipeUntil(t, pr, want); tail != want {
		t.Errorf("PTY bytes after the filler = %q, want ESC[O then ESC[I", tail)
	}
	if told := lockedFocusTold(h); told != focusToldIn {
		t.Errorf("focusTold = %v, want focus-in, the last report written", told)
	}
}

// focusPipeHandler returns a started handler whose PTY is a pipe, and the
// pipe's read end. No process runs.
func focusPipeHandler(t *testing.T) (*Handler, *os.File) {
	t.Helper()
	h := NewHandler([]string{"true"}, WithLogger(nil))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	h.ptmx = pw
	h.started.Store(true)
	startReplyLoop(t, h)
	return h, pr
}

// readPipeUntil reads pr until the bytes after the 'x' filler hold want, or
// two seconds pass, and returns those bytes.
func readPipeUntil(t *testing.T, pr *os.File, want string) string {
	t.Helper()
	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var tail []byte
	buf := make([]byte, 64<<10)
	for len(tail) < len(want) {
		n, err := pr.Read(buf)
		tail = append(tail, bytes.TrimLeft(buf[:n], "x")...)
		if err != nil {
			break
		}
	}
	return string(tail)
}

func readPipe(t *testing.T, pr *os.File, n int) string {
	t.Helper()
	if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(pr, buf)
	if err != nil {
		t.Fatalf("reading %d PTY bytes: %v (got %q)", n, err, buf[:got])
	}
	return string(buf)
}

// fillPipe writes 'x' bytes into w until the pipe is full, so the next write
// blocks.
func fillPipe(t *testing.T, w *os.File) {
	t.Helper()
	raw, err := w.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	// A write of up to PIPE_BUF bytes is all or nothing, so the page-sized
	// writes stop with up to a page free and the single bytes take the rest.
	if err := raw.Write(func(fd uintptr) bool {
		for _, chunk := range [][]byte{bytes.Repeat([]byte("x"), 4096), []byte("x")} {
			for {
				if _, werr := syscall.Write(int(fd), chunk); werr != nil {
					break
				}
			}
		}
		return true
	}); err != nil {
		t.Fatalf("fill pipe: %v", err)
	}
}

func lockedFocusTold(h *Handler) toldFocus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.focusTold
}

func awaitFocusTold(t *testing.T, h *Handler, want toldFocus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for lockedFocusTold(h) != want {
		if time.Now().After(deadline) {
			t.Fatalf("focusTold never became %v", want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWithKeepUnfocused_optionSemantics pins the option's own wiring, carried
// over from the retired focusOutOnEnable suite: keep=false must reproduce the
// no-option default so a consumer can thread its own flag, and the last option
// in the list decides.
func TestWithKeepUnfocused_optionSemantics(t *testing.T) {
	focusedHandler := func(t *testing.T, opts ...Option) *Handler {
		t.Helper()
		h := reportingHandler(t, opts...)
		state := h.registry.Add(&websocket.Conn{})
		h.registry.SetClientFocus(state, true)
		return h
	}

	t.Run("absent: a focused client is reported focused", func(t *testing.T) {
		h := focusedHandler(t)
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked without WithKeepUnfocused = %q, want ESC[I", got)
		}
	})

	t.Run("explicit false reproduces the no-option default", func(t *testing.T) {
		h := focusedHandler(t, WithKeepUnfocused(false))
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked with WithKeepUnfocused(false) = %q, want ESC[I", got)
		}
	})

	t.Run("the last WithKeepUnfocused wins", func(t *testing.T) {
		h := focusedHandler(t, WithKeepUnfocused(true), WithKeepUnfocused(false))
		if got := h.focusReportLocked(h.registry.AnyClientFocused()); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked after true-then-false = %q, want ESC[I (last option wins)", got)
		}
		h2 := focusedHandler(t, WithKeepUnfocused(false), WithKeepUnfocused(true))
		if got := h2.focusReportLocked(h2.registry.AnyClientFocused()); !bytes.Equal(got, focusOutSeq) {
			t.Errorf("focusReportLocked after false-then-true = %q, want ESC[O (last option wins)", got)
		}
	})
}

// focusPTYFixture returns a handler whose PTY is a pipe and whose DEC 1004 mode
// is already enabled, one dialed client socket, and a read of what the handler
// wrote to that PTY. The process is marked started without one running, so
// nothing but the focus derivation touches the pipe.
func focusPTYFixture(t *testing.T) (ws *websocket.Conn, awaitPTY func(n int) string) {
	t.Helper()
	h := NewHandler([]string{"true"}, WithLogger(nil))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	h.ptmx = pw
	h.started.Store(true)
	h.screen.FocusReporting = true
	startReplyLoop(t, h)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	//nolint:bodyclose // library contract: Body is nil on success
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	cancel()
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	// Drain server frames, so a dispatch to this client can never block the
	// goroutine the triggers run on. Background context, not t.Context(): the
	// pump must outlive the test body and ends when the conn closes.
	go func() {
		for {
			if _, _, readErr := c.Read(context.Background()); readErr != nil {
				return
			}
		}
	}()

	return c, func(n int) string {
		t.Helper()
		if err := pr.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set pty read deadline: %v", err)
		}
		buf := make([]byte, n)
		got, readErr := io.ReadFull(pr, buf)
		if readErr != nil {
			t.Fatalf("reading %d PTY bytes: %v (got %q)", n, readErr, buf[:got])
		}
		return string(buf)
	}
}

// TestFocusReport_triggers drives a real socket and reads the reports off the
// PTY. The derivation above is pinned by calling it directly, which leaves the
// TRIGGER SET untested — and the trigger set is what an application depends on:
// with a report missing from attach, the focus control or detach, the derivation
// stays correct and the session stays silent. The detach case is the one with no
// other witness, because a departing client emits no focus-out of its own.
func TestFocusReport_triggers(t *testing.T) {
	ws, awaitPTY := focusPTYFixture(t)

	if got := awaitPTY(len(focusOutSeq)); got != string(focusOutSeq) {
		t.Fatalf("PTY write on attach = %q, want ESC[O (a socket starts unfocused)", got)
	}

	sendControl(t, ws, map[string]any{"type": "focus", "focused": true})
	if got := awaitPTY(len(focusInSeq)); got != string(focusInSeq) {
		t.Fatalf("PTY write after the focus control = %q, want ESC[I", got)
	}

	if err := ws.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("client close: %v", err)
	}
	if got := awaitPTY(len(focusOutSeq)); got != string(focusOutSeq) {
		t.Errorf("PTY write on detach = %q, want ESC[O (the session's focus dropped to nobody)", got)
	}
}
