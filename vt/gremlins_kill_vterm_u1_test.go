package vt

// Mutant-killing tests for unit vterm-u1 (package vt).
// Targets living gremlins mutants in charset.go and csi.go.
// Tests only; internal package so unexported state is reachable.

import "testing"

// gk_vterm_u1_wantInt fails the test when got != want.
func gk_vterm_u1_wantInt(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", what, got, want)
	}
}

// ---------------------------------------------------------------------------
// charset.go:84 — `s.singleShft = -1` (after a single shift is consumed).
// charset.go:123 — `s.singleShft = -1` (resetCharsets).
// Both INVERT_NEGATIVES and ARITHMETIC_BASE at those positions turn the
// assignment from `= -1` into `= 1`. singleShft is only ever read via
// `if s.singleShft >= 2` (charset.go:82), so a leftover 1 vs -1 both fall in
// the "no single shift" else branch — the documented sentinel for "none" is
// exactly -1 (charset.go:38). Asserting the reset returns to that sentinel is
// what distinguishes original (-1) from mutant (1).
// ---------------------------------------------------------------------------

func TestGkVtermU1_SingleShiftConsumedResetsToMinusOne(t *testing.T) {
	s := New(2, 20)
	// G2 = DEC Special Graphics, SS2 (ESC N), then print one char.
	// translateChar consumes the single shift and must reset singleShft to -1.
	s.Write([]byte("\x1b*0\x1bNq"))

	// Sanity: SS2 translated 'q' through G2 graphics → U+2500 (─).
	if got := s.Cells[0][0].Ch; got != '\u2500' {
		t.Errorf("SS2 translate('q') = U+%04X, want U+2500", got)
	}
	// Kill target (charset.go:84): the consumed single-shift sentinel.
	if got := int(s.singleShft); got != -1 {
		t.Errorf("singleShft after single-shift consumed = %d, want -1", got)
	}
}

func TestGkVtermU1_ResetCharsetsSetsSentinelMinusOne(t *testing.T) {
	s := New(2, 20)
	// Put charset state into a distinct non-default value first.
	s.singleShft = 7
	s.gl = 3
	s.gsets = [4]charset{charsetGraphic, charsetGraphic, charsetGraphic, charsetGraphic}

	s.resetCharsets()

	// Kill target (charset.go:123): reset must restore the -1 sentinel.
	if got := int(s.singleShft); got != -1 {
		t.Errorf("resetCharsets singleShft = %d, want -1", got)
	}
	// Confirm resetCharsets ran fully (behavioural coverage of the function).
	gk_vterm_u1_wantInt(t, "resetCharsets gl", int(s.gl), 0)
	for i, c := range s.gsets {
		if c != charsetASCII {
			t.Errorf("resetCharsets gsets[%d] = %d, want charsetASCII(%d)", i, c, charsetASCII)
		}
	}
}

// ---------------------------------------------------------------------------
// csi.go:18 — `if s.numInterm > 0 && s.pIntermed[0] == ' '` (SP-prefix branch).
// CONDITIONALS_BOUNDARY `>` -> `>=` makes the guard `numInterm >= 0`, which is
// always true (numInterm is uint8). parserClear() zeroes numInterm but leaves
// the pIntermed array intact, so a stale ' ' from a prior `CSI SP q` reaches a
// later `CSI 2 A` with numInterm == 0. Original then runs main-switch 'A'
// (cursor up); mutant runs the SP-branch 'A' (SR, shift-right) which leaves the
// cursor put.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CSI_SP_GuardRequiresNonZeroInterm(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[6;1H")) // cursor to row 5 (0-indexed)
	s.Write([]byte("\x1b[ q"))   // DECSCUSR: leaves pIntermed[0]==' ', numInterm==1
	s.Write([]byte("\x1b[2A"))   // CSI 2 A with numInterm==0 → must be cursor-up

	row, _ := s.CursorPos()
	// Original: cursor up by 2 → row 3. Mutant (>=): SP-branch 'A' is SR, no move → row 5.
	gk_vterm_u1_wantInt(t, "curY after CSI 2 A with numInterm==0", row, 3)
}

// ---------------------------------------------------------------------------
// csi.go:39 — `if s.numInterm > 0 && s.pIntermed[0] == '$'` (DECRQM branch).
// Same boundary mutation as line 18, for the '$' branch. Prime pIntermed[0] to
// '$' with a real DECRQM, clear Response, then send `CSI 6 p` with
// numInterm == 0. Original skips the '$' branch ('p' is unhandled → no reply);
// mutant (>=) enters it and handleDECRQM emits a reply.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CSI_Dollar_GuardRequiresNonZeroInterm(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[$p")) // DECRQM: leaves pIntermed[0]=='$', numInterm==1
	s.Response = nil
	s.Write([]byte("\x1b[6p")) // CSI 6 p with numInterm==0

	// Original: '$' branch skipped, 'p' unhandled → no Response.
	// Mutant (>=): handleDECRQM emits a DECRQM reply.
	gk_vterm_u1_wantInt(t, "len(Response) after CSI 6 p with numInterm==0", len(s.Response), 0)
}

// ---------------------------------------------------------------------------
// csi.go:30 — `s.CursorBlink = v == 0 || v%2 == 1` (DECSCUSR).
//   30:23 CONDITIONALS_NEGATION (v==0 -> v!=0): killed by v=0 (true vs false).
//   30:32 ARITHMETIC_BASE     (v%2  -> v*2)   : killed by v=3 (true vs false).
//   30:35 CONDITIONALS_NEGATION (v%2==1 -> v%2!=1): killed by v=2 (false vs true).
// Each case pre-sets the opposite blink state so the assertion proves the code
// actually wrote the value.
// ---------------------------------------------------------------------------

func TestGkVtermU1_DECSCUSR_CursorBlink(t *testing.T) {
	cases := []struct {
		seq       string
		v         int
		wantStyle uint8
		wantBlink bool
	}{
		{"\x1b[0 q", 0, 0, true},
		{"\x1b[1 q", 1, 1, true},
		{"\x1b[2 q", 2, 2, false},
		{"\x1b[3 q", 3, 3, true},
		{"\x1b[4 q", 4, 4, false},
		{"\x1b[5 q", 5, 5, true},
		{"\x1b[6 q", 6, 6, false},
	}
	for _, tc := range cases {
		s := New(2, 5)
		// Force the opposite prior state so a passing assertion proves a write.
		s.CursorBlink = !tc.wantBlink
		s.CursorStyle = 99
		s.Write([]byte(tc.seq))

		if s.CursorBlink != tc.wantBlink {
			t.Errorf("CursorBlink after %q (v=%d) = %v, want %v", tc.seq, tc.v, s.CursorBlink, tc.wantBlink)
		}
		if s.CursorStyle != tc.wantStyle {
			t.Errorf("CursorStyle after %q (v=%d) = %d, want %d", tc.seq, tc.v, s.CursorStyle, tc.wantStyle)
		}
	}
}

// ---------------------------------------------------------------------------
// csi.go:64 — case 'B' (CUD): `if s.curY >= s.Height { s.curY = s.Height - 1 }`.
// CONDITIONALS_BOUNDARY `>=` -> `>` lets curY == Height escape the clamp.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CUD_ClampAtHeightBoundary(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[5B")) // down 5 from row 0 → curY reaches Height(5)

	row, _ := s.CursorPos()
	// Original: clamped to Height-1 = 4. Mutant (>): stays at 5 (out of bounds).
	gk_vterm_u1_wantInt(t, "curY after CSI 5 B", row, 4)
}

// ---------------------------------------------------------------------------
// csi.go:96 — case 'G' (CHA): `if s.curX >= s.Width { s.curX = s.Width - 1 }`.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CHA_ClampAtWidthBoundary(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[11G")) // CHA col 11 → curX = 10 == Width

	_, col := s.CursorPos()
	// Original: clamped to Width-1 = 9. Mutant (>): stays at 10.
	gk_vterm_u1_wantInt(t, "curX after CSI 11 G", col, 9)
}

// ---------------------------------------------------------------------------
// csi.go:109 — case 'H' non-origin: `else if y >= s.Height { y = s.Height - 1 }`.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CUP_RowClampAtHeightBoundary(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[6H")) // row 6 → y = 5 == Height (origin mode off by default)

	row, _ := s.CursorPos()
	gk_vterm_u1_wantInt(t, "curY after CSI 6 H", row, 4)
}

// ---------------------------------------------------------------------------
// csi.go:112 — case 'H': `if x >= s.Width { x = s.Width - 1 }`.
// ---------------------------------------------------------------------------

func TestGkVtermU1_CUP_ColClampAtWidthBoundary(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[1;11H")) // row 1, col 11 → x = 10 == Width

	row, col := s.CursorPos()
	gk_vterm_u1_wantInt(t, "curY after CSI 1;11 H", row, 0)
	gk_vterm_u1_wantInt(t, "curX after CSI 1;11 H", col, 9)
}

// ---------------------------------------------------------------------------
// csi.go:156 — case 'S' (SU): `regionH := s.scrollBottom - s.scrollTop + 1`.
// 156:43 ARITHMETIC_BASE `+ 1` -> `- 1` shrinks the scroll cap. On a full-screen
// region each scrollUpOnce drains the top line, so the drained-row count reveals
// the effective cap.
// ---------------------------------------------------------------------------

func TestGkVtermU1_SU_RegionHeightCapDrains(t *testing.T) {
	s := New(4, 5) // full-screen region: scrollTop=0, scrollBottom=3 → regionH=4
	s.Write([]byte("\x1b[4S"))

	// Original regionH=4 → n=4 → 4 lines drained.
	// Mutant (+1 -> -1): regionH=2 → n capped to 2 → 2 lines drained.
	gk_vterm_u1_wantInt(t, "len(Drained) after CSI 4 S", len(s.Drained), 4)
}

// ---------------------------------------------------------------------------
// csi.go:166 — case 'T' (SD): `if n > regionH { n = regionH }`.
// CONDITIONALS_NEGATION `>` -> `<=`: for n < regionH the original leaves n
// alone (partial scroll); the mutant sets n = regionH (blanks the whole region).
// ---------------------------------------------------------------------------

func TestGkVtermU1_SD_NoCapBelowRegionHeight(t *testing.T) {
	s := New(4, 5)
	// Fill 4 rows with distinct content via cursor positioning (no scrolling).
	s.Write([]byte("\x1b[1;1HAAAA"))
	s.Write([]byte("\x1b[2;1HBBBB"))
	s.Write([]byte("\x1b[3;1HCCCC"))
	s.Write([]byte("\x1b[4;1HDDDD"))
	s.Write([]byte("\x1b[1T")) // SD 1

	// Original: scroll down once → row 1 holds the former row 0 ("AAAA").
	// Mutant (<=): 1 <= regionH(4) → n=4 → region fully blanked → row 1 empty.
	if got := s.RowString(1); got != "AAAA" {
		t.Errorf("RowString(1) after CSI 1 T = %q, want %q", got, "AAAA")
	}
}
