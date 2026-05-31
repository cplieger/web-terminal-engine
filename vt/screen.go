// Package vt implements a minimal VT100 screen buffer for intercepting
// Ink's cursor-up + overwrite rendering pattern. It maintains a rows×cols
// grid, processes escape sequences to update it, and captures lines that
// scroll off the top (the "scrollback drain").
//
// File layout:
//
//	types.go   — public types (Color, Style, Cell, parser state enum)
//	screen.go  — the Screen struct, ctor, write entry, basic state ops
//	parse.go   — VT500-style byte-at-a-time state machine
//	csi.go     — CSI sequence dispatch + cell-level operations
//	sgr.go     — SGR (color/attribute) parsing + ANSI emission helpers
//	wire.go    — wire format (style runs) for the JSON protocol
//	html.go    — legacy HTML row rendering (used by tests)
//
// Derived from github.com/tonistiigi/vt100 (MIT license, Docker BuildKit).
package vt

import (
	"strings"
	"time"
)

// Screen is a minimal VT100 screen buffer with SGR support.
type Screen struct {
	FlushHoldUntil time.Time
	Cells          [][]Cell
	Drained        [][]WireRun
	Response       []byte
	ParserState
	altScreenState
	CursorState
	scrollBottom     int
	Width            int
	Height           int
	scrollTop        int
	lastPrintedRune  rune
	lastPrintedStyle Style
	OriginMode       bool
	AutoWrap         bool
	pendingWrap      bool
	BracketedPaste   bool
	AppCursorKeys    bool
	CursorBlink      bool
	CursorHidden     bool
	CursorStyle      uint8
	BellRing         bool
}

// CursorState holds cursor position, saved cursor, and current style.
// Embedded in Screen.
type CursorState struct {
	savedX int
	savedY int
	curY   int
	curX   int
	style  Style
}

// altScreenState holds alt-screen save/restore state. Embedded in Screen.
type altScreenState struct {
	savedMainCells        [][]Cell
	savedMainCurX         int
	savedMainCurY         int
	savedMainScrollTop    int
	savedMainScrollBottom int
	savedMainStyle        Style
	InAltScreen           bool
}

// New creates a screen buffer of the given dimensions.
func New(rows, cols int) *Screen {
	s := &Screen{Height: rows, Width: cols, Cells: make([][]Cell, rows), scrollTop: 0, scrollBottom: rows - 1, AutoWrap: true, CursorBlink: true}
	for i := range s.Cells {
		s.Cells[i] = makeRow(cols, Color{})
	}
	return s
}

// Write processes raw PTY output one byte at a time, updating the screen buffer.
func (s *Screen) Write(dt []byte) (int, error) {
	for _, b := range dt {
		s.feed(b)
	}
	return len(dt), nil
}

// DrainScrollback returns and clears accumulated scrolled-off lines.
func (s *Screen) DrainScrollback() [][]WireRun {
	d := s.Drained
	s.Drained = nil
	return d
}

// HoldFlush requests that the flush loop skip flushing the screen until
// the given time. Used to hide partial state during atomic batches —
// callers include CSI ?2026h ("synchronized output mode") and the resize
// handler (covers the SIGWINCH redraw window). Subsequent calls extend
// the hold but never shorten it.
func (s *Screen) HoldFlush(until time.Time) {
	if until.After(s.FlushHoldUntil) {
		s.FlushHoldUntil = until
	}
}

// ReleaseFlush clears any pending flush hold (called on CSI ?2026l).
func (s *Screen) ReleaseFlush() {
	s.FlushHoldUntil = time.Time{}
}

// IsFlushHeld reports whether the flush gate is currently held.
func (s *Screen) IsFlushHeld() bool {
	return time.Now().Before(s.FlushHoldUntil)
}

// CursorPos returns the current cursor row and column (0-indexed).
func (s *Screen) CursorPos() (row, col int) {
	return s.curY, s.curX
}

// Resize adjusts the screen dimensions, preserving existing content where
// possible. When dimensions actually change, cells are cleared so the host application's
// SIGWINCH redraw starts from a clean slate; on a no-op resize (e.g. client
// reconnect at the same size), content is preserved.
func (s *Screen) Resize(rows, cols int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	dimsChanged := rows != s.Height || cols != s.Width
	// When growing height, prepend empty rows at the TOP rather than
	// appending them at the bottom. xterm/iTerm/Terminal.app all
	// behave this way: existing content keeps its relative position
	// to the cursor (which moves down toward the new bottom), and
	// fresh empty space appears above (in scrollback territory).
	//
	// The previous append-at-bottom behaviour put empty rows BELOW
	// the cursor, where they remained visible until the host application's
	// SIGWINCH-driven redraw filled them — a window the user sees as
	// a "black gap between content and the input bar" on iPhone →
	// iPad device switches. Combined with the client's trim of
	// trailing empty rows in render.ts, this makes the gap impossible
	// regardless of whether the host's repaint happens immediately.
	if rows > s.Height {
		grow := rows - s.Height
		newRows := make([][]Cell, grow, grow+s.Height)
		for i := range newRows {
			newRows[i] = makeRow(s.Width, Color{})
		}
		s.Cells = append(newRows, s.Cells...)
		s.Height = rows
		// Cursor stays at the same content row (which is now further
		// down in the new larger buffer).
		s.curY += grow
	}
	if rows < s.Height {
		s.Cells = s.Cells[:rows]
		s.Height = rows
	}
	if cols != s.Width {
		for i := range s.Cells {
			old := s.Cells[i]
			s.Cells[i] = makeRow(cols, Color{})
			copy(s.Cells[i], old)
		}
		s.Width = cols
	}
	if s.curY >= s.Height {
		s.curY = s.Height - 1
	}
	if s.curX >= s.Width {
		s.curX = s.Width - 1
	}
	// Reset scroll region to full screen on resize.
	s.scrollTop = 0
	s.scrollBottom = s.Height - 1
	// Note: we deliberately do NOT clear cells or reset the cursor here.
	// the host application starts at the correct dimensions (first resize message
	// triggers ensureStarted) so initial-paint stale content is no longer
	// a concern. SIGWINCH will trigger the host application to redraw, which will
	// overwrite cells in place. Clearing here causes a visible "blank
	// screen + cursor at top-left" flash on every keyboard transition.
	_ = dimsChanged

	// Resize the saved main-screen buffer too if we're in alt-screen
	// mode, so exiting alt-screen restores correctly at the new size.
	if s.savedMainCells != nil {
		resized := make([][]Cell, rows)
		for i := range resized {
			row := makeRow(cols, Color{})
			if i < len(s.savedMainCells) {
				copy(row, s.savedMainCells[i])
			}
			resized[i] = row
		}
		s.savedMainCells = resized
		if s.savedMainCurY >= rows {
			s.savedMainCurY = rows - 1
		}
		if s.savedMainCurX >= cols {
			s.savedMainCurX = cols - 1
		}
	}
}

// RenderViewport returns the entire screen as ANSI-colored text. Used by
// tests and the legacy debug endpoint.
func (s *Screen) RenderViewport() string {
	var buf strings.Builder
	for y := range s.Cells {
		var prev Style
		for x, cell := range s.Cells[y] {
			if x == 0 || cell.Style != prev {
				buf.WriteString(sgrSequence(cell.Style))
			}
			prev = cell.Style
			buf.WriteRune(cell.Ch)
		}
		buf.WriteString("\x1b[0m")
		if y < len(s.Cells)-1 {
			buf.WriteString("\r\n")
		}
	}
	return buf.String()
}

// RowString returns the text content of a row (no styling).
func (s *Screen) RowString(y int) string {
	if y < 0 || y >= len(s.Cells) {
		return ""
	}
	var buf strings.Builder
	for _, cell := range s.Cells[y] {
		ch := cell.Ch
		if ch == 0 {
			ch = ' '
		}
		buf.WriteRune(ch)
	}
	return strings.TrimRight(buf.String(), " ")
}

// --- Cell-level helpers used across files ---

func (s *Screen) put(r rune) {
	// Pending wrap: if previous put landed cursor at width-1 and another
	// char arrives, wrap to next line first. xterm.js behavior.
	if s.pendingWrap {
		s.pendingWrap = false
		s.curX = 0
		s.curY++
		s.scrollIfNeeded()
	}
	if s.curY < s.Height && s.curX < s.Width {
		s.Cells[s.curY][s.curX] = Cell{Ch: r, Style: s.style}
	}
	s.lastPrintedRune = r
	s.lastPrintedStyle = s.style
	if s.curX == s.Width-1 {
		// Don't advance — set pending wrap for next put.
		s.pendingWrap = true
	} else {
		s.curX++
	}
}

func (s *Screen) scrollIfNeeded() {
	if s.curX >= s.Width {
		s.curX = 0
		s.curY++
	}
	if s.curY > s.scrollBottom {
		s.scrollUpOnce()
		s.curY = s.scrollBottom
	}
	if s.curY >= s.Height {
		s.Drained = append(s.Drained, cellsToRuns(s.Cells[0]))
		copy(s.Cells, s.Cells[1:])
		s.Cells[s.Height-1] = makeRow(s.Width, s.style.BG)
		s.curY = s.Height - 1
	}
}

func (s *Screen) eraseRegion(y1, x1, y2, x2 int) {
	for y := y1; y <= y2; y++ {
		if y < 0 || y >= s.Height {
			continue
		}
		for x := x1; x <= x2; x++ {
			if x < 0 || x >= s.Width {
				continue
			}
			s.Cells[y][x] = Cell{Ch: ' ', Style: Style{BG: s.style.BG}}
		}
	}
}

func makeRow(cols int, bg Color) []Cell {
	r := make([]Cell, cols)
	for i := range r {
		r[i] = Cell{Ch: ' ', Style: Style{BG: bg}}
	}
	return r
}

// enterAltScreen saves the current main-screen state and switches to a
// fresh alt buffer. Idempotent: a second h while already in alt is a
// no-op (matches xterm behavior).
func (s *Screen) enterAltScreen() {
	if s.InAltScreen {
		return
	}
	// Save main-screen cells (deep copy — alt screen will mutate them
	// directly, and we need to restore on exit).
	saved := make([][]Cell, len(s.Cells))
	for i, row := range s.Cells {
		saved[i] = make([]Cell, len(row))
		copy(saved[i], row)
	}
	s.savedMainCells = saved
	s.savedMainCurY = s.curY
	s.savedMainCurX = s.curX
	s.savedMainStyle = s.style
	s.savedMainScrollTop = s.scrollTop
	s.savedMainScrollBottom = s.scrollBottom

	// Fresh alt buffer.
	s.Cells = make([][]Cell, s.Height)
	for i := range s.Cells {
		s.Cells[i] = makeRow(s.Width, Color{})
	}
	s.curY, s.curX = 0, 0
	s.style = Style{}
	s.scrollTop = 0
	s.scrollBottom = s.Height - 1
	s.InAltScreen = true
}

// exitAltScreen restores the saved main-screen state.
func (s *Screen) exitAltScreen() {
	if !s.InAltScreen || s.savedMainCells == nil {
		return
	}
	// Resize-resilient restore: if dimensions changed while in alt,
	// truncate or pad rows to match current height.
	restored := make([][]Cell, s.Height)
	for i := range restored {
		switch {
		case i < len(s.savedMainCells) && len(s.savedMainCells[i]) == s.Width:
			restored[i] = s.savedMainCells[i]
		case i < len(s.savedMainCells):
			// Width changed — copy what fits, pad with spaces.
			row := makeRow(s.Width, Color{})
			copy(row, s.savedMainCells[i])
			restored[i] = row
		default:
			restored[i] = makeRow(s.Width, Color{})
		}
	}
	s.Cells = restored
	s.curY = s.savedMainCurY
	s.curX = s.savedMainCurX
	s.style = s.savedMainStyle
	s.scrollTop = s.savedMainScrollTop
	s.scrollBottom = s.savedMainScrollBottom
	if s.curY >= s.Height {
		s.curY = s.Height - 1
	}
	if s.curX >= s.Width {
		s.curX = s.Width - 1
	}
	if s.scrollBottom >= s.Height {
		s.scrollBottom = s.Height - 1
	}
	s.savedMainCells = nil
	s.InAltScreen = false
}
