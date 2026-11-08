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
// nil when nothing changed. The caller holds h.mu; replyLoop owns the write.
// Every 1004 enable re-reports the current state, as xterm.js's _reportFocus
// does.
func (h *Handler) focusReportLocked(clientFocused bool) []byte {
	if h.screen.FocusEnableSeq != h.focusEnableSeen {
		h.focusEnableSeen = h.screen.FocusEnableSeq
		h.focusTold = focusUnknown
	}
	if !h.screen.FocusReporting {
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

// reportFocus asks replyLoop to re-derive the DEC 1004 answer: on a client's
// focus control, on attach and on detach.
func (h *Handler) reportFocus() {
	h.wakeReplies()
}

// focusControl applies one client's reported widget focus and re-derives the
// session's DEC 1004 answer.
func (h *Handler) focusControl(state *clientState, focused bool) {
	h.registry.SetClientFocus(state, focused)
	h.reportFocus()
}
