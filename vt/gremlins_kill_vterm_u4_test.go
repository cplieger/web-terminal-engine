package vt

import "testing"

// gk_vterm_u4_makeSaved builds a saved-main-screen buffer of the given
// dimensions where row r carries the marker rune 'A'+r in column 0 (the rest
// spaces), so a row's identity survives a savedMainCells rebuild and can be
// asserted by content.
func gk_vterm_u4_makeSaved(rows, cols int) [][]Cell {
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

// Test_gk_vterm_u4_ReverseIndexCursorY pins the RI (ESC M) cursor-up branch in
// dispatchEsc. When the cursor sits above the scroll-region top it must NOT
// move past row 0; when inside the region it decrements by exactly one.
//
// Kills parse.go:334:20 (`s.curY > 0`): the "above region at row zero" case
// drives curY==0 with scrollTop==1, so the original (`> 0`) leaves the cursor
// put, while the boundary mutant (`>= 0`) and the negation mutant (`<= 0`)
// would decrement to -1. Kills parse.go:335:10 (`s.curY--`): the "inside
// region" case requires the decrement to land on exactly 2 (the `++` mutant
// would give 4), and also kills the 334 negation mutant (`<= 0` is false at
// curY==3, leaving curY at 3).
func Test_gk_vterm_u4_ReverseIndexCursorY(t *testing.T) {
	cases := []struct {
		name      string
		scrollTop int
		startY    int
		wantY     int
	}{
		{"above region at row zero", 1, 0, 0},
		{"inside region", 1, 3, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(10, 10)
			s.scrollTop = tc.scrollTop
			s.curY = tc.startY

			s.dispatchEsc('M') // RI

			if s.curY != tc.wantY {
				t.Errorf("RI(scrollTop=%d, startY=%d) curY = %d, want %d",
					tc.scrollTop, tc.startY, s.curY, tc.wantY)
			}
		})
	}
}

// Test_gk_vterm_u4_RISErasesEntireScreen pins the RIS (ESC c) full-screen
// clear, including the bottom-right corner cell.
//
// This documents the eraseRegion(0, 0, Height-1, Width-1) contract behind
// parse.go:347. The arithmetic/invert-negative mutants there are equivalent:
// eraseRegion clamps out-of-range coordinates, so Height+1 / Width+1 erase the
// exact same full screen. This test still asserts the contract (and would
// catch any region-reducing change) without claiming a kill it cannot make.
func Test_gk_vterm_u4_RISErasesEntireScreen(t *testing.T) {
	s := New(4, 5)
	for y := range s.Cells {
		for x := range s.Cells[y] {
			s.Cells[y][x] = Cell{Ch: 'X'}
		}
	}

	s.dispatchEsc('c') // RIS

	for y := range s.Cells {
		for x := range s.Cells[y] {
			if got := s.Cells[y][x].Ch; got != ' ' {
				t.Errorf("after RIS Cells[%d][%d].Ch = %q, want ' '", y, x, got)
			}
		}
	}
}

// Test_gk_vterm_u4_ResizeTabStopsGrow pins the default-tab-stop fill that runs
// when a non-nil tabStops slice is widened on resize: stops are placed at
// positive multiples of 8 only, column 0 never gets a stop, and the loop is
// bounded by the new column count.
//
// Setup uses a length-0 (but non-nil) tabStops so the fill loop starts at i=0,
// and a target width that is a multiple of 8.
//
//   - Kills screen.go:220:32 negation (`i < cols` -> `i >= cols`): the loop is
//     skipped, so the multiple-of-8 stop never lands (tabStops[8] stays false).
//   - Kills screen.go:220:32 boundary (`i < cols` -> `i <= cols`): at i==cols
//     (16, a multiple of 8) the body writes newStops[16] on a length-16 slice,
//     panicking the resize.
//   - Kills screen.go:221:10 boundary (`i > 0` -> `i >= 0`) and negation
//     (`i > 0` -> `i <= 0`): both let i==0 satisfy the guard, so column 0 gets
//     a stop (tabStops[0] becomes true); the negation also drops the i==8 stop.
//   - Kills screen.go:221:18 arithmetic (`i % 8` -> `i * 8` or `i / 8`): the
//     stop pattern changes (tabStops[8] no longer set, or wrong columns set).
//   - Kills screen.go:221:21 negation (`== 0` -> `!= 0`): stops would land on
//     non-multiples of 8 instead (tabStops[8] false, tabStops[1] true).
func Test_gk_vterm_u4_ResizeTabStopsGrow(t *testing.T) {
	s := New(5, 4)
	s.tabStops = make([]bool, 0) // non-nil, length 0 -> fill loop starts at i=0

	s.Resize(5, 16) // width is a multiple of 8

	if got := len(s.tabStops); got != 16 {
		t.Fatalf("len(tabStops) after grow = %d, want 16", got)
	}
	if s.tabStops[0] {
		t.Errorf("tabStops[0] = true, want false (column 0 must never get a stop)")
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

// Test_gk_vterm_u4_ResizeRebuildsSavedMainCells pins that a resize taken while
// a saved main-screen buffer exists rebuilds that buffer at the new
// dimensions.
//
// Kills screen.go:247:22 (`s.savedMainCells != nil`): under the negation
// (`== nil`) the rebuild block is skipped, so the saved buffer keeps its old
// 5x10 size instead of becoming 8x20.
func Test_gk_vterm_u4_ResizeRebuildsSavedMainCells(t *testing.T) {
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

// Test_gk_vterm_u4_ResizeSavedMainCopyBounds pins the bounded copy of saved
// rows into the resized saved buffer: rows present in the old saved buffer are
// copied by content, rows beyond it are left blank, and the index guard never
// reads out of range.
//
// The old saved buffer has 2 rows and the resize grows to 4, so the loop index
// reaches 2.
//   - Kills screen.go:251:9 boundary (`i < len` -> `i <= len`): at i==2 the
//     body reads savedMainCells[2] on a length-2 slice, panicking the resize.
//   - Kills screen.go:251:9 negation (`i < len` -> `i >= len`): at i==2 the
//     guard is true and reads savedMainCells[2], also panicking.
//
// Under the original code no out-of-range read happens; the content
// assertions document the copy-then-pad behaviour.
func Test_gk_vterm_u4_ResizeSavedMainCopyBounds(t *testing.T) {
	s := New(2, 4)
	s.InAltScreen = true
	s.savedMainCells = gk_vterm_u4_makeSaved(2, 4) // row0[0]='A', row1[0]='B'

	s.Resize(4, 4) // grow rows 2 -> 4

	if got := len(s.savedMainCells); got != 4 {
		t.Fatalf("savedMainCells rows after resize = %d, want 4", got)
	}
	if got := s.savedMainCells[0][0].Ch; got != 'A' {
		t.Errorf("savedMainCells[0][0].Ch = %q, want 'A' (copied)", got)
	}
	if got := s.savedMainCells[1][0].Ch; got != 'B' {
		t.Errorf("savedMainCells[1][0].Ch = %q, want 'B' (copied)", got)
	}
	if got := s.savedMainCells[2][0].Ch; got != ' ' {
		t.Errorf("savedMainCells[2][0].Ch = %q, want ' ' (blank pad row)", got)
	}
	if got := s.savedMainCells[3][0].Ch; got != ' ' {
		t.Errorf("savedMainCells[3][0].Ch = %q, want ' ' (blank pad row)", got)
	}
}

// Test_gk_vterm_u4_ResizeClampsSavedCursorY pins clamping of the saved
// main-screen cursor row to the new height.
//
// Kills screen.go:257:22 (`s.savedMainCurY >= rows`):
//   - boundary case (savedMainCurY == rows): the original clamps to rows-1,
//     while both the boundary mutant (`> rows`) and negation mutant
//     (`< rows`) leave it at rows.
//   - below case (savedMainCurY < rows): the original leaves it unchanged,
//     while the negation mutant (`< rows`) clamps it down to rows-1.
func Test_gk_vterm_u4_ResizeClampsSavedCursorY(t *testing.T) {
	// Boundary: savedMainCurY == rows must clamp to rows-1.
	s := New(3, 4)
	s.InAltScreen = true
	s.savedMainCells = gk_vterm_u4_makeSaved(3, 4)
	s.savedMainCurY = 4

	s.Resize(4, 4)

	if s.savedMainCurY != 3 {
		t.Errorf("savedMainCurY at boundary = %d, want 3 (clamped to rows-1)", s.savedMainCurY)
	}

	// Below limit: savedMainCurY < rows must be left unchanged.
	s2 := New(3, 4)
	s2.InAltScreen = true
	s2.savedMainCells = gk_vterm_u4_makeSaved(3, 4)
	s2.savedMainCurY = 0

	s2.Resize(4, 4)

	if s2.savedMainCurY != 0 {
		t.Errorf("savedMainCurY below limit = %d, want 0 (not clamped)", s2.savedMainCurY)
	}
}
