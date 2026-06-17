package vt

// Mutant-killing tests for unit vterm-u2 (package vt, csi.go).
// Each test pins an observable that depends on the exact operator/bound at the
// targeted line so the gremlins mutation is detected. Helpers are prefixed
// gk_vterm_u2_ to avoid colliding with sibling units sharing this package.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// gk_vterm_u2_recoverPanic runs fn and reports whether it panicked. Used to
// turn an out-of-bounds access (caused by a flipped range guard) into a clean
// test failure.
func gk_vterm_u2_recoverPanic(t *testing.T, fn func()) (panicked bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// 233:24 CONDITIONALS_NEGATION — `if s.lastPrintedRune != 0` in REP (CSI b).
// Original repeats the last rune; the negation (== 0) would do nothing for a
// real (non-zero) last rune.
func TestGk_vterm_u2_REP_repeatsLastRune(t *testing.T) {
	s := New(5, 5)
	s.lastPrintedRune = 'X'

	s.dispatchCSI('b') // REP, default count 1, writes 'X' at (0,0)

	if got := s.Cells[0][0].Ch; got != 'X' {
		t.Errorf("REP with lastPrintedRune='X': Cells[0][0].Ch = %q, want 'X' (!=0 branch must repeat)", got)
	}
}

// 266:13 CONDITIONALS_BOUNDARY — `if bottom >= s.Height` in DECSTBM (CSI r).
// With bottom == Height the `>=` clamps to Height-1; `>` would leave the scroll
// bottom out of range.
func TestGk_vterm_u2_DECSTBM_bottomClampBoundary(t *testing.T) {
	s := New(10, 20) // Height=10, scrollBottom starts at 9
	// ESC [ 1 ; 11 r  -> top=0, bottom=11-1=10 (== Height), must clamp to 9.
	if _, err := s.Write([]byte("\x1b[1;11r")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if s.scrollBottom != 9 {
		t.Errorf("DECSTBM bottom=Height: scrollBottom = %d, want 9 (>= clamps to Height-1)", s.scrollBottom)
	}
}

// 269:10 CONDITIONALS_BOUNDARY — `if top < bottom` in DECSTBM (CSI r).
// When top == bottom the strict `<` leaves the scroll region unchanged; `<=`
// would (wrongly) set a degenerate 1-line region.
func TestGk_vterm_u2_DECSTBM_topEqualsBottomNoRegion(t *testing.T) {
	s := New(10, 20) // default scrollTop=0, scrollBottom=9
	// ESC [ 5 ; 5 r -> top=4, bottom=4 (equal): region must stay 0..9.
	if _, err := s.Write([]byte("\x1b[5;5r")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if s.scrollTop != 0 || s.scrollBottom != 9 {
		t.Errorf("DECSTBM top==bottom: scrollTop,scrollBottom = %d,%d, want 0,9 (region unchanged under strict <)", s.scrollTop, s.scrollBottom)
	}
}

// 305:12 CONDITIONALS_NEGATION — `if final != 0` in the default CSI branch.
// A real (non-zero) unhandled final must emit the "unhandled CSI" log; the
// negation (== 0) would suppress it.
func TestGk_vterm_u2_UnhandledCSI_logsWhenFinalNonZero(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	s := New(5, 5)
	s.dispatchCSI('W') // 'W' is unhandled -> default branch

	if !strings.Contains(buf.String(), "unhandled CSI") {
		t.Errorf("dispatchCSI('W') log = %q, want it to contain \"unhandled CSI\" (final != 0 must log)", buf.String())
	}
}

// 424:26 CONDITIONALS_BOUNDARY — `s.curY >= s.Height` guard in insertChars.
// At curY == Height the `>=` must early-return; `>` would index s.Cells[Height]
// and panic.
func TestGk_vterm_u2_InsertChars_guardAtHeight(t *testing.T) {
	s := New(5, 5)
	s.curY = s.Height // 5, out of range

	if gk_vterm_u2_recoverPanic(t, func() { s.insertChars(1) }) {
		t.Errorf("insertChars with curY==Height panicked; the `>= Height` guard must early-return")
	}
}

// 437:26 CONDITIONALS_BOUNDARY — `s.curY >= s.Height` guard in deleteChars.
func TestGk_vterm_u2_DeleteChars_guardAtHeight(t *testing.T) {
	s := New(5, 5)
	s.curY = s.Height // 5, out of range

	if gk_vterm_u2_recoverPanic(t, func() { s.deleteChars(1) }) {
		t.Errorf("deleteChars with curY==Height panicked; the `>= Height` guard must early-return")
	}
}

// 441:16 (ARITHMETIC_BASE / INVERT_NEGATIVES) and 442:15 (ARITHMETIC_BASE /
// INVERT_NEGATIVES) — the `s.Width - s.curX` clamp value in deleteChars.
// With curX=2, Width=10, deleting 10 chars clamps to exactly Width-curX=8, so
// columns 0,1 (left of the cursor) survive. Any change to that subtraction
// (e.g. `+`) either erases column 0 or produces a negative loop bound that
// panics.
func TestGk_vterm_u2_DeleteChars_clampToWidthMinusCurX(t *testing.T) {
	s := New(5, 10) // Height=5, Width=10
	s.curY = 0
	s.curX = 2
	fill := "ABCDEFGHIJ"
	for x, r := range fill {
		s.Cells[0][x] = Cell{Ch: r}
	}

	if gk_vterm_u2_recoverPanic(t, func() { s.deleteChars(10) }) {
		t.Fatalf("deleteChars(10) panicked; clamp must be Width-curX so the fill loop stays in bounds")
	}

	if got := s.Cells[0][0].Ch; got != 'A' {
		t.Errorf("deleteChars(10) at curX=2: Cells[0][0].Ch = %q, want 'A' (cols left of cursor preserved)", got)
	}
	if got := s.Cells[0][1].Ch; got != 'B' {
		t.Errorf("deleteChars(10) at curX=2: Cells[0][1].Ch = %q, want 'B'", got)
	}
	if got := s.Cells[0][2].Ch; got != ' ' {
		t.Errorf("deleteChars(10) at curX=2: Cells[0][2].Ch = %q, want ' ' (erased region starts at cursor)", got)
	}
}

// 453:36 CONDITIONALS_BOUNDARY — `s.curY > s.scrollBottom` guard in insertLines.
// At curY == scrollBottom the strict `>` proceeds (blanking that row); `>=`
// would early-return and leave the row untouched.
func TestGk_vterm_u2_InsertLines_guardAtScrollBottom(t *testing.T) {
	s := New(5, 5)
	s.scrollTop = 0
	s.scrollBottom = 4
	s.curY = s.scrollBottom // 4
	s.Cells[4][0] = Cell{Ch: 'Z'}

	s.insertLines(1)

	if got := s.Cells[4][0].Ch; got != ' ' {
		t.Errorf("insertLines at curY==scrollBottom: Cells[4][0].Ch = %q, want ' ' (`>` must proceed and blank the line)", got)
	}
}

// 456:45 CONDITIONALS_NEGATION — `n > avail` clamp test in insertLines.
// Inserting 1 line at curY=1 must shift exactly one row down; inverting `>` to
// `<=` would clamp n up to avail (4) and blank the whole region.
func TestGk_vterm_u2_InsertLines_clampToAvail(t *testing.T) {
	s := New(5, 5)
	s.scrollTop = 0
	s.scrollBottom = 4
	s.curY = 1
	s.Cells[1][0] = Cell{Ch: '1'}
	s.Cells[2][0] = Cell{Ch: '2'}
	s.Cells[3][0] = Cell{Ch: '3'}
	s.Cells[4][0] = Cell{Ch: '4'}

	s.insertLines(1)

	if got := s.Cells[2][0].Ch; got != '1' {
		t.Errorf("insertLines(1) at curY=1: Cells[2][0].Ch = %q, want '1' (only one blank line inserted)", got)
	}
	if got := s.Cells[4][0].Ch; got != '3' {
		t.Errorf("insertLines(1) at curY=1: Cells[4][0].Ch = %q, want '3' (rows shift down by one)", got)
	}
}

// 468:36 CONDITIONALS_BOUNDARY — `s.curY > s.scrollBottom` guard in deleteLines.
func TestGk_vterm_u2_DeleteLines_guardAtScrollBottom(t *testing.T) {
	s := New(5, 5)
	s.scrollTop = 0
	s.scrollBottom = 4
	s.curY = s.scrollBottom // 4
	s.Cells[4][0] = Cell{Ch: 'Z'}

	s.deleteLines(1)

	if got := s.Cells[4][0].Ch; got != ' ' {
		t.Errorf("deleteLines at curY==scrollBottom: Cells[4][0].Ch = %q, want ' ' (`>` must proceed and blank the line)", got)
	}
}

// 471:38 ARITHMETIC_BASE — the `+ 1` in `avail := scrollBottom - curY + 1`
// (deleteLines). With curY=1, scrollBottom=4 the true avail is 4, so deleting 4
// lines clears the whole region. If `+1` becomes `-1`, avail=2 clamps n to 2
// and rows 1,2 retain shifted content.
func TestGk_vterm_u2_DeleteLines_availClampValue(t *testing.T) {
	s := New(5, 5)
	s.scrollTop = 0
	s.scrollBottom = 4
	s.curY = 1
	s.Cells[1][0] = Cell{Ch: '1'}
	s.Cells[2][0] = Cell{Ch: '2'}
	s.Cells[3][0] = Cell{Ch: '3'}
	s.Cells[4][0] = Cell{Ch: '4'}

	s.deleteLines(4)

	if got := s.Cells[1][0].Ch; got != ' ' {
		t.Errorf("deleteLines(4) at curY=1: Cells[1][0].Ch = %q, want ' ' (avail=4 must clear the region)", got)
	}
	if got := s.Cells[2][0].Ch; got != ' ' {
		t.Errorf("deleteLines(4) at curY=1: Cells[2][0].Ch = %q, want ' '", got)
	}
}

// 471:45 CONDITIONALS_NEGATION — `n > avail` clamp test in deleteLines.
// Deleting 1 line at curY=1 must shift exactly one row up; inverting `>` to
// `<=` would clamp n up to avail (4) and clear the whole region.
func TestGk_vterm_u2_DeleteLines_clampToAvail(t *testing.T) {
	s := New(5, 5)
	s.scrollTop = 0
	s.scrollBottom = 4
	s.curY = 1
	s.Cells[1][0] = Cell{Ch: '1'}
	s.Cells[2][0] = Cell{Ch: '2'}
	s.Cells[3][0] = Cell{Ch: '3'}
	s.Cells[4][0] = Cell{Ch: '4'}

	s.deleteLines(1)

	if got := s.Cells[1][0].Ch; got != '2' {
		t.Errorf("deleteLines(1) at curY=1: Cells[1][0].Ch = %q, want '2' (only one line deleted, rows shift up)", got)
	}
	if got := s.Cells[3][0].Ch; got != '4' {
		t.Errorf("deleteLines(1) at curY=1: Cells[3][0].Ch = %q, want '4'", got)
	}
}

// 498:12 CONDITIONALS_BOUNDARY — `s.curY >= s.Height` clamp in lineDown.
// With a scroll region whose bottom is above the last row, lineDown can advance
// curY to Height; the `>=` clamps it back to Height-1, `>` would leave it out
// of range.
func TestGk_vterm_u2_LineDown_clampCurYToHeight(t *testing.T) {
	s := New(5, 5) // Height=5
	s.scrollBottom = 2
	s.curY = s.Height - 1 // 4, not equal to scrollBottom -> increments to Height

	s.lineDown()

	if s.curY != s.Height-1 {
		t.Errorf("lineDown from curY=Height-1 (below scroll region): curY = %d, want %d (`>= Height` clamp)", s.curY, s.Height-1)
	}
}
