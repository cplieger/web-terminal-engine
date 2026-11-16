package vt

import (
	"slices"
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

// --- Edits that cut a wide character in two ---

// cellsString renders row y with a wide character's right half as '|' and
// the trailing blanks dropped.
func cellsString(s *Screen, y int) string {
	return rowText(s.Cells[y])
}

// An edit that replaces, erases or shifts away one half of a wide character
// blanks the other half too, as xterm (screen.c ClearCells, ScrnWriteText)
// and xterm.js (BufferLine.replaceCells, insertCells, deleteCells) do.
func TestEditsBlankBothHalvesOfACutWideCharacter(t *testing.T) {
	const line = "a中b中c" // columns: a 中 | b 中 | c
	for _, tc := range []struct {
		name, setup, edit, want string
		width, row              int
	}{
		{"a narrow character over the right half", line, "\x1b[1;3HX", "a Xb中|c", 8, 0},
		{"a narrow character over the left half", line, "\x1b[1;2HX", "aX b中|c", 8, 0},
		{"a wide character over a right half", line, "\x1b[1;3H字", "a 字|中|c", 8, 0},
		{"a wide character over a left half", line, "\x1b[1;4H字", "a中|字| c", 8, 0},
		{"EL 0 from a right half", line, "\x1b[1;3H\x1b[K", "a", 8, 0},
		{"EL 1 to a left half", line, "\x1b[1;5H\x1b[1K", "      c", 8, 0},
		{"ECH on a left half", line, "\x1b[1;2H\x1b[X", "a  b中|c", 8, 0},
		{"DCH at a right half", line, "\x1b[1;3H\x1b[P", "a b中|c", 8, 0},
		{"DCH ending on a left half", line, "\x1b[1;4H\x1b[2P", "a中| c", 8, 0},
		{"ICH at a right half", line, "\x1b[1;3H\x1b[@", "a   b中|c", 8, 0},
		{"ICH pushing a right half past the edge", "abcdef中", "\x1b[1;1H\x1b[@", " abcdef", 8, 0},
		{"DECFRA over a right half", line, "\x1b[88;1;3;1;3$x", "a Xb中|c", 8, 0},
		{"DECCRA copying a lone right half", line, "\x1b[1;3;1;3;1;1;8$v", "a中|b中|c", 8, 0},
		{"SL shifting a right half to the edge", line, "\x1b[2 @", " b中|c", 8, 0},
		{"SU in a box whose edge cuts a wide character", line + "\r\n12345678", "\x1b[?69h\x1b[3;8s\x1b[S", "a 345678", 8, 0},
		{"EL on a protected wide character's right edge", "a\x1b[1\"q中\x1b[0\"qbc", "\x1b[1;3H\x1b[?K", "a中|", 8, 0},
		{"EL 1 on a protected wide character's left edge", "ab\x1b[1\"q中\x1b[0\"qc", "\x1b[1;3H\x1b[?1K", "  中|c", 8, 0},
		{"DECCRA copying a lone left half", "a中bcdefg\r\n", "\x1b[1;1;1;2;1;2;1;1$v", "a", 8, 1},
		{"a wrap pad on a right half", "abcdef中", "\x1b[1;8H字", "abcdef", 8, 0},
		{"DCH shifting a cut-off wide character", "\x1b[?7lab中", "\x1b[1;1H\x1b[P", "b", 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(3, tc.width)
			writeString(t, s, tc.setup)
			writeString(t, s, tc.edit)
			if got := cellsString(s, tc.row); got != tc.want {
				t.Errorf("row %d = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// A one-cell wide glyph DECFRA wrote has no half to lose, so an edit moving
// it moves it whole, as xterm's ScrnFillRectangle leaves it.
func TestEditsMoveADECFRAWideGlyphWhole(t *testing.T) {
	const filled = "abcdefgh\x1b[20013;1;8;1;8$x" // the glyph in the last column
	for _, tc := range []struct{ name, edit, want string }{
		{"DCH shifting it out of the last column", "\x1b[1;1H\x1b[P", "bcdefg中"},
		{"DECCRA copying it from a source's right edge", "\x1b[1;7;1;8;1;1;1$v", "g中cdefg中"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(3, 8)
			writeString(t, s, filled+tc.edit)
			if got := cellsString(s, 0); got != tc.want {
				t.Errorf("row 0 = %q, want %q", got, tc.want)
			}
		})
	}
}

// A wide character an alternate-screen narrowing cuts at the new last column
// is a cut-off character like one written there, so DCH blanks it.
func TestDCHBlanksAWideCharacterAnAlternateScreenNarrowingCut(t *testing.T) {
	s := New(3, 8)
	writeString(t, s, "\x1b[?1049habcdef中")
	s.Resize(3, 7)
	writeString(t, s, "\x1b[1;1H\x1b[P")
	if got, want := cellsString(s, 0), "bcdef"; got != want {
		t.Errorf("row 0 = %q, want %q", got, want)
	}
}

// An alternate-screen widening gives a cut-off wide character its right half
// back, so the row reads and edits like the same bytes written at the new
// width.
func TestAltScreenWideningGivesACutOffWideCharacterItsRightHalfBack(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		widths     []int
	}{
		{"cut by a narrowing", "\x1b[?1049habcdef中", []int{8, 7, 12}},
		{"written in the last column with autowrap off", "\x1b[?1049h\x1b[?7labcdef中", []int{7, 12}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(3, tc.widths[0])
			writeString(t, s, tc.text)
			for _, w := range tc.widths[1:] {
				s.Resize(3, w)
			}
			want := New(3, 12)
			writeString(t, want, tc.text)
			for _, edit := range []struct{ name, seq string }{{"the widening", ""}, {"ICH 5", "\x1b[1;1H\x1b[5@"}} {
				writeString(t, s, edit.seq)
				writeString(t, want, edit.seq)
				if got, wantRow := cellsString(s, 0), cellsString(want, 0); got != wantRow {
					t.Errorf("after %s, row 0 = %q, want %q", edit.name, got, wantRow)
				}
				if got, wantCut := cutColumns(s.Cells[0]), cutColumns(want.Cells[0]); !slices.Equal(got, wantCut) {
					t.Errorf("after %s, cut-off columns = %v, want %v", edit.name, got, wantCut)
				}
			}
		})
	}
}

// cutColumns lists the columns of row marked as a wide character cut off from
// its right half.
func cutColumns(row []Cell) []int {
	var cols []int
	for x, c := range row {
		if c.wideCut {
			cols = append(cols, x)
		}
	}
	return cols
}
