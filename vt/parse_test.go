package vt

import (
	"strings"
	"testing"
)

// --- C1 control bytes (0x80-0x9F) ---

// TestC1BytesInGroundEmitReplacement verifies every C1 byte emits U+FFFD in
// Ground (UTF-8 mode) and does not change parser state.
func TestC1BytesInGroundEmitReplacement(t *testing.T) {
	for b := byte(0x80); b <= 0x9F; b++ {
		s := New(1, 5)
		s.Write([]byte{b})
		if s.Cells[0][0].Ch != 0xFFFD {
			t.Errorf("byte 0x%02x in Ground: got %U, want U+FFFD", b, s.Cells[0][0].Ch)
		}
		if s.pState != stGround {
			t.Errorf("byte 0x%02x in Ground: state=%d, want Ground", b, s.pState)
		}
	}
}

// TestC1_0x9B_InGroundDoesNotStartCSI verifies 0x9B in Ground emits U+FFFD and
// the following bytes are printed literally (it does not begin a CSI).
func TestC1_0x9B_InGroundDoesNotStartCSI(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0x9B, '3', '1', 'm'})
	if s.Cells[0][0].Ch != 0xFFFD {
		t.Errorf("0x9B in Ground: got %U, want U+FFFD", s.Cells[0][0].Ch)
	}
	if s.Cells[0][1].Ch != '3' {
		t.Errorf("after 0x9B: cell[1] got %U, want '3'", s.Cells[0][1].Ch)
	}
}

// TestC1_0x90_InGroundDoesNotStartDCS verifies 0x90 in Ground emits U+FFFD and
// stays in Ground.
func TestC1_0x90_InGroundDoesNotStartDCS(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0x90})
	if s.Cells[0][0].Ch != 0xFFFD {
		t.Errorf("0x90 in Ground: got %U, want U+FFFD", s.Cells[0][0].Ch)
	}
	if s.pState != stGround {
		t.Errorf("0x90 in Ground: state=%d, want Ground", s.pState)
	}
}

// TestC1_0x9B_InEscapeStartsCSI verifies 0x9B (8-bit CSI) initiates a CSI when
// the parser is already in a non-Ground (Escape) state.
func TestC1_0x9B_InEscapeStartsCSI(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0x1B}) // Escape
	s.Write([]byte{0x9B}) // 8-bit CSI
	if s.pState != stCsiEntry {
		t.Errorf("0x9B in Escape: state=%d, want CsiEntry", s.pState)
	}
}

// TestC1_0x90_InEscapeStartsDCS verifies 0x90 (8-bit DCS) initiates a DCS from
// the Escape state.
func TestC1_0x90_InEscapeStartsDCS(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0x1B})
	s.Write([]byte{0x90})
	if s.pState != stDcsEntry {
		t.Errorf("0x90 in Escape: state=%d, want DcsEntry", s.pState)
	}
}

// TestC1_0x9D_InEscapeStartsOSC verifies 0x9D (8-bit OSC) initiates an OSC from
// the Escape state.
func TestC1_0x9D_InEscapeStartsOSC(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0x1B})
	s.Write([]byte{0x9D})
	if s.pState != stOscString {
		t.Errorf("0x9D in Escape: state=%d, want OscString", s.pState)
	}
}

// TestC1_0x9C_InCsiEntryGoesToGround verifies 0x9C (8-bit ST) aborts an
// in-progress CSI back to Ground.
func TestC1_0x9C_InCsiEntryGoesToGround(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b["))
	if s.pState != stCsiEntry {
		t.Fatalf("not in CsiEntry: state=%d", s.pState)
	}
	s.Write([]byte{0x9C})
	if s.pState != stGround {
		t.Errorf("0x9C in CsiEntry: state=%d, want Ground", s.pState)
	}
}

// TestC1_UTF8MultibyteNotCorrupted verifies a valid multi-byte UTF-8 sequence
// decodes correctly (C1 handling does not corrupt it).
func TestC1_UTF8MultibyteNotCorrupted(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0xE6, 0xBC, 0xA2}) // 漢
	if s.Cells[0][0].Ch != '漢' {
		t.Errorf("UTF-8 漢: got %U", s.Cells[0][0].Ch)
	}
}

// --- C0 controls execute mid-sequence ---

// TestC0_BEL_ExecutesMidCSI verifies a C0 control (BEL) executes even while a
// CSI sequence is being parsed.
func TestC0_BEL_ExecutesMidCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[1\x07;1H")) // BEL mid-CSI
	if !s.BellRing {
		t.Error("BEL not executed mid-CSI")
	}
}

// TestC0_LF_ExecutesMidEscape verifies LF executes mid-escape (cursor moves
// down) before the escape completes.
func TestC0_LF_ExecutesMidEscape(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b\nH")) // ESC, LF, then ESC H
	if row, _ := s.CursorPos(); row < 1 {
		t.Errorf("LF not executed mid-escape: row=%d, want >=1", row)
	}
}

// --- CAN / SUB abort ---

// TestCAN_AbortsCSICleanly verifies CAN aborts a partial CSI to Ground and the
// next sequence parses normally.
func TestCAN_AbortsCSICleanly(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[31")) // partial CSI
	s.Write([]byte{0x18})      // CAN
	if s.pState != stGround {
		t.Fatalf("CAN in CSI: state=%d, want Ground", s.pState)
	}
	s.Write([]byte("\x1b[1;1H"))
	if row, col := s.CursorPos(); row != 0 || col != 0 {
		t.Errorf("after CAN recovery = %d,%d, want 0,0", row, col)
	}
}

// TestSUB_AbortsCSI verifies SUB aborts a private-mode CSI (the alt-screen mode
// is not entered) and returns to Ground.
func TestSUB_AbortsCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[?1049\x1A")) // SUB aborts before final byte
	if s.InAltScreen {
		t.Fatal("SUB should have aborted the CSI, not entered alt screen")
	}
	if s.pState != stGround {
		t.Fatalf("parser not in Ground after SUB, got %d", s.pState)
	}
}

// --- Split / malformed sequences across writes ---

// TestParserSplitCSIAcrossWrites verifies a CSI split one byte per Write call
// still parses correctly (parser state persists across writes).
func TestParserSplitCSIAcrossWrites(t *testing.T) {
	s := New(5, 80)
	for _, b := range []byte("\x1b[3;5H") {
		s.Write([]byte{b})
	}
	if row, col := s.CursorPos(); row != 2 || col != 4 {
		t.Fatalf("split CSI H = %d,%d, want 2,4", row, col)
	}
}

// TestParserSplitUTF8AcrossWrites verifies a multi-byte UTF-8 rune split across
// Write calls is reassembled correctly.
func TestParserSplitUTF8AcrossWrites(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0xE6})
	s.Write([]byte{0xBC})
	s.Write([]byte{0xA2})
	if s.Cells[0][0].Ch != '漢' {
		t.Fatalf("split UTF-8: got %q, want '漢'", s.Cells[0][0].Ch)
	}
}

// TestParserInvalidUTF8Continuation verifies an invalid continuation byte
// resets UTF-8 state without leaving the cursor out of bounds.
func TestParserInvalidUTF8Continuation(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0xE6, 'A'}) // 0xE6 starts a 3-byte rune, 'A' is not a continuation
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d out of bounds", row)
	}
}

// TestParserMalformedCSIMissingFinal verifies a CSI interrupted by a new ESC is
// abandoned and the following sequence parses correctly.
func TestParserMalformedCSIMissingFinal(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[123\x1b[1;1H"))
	if row, col := s.CursorPos(); row != 0 || col != 0 {
		t.Fatalf("malformed CSI recovery = %d,%d, want 0,0", row, col)
	}
}

// TestRapidESCTransitions verifies many back-to-back ESC bytes leave the parser
// recoverable: a following ground-state char returns it to Ground.
func TestRapidESCTransitions(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte(strings.Repeat("\x1b\x1b\x1b", 100)))
	s.Write([]byte("A"))
	if s.pState != stGround {
		t.Fatalf("not in Ground after repeated ESC + final char, got %d", s.pState)
	}
}

// TestInterleavedESCAndCSI verifies a fresh ESC[ abandons a prior incomplete
// CSI and the second sequence takes effect.
func TestInterleavedESCAndCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[\x1b[1;1H"))
	if row, col := s.CursorPos(); row != 0 || col != 0 {
		t.Fatalf("got %d,%d, want 0,0", row, col)
	}
}

// --- ESC dispatch: RI, RIS, backspace ---

// TestReverseIndexScrollsAndMoves verifies RI (ESC M) moves the cursor up when
// it is below the scroll top, and scrolls content down when it is at the top.
func TestReverseIndexScrollsAndMoves(t *testing.T) {
	// curY != scrollTop -> cursor moves up.
	s := New(3, 10)
	s.curY = 2
	s.Write([]byte("\x1bM"))
	if row, _ := s.CursorPos(); row != 1 {
		t.Errorf("RI with curY=2: row=%d, want 1", row)
	}
	// curY == scrollTop -> content scrolls down, cursor stays.
	s2 := New(3, 10)
	s2.Write([]byte("TOP"))
	s2.Write([]byte("\x1bM"))
	if got := s2.RowString(1); got != "TOP" {
		t.Errorf("RI at scrollTop: RowString(1) = %q, want TOP", got)
	}
	if row, _ := s2.CursorPos(); row != 0 {
		t.Errorf("RI at scrollTop: row=%d, want 0", row)
	}
}

// TestReverseIndexCursorClamp verifies RI clamps the cursor at the top: from
// row 0 above the region it stays put, inside the region it decrements by one.
func TestReverseIndexCursorClamp(t *testing.T) {
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
				t.Errorf("RI(scrollTop=%d, startY=%d) curY = %d, want %d", tc.scrollTop, tc.startY, s.curY, tc.wantY)
			}
		})
	}
}

// TestRISErasesScreen verifies RIS (ESC c) clears the entire screen.
func TestRISErasesScreen(t *testing.T) {
	s := New(4, 5)
	for y := range s.Cells {
		for x := range s.Cells[y] {
			s.Cells[y][x] = Cell{Ch: 'X'}
		}
	}
	s.dispatchEsc('c') // RIS
	for y := range s.Cells {
		for x := range s.Cells[y] {
			if s.Cells[y][x].Ch != ' ' {
				t.Errorf("after RIS Cells[%d][%d].Ch = %q, want ' '", y, x, s.Cells[y][x].Ch)
			}
		}
	}
}

// TestRISResetsStyleAndCursor verifies RIS resets the SGR style and unhides the
// cursor.
func TestRISResetsStyleAndCursor(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[1;31m")) // bold + red
	s.Write([]byte("\x1b[?25l"))  // hide cursor
	s.Write([]byte("\x1bc"))      // RIS
	if s.CursorHidden {
		t.Error("RIS did not unhide cursor")
	}
	if s.style != (Style{}) {
		t.Errorf("RIS did not reset style: %+v", s.style)
	}
}

// TestBackspaceDecrementsCurX verifies BS (0x08) moves the cursor one column
// left.
func TestBackspaceDecrementsCurX(t *testing.T) {
	s := New(1, 20)
	s.Write([]byte("hello")) // cursor at col 5
	s.Write([]byte{0x08})    // BS
	if _, col := s.CursorPos(); col != 4 {
		t.Errorf("backspace from column 5: col=%d, want 4", col)
	}
}

// TestBackspaceAtColumnZeroStaysPut verifies BS (0x08) at the left margin is a
// no-op: the cursor must not move past column 0 into a negative column.
func TestBackspaceAtColumnZeroStaysPut(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte{0x08}) // BS while already at column 0
	if _, col := s.CursorPos(); col != 0 {
		t.Errorf("backspace at column 0: col=%d, want 0 (must not decrement below 0)", col)
	}
}
