package vt

import (
	"strconv"
	"testing"
)

// Wide-character, combining-mark, and emoji handling under adversarial input:
// repeated wide chars on narrow screens, wide chars split across writes and
// across resizes, REP of wide/combining runes near the edge, scrollback drains
// of wide content, and alt-screen + resize combinations. The shared invariant
// is that the cursor never leaves [0,Width) x [0,Height) and nothing panics.

// --- Wide chars on narrow screens ---

func TestWideCharWidth1ScreenRepeated(t *testing.T) {
	s := New(3, 1)
	for range 100 {
		s.Write([]byte("漢"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d out of bounds after wide char on 1-col", col)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d out of bounds", row)
		}
	}
}

func TestWideCharWidth1ScreenMixedASCII(t *testing.T) {
	s := New(3, 1)
	for range 50 {
		s.Write([]byte("漢A字B"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d out of bounds", col)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d out of bounds", row)
		}
	}
}

func TestWideCharWidth2Screen(t *testing.T) {
	s := New(3, 2)
	s.Write([]byte("漢")) // fits exactly
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds on 2-col screen", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds on 2-col screen", row)
	}
	s.Write([]byte("字")) // second wide char wraps
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after 2nd wide on 2-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds after 2nd wide", row)
	}
}

func TestWideCharWidth3Screen(t *testing.T) {
	s := New(3, 3)
	s.Write([]byte("AB")) // cursor at col 2
	s.Write([]byte("漢"))  // width-2 can't fit at col 2 -> wrap
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds on 3-col screen", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

// --- Combining marks ---

func TestCombiningCharAsFirstInput(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\xcc\x81")) // U+0301 as the very first byte
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after combining as first char", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestCombiningCharOnWidth1Screen(t *testing.T) {
	s := New(3, 1)
	s.Write([]byte("\xcc\x81"))
	s.Write([]byte("A"))
	s.Write([]byte("\xcc\x81"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

// --- Wide char split across writes and resizes ---

func TestWideCharSplitWritesThenResize1Col(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0xE6}) // first byte of 漢
	s.Resize(5, 1)        // resize mid-codepoint
	s.Write([]byte{0xBC, 0xA2})
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after split-write + resize", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestWideCharSplitWriteThenResize1ColThenMore(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0xE6, 0xBC}) // first 2 bytes of 漢
	s.Resize(3, 1)
	s.Write([]byte{0xA2}) // complete the char on 1-col screen
	for range 20 {
		s.Write([]byte("漢A"))
	}
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

// --- REP of wide / combining chars near the edge ---

func TestREPWideCharNearEdge(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[1;8H")) // near right edge
	s.Write([]byte("漢"))         // wraps
	s.Write([]byte("\x1b[100b")) // REP 100 times
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after REP wide char near edge", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestREPWideCharOnWidth1(t *testing.T) {
	s := New(5, 1)
	s.Write([]byte("漢"))
	s.Write([]byte("\x1b[500b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after REP on 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestREPWideCharOnWidth2(t *testing.T) {
	s := New(5, 2)
	s.Write([]byte("漢"))
	s.Write([]byte("\x1b[500b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after REP on 2-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestREPWideCharAtLastCol(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 5, 10, 80} {
		s := New(5, w)
		s.Write([]byte("\x1b[1;" + strconv.Itoa(w) + "H")) // last column
		s.Write([]byte("漢"))                               // wraps
		s.Write([]byte("\x1b[65535b"))                     // REP max times
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("width=%d: cursor col %d out of bounds after REP at edge", w, col)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("width=%d: cursor row %d out of bounds", w, row)
		}
	}
}

func TestREPCombiningChar(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("A\xcc\x81")) // A + combining acute (width 0)
	s.Write([]byte("\x1b[100b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after REP of combining", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

// --- Scrollback drain with wide chars ---

func TestScrollbackDrainWideChars(t *testing.T) {
	s := New(3, 4)
	for range 50 {
		s.Write([]byte("漢字\n"))
	}
	d := s.DrainScrollback()
	for i, runs := range d {
		totalCols := 0
		for _, run := range runs {
			totalCols += len([]rune(run.T))
		}
		if totalCols != 4 {
			t.Errorf("drained line %d has %d cols, want 4", i, totalCols)
			break
		}
	}
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

func TestScrollbackDrainWideCharsWidth1(t *testing.T) {
	s := New(2, 1)
	for range 100 {
		s.Write([]byte("漢"))
	}
	d := s.DrainScrollback()
	for i, runs := range d {
		totalCols := 0
		for _, run := range runs {
			totalCols += len([]rune(run.T))
		}
		if totalCols != 1 {
			t.Errorf("drained line %d has %d cols, want 1", i, totalCols)
			break
		}
	}
}

// --- Alt screen + wide chars + resize ---

func TestAltScreenWideCharResize(t *testing.T) {
	s := New(10, 80)
	s.Write([]byte("Main漢字Content"))
	s.Write([]byte("\x1b[?1049h"))
	if !s.InAltScreen {
		t.Fatal("expected alt screen")
	}
	s.Write([]byte("漢字テスト"))
	s.Resize(3, 1) // resize to 1 col while in alt
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after alt resize to 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
	s.Write([]byte("\x1b[?1049l"))
	if s.InAltScreen {
		t.Fatal("expected main screen")
	}
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after exiting alt at 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds after exiting alt", row)
	}
}

func TestAltScreenResizeWideCharBoundary(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("漢字テスト長い文章"))
	s.Write([]byte("\x1b[?1049h"))
	s.Write([]byte("ALT漢字"))
	s.Resize(3, 2) // wide-char boundary
	s.Write([]byte("字"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
	s.Write([]byte("\x1b[?1049l"))
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after exit", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds after exit", row)
	}
}

// --- Resize storms ---

func TestResizeStormWideChars(t *testing.T) {
	s := New(10, 80)
	for i := range 200 {
		s.Write([]byte("漢字"))
		s.Resize(1+(i%5), 1+(i%3))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("iter %d: cursor col %d out of bounds [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: cursor row %d out of bounds [0,%d)", i, row, s.Height)
		}
	}
}

// --- Emoji (4-byte wide char) ---

func TestEmojiWidth1Screen(t *testing.T) {
	s := New(3, 1)
	s.Write([]byte("\xf0\x9f\x98\x80")) // U+1F600, width 2
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after emoji on 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
	for range 30 {
		s.Write([]byte("\xf0\x9f\x98\x80"))
	}
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds after many emoji", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds after many emoji", row)
	}
}

// --- Cursor move + wide char on narrow screen ---

func TestCursorMoveWideNarrow(t *testing.T) {
	s := New(3, 1)
	s.Write([]byte("\x1b[1;1H漢\x1b[1;1H漢\x1b[1;1H"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds", row)
	}
}

// --- Wide + combining + scroll + resize-to-1col combinations ---

func TestWideCharCombiningScrollResize1Col(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("漢\xcc\x81"))  // 漢 + combining acute
	s.Write([]byte("字\xcc\x83"))  // 字 + combining tilde
	s.Write([]byte("\n\n\n\n\n")) // force scrolling
	s.Resize(3, 1)                // shrink to 1 col
	s.Write([]byte("漢\xcc\x81"))
	s.Write([]byte("\n"))
	s.Write([]byte("字\xcc\x83A"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d out of bounds [0,%d)", col, s.Width)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d out of bounds [0,%d)", row, s.Height)
	}
}

func TestWideCombiningScrollResize1ColStress(t *testing.T) {
	s := New(4, 10)
	for i := range 100 {
		s.Write([]byte("漢\xcc\x81字\xcc\x83\n"))
		if i%10 == 0 {
			s.Resize(2, 1)
		}
		if i%10 == 5 {
			s.Resize(4, 10)
		}
		s.Write([]byte("\x1b[5S"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("iter %d: cursor col %d out of bounds [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: cursor row %d out of bounds [0,%d)", i, row, s.Height)
		}
	}
}

func TestWideScrollResizeCycle(t *testing.T) {
	s := New(3, 2)
	for i := range 200 {
		s.Write([]byte("漢"))
		s.Write([]byte("\x1b[1S"))
		s.Write([]byte("\x1b[1T"))
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
			t.Fatalf("iter %d: col %d out of bounds [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: row %d out of bounds [0,%d)", i, row, s.Height)
		}
	}
}

// --- Insert/delete chars at boundary with wide chars ---

func TestInsertCharsAtEdgeWithWideChar(t *testing.T) {
	s := New(3, 4)
	s.Write([]byte("漢字"))      // fills 4 cols
	s.Write([]byte("\x1b[1G")) // back to col 0
	s.Write([]byte("\x1b[3@")) // insert 3 chars
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d out of bounds", row)
	}
}

func TestDeleteCharsWithWideCharSplit(t *testing.T) {
	s := New(3, 6)
	s.Write([]byte("A漢B字C"))   // col0=A, col1-2=漢, col3=B, col4-5=字
	s.Write([]byte("\x1b[1G")) // back to col 0
	s.Write([]byte("\x1b[1P")) // delete 1 char, splitting the wide char at col 1
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("col %d out of bounds", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("row %d out of bounds", row)
	}
}
