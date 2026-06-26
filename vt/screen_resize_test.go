package vt

import "testing"

// TestResizeGrowsAtTop verifies that growing the screen height inserts
// new empty rows at the TOP of the buffer rather than appending them
// at the bottom. Existing content keeps its position relative to the
// cursor (which moves down by the grow amount), and fresh empty space
// shows up where xterm/iTerm/Terminal.app would put it: above the
// content, in scrollback territory. The previous append-at-bottom
// behaviour left empty rows BELOW the cursor, where they remained
// visible until the host application's SIGWINCH-driven redraw filled them — the
// "black gap between content and the input bar after iPhone → iPad
// switch" symptom that motivated this change.
func TestResizeGrowsAtTop(t *testing.T) {
	s := New(5, 10)
	// Mark the original content so we can locate it after resize.
	for x := range s.Width {
		s.Cells[0][x].Ch = 'A'
		s.Cells[4][x].Ch = 'E'
	}
	s.curY = 4 // cursor on the last row

	s.Resize(10, 10)

	if s.Height != 10 {
		t.Fatalf("Height = %d, want 10", s.Height)
	}
	// Original row 0 should now be at row 5, original row 4 at row 9.
	if s.Cells[5][0].Ch != 'A' {
		t.Errorf("expected 'A' at row 5 col 0 after grow, got %q", s.Cells[5][0].Ch)
	}
	if s.Cells[9][0].Ch != 'E' {
		t.Errorf("expected 'E' at row 9 col 0 after grow, got %q", s.Cells[9][0].Ch)
	}
	// Cursor should have moved down by the grow amount.
	if s.curY != 9 {
		t.Errorf("curY = %d, want 9 (was 4 + grow=5)", s.curY)
	}
	// Newly-prepended rows should be empty.
	for y := range 5 {
		for x := range s.Width {
			if s.Cells[y][x].Ch != 0 && s.Cells[y][x].Ch != ' ' {
				t.Errorf("row %d col %d should be empty, got %q", y, x, s.Cells[y][x].Ch)
			}
		}
	}
}

// TestResizeShrinksFromBottom: shrinking still drops rows from the
// bottom (truncates s.Cells[:rows]). The cursor clamps into the new
// range. This is unchanged behaviour — verifying it didn't regress.
func TestResizeShrinksFromBottom(t *testing.T) {
	s := New(10, 10)
	for x := range s.Width {
		s.Cells[0][x].Ch = 'A'
		s.Cells[9][x].Ch = 'B'
	}
	s.curY = 9

	s.Resize(5, 10)

	if s.Height != 5 {
		t.Fatalf("Height = %d, want 5", s.Height)
	}
	if s.Cells[0][0].Ch != 'A' {
		t.Errorf("top row 'A' should survive shrink, got %q", s.Cells[0][0].Ch)
	}
	if s.curY != 4 {
		t.Errorf("curY = %d, want 4 (clamped from 9)", s.curY)
	}
}

// TestResizeWidthOnly verifies that growing/shrinking only the width
// (no height change) preserves all rows in place — no prepend/append.
func TestResizeWidthOnly(t *testing.T) {
	s := New(3, 5)
	s.Cells[1][0].Ch = 'X'

	s.Resize(3, 20)

	if s.Width != 20 || s.Height != 3 {
		t.Fatalf("dims = %dx%d, want 20x3", s.Width, s.Height)
	}
	if s.Cells[1][0].Ch != 'X' {
		t.Errorf("'X' should still be at row 1 col 0, got %q", s.Cells[1][0].Ch)
	}
}

// makeSavedMain builds a saved-main-screen buffer where row r carries the
// marker rune 'A'+r in column 0 (the rest spaces), so a row's identity survives
// a savedMainCells rebuild and can be asserted by content.
func makeSavedMain(rows, cols int) [][]Cell {
	g := make([][]Cell, rows)
	for i := range g {
		row := make([]Cell, cols)
		for j := range row {
			row[j] = Cell{Ch: ' '}
		}
		if cols > 0 {
			row[0] = Cell{Ch: rune('A' + i)}
		}
		g[i] = row
	}
	return g
}

// resizeSavedCursor enters alt-screen, overrides the saved main cursor, resizes,
// and returns the post-resize saved main cursor.
func resizeSavedCursor(savedX, savedY, newRows, newCols int) (gotX, gotY int) {
	s := New(8, 12)
	s.enterAltScreen()
	s.savedMainCurX = savedX
	s.savedMainCurY = savedY
	s.Resize(newRows, newCols)
	return s.savedMainCurX, s.savedMainCurY
}

// TestResizeTabStopsGrow verifies that widening a non-nil tabStops slice fills
// default stops at positive multiples of 8 only, never at column 0, bounded by
// the new column count.
func TestResizeTabStopsGrow(t *testing.T) {
	s := New(5, 4)
	s.tabStops = make([]bool, 0) // non-nil, length 0 -> fill loop starts at i=0
	s.Resize(5, 16)              // width is a multiple of 8
	if got := len(s.tabStops); got != 16 {
		t.Fatalf("len(tabStops) after grow = %d, want 16", got)
	}
	if s.tabStops[0] {
		t.Errorf("tabStops[0] = true, want false (column 0 never gets a stop)")
	}
	if s.tabStops[1] {
		t.Errorf("tabStops[1] = true, want false (1 is not a multiple of 8)")
	}
	if s.tabStops[7] {
		t.Errorf("tabStops[7] = true, want false (7 is not a multiple of 8)")
	}
	if !s.tabStops[8] {
		t.Errorf("tabStops[8] = false, want true (8 is a positive multiple of 8)")
	}
	if s.tabStops[15] {
		t.Errorf("tabStops[15] = true, want false (15 is not a multiple of 8)")
	}
}

// TestResizeRebuildsSavedMainCells verifies a resize taken while a saved
// main-screen buffer exists rebuilds that buffer at the new dimensions.
func TestResizeRebuildsSavedMainCells(t *testing.T) {
	s := New(5, 10)
	s.enterAltScreen() // populates savedMainCells (5x10)
	s.Resize(8, 20)
	if got := len(s.savedMainCells); got != 8 {
		t.Fatalf("savedMainCells rows after resize = %d, want 8", got)
	}
	if got := len(s.savedMainCells[0]); got != 20 {
		t.Errorf("savedMainCells cols after resize = %d, want 20", got)
	}
}

// TestResizeSavedMainCopyBounds verifies the bounded copy of saved rows into the
// resized saved buffer: existing rows are copied by content, new rows are blank,
// and the index guard never reads out of range.
func TestResizeSavedMainCopyBounds(t *testing.T) {
	s := New(2, 4)
	s.InAltScreen = true
	s.savedMainCells = makeSavedMain(2, 4) // row0[0]='A', row1[0]='B'
	s.Resize(4, 4)                         // grow rows 2 -> 4
	if got := len(s.savedMainCells); got != 4 {
		t.Fatalf("savedMainCells rows after resize = %d, want 4", got)
	}
	if s.savedMainCells[0][0].Ch != 'A' {
		t.Errorf("savedMainCells[0][0].Ch = %q, want 'A' (copied)", s.savedMainCells[0][0].Ch)
	}
	if s.savedMainCells[1][0].Ch != 'B' {
		t.Errorf("savedMainCells[1][0].Ch = %q, want 'B' (copied)", s.savedMainCells[1][0].Ch)
	}
	if s.savedMainCells[2][0].Ch != ' ' {
		t.Errorf("savedMainCells[2][0].Ch = %q, want ' ' (blank pad row)", s.savedMainCells[2][0].Ch)
	}
	if s.savedMainCells[3][0].Ch != ' ' {
		t.Errorf("savedMainCells[3][0].Ch = %q, want ' ' (blank pad row)", s.savedMainCells[3][0].Ch)
	}
}

// TestResizeClampsSavedCursorY verifies clamping of the saved main-screen cursor
// row to the new height (boundary clamps, in-range is preserved).
func TestResizeClampsSavedCursorY(t *testing.T) {
	s := New(3, 4)
	s.InAltScreen = true
	s.savedMainCells = makeSavedMain(3, 4)
	s.savedMainCurY = 4
	s.Resize(4, 4)
	if s.savedMainCurY != 3 {
		t.Errorf("savedMainCurY at boundary = %d, want 3 (clamped to rows-1)", s.savedMainCurY)
	}

	s2 := New(3, 4)
	s2.InAltScreen = true
	s2.savedMainCells = makeSavedMain(3, 4)
	s2.savedMainCurY = 0
	s2.Resize(4, 4)
	if s2.savedMainCurY != 0 {
		t.Errorf("savedMainCurY below limit = %d, want 0 (not clamped)", s2.savedMainCurY)
	}
}

// TestResizeSavedCursorXClamp verifies the saved main-screen cursor column is
// clamped to cols-1 when out of range and preserved otherwise.
func TestResizeSavedCursorXClamp(t *testing.T) {
	cases := []struct {
		name    string
		savedX  int
		newCols int
		wantX   int
	}{
		{"over clamps to cols-1", 8, 5, 4},
		{"equal clamps to cols-1", 5, 5, 4},
		{"under unchanged", 2, 5, 2},
	}
	for _, c := range cases {
		gotX, _ := resizeSavedCursor(c.savedX, 0, 8, c.newCols)
		if gotX != c.wantX {
			t.Errorf("%s: Resize savedMainCurX (savedX=%d, cols=%d) = %d, want %d",
				c.name, c.savedX, c.newCols, gotX, c.wantX)
		}
	}
}

// TestSaveCursorResizeSmallerRestore verifies DECSC then a shrink then DECRC
// leaves the cursor in bounds.
func TestSaveCursorResizeSmallerRestore(t *testing.T) {
	s := New(20, 80)
	s.Write([]byte("\x1b[16;71H")) // row 15, col 70
	s.Write([]byte("\x1b7"))       // DECSC
	s.Resize(5, 10)                // shrink
	s.Write([]byte("\x1b8"))       // DECRC
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d out of bounds [0,%d) after restore post-resize", col, s.Width)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d out of bounds [0,%d) after restore post-resize", row, s.Height)
	}
}

// TestResizeTo1ColMidCSI verifies resizing to a single column partway through a
// CSI sequence leaves the cursor in bounds once the sequence completes.
func TestResizeTo1ColMidCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[")) // start CSI
	s.Resize(3, 1)           // resize mid-sequence
	s.Write([]byte("1;1H"))  // complete
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d out of bounds after resize mid-CSI", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d out of bounds after resize mid-CSI", row)
	}
}
