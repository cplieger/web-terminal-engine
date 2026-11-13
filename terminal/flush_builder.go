package terminal

import (
	"bytes"
	"slices"

	"github.com/coder/websocket"
	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// flushFrameBuilder computes outbound flush frames by diffing the current
// screen against the previously sent state, keyed by absolute line index, not
// screen row: a pure scroll re-sends nothing, and the client stores each line
// idempotently by index.
type flushFrameBuilder struct {
	prevRowWires [][]vt.WireRun // last-sent window content; prevRowWires[i] is absolute index prevBase+i
	announce     announcement
	prevBase     uint64 // absolute index of prevRowWires[0]
	prevCurRow   int
	prevCurCol   int
	prevAlt      bool
	prevCurValid bool
	prevAltValid bool
}

// announcement is the session's current modes, title and grid frames, each
// with a version that moves when its content does. announceTo decides which
// socket is sent which frame.
type announcement struct {
	titleText string
	modes     []byte
	title     []byte
	grid      []byte
	modesVer  uint64
	titleVer  uint64
	gridVer   uint64
}

// RepaintWindow clears the previous-window cache, forcing the next frame to
// treat every window row as changed.
func (b *flushFrameBuilder) RepaintWindow() {
	b.prevRowWires = nil
	b.prevBase = 0
	b.prevCurValid = false
}

// refreshAnnouncement brings the announcement up to the screen's modes, title
// and grid and returns it. Versions start at 1, so a socket's zero record is
// always behind.
func (b *flushFrameBuilder) refreshAnnouncement(screen *vt.Screen) announcement {
	modes := encodeModesMsg(screen.BracketedPaste, screen.AppCursorKeys, screen.MouseSGR,
		screen.FocusReporting, screen.AppKeypad, screen.ReverseVideo, screen.MousePixels,
		screen.MouseMode, screen.KeyboardFlags())
	if b.announce.modesVer == 0 || !bytes.Equal(modes, b.announce.modes) {
		b.announce.modes = modes
		b.announce.modesVer++
	}
	if b.announce.titleVer == 0 || screen.Title != b.announce.titleText {
		b.announce.titleText = screen.Title
		b.announce.title = encodeTitleMsg(screen.Title)
		b.announce.titleVer++
	}
	if grid := encodeGridMsg(screen.Width, screen.Height); b.announce.gridVer == 0 || !bytes.Equal(grid, b.announce.grid) {
		b.announce.grid = grid
		b.announce.gridVer++
	}
	return b.announce
}

// behind reports whether st has not been told a's modes, title, or the grid it
// asked for.
func (a *announcement) behind(st *clientState) bool {
	return st.modesTold.Load() != a.modesVer || st.titleTold.Load() != a.titleVer ||
		(st.gridFrames.Load() && st.gridTold.Load() != a.gridVer)
}

// Build computes the next outbound frame, or nil when there is nothing to
// send. Until sizeEstablished it only drains scrollback, since a frame would
// render a zero-size screen. committedBefore is the top row's absolute index
// before this frame's drain is committed. writers are the sockets the frame
// goes to; one not yet told the current modes or title makes a frame.
func (b *flushFrameBuilder) Build(screen *vt.Screen, sizeEstablished bool, clients map[*websocket.Conn]uint64, writers map[*websocket.Conn]*clientState, committedBefore uint64) *flushFrame {
	if !sizeEstablished {
		screen.DrainScrollback()
		return nil
	}
	if screen.IsFlushHeld() {
		return nil
	}

	// An alt-screen transition (enter or exit) reshapes the whole
	// viewport; force a full window repaint so the client rebuilds it.
	if !b.prevAltValid || screen.InAltScreen != b.prevAlt {
		b.RepaintWindow()
		b.prevAlt = screen.InAltScreen
		b.prevAltValid = true
	}

	// vt.Screen.Drained is main-screen history only, so all of it commits,
	// whichever buffer is showing now. The base is one past the newest
	// committed line on either buffer: in alt it is the frozen main base.
	scrollOut, scrollWrapped := drainScrollback(screen)
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

	ann := b.refreshAnnouncement(screen)
	if len(changed) == 0 && len(scrollOut) == 0 && !cursorMoved && !anyBehind(&ann, writers) {
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
		announce:       ann,
		bell:           bell,
	}
}

// anyBehind reports whether a socket in writers has not been told ann.
func anyBehind(ann *announcement, writers map[*websocket.Conn]*clientState) bool {
	for _, st := range writers {
		if ann.behind(st) {
			return true
		}
	}
	return false
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
