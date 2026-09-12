package terminal

import "time"

const (
	// maxEphemeralInputBytes caps one ephemeral payload. An SGR-1006 mouse report
	// is at most ~19 bytes, so 64 leaves headroom for a longer report form while
	// staying too small to carry a payload that is not one report.
	maxEphemeralInputBytes = 64

	// ephemeralBurst/ephemeralRefill bound this UNCOUNTED write path, which the
	// reliable path's outbox and ack ledger do not reach. 8 ms (125/s) clears the
	// 120/s a coalesced drag produces on a 120 Hz display.
	ephemeralBurst  = 240.0
	ephemeralRefill = 8 * time.Millisecond
)

// takeEphemeralToken spends one token from the socket's ephemeral-input bucket,
// reporting false when it is empty. Called only from the socket's read loop
// (control messages are serialized per socket), so the state needs no lock. The
// bucket starts full: ephemeralLast's zero value dates the last refill to the
// epoch, so the first call tops it up to the burst.
func (st *clientState) takeEphemeralToken() bool {
	now := time.Now()
	if !st.ephemeralLast.IsZero() {
		st.ephemeralTokens += now.Sub(st.ephemeralLast).Seconds() / ephemeralRefill.Seconds()
	} else {
		st.ephemeralTokens = ephemeralBurst
	}
	if st.ephemeralTokens > ephemeralBurst {
		st.ephemeralTokens = ephemeralBurst
	}
	st.ephemeralLast = now
	if st.ephemeralTokens < 1 {
		return false
	}
	st.ephemeralTokens--
	return true
}

// ephemeralInputControl writes an `ephemeralInput` payload to the PTY as
// BEST-EFFORT input: uncounted, never retransmitted, not recoverable — a mouse
// report describes a screen a resume has since repainted.
//
// It MUST NOT call IncrementReceived. The resume ledger is byte-exact both ways:
// a counted byte here pushes the server's received count past the client's
// bytesSent, whose clamp pins bytesAcked and empties the outbox, silently acking
// keystrokes the server never received.
func (h *Handler) ephemeralInputControl(state *clientState, c *controlMsg) {
	if c.Data == "" || len(c.Data) > maxEphemeralInputBytes {
		h.cfg.logger.Debug("terminal: ephemeral input payload out of range", "bytes", len(c.Data))
		return
	}
	if !state.takeEphemeralToken() {
		h.cfg.logger.Warn("terminal: ephemeral input throttled")
		return
	}
	if !h.started.Load() {
		// No ensureStarted, unlike handleBinaryFrame: a mouse report must not BOOT
		// the process, because there is nothing on screen it can have been aimed at.
		h.cfg.logger.Debug("terminal: ephemeral input before process start; dropping")
		return
	}
	if _, err := h.ptmx.WriteString(c.Data); err != nil {
		h.cfg.logger.Debug("terminal: ephemeral pty write", "error", err)
		return
	}
	h.markDirty()
}
