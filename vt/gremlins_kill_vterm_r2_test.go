package vt

// Round-2 mutant-killing tests for package vt. Each test pins a surviving
// gremlins mutant that is genuinely killable by a direct test (the clamp /
// dead-guard survivors were killed by production min/max/dead-code edits
// instead). Identifiers are prefixed gk_vterm_r2_ / Test_gk_vterm_r2_ so they
// never collide with sibling units sharing this package directory.

import "testing"

// csi.go:39:17 CONDITIONALS_BOUNDARY — `if s.numInterm > 0 && s.pIntermed[0] == '!'`
// (the DECSTR / `CSI ! p` soft-reset branch). `> 0` -> `>= 0` makes the guard
// `numInterm >= 0`, always true for the uint8 counter. parserClear() zeroes
// numInterm but leaves the pIntermed array intact, so a stale '!' from a prior
// `CSI ! p` reaches a later `CSI p` with numInterm == 0. The original skips the
// '!' branch (main-switch 'p' is unhandled, no effect); the mutant enters it and
// soft-resets. The scroll region set between the two sequences is the observable:
// a soft reset would wipe it back to the full screen.
// (Mirrors the SP/`$` branch tests in unit vterm-u1 for the third intermediate.)
func Test_gk_vterm_r2_CSI_Bang_GuardRequiresNonZeroInterm(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[!p"))   // DECSTR: leaves pIntermed[0]=='!', numInterm==1
	s.Write([]byte("\x1b[3;5r")) // DECSTBM: scrollTop=2, scrollBottom=4 (numInterm back to 0)
	if s.scrollTop != 2 {
		t.Fatalf("precondition: scrollTop = %d, want 2 after CSI 3;5 r", s.scrollTop)
	}
	s.Write([]byte("\x1b[p")) // CSI p with numInterm==0

	// Original: '!' branch skipped, 'p' unhandled → scroll region intact.
	// Mutant (>=): softReset runs → scrollTop reset to 0.
	if s.scrollTop != 2 {
		t.Errorf("scrollTop after CSI p with numInterm==0 = %d, want 2 (DECSTR branch must require numInterm > 0)", s.scrollTop)
	}
}

// csi.go:544:17 CONDITIONALS_NEGATION — `for x := 0; x < n; x++` clears the
// columns vacated by shiftRight (SR / `CSI SP A`). `x < n` -> `x >= n` would
// instead clear columns [n, Width) and leave the vacated leading columns
// untouched. (The redundant `&& x < s.Width` clause was removed in production
// since n < Width is guaranteed by the earlier full-clear early-return.)
func Test_gk_vterm_r2_ShiftRightClearsVacatedColumns(t *testing.T) {
	s := New(1, 5)
	for x := range 5 {
		s.Cells[0][x] = Cell{Ch: rune('A' + x)} // A B C D E
	}

	s.shiftRight(2) // shift content right by 2: cols 2,3,4 = A,B,C; cols 0,1 cleared

	if got := s.Cells[0][0].Ch; got != ' ' {
		t.Errorf("shiftRight(2): Cells[0][0].Ch = %q, want ' ' (vacated column must be cleared)", got)
	}
	if got := s.Cells[0][1].Ch; got != ' ' {
		t.Errorf("shiftRight(2): Cells[0][1].Ch = %q, want ' ' (vacated column must be cleared)", got)
	}
	if got := s.Cells[0][2].Ch; got != 'A' {
		t.Errorf("shiftRight(2): Cells[0][2].Ch = %q, want 'A' (content shifted right by 2)", got)
	}
}

// screen.go:117:17 INVERT_NEGATIVES / ARITHMETIC_BASE — `s.singleShft = -1` in
// New(). Both mutations turn the initial assignment into `= 1`. -1 is the
// documented "no single shift pending" sentinel (see charset.go); a fresh
// screen must carry it. (charset.go's two other `= -1` sites are pinned by
// unit vterm-u1; this covers the constructor.)
func Test_gk_vterm_r2_NewInitializesSingleShiftSentinel(t *testing.T) {
	s := New(2, 20)
	if got := int(s.singleShft); got != -1 {
		t.Errorf("New().singleShft = %d, want -1 (the no-single-shift sentinel)", got)
	}
}

// sgr.go:12:58 CONDITIONALS_NEGATION — `s.pParams[0] == 0` in the
// "empty/zero SGR resets style" fast path. `== 0` -> `!= 0` makes a single
// non-zero SGR param (e.g. `CSI 1 m`, bold) satisfy the early-reset clause, so
// the style would be reset to default instead of having bold applied. The
// original takes the loop path and sets Bold.
func Test_gk_vterm_r2_SgrSingleNonZeroParamApplies(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b[1m")) // SGR 1 = bold

	if !s.style.Bold {
		t.Errorf("CSI 1 m: style.Bold = false, want true (a single non-zero SGR must apply, not reset)")
	}
}
