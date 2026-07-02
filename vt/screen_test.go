package vt

import (
	"strings"
	"testing"
)

// restoredAltCursor enters alt-screen, overrides the saved main cursor, exits
// alt-screen, and returns the restored (clamped) cursor on an 5x8 screen.
func restoredAltCursor(savedX, savedY int) (gotX, gotY int) {
	s := New(5, 8)
	s.enterAltScreen(1049)
	s.savedMainCurX = savedX
	s.savedMainCurY = savedY
	s.exitAltScreen(1049)
	return s.curX, s.curY
}

// exitedAltScreen builds an alt-screen with the given saved main-screen
// scrollBottom, exits alt-screen, and returns the resulting Screen so the
// post-restore scrollBottom clamp can be inspected.
func exitedAltScreen(t *testing.T, height, width, savedScrollBottom int) *Screen {
	t.Helper()
	s := New(height, width)
	s.InAltScreen = true
	s.savedMainCells = make([][]Cell, height)
	for i := range s.savedMainCells {
		s.savedMainCells[i] = make([]Cell, width)
	}
	s.savedMainScrollBottom = savedScrollBottom
	s.exitAltScreen(1049)
	return s
}

// TestNewInitializesSingleShiftSentinel verifies New() seeds singleShft with
// the -1 "no single shift pending" sentinel.
func TestNewInitializesSingleShiftSentinel(t *testing.T) {
	s := New(2, 20)
	if got := int(s.singleShft); got != -1 {
		t.Errorf("New().singleShft = %d, want -1", got)
	}
}

// --- RenderViewport ---

// TestRenderViewportStartsRunPerRow verifies a row of cells sharing one uniform
// non-default style emits its SGR sequence exactly once (at the row start).
func TestRenderViewportStartsRunPerRow(t *testing.T) {
	s := New(1, 2)
	s.Cells[0][0] = Cell{Ch: 'A', Style: Style{Bold: true}}
	s.Cells[0][1] = Cell{Ch: 'B', Style: Style{Bold: true}}
	out := s.RenderViewport()
	if n := strings.Count(out, "\x1b[0;1m"); n != 1 {
		t.Errorf("uniform bold row emitted bold SGR %d times, want 1; out=%q", n, out)
	}
}

// TestRenderViewportEmitsOnStyleChange verifies a cell whose style differs from
// the previous cell emits a fresh SGR sequence.
func TestRenderViewportEmitsOnStyleChange(t *testing.T) {
	s := New(1, 2)
	s.Cells[0][0] = Cell{Ch: 'A', Style: Style{Bold: true}}
	s.Cells[0][1] = Cell{Ch: 'B', Style: Style{Italic: true}}
	out := s.RenderViewport()
	if !strings.Contains(out, "\x1b[0;3m") {
		t.Errorf("did not emit italic SGR on style change; out=%q", out)
	}
}

// TestRenderViewportRowSeparators verifies CRLF separates rows but is not
// appended after the last row.
func TestRenderViewportRowSeparators(t *testing.T) {
	s := New(3, 1)
	got := s.RenderViewport()
	want := "\x1b[0m \x1b[0m\r\n\x1b[0m \x1b[0m\r\n\x1b[0m \x1b[0m"
	if got != want {
		t.Errorf("RenderViewport(3x1) = %q, want %q", got, want)
	}
	if n := strings.Count(got, "\r\n"); n != 2 {
		t.Errorf("CRLF count = %d, want 2", n)
	}
}

// --- RowString ---

// TestRowStringOutOfRangeRow verifies RowString returns "" for a row index
// equal to Height rather than indexing out of range.
func TestRowStringOutOfRangeRow(t *testing.T) {
	s := New(3, 5)
	var got string
	if didPanic(func() { got = s.RowString(3) }) {
		t.Fatalf("RowString(len(Cells)) panicked: out-of-range row not guarded")
	}
	if got != "" {
		t.Errorf("RowString(3) = %q, want \"\"", got)
	}
}

// --- put guards ---

// TestPutGuardCurY verifies put() does not write (or panic) when curY equals
// Height.
func TestPutGuardCurY(t *testing.T) {
	s := New(4, 6)
	s.curY = 4 // == Height
	s.curX = 0
	if didPanic(func() { s.put('A') }) {
		t.Errorf("put with curY==Height panicked: write guard not effective")
	}
}

// TestPutGuardCurX verifies put() does not write (or panic) when curX equals
// Width.
func TestPutGuardCurX(t *testing.T) {
	s := New(4, 6)
	s.curY = 0
	s.curX = 6 // == Width
	if didPanic(func() { s.put('A') }) {
		t.Errorf("put with curX==Width panicked: write guard not effective")
	}
}

// TestPutSpacerGuardCurY verifies the width-2 spacer write is guarded against
// curY == Height.
func TestPutSpacerGuardCurY(t *testing.T) {
	s := New(4, 6)
	s.curY = 4                                // == Height
	s.curX = 2                                // not Width-1, so the early width-2 wrap does not fire
	if didPanic(func() { s.put('\u4e16') }) { // CJK wide rune, width 2
		t.Errorf("put(width-2) with curY==Height panicked: spacer guard not effective")
	}
}

// --- scrollIfNeeded ---

// TestScrollIfNeededWrapsAtWidth verifies scrollIfNeeded wraps to the next line
// when curX equals Width.
func TestScrollIfNeededWrapsAtWidth(t *testing.T) {
	s := New(10, 5)
	s.curX = 5 // == Width
	s.curY = 0
	s.scrollIfNeeded()
	if s.curX != 0 || s.curY != 1 {
		t.Errorf("scrollIfNeeded curX==Width: got (curX=%d, curY=%d), want (0, 1)", s.curX, s.curY)
	}
}

// TestScrollIfNeededNoScrollAtBottom verifies the region does not scroll when
// curY equals scrollBottom.
func TestScrollIfNeededNoScrollAtBottom(t *testing.T) {
	s := New(3, 4)
	s.Cells[0][0] = Cell{Ch: 'A'}
	s.curX = 0
	s.curY = 2 // == scrollBottom
	s.scrollIfNeeded()
	if len(s.Drained) != 0 {
		t.Errorf("scrollIfNeeded curY==scrollBottom drained %d line(s), want 0", len(s.Drained))
	}
	if s.Cells[0][0].Ch != 'A' {
		t.Errorf("scrollIfNeeded curY==scrollBottom scrolled buffer: Cells[0][0]=%q, want 'A'", s.Cells[0][0].Ch)
	}
}

// TestScrollIfNeededDrainsAtHeight verifies the buffer scrolls (drains one line)
// when curY reaches Height.
func TestScrollIfNeededDrainsAtHeight(t *testing.T) {
	s := New(3, 4)
	s.Cells[0][0] = Cell{Ch: 'A'}
	s.scrollBottom = 3 // == Height, isolates the curY>=Height branch
	s.curX = 0
	s.curY = 3 // == Height
	s.scrollIfNeeded()
	if len(s.Drained) != 1 {
		t.Errorf("scrollIfNeeded curY==Height drained %d line(s), want 1", len(s.Drained))
	}
	if s.curY != 2 {
		t.Errorf("scrollIfNeeded curY==Height: curY=%d, want 2", s.curY)
	}
}

// --- eraseRegion ---

// TestEraseRegionInclusiveRow verifies eraseRegion's row range is inclusive: a
// single-row region (y1==y2) erases that row.
func TestEraseRegionInclusiveRow(t *testing.T) {
	s := New(3, 5)
	s.Cells[1][0] = Cell{Ch: 'X'}
	s.eraseRegion(1, 0, 1, 0)
	if s.Cells[1][0].Ch != ' ' {
		t.Errorf("eraseRegion(1,0,1,0): Cells[1][0]=%q, want ' '", s.Cells[1][0].Ch)
	}
}

// TestEraseRegionSkipsOutOfRangeRow verifies a row index equal to Height is
// skipped rather than indexed out of range.
func TestEraseRegionSkipsOutOfRangeRow(t *testing.T) {
	s := New(3, 4)
	if didPanic(func() { s.eraseRegion(3, 0, 3, 0) }) { // y == Height
		t.Errorf("eraseRegion at y==Height panicked: out-of-range row not skipped")
	}
}

// TestEraseRegionSkipsOutOfRangeCol verifies a column index equal to Width is
// skipped rather than indexed out of range.
func TestEraseRegionSkipsOutOfRangeCol(t *testing.T) {
	s := New(3, 4)
	if didPanic(func() { s.eraseRegion(0, 4, 0, 4) }) { // x == Width
		t.Errorf("eraseRegion at x==Width panicked: out-of-range column not skipped")
	}
}

// --- alt screen ---

// TestEnterAltScreenScrollBottom verifies enterAltScreen sets scrollBottom to
// Height-1.
func TestEnterAltScreenScrollBottom(t *testing.T) {
	s := New(5, 8)
	s.scrollBottom = 99 // poison; enterAltScreen must overwrite with Height-1
	s.enterAltScreen(1049)
	if s.scrollBottom != 4 {
		t.Errorf("enterAltScreen scrollBottom = %d, want 4 (Height-1)", s.scrollBottom)
	}
}

// TestExitAltScreenClampsCurY verifies exitAltScreen clamps the restored cursor
// row to Height-1 when it is out of range, and preserves an in-range row.
func TestExitAltScreenClampsCurY(t *testing.T) {
	cases := []struct {
		name   string
		savedY int
		wantY  int
	}{
		{"equal height clamps", 5, 4},
		{"in range unchanged", 0, 0},
	}
	for _, c := range cases {
		gotX, gotY := restoredAltCursor(0, c.savedY)
		if gotY != c.wantY {
			t.Errorf("%s: exitAltScreen curY (savedY=%d) = %d, want %d", c.name, c.savedY, gotY, c.wantY)
		}
		if gotX != 0 {
			t.Errorf("%s: exitAltScreen curX = %d, want 0", c.name, gotX)
		}
	}
}

// TestExitAltScreenClampsCurX verifies exitAltScreen clamps the restored cursor
// column to Width-1 when it is out of range, and preserves an in-range column.
func TestExitAltScreenClampsCurX(t *testing.T) {
	cases := []struct {
		name   string
		savedX int
		wantX  int
	}{
		{"equal width clamps", 8, 7},
		{"in range unchanged", 0, 0},
	}
	for _, c := range cases {
		gotX, gotY := restoredAltCursor(c.savedX, 0)
		if gotX != c.wantX {
			t.Errorf("%s: exitAltScreen curX (savedX=%d) = %d, want %d", c.name, c.savedX, gotX, c.wantX)
		}
		if gotY != 0 {
			t.Errorf("%s: exitAltScreen curY = %d, want 0", c.name, gotY)
		}
	}
}

// TestExitAltScreenClampsScrollBottom verifies exitAltScreen clamps the restored
// scrollBottom to Height-1 when it is out of range, and preserves an in-range
// value.
func TestExitAltScreenClampsScrollBottom(t *testing.T) {
	s := exitedAltScreen(t, 5, 10, 5) // savedScrollBottom == Height
	if s.scrollBottom != 4 {
		t.Errorf("exitAltScreen(savedScrollBottom=5, height=5): scrollBottom = %d, want 4", s.scrollBottom)
	}
	s2 := exitedAltScreen(t, 5, 10, 2)
	if s2.scrollBottom != 2 {
		t.Errorf("exitAltScreen(savedScrollBottom=2, height=5): scrollBottom = %d, want 2", s2.scrollBottom)
	}
}

// TestAltScreenEnterExitPreservesMain verifies the alt-screen enter/exit
// sequence (CSI ?1049h/l) restores the main screen content on exit.
func TestAltScreenEnterExitPreservesMain(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("Main"))
	s.Write([]byte("\x1b[?1049h"))
	if !s.InAltScreen {
		t.Fatal("not in alt screen")
	}
	s.Write([]byte("Alt"))
	s.Write([]byte("\x1b[?1049l"))
	if s.InAltScreen {
		t.Fatal("still in alt screen")
	}
	if s.Cells[0][0].Ch != 'M' {
		t.Errorf("main screen not restored: got %q", s.Cells[0][0].Ch)
	}
}
