package vt

import "testing"

// Unit vterm-u3: tests that kill surviving gremlins mutants in csi.go,
// osc.go, and parse.go. All identifiers are prefixed gk_vterm_u3_ so they
// never collide with a sibling unit sharing this package directory.
//
// Equivalent mutants (no test — applying the mutation changes no assertion):
//   - csi.go:511:7  CONDITIONALS_BOUNDARY (shiftLeft  n>=Width -> n>Width):
//       the two operators disagree only at n==Width; there the else branch's
//       copy loop runs zero times and its clear loop blanks columns [0,Width)
//       with the same Cell{Ch:' '} the full-clear branch uses, so the Cells
//       are byte-identical for every scroll region.
//   - csi.go:531:7  CONDITIONALS_BOUNDARY (shiftRight n>=Width -> n>Width):
//       identical reasoning to 511:7 (shiftRight's else clear loop covers
//       [0,Width) when n==Width).
//   - csi.go:596:8  CONDITIONALS_BOUNDARY (csiArg n>max -> n>=max): the clamp
//       value (maxCSIArgValue=65535) equals the boundary; once n reaches
//       65535 both forms yield 65535 (early clamp vs the trailing return n).
//   - csi.go:600:7, parse.go:249:12, parse.go:252:31: these were round-1
//       equivalents under the test-only constraint; round 2 (unit vterm-r2)
//       killed them with production edits — csiArg's dead `if n < 0` guard was
//       removed (n is built from digits, never negative), and paramGroup's two
//       length clamps were folded into one `min(length, 8,
//       uint8(maxParams-int(startIdx)))` — so they no longer exist as mutants.
//   - parse.go:274:19 CONDITIONALS_BOUNDARY (paramVal def>0 -> def>=0): the
//       branch only fires when v==0; at def==0 the original returns v(0) and
//       the mutant returns def(0); def<0 and def>0 agree. Same output always.
//   - parse.go:277:7  CONDITIONALS_BOUNDARY (paramVal v>max -> v>=max): v comes
//       from a uint16 (<=65535==maxCSIArgValue); at v==65535 both return 65535.

// --- csi.go: shiftLeft / shiftRight ---

// Kills csi.go:512:27 CONDITIONALS_BOUNDARY (shiftLeft full-clear loop
// y<=scrollBottom -> y<scrollBottom). With n>=Width the full-clear branch
// runs; the original clears every row up to and including scrollBottom (the
// last row), the mutant stops one row short and leaves it untouched.
func Test_gk_vterm_u3_ShiftLeftFullClearLastRow(t *testing.T) {
	s := New(3, 4) // scrollTop=0, scrollBottom=2 (last row index 2)
	s.Cells[2][0] = Cell{Ch: 'Z'}
	s.shiftLeft(s.Width) // n==Width -> full-clear branch
	if got := s.Cells[2][0].Ch; got != ' ' {
		t.Errorf("shiftLeft(Width): Cells[scrollBottom][0].Ch = %q, want ' ' (last row must be cleared)", got)
	}
}

// Kills csi.go:532:27 CONDITIONALS_BOUNDARY (shiftRight full-clear loop
// y<=scrollBottom -> y<scrollBottom). Same shape as 512:27 for shiftRight.
func Test_gk_vterm_u3_ShiftRightFullClearLastRow(t *testing.T) {
	s := New(3, 4) // scrollBottom=2
	s.Cells[2][0] = Cell{Ch: 'Z'}
	s.shiftRight(s.Width) // n==Width -> full-clear branch
	if got := s.Cells[2][0].Ch; got != ' ' {
		t.Errorf("shiftRight(Width): Cells[scrollBottom][0].Ch = %q, want ' ' (last row must be cleared)", got)
	}
}

// Kills csi.go:531:7 CONDITIONALS_NEGATION (shiftRight n>=Width -> n<Width).
// With n=1 < Width the original takes the else branch and shifts content one
// column to the right (col0 -> col1); the negated guard instead enters the
// full-clear branch and blanks everything, so col1 would be a space.
func Test_gk_vterm_u3_ShiftRightGuardNegation(t *testing.T) {
	s := New(1, 5)
	s.Write([]byte("X")) // Cells[0][0] = 'X'
	s.shiftRight(1)      // 1 < Width: original shifts 'X' to col1
	if got := s.Cells[0][1].Ch; got != 'X' {
		t.Errorf("shiftRight(1): Cells[0][1].Ch = %q, want 'X' (content shifted right, not full-cleared)", got)
	}
}

// --- csi.go: softReset ---

// Kills csi.go:553:28 INVERT_NEGATIVES and ARITHMETIC_BASE on
// `s.scrollBottom = s.Height - 1`. softReset must restore the scroll region's
// bottom to Height-1 (9 for Height=10). ARITHMETIC_BASE (- -> +) yields 11 and
// INVERT_NEGATIVES likewise changes the result; both differ from 9.
func Test_gk_vterm_u3_SoftResetScrollBottom(t *testing.T) {
	s := New(10, 20) // Height=10
	s.scrollBottom = 3
	s.softReset()
	if s.scrollBottom != s.Height-1 {
		t.Errorf("softReset(): scrollBottom = %d, want %d (Height-1)", s.scrollBottom, s.Height-1)
	}
}

// --- csi.go: csiArg ---

// Kills csi.go:579:12 CONDITIONALS_NEGATION (clean != "" -> clean == "") in the
// leading-marker strip loop. With "?5" the original strips '?' and parses 5;
// the negated loop condition never runs (clean is non-empty), leaving the '?'
// in place so the number scan finds no digit and returns def (0).
func Test_gk_vterm_u3_CsiArgStripsPrivateMarker(t *testing.T) {
	if got := csiArg("?5", 0); got != 5 {
		t.Errorf("csiArg(%q, 0) = %d, want 5 (leading '?' must be stripped)", "?5", got)
	}
}

// Kills csi.go:587:37 CONDITIONALS_BOUNDARY (clean[end] >= '0' -> > '0') in the
// digit scan. Input "0" is the exact boundary char: the original accepts it as
// a digit and returns 0; the mutant (> '0') rejects '0', scans zero digits, and
// returns def (5).
func Test_gk_vterm_u3_CsiArgParsesZeroDigit(t *testing.T) {
	if got := csiArg("0", 5); got != 0 {
		t.Errorf("csiArg(%q, 5) = %d, want 0 ('0' is a digit and must be parsed)", "0", got)
	}
}

// --- osc.go: dispatchOsc ---

// Kills osc.go:34:8 CONDITIONALS_BOUNDARY (digit loop i<len -> i<=len) and
// osc.go:41:7 CONDITIONALS_BOUNDARY (separator guard i<len -> i<=len). An
// all-digit payload with no ';' drives i to len(payload). The original never
// reads payload[len] and sets the title to the empty data; either <= mutant
// reads payload[len] -> index out of range panic -> this test fails -> killed.
func Test_gk_vterm_u3_OscAllDigitPayloadBounds(t *testing.T) {
	s := New(1, 10)
	s.Title = "gk_vterm_u3_prev"
	s.oscBuf = []byte("2") // OSC id 2, all digits, no separator
	s.dispatchOsc()
	if s.Title != "" {
		t.Errorf("dispatchOsc(%q): Title = %q, want \"\" (OSC 2 sets title to empty data)", "2", s.Title)
	}
}

// Kills osc.go:34:58 CONDITIONALS_BOUNDARY (digit loop payload[i] <= '9' ->
// < '9'). '9' is the exact upper boundary: the original treats it as a digit so
// the id parses to 9 (an unhandled OSC, title left alone); the mutant (< '9')
// stops at i=0 so id stays 0, and OSC 0 overwrites the title with the empty
// data, clearing the sentinel.
func Test_gk_vterm_u3_OscDigitNineIsParsed(t *testing.T) {
	s := New(1, 10)
	const keep = "gk_vterm_u3_keep"
	s.Title = keep
	s.oscBuf = []byte("9") // id 9 (unhandled) -> title unchanged
	s.dispatchOsc()
	if s.Title != keep {
		t.Errorf("dispatchOsc(%q): Title = %q, want %q (id 9 is unhandled, title unchanged)", "9", s.Title, keep)
	}
}

// --- parse.go: pushParam / finalizeParams ---

// Kills parse.go:155:17 CONDITIONALS_BOUNDARY (pushParam numGroups>0 ->
// numGroups>=0). A fresh screen has numGroups==0. The original guard skips the
// per-group length bump; the mutant takes the branch, underflows
// numGroups-1 to 255, resolves the group start to 0, and bumps pGroupLen[0].
func Test_gk_vterm_u3_PushParamZeroGroupsGuard(t *testing.T) {
	s := New(1, 10) // fresh: numGroups==0, numParams==0, pGroupLen all 0
	s.curParam = 7
	s.pushParam(false)
	if s.pGroupLen[0] != 0 {
		t.Errorf("pushParam with numGroups==0: pGroupLen[0] = %d, want 0 (guard must skip the group bump)", s.pGroupLen[0])
	}
}

// Kills parse.go:164:18 CONDITIONALS_BOUNDARY (new-group cap numGroups<maxParams
// -> numGroups<=maxParams). At numGroups==maxParams the original refuses to
// open another group (count stays maxParams); the mutant (<=) opens one more,
// pushing numGroups past the cap to maxParams+1.
func Test_gk_vterm_u3_PushParamNewGroupCap(t *testing.T) {
	s := New(1, 10)
	s.numGroups = maxParams // at the cap
	s.numParams = 0
	s.pushParam(true) // newGroup=true exercises the cap check
	if s.numGroups != maxParams {
		t.Errorf("pushParam(true) at numGroups==maxParams: numGroups = %d, want %d (must not exceed the cap)", s.numGroups, maxParams)
	}
}

// Kills parse.go:179:17 CONDITIONALS_BOUNDARY (finalizeParams numParams>=maxParams
// -> numParams>maxParams). At numParams==maxParams the original returns before
// writing; the mutant (>) proceeds to pParams[maxParams], which is out of range
// (pParams has indices 0..maxParams-1) -> panic -> this test fails -> killed.
func Test_gk_vterm_u3_FinalizeParamsFullArrayGuard(t *testing.T) {
	s := New(1, 10)
	s.numParams = maxParams // full array
	s.finalizeParams()
	if s.numParams != maxParams {
		t.Errorf("finalizeParams at numParams==maxParams: numParams = %d, want %d (full-array guard must return early)", s.numParams, maxParams)
	}
}

// Kills parse.go:183:13 INCREMENT_DECREMENT (finalizeParams numParams++ ->
// numParams--) and parse.go:184:17 CONDITIONALS_BOUNDARY (numGroups>0 ->
// numGroups>=0). On a fresh screen finalizeParams pushes one param: the original
// leaves numParams==1 and (numGroups==0) skips the group bump, so pGroupLen[0]
// stays 0. The ++ -> -- mutant underflows numParams to 255; the >0 -> >=0 mutant
// bumps pGroupLen[0] to 1.
func Test_gk_vterm_u3_FinalizeParamsFreshScreen(t *testing.T) {
	s := New(1, 10) // fresh: numParams==0, numGroups==0, pGroupLen all 0
	s.curParam = 9
	s.finalizeParams()
	if s.numParams != 1 {
		t.Errorf("finalizeParams: numParams = %d, want 1 (must increment by one)", s.numParams)
	}
	if s.pGroupLen[0] != 0 {
		t.Errorf("finalizeParams with numGroups==0: pGroupLen[0] = %d, want 0 (guard must skip the group bump)", s.pGroupLen[0])
	}
}

// --- parse.go: paramGroup ---

// Kills the ARITHMETIC_BASE mutant on `maxParams-int(startIdx)` inside
// paramGroup's `min(length, 8, uint8(maxParams-int(startIdx)))` clamp (round 2
// folded the two length clamps into this min). With group 1 starting at index
// 30 and a stored length of 5, the original clamps length to min(5,8,32-30)=2
// so the copy loop stays in bounds. Mutating `-` to `+` gives min(5,8,32+30)=5,
// leaving length 5 so the loop reads pParams[32] (out of range) -> panic.
func Test_gk_vterm_u3_ParamGroupStartIdxOverflowClamp(t *testing.T) {
	s := New(1, 80)
	s.numGroups = 2
	s.pGroupLen[0] = 30 // group 1 begins at index 30
	s.pGroupLen[30] = 5 // raw length 5 -> 30+5=35 must be clamped to 2
	g := s.paramGroup(1)
	if g.Len != 2 {
		t.Errorf("paramGroup(1): Len = %d, want 2 (length must be clamped so startIdx+length stays within maxParams)", g.Len)
	}
}

// --- parse.go: execControl backspace ---

// Kills parse.go:290:10 INCREMENT_DECREMENT (backspace s.curX-- -> s.curX++).
// After printing five columns the cursor sits at column 5; a backspace must
// move it left to 4. The mutant moves it right to 6.
func Test_gk_vterm_u3_BackspaceDecrementsCurX(t *testing.T) {
	s := New(1, 20)
	s.Write([]byte("hello")) // cursor advances to column 5
	s.Write([]byte{0x08})    // BS
	if _, col := s.CursorPos(); col != 4 {
		t.Errorf("backspace from column 5: col = %d, want 4 (cursor moves left)", col)
	}
}

// --- parse.go: dispatchEsc reverse index (ESC M) ---

// Kills parse.go:332:13 CONDITIONALS_NEGATION (RI s.curY==s.scrollTop ->
// s.curY!=s.scrollTop). RI ("ESC M") behaves two ways: away from the scroll
// top it moves the cursor up one row; at the scroll top it scrolls content down
// and leaves the cursor put. The test exercises both branches so the negated
// guard is caught either way.
func Test_gk_vterm_u3_ReverseIndexBranch(t *testing.T) {
	// curY != scrollTop -> cursor moves up (else-if branch).
	s := New(3, 10)
	s.curY = 2 // not at scrollTop (0)
	s.Write([]byte("\x1bM"))
	if row, _ := s.CursorPos(); row != 1 {
		t.Errorf("RI with curY=2: row = %d, want 1 (cursor moves up one row)", row)
	}

	// curY == scrollTop -> content scrolls down, cursor stays (if branch).
	s2 := New(3, 10)
	s2.Write([]byte("TOP")) // row 0 = "TOP", curY=0 == scrollTop
	s2.Write([]byte("\x1bM"))
	if got := s2.RowString(1); got != "TOP" {
		t.Errorf("RI at scrollTop: RowString(1) = %q, want \"TOP\" (content scrolled down)", got)
	}
	if row, _ := s2.CursorPos(); row != 0 {
		t.Errorf("RI at scrollTop: cursor row = %d, want 0 (cursor unchanged)", row)
	}
}
