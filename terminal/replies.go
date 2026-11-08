package terminal

import "context"

// maxPendingReplies bounds the replies queued while replyLoop is blocked on a
// PTY write (the child has stopped reading its input). Beyond it a reply is
// dropped, as a reply's write was already best-effort, so a child that floods
// queries without reading cannot grow the queue without limit.
const maxPendingReplies = 64 << 10

// queueReplyLocked queues the screen's answers to device queries for replyLoop.
// The caller holds h.mu.
func (h *Handler) queueReplyLocked(resp []byte) {
	if len(resp) == 0 || len(h.pendingReplies)+len(resp) > maxPendingReplies {
		return
	}
	h.pendingReplies = append(h.pendingReplies, resp...)
}

// wakeReplies asks replyLoop for one pass. The 1-buffered channel coalesces
// any number of wakes into one pending pass.
func (h *Handler) wakeReplies() {
	select {
	case h.replyWake <- struct{}{}:
	default:
	}
}

// replyLoop is the only writer of the session's replies to its PTY: the queued
// device-query answers, then the DEC 1004 report it derives itself. One
// goroutine writes them, so the child reads them in derivation order, and a
// write blocked on the child's full input queue stalls no other goroutine: the
// read loop keeps draining output, which is what lets the child read again.
func (h *Handler) replyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.replyWake:
		}
		h.mu.Lock()
		out := h.pendingReplies
		h.pendingReplies = nil
		// One atomic load, so this nests no registry lock inside h.mu.
		if seq := h.focusReportLocked(h.registry.AnyClientFocused()); len(seq) > 0 {
			out = append(out, seq...)
		}
		h.mu.Unlock()
		if len(out) > 0 {
			h.ptmx.Write(out) //nolint:errcheck // best-effort
		}
	}
}
