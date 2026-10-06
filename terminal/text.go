package terminal

import (
	"slices"
	"strings"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// TextSnapshot is a plain-text read of a session's terminal.
type TextSnapshot struct {
	// Text is the newest lines of output, oldest first, separated by "\n".
	Text string
	// AltScreen reports that a full-screen program holds the alternate screen.
	// Text is then the main screen and history beneath it, not what is shown.
	AltScreen bool
}

// Text returns the last maxLines lines of the session as plain text, oldest
// first: retained scrollback, lines scrolled off since the last frame, then the
// main screen. It reads the parsed screen, not the PTY stream, so wrapped rows
// join into one line, trailing blanks and empty lines are dropped, and no escape
// sequences remain. A non-positive maxLines returns every retained line, holding
// the session lock for time proportional to the retained depth. ok is false
// before the process has started. Text consumes nothing: the next frame still
// carries every pending line. Safe for concurrent use.
func (h *Handler) Text(maxLines int) (snap TextSnapshot, ok bool) {
	h.mu.Lock()
	if !h.started.Load() {
		h.mu.Unlock()
		return TextSnapshot{}, false
	}
	t := textTail{max: maxLines}
	h.collectTextLocked(&t)
	snap.AltScreen = h.screen.InAltScreen
	h.mu.Unlock()
	slices.Reverse(t.lines)
	snap.Text = strings.Join(t.lines, "\n")
	return snap, true
}

// collectTextLocked feeds t the main screen, the pending drain and then the
// ring, newest row first, until t is full. The caller holds h.mu.
func (h *Handler) collectTextLocked(t *textTail) {
	rows, wrapped := h.screen.MainScreenText()
	for y, row := range slices.Backward(rows) {
		if !t.push(row, wrapped[y]) {
			return
		}
	}
	// Build discards a drain that is alt content or straddles an alt
	// transition, so it never becomes history and is not text either.
	if !h.screen.InAltScreen && !h.builder.altTransitionPending(h.screen) {
		drainedWrapped := h.screen.DrainedWrapped()
		for i, line := range slices.Backward(h.screen.Drained) {
			if !t.push(runsText(line), drainedWrapped[i]) {
				return
			}
		}
	}
	for i := h.scrollback.Len() - 1; i >= 0; i-- {
		line, w := h.scrollback.at(i)
		if !t.push(runsText(line), w) {
			return
		}
	}
	t.flush()
}

// textTail assembles logical lines from physical rows fed newest first.
type textTail struct {
	lines []string // completed logical lines, newest first
	parts []string // rows of the line being assembled, newest first
	max   int      // non-positive means no limit
}

// push adds the row above every row pushed so far. wrapped reports that the
// row soft-wraps from the row above it, so the logical line is not complete
// yet. It returns false once max lines are complete.
func (t *textTail) push(row string, wrapped bool) bool {
	t.parts = append(t.parts, row)
	if wrapped {
		return true
	}
	t.flush()
	return t.max <= 0 || len(t.lines) < t.max
}

// flush completes the line being assembled. Empty lines below the newest
// content are dropped, so trailing blank screen rows never count toward max.
func (t *textTail) flush() {
	if len(t.parts) == 0 {
		return
	}
	slices.Reverse(t.parts)
	line := strings.TrimRight(strings.Join(t.parts, ""), " ")
	t.parts = t.parts[:0]
	if line == "" && len(t.lines) == 0 {
		return
	}
	t.lines = append(t.lines, line)
}

// runsText is a wire line's text, with the wide-character spacer marker that
// cellsToRuns writes for a cell holding no glyph removed.
func runsText(line []vt.WireRun) string {
	var b strings.Builder
	for _, r := range line {
		for _, ch := range r.T {
			if ch != '\uFFFF' {
				b.WriteRune(ch)
			}
		}
	}
	return b.String()
}
