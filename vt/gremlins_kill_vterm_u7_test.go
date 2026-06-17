package vt

import "testing"

// Unit vterm-u7: tests that kill surviving CONDITIONALS_BOUNDARY /
// CONDITIONALS_NEGATION mutants in tabstops.go, width.go, and wire.go.
// All identifiers are prefixed gk_vterm_u7_ to avoid colliding with any
// sibling unit that shares this package directory.
//
// Equivalent mutants (no test — would pass regardless of the mutation):
//   - tabstops.go:59:22 (i>=0 -> i>0 in prevTabStop): the only iteration the
//     two operators disagree on is i==0, but the loop returns i (==0) there
//     and the fallthrough also returns 0, so every input yields the same
//     result. Behaviorally identical.
//   - wire.go:47:8 (x>0 -> x>=0 in cellsToRuns): at x==0, prev/prevURL are
//     seeded from row[0], so (cell.Style != prev || cell.Hyperlink != prevURL)
//     is always false; the flush block is skipped either way. Identical flow.
//   - wire.go:59:15 (buf.Len()>0 -> buf.Len()>=0): line 59 is only reached for
//     non-empty rows (empty rows early-return) and the loop unconditionally
//     WriteRunes the final cell, so buf.Len() is always >= 1 here. Both
//     operators are always true.

// --- width.go runeWidth boundaries ---

func gk_vterm_u7_wantRuneWidth(t *testing.T, r rune, want int) {
	t.Helper()
	if got := runeWidth(r); got != want {
		t.Errorf("runeWidth(%#U) = %d, want %d", r, got, want)
	}
}

// Kills width.go:25:7 (CONDITIONALS_BOUNDARY r<0x7F -> r<=0x7F),
// width.go:28:7 (CONDITIONALS_NEGATION r>=0x7F -> r<0x7F), and
// width.go:28:7 (CONDITIONALS_BOUNDARY r>=0x7F -> r>0x7F).
// At r==0x7F the original returns 0 (DEL is in the C1 zero-width band
// [0x7F,0xA0)); every one of those three mutations instead returns 1.
func Test_gk_vterm_u7_RuneWidthDelIsZeroWidth(t *testing.T) {
	gk_vterm_u7_wantRuneWidth(t, 0x7F, 0) // exact boundary that kills 25:7 and both 28:7 mutants
	gk_vterm_u7_wantRuneWidth(t, 0x7E, 1) // anchor: just below 0x7F stays width 1
	gk_vterm_u7_wantRuneWidth(t, 0x9F, 0) // anchor: interior of the C1 zero-width band
}

// Kills width.go:28:20 (CONDITIONALS_BOUNDARY r<0xA0 -> r<=0xA0).
// At r==0xA0 (NBSP) the original returns 1 (not combining, not wide); the
// mutated <= makes 0xA0>=0x7F && 0xA0<=0xA0 true and returns 0.
func Test_gk_vterm_u7_RuneWidthNbspIsWidthOne(t *testing.T) {
	gk_vterm_u7_wantRuneWidth(t, 0xA0, 1)
}

// --- width.go inTable boundary endpoints ---

// Kills width.go:69:7 (CONDITIONALS_BOUNDARY r<table[0].first -> r<=first).
// combining[0] is {0x0300,0x036F}; at r==0x0300 the original proceeds to the
// binary search and returns true, while the mutated r<=first early-returns
// false.
func Test_gk_vterm_u7_InTableLowerEndpointMatches(t *testing.T) {
	if !inTable(0x0300, combining) {
		t.Errorf("inTable(0x0300, combining) = false, want true (lower endpoint of combining[0])")
	}
}

// Kills width.go:69:29 (CONDITIONALS_BOUNDARY r>table[hi].last -> r>=last).
// The last combining range is {0xE0100,0xE01EF}; at r==0xE01EF the original
// proceeds to the binary search and returns true, while the mutated r>=last
// early-returns false.
func Test_gk_vterm_u7_InTableUpperEndpointMatches(t *testing.T) {
	if !inTable(0xE01EF, combining) {
		t.Errorf("inTable(0xE01EF, combining) = false, want true (upper endpoint of last range)")
	}
}

// --- tabstops.go restoreCursor clamps ---

// Kills tabstops.go:98:12 (CONDITIONALS_BOUNDARY s.curY>=s.Height -> >).
// With a saved curY exactly at Height, the original clamps to Height-1; the
// mutated > leaves it at Height (out of bounds).
func Test_gk_vterm_u7_RestoreCursorClampsCurYAtHeight(t *testing.T) {
	s := New(5, 10) // Height=5, Width=10
	s.cursorStateSaved = true
	s.savedY = s.Height // exactly at the boundary
	s.savedX = 0
	s.restoreCursor()
	if s.curY != s.Height-1 {
		t.Errorf("restoreCursor with savedY=Height: curY = %d, want %d", s.curY, s.Height-1)
	}
}

// Kills tabstops.go:101:12 (CONDITIONALS_BOUNDARY s.curX>=s.Width -> >).
// With a saved curX exactly at Width, the original clamps to Width-1; the
// mutated > leaves it at Width (out of bounds).
func Test_gk_vterm_u7_RestoreCursorClampsCurXAtWidth(t *testing.T) {
	s := New(5, 10) // Height=5, Width=10
	s.cursorStateSaved = true
	s.savedY = 0
	s.savedX = s.Width // exactly at the boundary
	s.restoreCursor()
	if s.curX != s.Width-1 {
		t.Errorf("restoreCursor with savedX=Width: curX = %d, want %d", s.curX, s.Width-1)
	}
}

// --- wire.go RenderRowWire bounds guard ---

// Kills wire.go:31:16 (CONDITIONALS_BOUNDARY y>=s.Height -> y>s.Height).
// At y==Height the original returns nil; the mutated > falls through to
// s.Cells[Height], which is out of range (Cells has indices 0..Height-1) and
// panics. The original path here also confirms an in-range row is non-nil.
func Test_gk_vterm_u7_RenderRowWireRejectsHeightIndex(t *testing.T) {
	s := New(3, 8) // Height=3 -> valid rows 0..2
	if got := s.RenderRowWire(s.Height); got != nil {
		t.Errorf("RenderRowWire(Height=%d) = %v, want nil", s.Height, got)
	}
	if got := s.RenderRowWire(s.Height - 1); got == nil {
		t.Errorf("RenderRowWire(Height-1=%d) = nil, want non-nil", s.Height-1)
	}
}

// --- wire.go basic16RGB palette bound ---

// Kills wire.go:129:14 (CONDITIONALS_BOUNDARY int(idx)<len(pal) -> <=).
// pal has length 16 (indices 0..15). At idx==16 the original returns the
// 0xaaaaaa fallback; the mutated <= indexes pal[16] and panics.
func Test_gk_vterm_u7_Basic16RGBOutOfRangeFallback(t *testing.T) {
	if got := basic16RGB(16); got != 0xaaaaaa {
		t.Errorf("basic16RGB(16) = 0x%06x, want 0xaaaaaa (out-of-range fallback)", got)
	}
	if got := basic16RGB(15); got != 0xffffff { // anchor: last in-range entry
		t.Errorf("basic16RGB(15) = 0x%06x, want 0xffffff", got)
	}
	if got := basic16RGB(0); got != 0x000000 { // anchor: first in-range entry
		t.Errorf("basic16RGB(0) = 0x%06x, want 0x000000", got)
	}
}
