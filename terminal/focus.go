package terminal

// DEC 1004 focus reporting, derived server-side because only the server sees the
// whole session: several devices on one PTY, a socket that dies silently, and
// WithKeepUnfocused. Sequences and mode: https://invisible-island.net/xterm/ctlseqs/ctlseqs.html

// focusInSeq / focusOutSeq are the DEC 1004 reports written to the PTY.
var (
	focusInSeq  = []byte("\x1b[I")
	focusOutSeq = []byte("\x1b[O")
)

// toldFocus is the focus state this session's process has been told, so the
// derivation can emit on transitions only. focusUnknown IS the 1004 enable edge:
// the next derivation reports whatever it computes.
type toldFocus int8

const (
	focusUnknown toldFocus = iota
	focusToldIn
	focusToldOut
)

// focusReportLocked derives the DEC 1004 answer from clientFocused (does any
// attached client report its widget focused), the session's own focus-reporting
// mode, and the WithKeepUnfocused declaration, returning the sequence to write or
// nil when nothing changed. The caller holds h.mu, reads clientFocused under it,
// and writes outside it.
//
// A 1004 disable forgets the reported state, so the next enable re-reports the
// CURRENT one rather than staying silent because it matches what the previous
// enable was told. That is xterm.js's _reportFocus behaviour.
func (h *Handler) focusReportLocked(clientFocused bool) []byte {
	if !h.screen.FocusReporting {
		h.focusTold = focusUnknown
		return nil
	}
	want := focusToldOut
	if !h.cfg.keepUnfocused && clientFocused {
		want = focusToldIn
	}
	if h.focusTold == want {
		return nil
	}
	h.focusTold = want
	if want == focusToldIn {
		return focusInSeq
	}
	return focusOutSeq
}

// reportFocus runs the derivation out of band — on a client's focus control, on
// attach, and on detach — and writes any report to the PTY. handlePTYData runs
// it inline instead, on the mode-change edge.
func (h *Handler) reportFocus() {
	if !h.started.Load() {
		return
	}
	h.mu.Lock()
	// The aggregate is read UNDER h.mu, which is what serializes one derivation
	// against the next: read before the lock, a focus control and a detach can
	// commit in the order that leaves focusTold describing the loser's reading,
	// and no further trigger is queued to correct it. One atomic load, so this
	// nests no registry lock inside h.mu.
	seq := h.focusReportLocked(h.registry.AnyClientFocused())
	ptmx := h.ptmx
	h.mu.Unlock()
	if len(seq) == 0 || ptmx == nil {
		return
	}
	ptmx.Write(seq) //nolint:errcheck // best-effort
}

// focusControl applies one client's reported widget focus and re-derives the
// session's DEC 1004 answer.
func (h *Handler) focusControl(state *clientState, focused bool) {
	h.registry.SetClientFocus(state, focused)
	h.reportFocus()
}
