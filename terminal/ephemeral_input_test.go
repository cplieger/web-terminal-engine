package terminal

// The ephemeral-input control: an UNCOUNTED best-effort write path.
//
// The payloads here are SGR-1006 mouse reports as xterm specifies them —
// CSI < Pb ; Px ; Py M for a press, ...m for a release, coordinates 1-based:
//   https://invisible-island.net/xterm/ctlseqs/ctlseqs.html
// The channel exists because such a report has no sequence number and describes
// a screen that may since have been repainted, so it must never be replayed.

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A press of button 0 at column 10, row 5, in the SGR-1006 encoding.
const sgrPressReport = "\x1b[<0;10;5M"

// ephemeralFixture returns a started handler whose PTY is a pipe, a client state
// with a resolved input ledger, and a drain that closes the write end and
// returns every byte the handler wrote to it.
func ephemeralFixture(t *testing.T) (h *Handler, state *clientState, drain func() string) {
	t.Helper()
	h = NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	h.ptmx = pw
	h.started.Store(true)
	state = &clientState{}
	h.registry.ResolveSession(state, "ephemeral-session")
	return h, state, func() string {
		_ = pw.Close()
		out, readErr := io.ReadAll(pr)
		if readErr != nil {
			t.Fatalf("reading what the handler wrote to the PTY: %v", readErr)
		}
		return string(out)
	}
}

// bytesReceived is the session ledger the client's outbox reconciles against.
func bytesReceived(t *testing.T, state *clientState) uint64 {
	t.Helper()
	sess := state.session.Load()
	if sess == nil {
		t.Fatalf("client state has no session; the fixture must resolve one")
	}
	return sess.bytesReceived
}

// TestEphemeralInput_writesWithoutCounting is the ledger invariant, and it
// asserts the COUNTER rather than the absence of a frame: a counted byte here
// pushes the server's received count past the client's bytesSent, whose clamp
// pins bytesAcked and empties the outbox, silently acking keystrokes that were
// never delivered.
func TestEphemeralInput_writesWithoutCounting(t *testing.T) {
	h, state, drain := ephemeralFixture(t)

	h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput, Data: sgrPressReport})

	if got := bytesReceived(t, state); got != 0 {
		t.Errorf("bytesReceived after an ephemeral write = %d, want 0 (the payload must not enter the resume ledger)", got)
	}
	// The counter is genuinely observable through this fixture, so the zero above
	// is a fact about the write path and not about the assertion.
	h.registry.IncrementReceived(state, 3)
	if got := bytesReceived(t, state); got != 3 {
		t.Errorf("bytesReceived after IncrementReceived(3) = %d, want 3", got)
	}
	if got := drain(); got != sgrPressReport {
		t.Errorf("bytes written to the PTY = %q, want %q", got, sgrPressReport)
	}
}

func TestEphemeralInput_refusals(t *testing.T) {
	t.Run("an empty payload writes nothing", func(t *testing.T) {
		h, state, drain := ephemeralFixture(t)
		h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput})
		if got := drain(); got != "" {
			t.Errorf("bytes written for an empty payload = %q, want none", got)
		}
	})

	t.Run("an over-cap payload writes nothing", func(t *testing.T) {
		h, state, drain := ephemeralFixture(t)
		// Literal lengths, not maxEphemeralInputBytes arithmetic: a payload
		// derived from the constant moves with it, so raising the cap would widen
		// the uncounted channel with this test still green.
		oversized := strings.Repeat("a", 65)
		h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput, Data: oversized})
		if got := drain(); got != "" {
			t.Errorf("bytes written for a 65-byte payload = %q, want none", got)
		}
	})

	t.Run("a payload exactly at the cap is accepted", func(t *testing.T) {
		// The boundary is the largest legal payload, so a refusal here would
		// reject a report the channel is sized to carry.
		h, state, drain := ephemeralFixture(t)
		atCap := strings.Repeat("a", 64)
		h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput, Data: atCap})
		if got := drain(); got != atCap {
			t.Errorf("bytes written for a 64-byte payload = %q, want the payload", got)
		}
	})

	t.Run("the cap is 64 bytes", func(t *testing.T) {
		// Stated so a change to it is a decision rather than a side effect: an
		// SGR-1006 report is at most ~19 bytes, and this is the only bound on a
		// write path the resume ledger does not count.
		if maxEphemeralInputBytes != 64 {
			t.Errorf("maxEphemeralInputBytes = %d, want 64", maxEphemeralInputBytes)
		}
	})

	t.Run("an empty bucket writes nothing", func(t *testing.T) {
		h, state, drain := ephemeralFixture(t)
		// Spend the bucket without waiting for its refill window.
		state.ephemeralLast = time.Now()
		state.ephemeralTokens = 0
		h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput, Data: sgrPressReport})
		if got := drain(); got != "" {
			t.Errorf("bytes written with an empty token bucket = %q, want none", got)
		}
	})

	t.Run("a payload before the process starts writes nothing", func(t *testing.T) {
		h, state, drain := ephemeralFixture(t)
		h.started.Store(false)
		h.ephemeralInputControl(state, &controlMsg{Type: ctlTypeEphemeralInput, Data: sgrPressReport})
		if got := drain(); got != "" {
			t.Errorf("bytes written before the process started = %q, want none (a mouse report must not boot it)", got)
		}
	})
}

// TestTakeEphemeralToken_boundsAFlood pins the bucket itself: a full bucket
// serves ephemeralBurst reports back to back and then refuses, which is what
// bounds an uncounted write path the reliable path's outbox does not bound.
func TestTakeEphemeralToken_boundsAFlood(t *testing.T) {
	state := &clientState{}
	for i := range int(ephemeralBurst) {
		if !state.takeEphemeralToken() {
			t.Fatalf("takeEphemeralToken() refused report %d of the %d-report burst", i+1, int(ephemeralBurst))
		}
	}
	if state.takeEphemeralToken() {
		t.Errorf("takeEphemeralToken() granted a %dth report, want a refusal past the burst", int(ephemeralBurst)+1)
	}
}

// TestControlDisposition_knownControls pins that both new controls are
// RECOGNIZED. The disposition is what stops a pre-latch socket being closed on
// them and a post-latch one silently dropping them, so an unrecognized control
// would take the whole capability out with no error anywhere.
func TestControlDisposition_knownControls(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"ephemeralInput", `{"type":"ephemeralInput","data":"x"}`},
		{"focus", `{"type":"focus","focused":true}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, state, _ := ephemeralFixture(t)
			d := h.handleControl(&websocket.Conn{}, state, []byte(tc.payload), nil)
			if !d.parsed {
				t.Fatalf("handleControl(%s).parsed = false, want true", tc.payload)
			}
			if !d.known {
				t.Errorf("handleControl(%s).known = false, want true (an unknown control is dropped post-latch and closes the socket pre-latch)", tc.payload)
			}
		})
	}
}
