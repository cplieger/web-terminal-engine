package terminal

import (
	"slices"

	"github.com/coder/websocket"
	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// flushFrameBuilder computes outbound flush frames by diffing the current
// screen against the previously sent state, keyed by absolute line index, not
// screen row: a pure scroll re-sends nothing, and the client stores each line
// idempotently by index.
type flushFrameBuilder struct {
	prevTitle       string
	prevRowWires    [][]vt.WireRun // last-sent window content; prevRowWires[i] is absolute index prevBase+i
	prevBase        uint64         // absolute index of prevRowWires[0]
	prevCurRow      int
	prevCurCol      int
	prevMouseMode   uint16
	prevKbdFlags    uint8
	prevMouseSGR    bool
	prevMousePixels bool
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
func (b *flushFrameBuilder) Reset() {
	b.prevRowWires = nil
	b.prevBase = 0
	b.prevCurValid = false
	// A new or resuming client needs modes and title re-announced, or it keeps
	// default modes (broken mouse and arrow encoding) and a stale title.
	b.modesAnnounced = false
	b.titleAnnounced = false
}

// Build computes the next outbound frame, or nil when there is nothing to
// send. Until sizeEstablished it only drains scrollback, since a frame would
// render a zero-size screen. committedBefore is the top row's absolute index
// before this frame's drain is committed.
func (b *flushFrameBuilder) Build(screen *vt.Screen, sizeEstablished bool, clients map[*websocket.Conn]uint64, committedBefore uint64) *flushFrame {
	if !sizeEstablished {
		screen.DrainScrollback()
		return nil
	}
	if screen.IsFlushHeld() {
		return nil
	}

	// An alt-screen transition (enter or exit) reshapes the whole
	// viewport; force a full window repaint so the client rebuilds it.
	altChanged := b.altTransitionPending(screen)
	if !b.prevAltValid || altChanged {
		b.Reset()
		b.prevAlt = screen.InAltScreen
		b.prevAltValid = true
	}

	drained, drainedWrapped := drainScrollback(screen)
	var scrollOut [][]vt.WireRun
	var scrollWrapped []bool
	// vt's scrollUpOnce drains alt-screen lines too, so commit only drain that
	// accrued on the main screen with no alt transition this tick.
	if !screen.InAltScreen && !altChanged && len(drained) > 0 {
		scrollOut, scrollWrapped = drained, drainedWrapped
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

	bell := screen.TakeBell()

	changed := b.diffWindow(rows, base)

	// Cursor-only moves (arrow keys, typing over an identical cell)
	// leave `changed` empty but still need the affected rows re-sent so
	// the inline cursor span moves. trackCursor folds those rows in.
	changed, cursorMoved := b.trackCursor(changed, len(rows), curRow, curCol)

	// A bell changes no cell, and the screen payload needs a changed row, so
	// fold in the cursor row for its cursorFlags bell bit to reach the client.
	if bell {
		changed = appendRowIfMissing(changed, curRow, len(rows))
	}

	if b.frameEmpty(screen, len(changed), len(scrollOut), cursorMoved) {
		return nil
	}

	return &flushFrame{
		clients:        clients,
		rows:           rows,
		scrollLines:    scrollOut,
		scrollWrapped:  scrollWrapped,
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

// altTransitionPending reports whether the screen's alt-screen state differs from the
// builder's last-observed value, i.e. an enter/exit has not yet been folded into a flush
// frame. Drain produced across such a transition belongs to the buffer just left and must
// not be committed as main-screen history.
func (b *flushFrameBuilder) altTransitionPending(screen *vt.Screen) bool {
	return b.prevAltValid && screen.InAltScreen != b.prevAlt
}

// frameEmpty reports whether the frame carries no observable change
// (no changed rows, no scroll lines, modes and title unchanged, cursor
// unmoved) so Build can drop it.
func (b *flushFrameBuilder) frameEmpty(screen *vt.Screen, changed, scrollOut int, cursorMoved bool) bool {
	return changed == 0 && scrollOut == 0 && b.modesStable(screen) && !cursorMoved && b.titleStable(screen)
}

// diffWindow returns the window-relative indices whose content at their
// absolute index differs from what was last sent. It updates the
// previous-window cache to the new window.
func (b *flushFrameBuilder) diffWindow(rows [][]vt.WireRun, base uint64) []int {
	var changed []int
	for y, row := range rows {
		prev, cached := b.prevRow(base + uint64(y))
		if !cached || !slices.Equal(prev, row) {
			changed = append(changed, y)
		}
	}
	b.prevRowWires = rows
	b.prevBase = base
	return changed
}

// prevRow returns the last-sent content for absolute index abs, and whether
// the cache covers that index at all.
//
// A blank row encodes to zero runs, so a nil slice is a real row value; read
// as "no baseline" it would make an idle screen emit a frame every tick.
func (b *flushFrameBuilder) prevRow(abs uint64) (runs []vt.WireRun, cached bool) {
	prevLen := uint64(len(b.prevRowWires))
	if prevLen == 0 || abs < b.prevBase || abs >= b.prevBase+prevLen {
		return nil, false
	}
	return b.prevRowWires[abs-b.prevBase], true
}

// modesStable reports whether the screen's DEC private mode state
// matches the last announced values.
func (b *flushFrameBuilder) modesStable(screen *vt.Screen) bool {
	return b.modesAnnounced &&
		screen.BracketedPaste == b.prevBracketed &&
		screen.AppCursorKeys == b.prevAppCursor &&
		screen.MouseSGR == b.prevMouseSGR &&
		screen.MousePixels == b.prevMousePixels &&
		screen.FocusReporting == b.prevFocusReport &&
		screen.MouseMode == b.prevMouseMode &&
		screen.AppKeypad == b.prevAppKeypad &&
		screen.ReverseVideo == b.prevReverseVid &&
		screen.KeyboardFlags() == b.prevKbdFlags
}

// buildModesPayload returns an encoded modes frame if any mode changed,
// or nil if stable.
func (b *flushFrameBuilder) buildModesPayload(screen *vt.Screen) []byte {
	if b.modesStable(screen) {
		return nil
	}
	b.prevBracketed = screen.BracketedPaste
	b.prevAppCursor = screen.AppCursorKeys
	b.prevMouseSGR = screen.MouseSGR
	b.prevMousePixels = screen.MousePixels
	b.prevFocusReport = screen.FocusReporting
	b.prevMouseMode = screen.MouseMode
	b.prevAppKeypad = screen.AppKeypad
	b.prevReverseVid = screen.ReverseVideo
	b.prevKbdFlags = screen.KeyboardFlags()
	b.modesAnnounced = true
	return encodeModesMsg(b.prevBracketed, b.prevAppCursor, b.prevMouseSGR, b.prevFocusReport, b.prevAppKeypad, b.prevReverseVid, b.prevMousePixels, b.prevMouseMode, b.prevKbdFlags)
}

// titleStable reports whether the screen's title matches the last
// announced value.
func (b *flushFrameBuilder) titleStable(screen *vt.Screen) bool {
	return b.titleAnnounced && screen.Title == b.prevTitle
}

// buildTitlePayload returns an encoded title frame if the title changed,
// or nil if stable.
func (b *flushFrameBuilder) buildTitlePayload(screen *vt.Screen) []byte {
	curTitle := screen.Title
	if b.titleAnnounced && curTitle == b.prevTitle {
		return nil
	}
	b.prevTitle = curTitle
	b.titleAnnounced = true
	return encodeTitleMsg(curTitle)
}

// trackCursor folds cursor-position changes into changed and updates the
// cached previous-position fields. Returns the (possibly amended)
// changed slice and whether the cursor moved versus the prior frame.
func (b *flushFrameBuilder) trackCursor(changed []int, rowCount, curRow, curCol int) ([]int, bool) {
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
