package vt

import "testing"

// Tests for the second feature batch: OSC 4 palette, OSC 52 clipboard,
// mouse mode 1016 (SGR-pixels), DECNKM (?66), and LNM (mode 20).

// --- OSC 4 palette ---

// OSC 4 overrides a palette index; the override reaches the wire color for both
// the basic (SGR 3x) and 256-color (38;5;N) forms of that index.
func TestOSC4PaletteOverride(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]4;1;rgb:00/ff/00\x07")) // index 1 -> pure green
	if !s.PaletteChanged {
		t.Errorf("OSC 4 set did not mark PaletteChanged")
	}
	s.Write([]byte("\x1b[31mX")) // SGR 31 = basic fg index 1
	runs := s.RenderRowWire(0)
	if len(runs) == 0 || runs[0].F != 0x00ff00 {
		t.Errorf("OSC 4 override (basic): run F = %#06x, want 0x00ff00", runs[0].F)
	}
	s2 := New(2, 10)
	s2.Write([]byte("\x1b]4;1;rgb:00/ff/00\x07\x1b[38;5;1mY"))
	if r2 := s2.RenderRowWire(0); len(r2) == 0 || r2[0].F != 0x00ff00 {
		t.Errorf("OSC 4 override (256-color): run F = %#06x, want 0x00ff00", r2[0].F)
	}
}

// OSC 4 with a "?" spec reports the current color of the index.
func TestOSC4Query(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]4;1;?\x07"))
	// Default palette index 1 is 0xaa0000; reported as 16-bit-per-channel.
	want := "\x1b]4;1;rgb:aaaa/0000/0000\x1b\\"
	if got := string(s.Response); got != want {
		t.Errorf("OSC 4 query = %q, want %q", got, want)
	}
}

// OSC 104 resets a palette override back to the default color.
func TestOSC104Reset(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]4;1;rgb:00/ff/00\x07"))
	s.Write([]byte("\x1b]104;1\x07")) // reset index 1
	s.Write([]byte("\x1b[31mX"))
	if runs := s.RenderRowWire(0); len(runs) == 0 || runs[0].F != 0xaa0000 {
		t.Errorf("after OSC 104 reset: run F = %#06x, want default 0xaa0000", runs[0].F)
	}
}

// The #RRGGBB color-spec form is also accepted.
func TestOSC4HashSpec(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]4;2;#0000ff\x07\x1b[32mX")) // index 2 -> blue; SGR 32 = index 2
	if runs := s.RenderRowWire(0); len(runs) == 0 || runs[0].F != 0x0000ff {
		t.Errorf("OSC 4 #RRGGBB: run F = %#06x, want 0x0000ff", runs[0].F)
	}
}

// --- OSC 52 clipboard ---

// OSC 52 SET decodes the base64 payload into PendingClipboard.
func TestOSC52ClipboardSet(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]52;c;aGVsbG8=\x07")) // base64("hello")
	if got := string(s.PendingClipboard); got != "hello" {
		t.Errorf("OSC 52 set: PendingClipboard = %q, want hello", got)
	}
}

// OSC 52 GET ("?") is denied: no clipboard event, no response.
func TestOSC52QueryDenied(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b]52;c;?\x07"))
	if s.PendingClipboard != nil {
		t.Errorf("OSC 52 query should be denied, got PendingClipboard %q", s.PendingClipboard)
	}
	if len(s.Response) != 0 {
		t.Errorf("OSC 52 query should not respond, got %q", s.Response)
	}
}

// --- Mouse mode 1016 (SGR-pixels) ---

func TestMouse1016Tracked(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b[?1016h"))
	if !s.MousePixels {
		t.Error("?1016h did not set MousePixels")
	}
	s.Response = nil
	s.Write([]byte("\x1b[?1016$p")) // DECRQM
	if got, want := string(s.Response), "\x1b[?1016;1$y"; got != want {
		t.Errorf("DECRQM ?1016 = %q, want %q", got, want)
	}
	s.Write([]byte("\x1b[?1016l"))
	if s.MousePixels {
		t.Error("?1016l did not clear MousePixels")
	}
}

// --- DECNKM (?66 application keypad) ---

func TestDECNKM(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("\x1b[?66h"))
	if !s.AppKeypad {
		t.Error("?66h did not set AppKeypad")
	}
	s.Response = nil
	s.Write([]byte("\x1b[?66$p"))
	if got, want := string(s.Response), "\x1b[?66;1$y"; got != want {
		t.Errorf("DECRQM ?66 = %q, want %q", got, want)
	}
	s.Write([]byte("\x1b[?66l"))
	if s.AppKeypad {
		t.Error("?66l did not clear AppKeypad")
	}
}

// --- LNM (mode 20, newline mode) ---

func TestLNM(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("\x1b[20h")) // LNM on
	if !s.LineFeedNewLine {
		t.Error("CSI 20h did not set LineFeedNewLine")
	}
	// With LNM, the LF also carriage-returns, so "cd" starts at column 0.
	s.Write([]byte("ab\ncd"))
	if got := s.RowString(1); got != "cd" {
		t.Errorf("LNM row 1 = %q, want %q", got, "cd")
	}
	s.Response = nil
	s.Write([]byte("\x1b[20$p"))
	if got, want := string(s.Response), "\x1b[20;1$y"; got != want {
		t.Errorf("DECRQM 20 = %q, want %q", got, want)
	}
	s.Write([]byte("\x1b[20l"))
	if s.LineFeedNewLine {
		t.Error("CSI 20l did not clear LineFeedNewLine")
	}
}

// Without LNM, a bare LF preserves the column (regression guard for the flag).
func TestLNMOffPreservesColumn(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("ab\ncd"))
	if got := s.RowString(1); got != "  cd" {
		t.Errorf("no LNM: row 1 = %q, want %q", got, "  cd")
	}
}

// --- Reverse-wraparound (mode ?45) ---

func TestReverseWraparound(t *testing.T) {
	s := New(3, 5)
	s.Write([]byte("\x1b[2;1H")) // row 1, col 0

	// Without ?45, BS at the left margin stays put.
	s.Write([]byte("\b"))
	if row, col := s.CursorPos(); row != 1 || col != 0 {
		t.Errorf("BS at col 0 without ?45 -> %d,%d, want 1,0", row, col)
	}

	// With ?45, BS at the left margin wraps to the end of the previous line.
	s.Write([]byte("\x1b[?45h\b"))
	if row, col := s.CursorPos(); row != 0 || col != 4 {
		t.Errorf("BS at col 0 with ?45 -> %d,%d, want 0,4 (end of previous line)", row, col)
	}

	// At the top of the screen, BS under ?45 wraps around to the bottom-right —
	// xterm's version-0 mode-45 behavior (the classic X10.4 upper-left ->
	// lower-right wrap), which esctest asserts by default (--xterm-reverse-wrap=0)
	// via test_BS_ReverseWrapGoesToBottom. The 2023 xterm split that limits ?45
	// and moves the wrap-around to ?1045 is a later, non-default version.
	s.Write([]byte("\x1b[1;1H\b"))
	if row, col := s.CursorPos(); row != 2 || col != 4 {
		t.Errorf("BS at top-left with ?45 -> %d,%d, want 2,4 (wrap around to bottom-right)", row, col)
	}

	s.Write([]byte("\x1b[?45l"))
	if s.ReverseWrap {
		t.Error("?45l did not clear ReverseWrap")
	}
}

// Reverse-wraparound requires DECAWM (autowrap): with ?45 set but ?7 reset, a
// Backspace at the left margin must NOT wrap back (xterm gates ?45 on ?7).
func TestReverseWraparoundRequiresAutowrap(t *testing.T) {
	s := New(3, 5)
	s.Write([]byte("\x1b[?45h\x1b[?7l")) // reverse-wrap on, autowrap OFF
	s.Write([]byte("\x1b[2;1H\b"))       // row 1, col 0, then BS
	if row, col := s.CursorPos(); row != 1 || col != 0 {
		t.Errorf("BS with ?45 but autowrap off -> %d,%d, want 1,0 (no reverse wrap)", row, col)
	}
}

// --- DECRQCRA (rectangular-area checksum, CSI Pid;Pp;Pt;Pl;Pb;Pr * y) ---

// DECRQCRA reports the negated 16-bit ordinal sum of a rectangle as
// DCS Pid ! ~ hhhh ST, matching xterm (patch < 279, esctest's default). This is
// the primitive esctest uses to read the screen back for content assertions.
func TestDECRQCRAChecksum(t *testing.T) {
	s := New(2, 10)
	s.AllowScreenReport = true
	s.Write([]byte("AB")) // row 0: 'A'(0x41) at col0, 'B'(0x42) at col1

	// Single cell containing 'A': checksum = 0x10000 - 0x41 = 0xFFBF.
	s.Response = nil
	s.Write([]byte("\x1b[1;0;1;1;1;1*y")) // Pid=1 Pp=0 rect=(top1,left1,bottom1,right1)
	if got, want := string(s.Response), "\x1bP1!~FFBF\x1b\\"; got != want {
		t.Errorf("DECRQCRA single cell 'A' = %q, want %q", got, want)
	}

	// Two cells "AB": checksum = 0x10000 - (0x41+0x42) = 0xFF7D.
	s.Response = nil
	s.Write([]byte("\x1b[2;0;1;1;1;2*y")) // Pid=2, cols 1..2
	if got, want := string(s.Response), "\x1bP2!~FF7D\x1b\\"; got != want {
		t.Errorf("DECRQCRA cells 'AB' = %q, want %q", got, want)
	}

	// A blank (unwritten) cell counts as space (0x20): checksum = 0xFFE0.
	s.Response = nil
	s.Write([]byte("\x1b[3;0;1;5;1;5*y")) // Pid=3, a blank cell at col 5
	if got, want := string(s.Response), "\x1bP3!~FFE0\x1b\\"; got != want {
		t.Errorf("DECRQCRA blank cell = %q, want %q", got, want)
	}
}

// DECRQCRA is gated: with AllowScreenReport off (production default) it produces
// no response, so it can't be used to scrape and re-inject screen content.
func TestDECRQCRAGatedOff(t *testing.T) {
	s := New(2, 10)
	s.Write([]byte("AB"))
	s.Response = nil
	s.Write([]byte("\x1b[1;0;1;1;1;1*y"))
	if len(s.Response) != 0 {
		t.Errorf("DECRQCRA with AllowScreenReport off should not respond, got %q", s.Response)
	}
}
