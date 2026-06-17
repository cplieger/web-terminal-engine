package vt

// Mutant-killing tests for unit vterm-u5 (package vt, screen.go).
// Internal test package so unexported methods/fields are reachable.
// All helpers/identifiers are prefixed gk_vterm_u5_ and all test names
// are prefixed Test_gk_vterm_u5_ to avoid collisions with sibling units
// that share this package dir.

import (
	"strings"
	"testing"
)

// gk_vterm_u5_didPanic runs fn and reports whether it panicked. Used for
// boundary mutants whose only observable difference is an out-of-bounds
// access (mutant proceeds past a guard and indexes out of range).
func gk_vterm_u5_didPanic(fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// gk_vterm_u5_resizeSaved enters alt-screen (so savedMainCells != nil),
// overrides the saved main cursor, resizes, and returns the post-resize
// saved main cursor (X, Y). Exercises the Resize savedMainCurX clamp.
func gk_vterm_u5_resizeSaved(savedX, savedY, newRows, newCols int) (gotX, gotY int) {
	s := New(8, 12)
	s.enterAltScreen()
	s.savedMainCurX = savedX
	s.savedMainCurY = savedY
	s.Resize(newRows, newCols)
	return s.savedMainCurX, s.savedMainCurY
}

// gk_vterm_u5_exitAltCursor enters alt-screen, overrides the saved main
// cursor, then exits alt-screen and returns the restored+clamped cursor.
func gk_vterm_u5_exitAltCursor(savedX, savedY int) (gotX, gotY int) {
	s := New(5, 8)
	s.enterAltScreen()
	s.savedMainCurX = savedX
	s.savedMainCurY = savedY
	s.exitAltScreen()
	return s.curX, s.curY
}

// screen.go:260:22 (NEGATION + BOUNDARY) and 261:27 (ARITHMETIC + INVERT):
// Resize clamps savedMainCurX to cols-1 when savedMainCurX >= cols.
func Test_gk_vterm_u5_resize_saved_curx_clamp(t *testing.T) {
	cases := []struct {
		name    string
		savedX  int
		newCols int
		wantX   int
	}{
		// 8 >= 5 -> clamp to cols-1 == 4. Kills negation(>=->'<') and
		// arithmetic/invert on cols-1 (mutant would give 6).
		{"over_clamps_to_cols_minus_1", 8, 5, 4},
		// 5 >= 5 (equal) -> clamp to 4. Kills boundary (>= -> >), which
		// would leave 5 unclamped.
		{"equal_clamps_to_cols_minus_1", 5, 5, 4},
		// 2 >= 5 false -> unchanged (2). Kills negation other direction
		// ('<' would be true and clamp to 4).
		{"under_unchanged", 2, 5, 2},
	}
	for _, c := range cases {
		gotX, _ := gk_vterm_u5_resizeSaved(c.savedX, 0, 8, c.newCols)
		if gotX != c.wantX {
			t.Errorf("%s: Resize savedMainCurX (savedX=%d, cols=%d) = %d, want %d",
				c.name, c.savedX, c.newCols, gotX, c.wantX)
		}
	}
}

// screen.go:273:9 (NEGATION of x == 0): RenderViewport emits an SGR at the
// start of every row. A row of cells with a single uniform non-default
// style emits that SGR exactly once; the mutant (x != 0) re-emits per cell.
func Test_gk_vterm_u5_renderviewport_x0_starts_run(t *testing.T) {
	s := New(1, 2)
	s.Cells[0][0] = Cell{Ch: 'A', Style: Style{Bold: true}}
	s.Cells[0][1] = Cell{Ch: 'B', Style: Style{Bold: true}}
	out := s.RenderViewport()
	if n := strings.Count(out, "\x1b[0;1m"); n != 1 {
		t.Errorf("RenderViewport uniform bold row emitted bold SGR %d times, want 1 (x==0 row-start); out=%q", n, out)
	}
}

// screen.go:273:28 (NEGATION of cell.Style != prev): the second cell's style
// differs from the first, so its SGR must be emitted. Mutant (== prev) would
// suppress it.
func Test_gk_vterm_u5_renderviewport_style_change_emits(t *testing.T) {
	s := New(1, 2)
	s.Cells[0][0] = Cell{Ch: 'A', Style: Style{Bold: true}}
	s.Cells[0][1] = Cell{Ch: 'B', Style: Style{Italic: true}}
	out := s.RenderViewport()
	if !strings.Contains(out, "\x1b[0;3m") {
		t.Errorf("RenderViewport did not emit italic SGR on style change (cell.Style != prev); out=%q", out)
	}
}

// screen.go:280:8 (NEGATION + BOUNDARY) and 280:22 (ARITHMETIC + INVERT):
// a CRLF separates rows but is NOT appended after the last row (y < len-1).
func Test_gk_vterm_u5_renderviewport_row_separators(t *testing.T) {
	s := New(3, 1)
	got := s.RenderViewport()
	want := "\x1b[0m \x1b[0m\r\n\x1b[0m \x1b[0m\r\n\x1b[0m \x1b[0m"
	if got != want {
		t.Errorf("RenderViewport(3x1 default) = %q, want %q (CRLF only between rows, none after last)", got, want)
	}
	if n := strings.Count(got, "\r\n"); n != 2 {
		t.Errorf("RenderViewport(3x1) CRLF count = %d, want 2", n)
	}
}

// screen.go:289:16 (BOUNDARY of y >= len(Cells)): RowString returns "" for an
// out-of-range row. At y == len(Cells) the original returns ""; the mutant
// (y > len) skips the guard and indexes out of range (panics).
func Test_gk_vterm_u5_rowstring_index_boundary(t *testing.T) {
	s := New(3, 5)
	var got string
	p := gk_vterm_u5_didPanic(func() { got = s.RowString(3) }) // y == len(Cells)
	if p {
		t.Errorf("RowString(len(Cells)) panicked: y>=len(Cells) boundary not guarded (line 289)")
	}
	if !p && got != "" {
		t.Errorf("RowString(3) = %q, want \"\" (out-of-range row)", got)
	}
}

// screen.go:330:12 (BOUNDARY of s.curY < s.Height): the cell-write guard must
// reject curY == Height. Original skips the write; mutant (<=) writes
// Cells[Height][...] and panics.
func Test_gk_vterm_u5_put_guard_cury(t *testing.T) {
	s := New(4, 6)
	s.curY = 4 // == Height
	s.curX = 0
	p := gk_vterm_u5_didPanic(func() { s.put('A') })
	if p {
		t.Errorf("put with curY==Height panicked: curY<Height write guard not effective (line 330)")
	}
}

// screen.go:330:33 (BOUNDARY of s.curX < s.Width): the cell-write guard must
// reject curX == Width. Original skips the write; mutant (<=) writes
// Cells[..][Width] and panics.
func Test_gk_vterm_u5_put_guard_curx(t *testing.T) {
	s := New(4, 6)
	s.curY = 0
	s.curX = 6 // == Width
	p := gk_vterm_u5_didPanic(func() { s.put('A') })
	if p {
		t.Errorf("put with curX==Width panicked: curX<Width write guard not effective (line 330)")
	}
}

// screen.go:339:33 (BOUNDARY of s.curY < s.Height): the width-2 spacer-cell
// write guard must reject curY == Height. A width-2 rune triggers the spacer
// path; original skips the spacer write, mutant (<=) panics.
func Test_gk_vterm_u5_put_spacer_guard_cury(t *testing.T) {
	s := New(4, 6)
	s.curY = 4                                            // == Height
	s.curX = 2                                            // not Width-1, so the early width-2 wrap does not fire
	p := gk_vterm_u5_didPanic(func() { s.put('\u4e16') }) // CJK wide rune, width 2
	if p {
		t.Errorf("put(width-2) with curY==Height panicked: spacer curY<Height guard not effective (line 339)")
	}
}

// screen.go:361:12 (BOUNDARY of s.curX >= s.Width): scrollIfNeeded wraps when
// curX == Width. Original wraps (curX=0, curY++); mutant (>) does not.
func Test_gk_vterm_u5_scrollifneeded_curx_wrap(t *testing.T) {
	s := New(10, 5)
	s.curX = 5 // == Width
	s.curY = 0
	s.scrollIfNeeded()
	if s.curX != 0 || s.curY != 1 {
		t.Errorf("scrollIfNeeded curX==Width: got (curX=%d, curY=%d), want (0, 1) (line 361 >=)", s.curX, s.curY)
	}
}

// screen.go:365:12 (BOUNDARY of s.curY > s.scrollBottom): at curY ==
// scrollBottom the region must NOT scroll. Original leaves the buffer
// untouched; mutant (>=) scrolls (drains the top row, shifts content).
func Test_gk_vterm_u5_scrollifneeded_cury_scrollbottom(t *testing.T) {
	s := New(3, 4)
	s.Cells[0][0] = Cell{Ch: 'A'}
	s.Cells[1][0] = Cell{Ch: 'B'}
	s.Cells[2][0] = Cell{Ch: 'C'}
	s.curX = 0
	s.curY = 2 // == scrollBottom (Height-1)
	s.scrollIfNeeded()
	if len(s.Drained) != 0 {
		t.Errorf("scrollIfNeeded curY==scrollBottom drained %d line(s), want 0 (line 365 must use >)", len(s.Drained))
	}
	if s.Cells[0][0].Ch != 'A' {
		t.Errorf("scrollIfNeeded curY==scrollBottom scrolled buffer: Cells[0][0]=%q, want 'A'", s.Cells[0][0].Ch)
	}
}

// screen.go:369:12 (BOUNDARY of s.curY >= s.Height): at curY == Height the
// buffer must scroll one line off the top (drain). scrollBottom is set to
// Height so the curY>scrollBottom branch is skipped and 369 is isolated.
func Test_gk_vterm_u5_scrollifneeded_cury_height(t *testing.T) {
	s := New(3, 4)
	s.Cells[0][0] = Cell{Ch: 'A'}
	s.scrollBottom = 3 // == Height, skips the curY>scrollBottom branch
	s.curX = 0
	s.curY = 3 // == Height
	s.scrollIfNeeded()
	if len(s.Drained) != 1 {
		t.Errorf("scrollIfNeeded curY==Height drained %d line(s), want 1 (line 369 >=)", len(s.Drained))
	}
	if s.curY != 2 {
		t.Errorf("scrollIfNeeded curY==Height: curY=%d, want 2 (Height-1)", s.curY)
	}
}

// screen.go:378:17 (BOUNDARY of y <= y2): eraseRegion's row loop is inclusive;
// a single-row region (y1==y2) must erase that row. Mutant (y < y2) erases
// nothing.
func Test_gk_vterm_u5_eraseregion_inclusive_row(t *testing.T) {
	s := New(3, 5)
	s.Cells[1][0] = Cell{Ch: 'X'}
	s.eraseRegion(1, 0, 1, 0)
	if s.Cells[1][0].Ch != ' ' {
		t.Errorf("eraseRegion(1,0,1,0): Cells[1][0]=%q, want ' ' (line 378 y<=y2 inclusive)", s.Cells[1][0].Ch)
	}
}

// screen.go:379:17 (BOUNDARY of y >= s.Height): a row index equal to Height
// must be skipped via continue. Original skips it; mutant (y > Height)
// proceeds and indexes Cells[Height] out of range (panics).
func Test_gk_vterm_u5_eraseregion_y_out_of_bounds(t *testing.T) {
	s := New(3, 4)
	p := gk_vterm_u5_didPanic(func() { s.eraseRegion(3, 0, 3, 0) }) // y == Height
	if p {
		t.Errorf("eraseRegion at y==Height panicked: y>=Height skip guard not effective (line 379)")
	}
}

// screen.go:383:18 (BOUNDARY of x >= s.Width): a column index equal to Width
// must be skipped. Original skips it; mutant (x > Width) proceeds and indexes
// Cells[y][Width] out of range (panics).
func Test_gk_vterm_u5_eraseregion_x_out_of_bounds(t *testing.T) {
	s := New(3, 4)
	p := gk_vterm_u5_didPanic(func() { s.eraseRegion(0, 4, 0, 4) }) // x == Width
	if p {
		t.Errorf("eraseRegion at x==Width panicked: x>=Width skip guard not effective (line 383)")
	}
}

// screen.go:428:28 (ARITHMETIC + INVERT of s.Height - 1): enterAltScreen sets
// scrollBottom to Height-1. Poison scrollBottom first so the assertion proves
// line 428 ran; mutant (Height+1) would give 6.
func Test_gk_vterm_u5_enteralt_scrollbottom(t *testing.T) {
	s := New(5, 8)
	s.scrollBottom = 99 // poison; line 428 must overwrite with Height-1
	s.enterAltScreen()
	if s.scrollBottom != 4 { // Height-1 == 5-1
		t.Errorf("enterAltScreen scrollBottom = %d, want 4 (Height-1; line 428)", s.scrollBottom)
	}
}

// screen.go:459:12 (NEGATION + BOUNDARY of s.curY >= s.Height): exitAltScreen
// clamps the restored cursor row to Height-1 when it is >= Height.
func Test_gk_vterm_u5_exitalt_cury_clamp(t *testing.T) {
	cases := []struct {
		name   string
		savedY int
		wantY  int
	}{
		// savedY == Height -> clamp to Height-1 (4). Kills boundary
		// (>=->'>' leaves 5) and negation('<' leaves 5).
		{"equal_height_clamps", 5, 4},
		// savedY in range -> unchanged (0). Kills negation other direction
		// ('<' would clamp 0 to 4).
		{"in_range_unchanged", 0, 0},
	}
	for _, c := range cases {
		gotX, gotY := gk_vterm_u5_exitAltCursor(0, c.savedY)
		if gotY != c.wantY {
			t.Errorf("%s: exitAltScreen curY (savedY=%d) = %d, want %d (line 459)", c.name, c.savedY, gotY, c.wantY)
		}
		if gotX != 0 {
			t.Errorf("%s: exitAltScreen curX = %d, want 0", c.name, gotX)
		}
	}
}

// screen.go:462:12 (NEGATION + BOUNDARY of s.curX >= s.Width): exitAltScreen
// clamps the restored cursor column to Width-1 when it is >= Width.
func Test_gk_vterm_u5_exitalt_curx_clamp(t *testing.T) {
	cases := []struct {
		name   string
		savedX int
		wantX  int
	}{
		// savedX == Width -> clamp to Width-1 (7). Kills boundary
		// (>=->'>' leaves 8) and negation('<' leaves 8).
		{"equal_width_clamps", 8, 7},
		// savedX in range -> unchanged (0). Kills negation other direction
		// ('<' would clamp 0 to 7).
		{"in_range_unchanged", 0, 0},
	}
	for _, c := range cases {
		gotX, gotY := gk_vterm_u5_exitAltCursor(c.savedX, 0)
		if gotX != c.wantX {
			t.Errorf("%s: exitAltScreen curX (savedX=%d) = %d, want %d (line 462)", c.name, c.savedX, gotX, c.wantX)
		}
		if gotY != 0 {
			t.Errorf("%s: exitAltScreen curY = %d, want 0", c.name, gotY)
		}
	}
}
