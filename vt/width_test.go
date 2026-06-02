package vt

import "testing"

func TestRuneWidth(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want int
	}{
		{"ASCII a", 'a', 1},
		{"CJK ideograph", '中', 2},
		{"Fullwidth A", 'Ａ', 2},
		{"Hangul syllable", '한', 2},
		{"Combining acute", '\u0301', 0},
		{"Combining diaeresis", '\u0308', 0},
		{"Zero-width space", '\u200B', 0},
		{"Ambiguous (degree)", '°', 1},
		{"CJK Ext B", 0x20000, 2},
		{"Latin space", ' ', 1},
		{"IDEOGRAPHIC HALF FILL SPACE", '\u303F', 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runeWidth(tt.r); got != tt.want {
				t.Errorf("runeWidth(%U) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}

func TestPutWideChar(t *testing.T) {
	s := New(3, 10)
	// Write a CJK character — should occupy 2 cells.
	s.Write([]byte("中"))
	if s.curX != 2 {
		t.Fatalf("curX after wide char = %d, want 2", s.curX)
	}
	if s.Cells[0][0].Ch != '中' {
		t.Fatalf("cell[0][0] = %U, want '中'", s.Cells[0][0].Ch)
	}
	// Spacer cell should be 0 (rendered as \uFFFF on wire).
	if s.Cells[0][1].Ch != 0 {
		t.Fatalf("cell[0][1] = %U, want 0 (spacer)", s.Cells[0][1].Ch)
	}
}

func TestPutCombiningMark(t *testing.T) {
	s := New(3, 10)
	// Write 'e' then combining acute accent — cursor should not advance.
	s.Write([]byte("e\xCC\x81")) // e + U+0301
	if s.curX != 1 {
		t.Fatalf("curX after combining = %d, want 1", s.curX)
	}
}

func TestWideCharAtRightEdgeWraps(t *testing.T) {
	s := New(3, 5)
	// Fill 4 cells with 'a', then write a wide char at col 4 (only 1 cell left).
	s.Write([]byte("aaaa"))
	if s.curX != 4 {
		t.Fatalf("curX after 4 a's = %d, want 4", s.curX)
	}
	s.Write([]byte("中"))
	// Wide char should wrap to next line.
	if s.curY != 1 {
		t.Fatalf("curY after wrap = %d, want 1", s.curY)
	}
	if s.curX != 2 {
		t.Fatalf("curX after wrap = %d, want 2", s.curX)
	}
	// The wide char should be at row 1, col 0.
	if s.Cells[1][0].Ch != '中' {
		t.Fatalf("cell[1][0] = %U, want '中'", s.Cells[1][0].Ch)
	}
	// The last cell of row 0 should be a space (dead cell from wrap).
	if s.Cells[0][4].Ch != ' ' {
		t.Fatalf("cell[0][4] = %U, want ' ' (dead cell)", s.Cells[0][4].Ch)
	}
}

func TestWideCharWireRuns(t *testing.T) {
	s := New(1, 6)
	s.Write([]byte("中a"))
	runs := s.RenderRowWire(0)
	// All same style → single run. Text should be: 中 + \uFFFF + a + spaces.
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	runes := []rune(runs[0].T)
	if runes[0] != '中' {
		t.Errorf("rune[0] = %U, want '中'", runes[0])
	}
	if runes[1] != '\uFFFF' {
		t.Errorf("rune[1] = %U, want \\uFFFF (spacer)", runes[1])
	}
	if runes[2] != 'a' {
		t.Errorf("rune[2] = %U, want 'a'", runes[2])
	}
}
