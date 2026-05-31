package vt

import (
	"testing"
)

func TestBasicRender(t *testing.T) {
	s := New(30, 120)
	input := "\x1b]0;kiro-cli\x07\x1b[?1h\x1b[?1049h\x1b[H\x1b[2JHello World\r\nLine 2\r\n\x1b[38;2;144;70;255mColored text\x1b[0m"
	s.Write([]byte(input))
	if s.RowString(0) != "Hello World" {
		t.Errorf("Row 0 = %q, want 'Hello World'", s.RowString(0))
	}
	if s.RowString(1) != "Line 2" {
		t.Errorf("Row 1 = %q, want 'Line 2'", s.RowString(1))
	}
	if s.RowString(2) != "Colored text" {
		t.Errorf("Row 2 = %q, want 'Colored text'", s.RowString(2))
	}
}

func TestSGRColors(t *testing.T) {
	s := New(5, 40)
	s.Write([]byte("\x1b[31mRed\x1b[0m"))
	if s.Cells[0][0].Style.FG.Type != 1 || s.Cells[0][0].Style.FG.Val != 1 {
		t.Errorf("expected red FG, got %+v", s.Cells[0][0].Style.FG)
	}
	if s.Cells[0][3].Style.FG.Type != 0 {
		t.Errorf("expected default FG after reset, got %+v", s.Cells[0][3].Style.FG)
	}
}

func TestSGR256Color(t *testing.T) {
	s := New(5, 40)
	s.Write([]byte("\x1b[38;5;200mHi\x1b[0m"))
	if s.Cells[0][0].Style.FG.Type != 2 || s.Cells[0][0].Style.FG.Val != 200 {
		t.Errorf("expected 256-color 200, got %+v", s.Cells[0][0].Style.FG)
	}
}

func TestSGRRGB(t *testing.T) {
	s := New(5, 40)
	s.Write([]byte("\x1b[38;2;100;150;200mX\x1b[0m"))
	c := s.Cells[0][0].Style.FG
	if c.Type != 3 || c.R != 100 || c.G != 150 || c.B != 200 {
		t.Errorf("expected RGB(100,150,200), got %+v", c)
	}
}

func TestNoBArtifacts(t *testing.T) {
	s := New(5, 40)
	s.Write([]byte("\x1b[?25h\x1b[?25lHello"))
	if s.RowString(0) != "Hello" {
		t.Errorf("Row 0 = %q, want 'Hello'", s.RowString(0))
	}
}

func TestRenderViewport(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("\x1b[31mHi\x1b[0m"))
	out := s.RenderViewport()
	if len(out) == 0 {
		t.Error("RenderViewport returned empty")
	}
	if !containsStr(out, "\x1b[") {
		t.Error("RenderViewport missing SGR sequences")
	}
}

func TestScrollbackDrain(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("Line1\nLine2\nLine3\nLine4\n"))
	drained := s.DrainScrollback()
	if len(drained) == 0 {
		t.Error("expected drained lines")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
