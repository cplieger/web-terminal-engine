package vt

import (
	"strings"
	"testing"
)

// TestWireRunEncoding locks the binary wire contract by asserting that
// WireRun field values and attribute bit positions match the canonical
// encoder in terminal/wire_binary.go (the authoritative byte layout).
func TestWireRunEncoding(t *testing.T) {
	s := New(3, 10)
	// Bold red FG (ANSI 1), default BG, with italic+underline.
	s.Write([]byte("\x1b[1;3;4;31mHi\x1b[0m rest"))
	runs := s.RenderRowWire(0)
	if len(runs) < 2 {
		t.Fatalf("expected >=2 runs, got %d", len(runs))
	}

	r := runs[0]
	if r.T != "Hi" {
		t.Errorf("run[0].T = %q, want %q", r.T, "Hi")
	}
	// Red (ANSI index 1) → 0xaa0000 per basic16RGB palette.
	if r.F != 0xaa0000 {
		t.Errorf("run[0].F = 0x%06x, want 0xaa0000", r.F)
	}
	// Default BG → -1.
	if r.B != -1 {
		t.Errorf("run[0].B = %d, want -1", r.B)
	}
	// Default underline color → -1.
	if r.Uc != -1 {
		t.Errorf("run[0].Uc = %d, want -1", r.Uc)
	}
	// Attrs: bold=1 | italic=2 | underline=4 = 7.
	if r.A != 7 {
		t.Errorf("run[0].A = %d, want 7 (bold|italic|underline)", r.A)
	}

	// Second run: plain text, default colors, no attrs.
	r2 := runs[1]
	if r2.F != -1 {
		t.Errorf("run[1].F = %d, want -1 (default)", r2.F)
	}
	if r2.A != 0 {
		t.Errorf("run[1].A = %d, want 0", r2.A)
	}
}

// TestWireRunRGBColor verifies true-color encoding in wire format.
func TestWireRunRGBColor(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("\x1b[38;2;255;128;0mX\x1b[0m"))
	runs := s.RenderRowWire(0)
	if len(runs) == 0 {
		t.Fatal("no runs")
	}
	// RGB(255,128,0) → 0xFF8000.
	if runs[0].F != 0xFF8000 {
		t.Errorf("F = 0x%06x, want 0xFF8000", runs[0].F)
	}
}

// TestWireRunAllAttributes verifies each attribute bit position per spec.
func TestWireRunAllAttributes(t *testing.T) {
	tests := []struct {
		seq  string
		name string
		bit  uint16
	}{
		{seq: "\x1b[1m", bit: 1, name: "bold"},
		{seq: "\x1b[3m", bit: 2, name: "italic"},
		{seq: "\x1b[4m", bit: 4, name: "underline"},
		{seq: "\x1b[7m", bit: 8, name: "inverse"},
		{seq: "\x1b[9m", bit: 16, name: "strikethrough"},
		{seq: "\x1b[2m", bit: 32, name: "dim"},
		{seq: "\x1b[8m", bit: 64, name: "hidden"},
		{seq: "\x1b[5m", bit: 128, name: "blink"},
		{seq: "\x1b[6m", bit: 128, name: "rapid-blink"},
		{seq: "\x1b[53m", bit: 256, name: "overline"},
		{seq: "\x1b[21m", bit: 512, name: "double-underline"},
	}
	for _, tc := range tests {
		s := New(1, 5)
		s.Write([]byte(tc.seq + "X\x1b[0m"))
		runs := s.RenderRowWire(0)
		if len(runs) == 0 {
			t.Errorf("%s: no runs", tc.name)
			continue
		}
		if runs[0].A&tc.bit == 0 {
			t.Errorf("%s: bit %d not set in A=%d", tc.name, tc.bit, runs[0].A)
		}
	}
}

// TestRenderRowWireRejectsOutOfRangeRow verifies the row-bounds guard:
// a row index equal to Height returns nil, while the last valid row is non-nil.
func TestRenderRowWireRejectsOutOfRangeRow(t *testing.T) {
	s := New(3, 8) // valid rows 0..2
	if got := s.RenderRowWire(s.Height); got != nil {
		t.Errorf("RenderRowWire(Height=%d) = %v, want nil", s.Height, got)
	}
	if got := s.RenderRowWire(s.Height - 1); got == nil {
		t.Errorf("RenderRowWire(Height-1=%d) = nil, want non-nil", s.Height-1)
	}
}

// TestBasic16RGBPaletteBounds verifies the 16-entry palette lookup: in-range
// indices map to their colors, and an out-of-range index returns the gray
// fallback rather than panicking.
func TestBasic16RGBPaletteBounds(t *testing.T) {
	if got := basic16RGB(0); got != 0x000000 {
		t.Errorf("basic16RGB(0) = 0x%06x, want 0x000000", got)
	}
	if got := basic16RGB(15); got != 0xffffff {
		t.Errorf("basic16RGB(15) = 0x%06x, want 0xffffff", got)
	}
	if got := basic16RGB(16); got != 0xaaaaaa {
		t.Errorf("basic16RGB(16) = 0x%06x, want 0xaaaaaa (out-of-range fallback)", got)
	}
}

// TestRenderRowWireWidePlaceholder verifies a wide character is followed by a
// U+FFFF spacer placeholder in the wire text, and the total rune count of the
// row equals the screen width.
func TestRenderRowWireWidePlaceholder(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("A漢B"))
	runs := s.RenderRowWire(0)
	var text strings.Builder
	for _, run := range runs {
		text.WriteString(run.T)
	}
	got := text.String()
	if !strings.Contains(got, "A漢\uFFFFB") {
		t.Errorf("wire row = %q, want it to contain %q", got, "A漢\uFFFFB")
	}
	if len([]rune(got)) != 10 {
		t.Errorf("wire row rune count = %d, want 10", len([]rune(got)))
	}
}
