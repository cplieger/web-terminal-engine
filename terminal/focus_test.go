package terminal

// SPEC-FIRST focus-reporting tests.
//
// The two sequences and their meaning come from xterm's own specification, not
// from focus.go: DEC private mode 1004 ("Send FocusIn/FocusOut events") makes the
// terminal send CSI I when it gains focus and CSI O when it loses it.
//   https://invisible-island.net/xterm/ctlseqs/ctlseqs.html
//
// Two derivation rules are taken from named reference implementations, because
// the spec says what a terminal reports and not how a multi-viewer terminal
// decides what its focus IS:
//
//   - Any focused attached client focuses the session. tmux's
//     window_pane_update_focus (window.c) derives a pane's focus from the pane
//     being active plus SOME attached client carrying CLIENT_FOCUSED, so one
//     focused viewer among several is focus-in.
//   - Enabling 1004 reports the CURRENT state, not only later transitions.
//     xterm.js's _reportFocus does exactly that when an application sets the
//     mode, so an application that enables 1004 while already focused is told so
//     rather than being left to infer it from silence.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
		// a focus-gated notifier (kiro-cli's OSC 9) keeps emitting.
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
		h.screen.FocusReporting = false
		if got := h.focusReportLocked(true); got != nil {
			t.Fatalf("focusReportLocked on the disable = %q, want nil", got)
		}
		h.screen.FocusReporting = true
		if got := h.focusReportLocked(true); !bytes.Equal(got, focusInSeq) {
			t.Errorf("focusReportLocked on re-enable = %q, want ESC[I (the state is re-reported)", got)
		}
	})
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
