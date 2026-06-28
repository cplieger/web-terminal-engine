package terminal

import (
	"slices"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// FlushFrameBuilder computes outbound flush frames by diffing the
// current screen state against the previously sent state.
//
// The diff is keyed by ABSOLUTE LINE INDEX, not by screen row. When the
// screen scrolls up by K lines, a given line of content keeps its
// absolute index (it just moves from window row y+K to window row y), so
// comparing by absolute index means a pure scroll re-sends nothing: only
// genuinely new or rewritten lines go on the wire. This is what lets the
// client store every line idempotently by absolute index and never see a
// duplicate (see docs/REBUILD.md section 6).
type FlushFrameBuilder struct {
	prevTitle       string
	prevRowWires    [][]vt.WireRun // last-sent window content; prevRowWires[i] is absolute index prevBase+i
	prevBase        uint64         // absolute index of prevRowWires[0]
	prevCurRow      int
	prevCurCol      int
	prevMouseMode   uint16
	prevMouseSGR    bool
	prevAppCursor   bool
	prevFocusReport bool
	prevAppKeypad   bool
	prevReverseVid  bool
	prevBracketed   bool
	prevAlt         bool
	modesAnnounced  bool
	titleAnnounced  bool
	prevCurValid    bool
	prevAltValid    bool
}

// Reset clears the previous-window cache, forcing the next frame to
// treat every window row as changed. Used after a resize, a new client
// connect, a resume, or an alt-screen transition.
func (b *FlushFrameBuilder) Reset() {
	b.prevRowWires = nil
	b.prevBase = 0
	b.prevCurValid = false
}

// Build computes the next outbound frame from the current screen state
// and client snapshot. committedBefore is the absolute index of the top
// screen row before this frame's drain is committed to history. Returns
// nil if there is nothing to send.
func (b *FlushFrameBuilder) Build(screen *vt.Screen, resized bool, clients map[*websocket.Conn]uint64, committedBefore uint64) *FlushFrame {
	if !resized {
		screen.DrainScrollback()
		return nil
	}
	if screen.IsFlushHeld() {
		return nil
	}

	// An alt-screen transition (enter or exit) reshapes the whole
	// viewport; force a full window repaint so the client rebuilds it.
	if !b.prevAltValid || screen.InAltScreen != b.prevAlt {
		b.Reset()
		b.prevAlt = screen.InAltScreen
		b.prevAltValid = true
	}

	drained := screen.DrainScrollback()
	var scrollOut [][]vt.WireRun
	if !screen.InAltScreen && len(drained) > 0 {
		scrollOut = drained
	}
	// In the alt screen the window is ephemeral and accrues no history,
	// so the absolute base stays frozen at committedBefore. On the main
	// screen the base advances past the lines just committed.
	base := committedBefore + uint64(len(scrollOut))

	rows := make([][]vt.WireRun, screen.Height)
	for y := range screen.Height {
		rows[y] = screen.RenderRowWire(y)
	}
	curRow, curCol := screen.CursorPos()

	bell := screen.BellRing
	screen.BellRing = false

	changed := b.diffWindow(rows, base)

	// Cursor-only moves (arrow keys, typing over an identical cell)
	// leave `changed` empty but still need the affected rows re-sent so
	// the inline cursor span moves. trackCursor folds those rows in.
	changed, cursorMoved := b.trackCursor(changed, len(rows), curRow, curCol)

	if len(changed) == 0 && len(scrollOut) == 0 && b.modesStable(screen) && !cursorMoved && b.titleStable(screen) {
		return nil
	}

	return &FlushFrame{
		clients:        clients,
		rows:           rows,
		scrollLines:    scrollOut,
		scrollFirstIdx: committedBefore,
		base:           base,
		changed:        changed,
		curRow:         curRow,
		curCol:         curCol,
		screenHeight:   screen.Height,
		altActive:      screen.InAltScreen,
		cursorStyle:    screen.CursorStyle,
		cursorHidden:   screen.CursorHidden,
		cursorBlink:    screen.CursorBlink,
		modesPayload:   b.buildModesPayload(screen),
		titlePayload:   b.buildTitlePayload(screen),
		bell:           bell,
	}
}

// diffWindow returns the window-relative indices whose content at their
// absolute index differs from what was last sent. It updates the
// previous-window cache to the new window.
func (b *FlushFrameBuilder) diffWindow(rows [][]vt.WireRun, base uint64) []int {
	var changed []int
	prevLen := uint64(len(b.prevRowWires))
	for y, row := range rows {
		abs := base + uint64(y)
		var prev []vt.WireRun
		if prevLen > 0 && abs >= b.prevBase && abs < b.prevBase+prevLen {
			prev = b.prevRowWires[abs-b.prevBase]
		}
		if prev == nil || !runsEqual(prev, row) {
			changed = append(changed, y)
		}
	}
	b.prevRowWires = rows
	b.prevBase = base
	return changed
}

// modesStable reports whether the screen's DEC private mode state
// matches the last announced values.
func (b *FlushFrameBuilder) modesStable(screen *vt.Screen) bool {
	return b.modesAnnounced &&
		screen.BracketedPaste == b.prevBracketed &&
		screen.AppCursorKeys == b.prevAppCursor &&
		screen.MouseSGR == b.prevMouseSGR &&
		screen.FocusReporting == b.prevFocusReport &&
		screen.MouseMode == b.prevMouseMode &&
		screen.AppKeypad == b.prevAppKeypad &&
		screen.ReverseVideo == b.prevReverseVid
}

// buildModesPayload returns an encoded modes frame if any mode changed,
// or nil if stable.
func (b *FlushFrameBuilder) buildModesPayload(screen *vt.Screen) []byte {
	if b.modesStable(screen) {
		return nil
	}
	b.prevBracketed = screen.BracketedPaste
	b.prevAppCursor = screen.AppCursorKeys
	b.prevMouseSGR = screen.MouseSGR
	b.prevFocusReport = screen.FocusReporting
	b.prevMouseMode = screen.MouseMode
	b.prevAppKeypad = screen.AppKeypad
	b.prevReverseVid = screen.ReverseVideo
	b.modesAnnounced = true
	return encodeModesMsg(0, b.prevBracketed, b.prevAppCursor, b.prevMouseSGR, b.prevFocusReport, b.prevAppKeypad, b.prevReverseVid, b.prevMouseMode)
}

// titleStable reports whether the screen's title matches the last
// announced value.
func (b *FlushFrameBuilder) titleStable(screen *vt.Screen) bool {
	return b.titleAnnounced && screen.Title == b.prevTitle
}

// buildTitlePayload returns an encoded title frame if the title changed,
// or nil if stable.
func (b *FlushFrameBuilder) buildTitlePayload(screen *vt.Screen) []byte {
	curTitle := screen.Title
	if b.titleAnnounced && curTitle == b.prevTitle {
		return nil
	}
	b.prevTitle = curTitle
	b.titleAnnounced = true
	return encodeTitleMsg(0, curTitle)
}

// trackCursor folds cursor-position changes into changed and updates the
// cached previous-position fields. Returns the (possibly amended)
// changed slice and whether the cursor moved versus the prior frame.
func (b *FlushFrameBuilder) trackCursor(changed []int, rowCount, curRow, curCol int) ([]int, bool) {
	cursorMoved := !b.prevCurValid || curRow != b.prevCurRow || curCol != b.prevCurCol
	if cursorMoved && b.prevCurValid {
		changed = appendRowIfMissing(changed, b.prevCurRow, rowCount)
		changed = appendRowIfMissing(changed, curRow, rowCount)
	}
	b.prevCurRow = curRow
	b.prevCurCol = curCol
	b.prevCurValid = true
	return changed, cursorMoved
}

// appendRowIfMissing returns changed with y appended iff y is in
// [0, rowCount) and not already present.
func appendRowIfMissing(changed []int, y, rowCount int) []int {
	if y < 0 || y >= rowCount {
		return changed
	}
	if slices.Contains(changed, y) {
		return changed
	}
	return append(changed, y)
}
