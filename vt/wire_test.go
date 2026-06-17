package vt

import "testing"

// TestWireRunEncoding locks the WIRE_PROTOCOL.md v1 contract by asserting
// that WireRun field values and attribute bit positions match the spec.
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
