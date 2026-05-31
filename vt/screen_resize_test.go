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
