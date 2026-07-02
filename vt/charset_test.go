package vt

import "testing"

func TestDesignateG0_DECSpecialGraphics(t *testing.T) {
	s := New(2, 20)
	// ESC(0 designates DEC Special Graphics into G0, then 'q' → ─ (U+2500)
	s.Write([]byte("\x1b(0q"))
	if got := s.Cells[0][0].Ch; got != '\u2500' {
		t.Errorf("expected U+2500 (─), got U+%04X", got)
	}
}

func TestDesignateG0_BackToASCII(t *testing.T) {
	s := New(2, 20)
	// ESC(0 then ESC(B switches back to ASCII
	s.Write([]byte("\x1b(0\x1b(Bq"))
	if got := s.Cells[0][0].Ch; got != 'q' {
		t.Errorf("expected 'q', got U+%04X", got)
	}
}

func TestDECSpecialGraphics_FullRange(t *testing.T) {
	s := New(2, 80)
	s.Write([]byte("\x1b(0"))
	// Write all mapped bytes 0x60-0x7E
	input := make([]byte, 31)
	for i := range input {
		input[i] = byte(0x60 + i)
	}
	s.Write(input)
	// The expected runes are the DEC Special Graphics -> Unicode mapping from the
	// VT220 Programmer Reference (Table 2-4) / the "DEC Special Graphics" chart,
	// i.e. the spec, not a copy of the engine's table: 0x60 '`' -> ◆, the
	// line-drawing box (j-x), the scan lines (o-s), and ≤ ≥ π ≠ £ · at the tail.
	expected := []rune{
		'\u25c6', '\u2592', '\u2409', '\u240c', '\u240d', '\u240a',
		'\u00b0', '\u00b1', '\u2424', '\u240b', '\u2518', '\u2510',
		'\u250c', '\u2514', '\u253c', '\u23ba', '\u23bb', '\u2500',
		'\u23bc', '\u23bd', '\u251c', '\u2524', '\u2534', '\u252c',
		'\u2502', '\u2264', '\u2265', '\u03c0', '\u2260', '\u00a3',
		'\u00b7',
	}
	for i, exp := range expected {
		got := s.Cells[0][i].Ch
		if got != exp {
			t.Errorf("byte 0x%02X: expected U+%04X, got U+%04X", 0x60+i, exp, got)
		}
	}
}

func TestDECSpecialGraphics_ASCIIUnchanged(t *testing.T) {
	s := New(2, 20)
	s.Write([]byte("\x1b(0"))
	// Bytes below 0x60 should pass through unchanged
	s.Write([]byte("ABC"))
	if s.Cells[0][0].Ch != 'A' || s.Cells[0][1].Ch != 'B' || s.Cells[0][2].Ch != 'C' {
		t.Errorf("ASCII chars below 0x60 should not be translated")
	}
}

func TestSOSI_LockingShift(t *testing.T) {
	s := New(2, 20)
	// Designate G1 as DEC Special Graphics
	s.Write([]byte("\x1b)0"))
	// SO (0x0E) shifts GL to G1
	s.Write([]byte("\x0eq"))
	if got := s.Cells[0][0].Ch; got != '\u2500' {
		t.Errorf("SO+G1=graphic: expected U+2500, got U+%04X", got)
	}
	// SI (0x0F) shifts GL back to G0 (ASCII)
	s.Write([]byte("\x0fq"))
	if got := s.Cells[0][1].Ch; got != 'q' {
		t.Errorf("SI+G0=ASCII: expected 'q', got U+%04X", got)
	}
}

func TestSS2_SingleShift(t *testing.T) {
	s := New(2, 20)
	// Designate G2 as DEC Special Graphics
	s.Write([]byte("\x1b*0"))
	// SS2 (ESC N) single-shifts G2 for one char
	s.Write([]byte("\x1bNq"))
	if got := s.Cells[0][0].Ch; got != '\u2500' {
		t.Errorf("SS2: expected U+2500, got U+%04X", got)
	}
	// Next char should be ASCII again (GL=G0=ASCII)
	s.Write([]byte("q"))
	if got := s.Cells[0][1].Ch; got != 'q' {
		t.Errorf("after SS2: expected 'q', got U+%04X", got)
	}
}

func TestSS3_SingleShift(t *testing.T) {
	s := New(2, 20)
	// Designate G3 as DEC Special Graphics
	s.Write([]byte("\x1b+0"))
	// SS3 (ESC O) single-shifts G3 for one char
	s.Write([]byte("\x1bOx"))
	if got := s.Cells[0][0].Ch; got != '\u2502' {
		t.Errorf("SS3: expected U+2502 (│), got U+%04X", got)
	}
	// Next char should be ASCII
	s.Write([]byte("x"))
	if got := s.Cells[0][1].Ch; got != 'x' {
		t.Errorf("after SS3: expected 'x', got U+%04X", got)
	}
}

func TestRIS_ResetsCharsets(t *testing.T) {
	s := New(2, 20)
	s.Write([]byte("\x1b(0"))
	// RIS (ESC c) should reset charsets
	s.Write([]byte("\x1bc"))
	s.Write([]byte("q"))
	if got := s.Cells[0][0].Ch; got != 'q' {
		t.Errorf("after RIS: expected 'q', got U+%04X", got)
	}
}

func TestSoftReset_ResetsCharsets(t *testing.T) {
	s := New(2, 20)
	s.Write([]byte("\x1b(0"))
	// DECSTR (CSI ! p) triggers softReset
	s.Write([]byte("\x1b[!p"))
	s.Write([]byte("q"))
	if got := s.Cells[0][0].Ch; got != 'q' {
		t.Errorf("after DECSTR: expected 'q', got U+%04X", got)
	}
}

func TestESCHash_Consumed(t *testing.T) {
	s := New(2, 20)
	// ESC # 8 (DECALN) should be consumed without error, not designate anything
	s.Write([]byte("\x1b#8q"))
	if got := s.Cells[0][0].Ch; got != 'q' {
		t.Errorf("ESC#8 should not affect charset: expected 'q', got U+%04X", got)
	}
}

// TestSingleShiftConsumedAffectsOneChar verifies a single shift (SS2/ESC N)
// applies to exactly ONE printable: the first char is translated through G2,
// and every char after it reverts to GL (G0 = ASCII), so the shift does not
// linger.
func TestSingleShiftConsumedAffectsOneChar(t *testing.T) {
	s := New(2, 20)
	// G2 = DEC Special Graphics; SS2 (ESC N), then three 'q'.
	s.Write([]byte("\x1b*0\x1bNqqq"))

	// Only the first 'q' is translated through G2 graphics -> U+2500 (─).
	if got := s.Cells[0][0].Ch; got != '\u2500' {
		t.Errorf("SS2 first char = U+%04X, want U+2500 (─)", got)
	}
	// The shift is consumed after one char: the rest print as ASCII 'q'.
	if got := s.Cells[0][1].Ch; got != 'q' {
		t.Errorf("char after single shift = U+%04X, want 'q' (shift consumed)", got)
	}
	if got := s.Cells[0][2].Ch; got != 'q' {
		t.Errorf("second char after single shift = U+%04X, want 'q'", got)
	}
}

// TestResetCharsetsRestoresDefaults verifies resetCharsets returns all charset
// state to its defaults: the single-shift sentinel (-1), GL=G0, and every Gn
// designated to ASCII.
func TestResetCharsetsRestoresDefaults(t *testing.T) {
	s := New(2, 20)
	// Put charset state into a distinct non-default value first.
	s.singleShft = 7
	s.gl = 3
	s.gsets = [4]charset{charsetGraphic, charsetGraphic, charsetGraphic, charsetGraphic}

	s.resetCharsets()

	if got := int(s.singleShft); got != -1 {
		t.Errorf("resetCharsets singleShft = %d, want -1", got)
	}
	if got := int(s.gl); got != 0 {
		t.Errorf("resetCharsets gl = %d, want 0", got)
	}
	for i, c := range s.gsets {
		if c != charsetASCII {
			t.Errorf("resetCharsets gsets[%d] = %d, want charsetASCII(%d)", i, c, charsetASCII)
		}
	}
}
