package vt

import (
	"strconv"
	"strings"
	"testing"
)

// Round 5 adversarial red-team tests: final convergence attack probing
// wide-char + combining + scroll + resize-to-1col combos, REP of wide char
// at edge, mouse coords at 223/224 boundary, parser fuzz with split/malformed
// sequences, OSC/DCS without terminator, and concurrency stress.

// --- Wide char + combining + scroll + resize-to-1col combo ---

func TestWideCharCombiningScrollResize1Col(t *testing.T) {
	s := New(5, 80)
	// Write wide char + combining accent
	s.Write([]byte("漢\xcc\x81"))  // 漢 + combining acute
	s.Write([]byte("字\xcc\x83"))  // 字 + combining tilde
	s.Write([]byte("\n\n\n\n\n")) // force scrolling
	s.Resize(3, 1)                // shrink to 1 col
	s.Write([]byte("漢\xcc\x81"))  // wide + combining on 1-col
	s.Write([]byte("\n"))
	s.Write([]byte("字\xcc\x83A")) // more content
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB [0,%d)", col, s.Width)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB [0,%d)", row, s.Height)
	}
}

func TestWideCombiningScrollResize1Col_Stress(t *testing.T) {
	s := New(4, 10)
	for i := range 100 {
		// Interleave wide+combining, scroll, and resize
		s.Write([]byte("漢\xcc\x81字\xcc\x83\n"))
		if i%10 == 0 {
			s.Resize(2, 1)
		}
		if i%10 == 5 {
			s.Resize(4, 10)
		}
		// Force scroll
		s.Write([]byte("\x1b[5S"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("iter %d: cursor col %d OOB [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: cursor row %d OOB [0,%d)", i, row, s.Height)
		}
	}
}

// --- REP of wide char at edge (column Width-1) ---

func TestREPWideCharAtLastCol(t *testing.T) {
	// Position cursor at the last column, write a wide char (forces wrap),
	// then REP it many times
	for _, w := range []int{1, 2, 3, 4, 5, 10, 80} {
		s := New(5, w)
		// Move to last column
		s.Write([]byte("\x1b[1;" + itoaR5(w) + "H"))
		s.Write([]byte("漢"))           // wide char at edge → wraps
		s.Write([]byte("\x1b[65535b")) // REP max times
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("width=%d: cursor col %d OOB [0,%d) after REP at edge", w, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("width=%d: cursor row %d OOB [0,%d)", w, row, s.Height)
		}
	}
}

func TestREPCombiningChar(t *testing.T) {
	// REP a combining char — combining chars have width 0, REP should be safe
	s := New(5, 10)
	s.Write([]byte("A\xcc\x81")) // A + combining acute
	// Now REP — lastPrintedRune should be 'A' (combining is width-0 and
	// doesn't update lastPrintedRune per put() logic)
	s.Write([]byte("\x1b[100b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after REP of combining", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- Mouse coords at 223/224 boundary (X10 encoding limit) ---

func TestMouseCoords223Boundary(t *testing.T) {
	// In X10 encoding, coords are encoded as byte+32, so max is 223+32=255.
	// The 224th column can't be represented. Test the emulator doesn't panic
	// when the screen is wider than 223 cols.
	s := New(5, 250)
	s.Write([]byte("\x1b[?1000h")) // enable mouse
	if s.MouseMode != 1000 {
		t.Fatalf("expected mouse mode 1000")
	}
	// Move cursor to col 224 (0-indexed 223)
	s.Write([]byte("\x1b[1;224H"))
	row, col := s.CursorPos()
	if col != 223 {
		t.Fatalf("expected col 223, got %d", col)
	}
	if row != 0 {
		t.Fatalf("expected row 0, got %d", row)
	}
	// Move to col 250
	s.Write([]byte("\x1b[1;250H"))
	_, col = s.CursorPos()
	if col != 249 {
		t.Fatalf("expected col 249, got %d", col)
	}
	// Enable SGR mouse — this handles coords > 223
	s.Write([]byte("\x1b[?1006h"))
	if !s.MouseSGR {
		t.Fatal("expected SGR mode")
	}
}

// --- Parser fuzz with split/malformed sequences ---

func TestParserSplitCSIAcrossWrites(t *testing.T) {
	s := New(5, 80)
	// Split a CSI H sequence byte by byte
	seq := []byte("\x1b[3;5H")
	for _, b := range seq {
		s.Write([]byte{b})
	}
	row, col := s.CursorPos()
	if row != 2 || col != 4 { // 1-indexed → 0-indexed
		t.Fatalf("split CSI H: got row=%d col=%d, want row=2 col=4", row, col)
	}
}

func TestParserMalformedCSI_MissingFinal(t *testing.T) {
	s := New(5, 80)
	// CSI with params but interrupted by another ESC
	s.Write([]byte("\x1b[123\x1b[1;1H"))
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Fatalf("malformed CSI recovery: got row=%d col=%d, want 0,0", row, col)
	}
}

func TestParserMalformedCSI_CANAbort(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("AB"))
	// Start a CSI, abort with CAN (0x18), then normal char
	s.Write([]byte("\x1b[999\x18X"))
	// X should have been printed at cursor position after CAN aborted CSI
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB after CAN abort", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB after CAN abort", row)
	}
	// X should be at col 3 (after AB)
	if s.Cells[0][2].Ch != 'X' {
		t.Fatalf("expected 'X' at col 2, got %q", s.Cells[0][2].Ch)
	}
}

func TestParserMalformedCSI_SUBAbort(t *testing.T) {
	s := New(5, 80)
	// SUB (0x1A) also aborts sequences
	s.Write([]byte("\x1b[?1049\x1A"))
	// Should not have entered alt screen
	if s.InAltScreen {
		t.Fatal("SUB should have aborted the CSI, not entered alt screen")
	}
	if s.pState != stateGround {
		t.Fatalf("parser not in ground state after SUB, got %d", s.pState)
	}
}

func TestParserSplitUTF8AcrossWrites(t *testing.T) {
	s := New(5, 80)
	// 漢 = E6 BC A2, split across 3 separate Write calls
	s.Write([]byte{0xE6})
	s.Write([]byte{0xBC})
	s.Write([]byte{0xA2})
	if s.Cells[0][0].Ch != '漢' {
		t.Fatalf("split UTF-8: got %q, want '漢'", s.Cells[0][0].Ch)
	}
}

func TestParserInvalidUTF8Continuation(t *testing.T) {
	s := New(5, 80)
	// Start a 3-byte UTF-8 sequence but feed a non-continuation byte
	s.Write([]byte{0xE6, 'A'}) // 0xE6 starts 3-byte, 'A' is not continuation
	// Should not panic; parser resets UTF-8 state
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB", row)
	}
}

// --- OSC/DCS without terminator ---

func TestOSCWithoutTerminator_LongStream(t *testing.T) {
	s := New(5, 80)
	// Start OSC, never terminate, feed lots of data
	s.Write([]byte("\x1b]2;"))
	// Feed 10KB without terminator
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte('A' + i%26)
	}
	for range 10 {
		s.Write(chunk)
	}
	// Buffer should be capped
	if len(s.oscBuf) > maxOSCLen {
		t.Fatalf("OSC buffer grew to %d, want <= %d", len(s.oscBuf), maxOSCLen)
	}
	// Parser should still be in OSC state (waiting for terminator)
	if s.pState != stateOscString {
		t.Fatalf("expected stateOscString, got %d", s.pState)
	}
	// CAN should abort
	s.Write([]byte{0x18})
	if s.pState != stateGround {
		t.Fatalf("CAN didn't abort OSC, state=%d", s.pState)
	}
}

func TestDCSWithoutTerminator(t *testing.T) {
	// DCS (ESC P) uses the same OSC buffer/state
	s := New(5, 80)
	s.Write([]byte("\x1bP"))
	// Feed data without ST terminator
	chunk := make([]byte, 512)
	for i := range chunk {
		chunk[i] = byte('0' + i%10)
	}
	for range 20 {
		s.Write(chunk)
	}
	if len(s.oscBuf) > maxOSCLen {
		t.Fatalf("DCS buffer grew to %d, want <= %d", len(s.oscBuf), maxOSCLen)
	}
	// Terminate with ST (ESC \)
	s.Write([]byte("\x1b\\"))
	if s.pState != stateGround {
		t.Fatalf("parser not in ground after DCS ST, got %d", s.pState)
	}
}

// --- Resize-to-1col while mid-CSI ---

func TestResizeTo1ColMidCSI(t *testing.T) {
	s := New(5, 80)
	// Start a CSI sequence
	s.Write([]byte("\x1b["))
	// Resize mid-sequence
	s.Resize(3, 1)
	// Complete the sequence (cursor move)
	s.Write([]byte("1;1H"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB after resize mid-CSI", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB after resize mid-CSI", row)
	}
}

// --- Combined: wide + scroll + resize rapid cycling ---

func TestWideScrollResizeCycle(t *testing.T) {
	s := New(3, 2)
	for i := range 200 {
		s.Write([]byte("漢"))       // wide char exactly fits 2-col
		s.Write([]byte("\x1b[1S")) // scroll up 1
		s.Write([]byte("\x1b[1T")) // scroll down 1
		if i%7 == 0 {
			s.Resize(2, 1)
		}
		if i%7 == 3 {
			s.Resize(3, 2)
		}
		if i%11 == 0 {
			s.Resize(1, 1)
			s.Write([]byte("漢\xcc\x81\n"))
		}
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("iter %d: col %d OOB [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: row %d OOB [0,%d)", i, row, s.Height)
		}
	}
}

// --- Insert/delete chars at boundary with wide char ---

func TestInsertCharsAtEdgeWithWideChar(t *testing.T) {
	s := New(3, 4)
	s.Write([]byte("漢字"))      // fills 4 cols (2+2)
	s.Write([]byte("\x1b[1G")) // back to col 0
	// Insert 3 chars — should shift wide chars out of bounds gracefully
	s.Write([]byte("\x1b[3@"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB", row)
	}
}

func TestDeleteCharsWithWideCharSplit(t *testing.T) {
	s := New(3, 6)
	s.Write([]byte("A漢B字C")) // A(1) 漢(2) B(1) — 4 cols used, then 字(2) wraps? Actually: A=1, 漢=2(col1-2), B=1(col3), 字=2(col4-5), C doesn't fit 6 cols? Let me think...
	// Actually: col0=A, col1-2=漢, col3=B, col4-5=字 → total 6 cols exactly
	s.Write([]byte("\x1b[1G")) // back to col 0
	// Delete 1 char — splits the wide char at col 1
	s.Write([]byte("\x1b[1P"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB", row)
	}
}

// --- Parser rapid ESC transitions ---

func TestRapidESCTransitions(t *testing.T) {
	s := New(5, 80)
	// Many ESC followed by non-sequence chars — tests state machine robustness
	input := strings.Repeat("\x1b\x1b\x1b", 100)
	s.Write([]byte(input))
	// Should be in escape state (last ESC has no following byte yet)
	// Feed a ground-state char to reset
	s.Write([]byte("A"))
	if s.pState != stateGround {
		t.Fatalf("not in ground after repeated ESC + final char, got %d", s.pState)
	}
}

func TestInterleavedESCAndCSI(t *testing.T) {
	s := New(5, 80)
	// ESC [ (start CSI) then ESC [ (new CSI) — first is abandoned
	s.Write([]byte("\x1b[\x1b[1;1H"))
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Fatalf("got row=%d col=%d, want 0,0", row, col)
	}
}

// --- Edge: insertChars when curX is at Width-1 ---

func TestInsertCharsAtLastCol(t *testing.T) {
	s := New(3, 5)
	s.Write([]byte("ABCDE"))    // fills row, cursor at col 4 with pendingWrap
	s.Write([]byte("\x1b[4G"))  // move to col 4 (1-indexed) = col 3
	s.Write([]byte("\x1b[10@")) // insert 10 chars (more than available)
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB", row)
	}
}

// --- Extremely deep scroll region with operations ---

func TestDeepScrollRegion(t *testing.T) {
	s := New(5, 10)
	// Set scroll region to single line
	s.Write([]byte("\x1b[3;3r")) // top=3, bottom=3 (1-indexed)
	// Scroll operations in single-line region
	s.Write([]byte("\x1b[100S"))
	s.Write([]byte("\x1b[100T"))
	s.Write([]byte("\x1b[100L"))
	s.Write([]byte("\x1b[100M"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB", row)
	}
}

// --- Save cursor, resize smaller, restore ---

func TestSaveCursorResizeSmallerRestore(t *testing.T) {
	s := New(20, 80)
	// Move to row 15, col 70
	s.Write([]byte("\x1b[16;71H"))
	// Save cursor (DECSC)
	s.Write([]byte("\x1b7"))
	// Resize much smaller
	s.Resize(5, 10)
	// Restore cursor (DECRC)
	s.Write([]byte("\x1b8"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d OOB [0,%d) after restore post-resize", col, s.Width)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d OOB [0,%d) after restore post-resize", row, s.Height)
	}
}

// --- Multiple OSC types without terminators ---

func TestMultipleOSCTypesNoTerminator(t *testing.T) {
	s := New(5, 80)
	// Start OSC 8 (hyperlink) without terminator, then abort with new ESC
	s.Write([]byte("\x1b]8;params;http://example.com"))
	// Without BEL/ST, feed a new ESC sequence that starts fresh
	s.Write([]byte("\x1b[1;1H"))
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Fatalf("expected cursor at 0,0 after OSC abort via ESC, got %d,%d", row, col)
	}
}

// helper
func itoaR5(n int) string {
	return strconv.Itoa(n)
}
