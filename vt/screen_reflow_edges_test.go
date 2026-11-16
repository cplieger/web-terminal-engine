package vt

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// historyLines returns the logical lines of the drained scrollback followed by
// the main screen: rows joined by their soft-wrap flags, trailing blanks
// dropped, trailing empty lines dropped.
func historyLines(s *Screen) []string {
	var lines []string
	var line strings.Builder
	started := false
	add := func(text string, continues bool) {
		if started && !continues {
			lines = append(lines, strings.TrimRight(line.String(), " "))
			line.Reset()
		}
		started = true
		line.WriteString(text)
	}
	drainedWrapped := s.DrainedWrapped()
	for i, runs := range s.Drained {
		add(wireRowText(runs), drainedWrapped[i])
	}
	for y, row := range s.Cells {
		var b strings.Builder
		for _, c := range row {
			if c.Ch != 0 {
				b.WriteRune(c.Ch)
			}
		}
		add(b.String(), s.wrapped[y])
	}
	lines = append(lines, strings.TrimRight(line.String(), " "))
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// An erase that blanks the end of a row, or a row from its start, ends the
// soft wrap through it (xterm clears a line's wrap flag on EL and ED), so a
// later width change keeps the text written after the erase on its own line.
func TestResizeKeepsLinesAnEraseSeparatedApart(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"erase below from the top of a wrapped line", strings.Repeat("x", 30) + "\x1b[H\x1b[Jshort\r\nnext", []string{"short", "next"}},
		{"line edit that erases both rows of a wrapped line", "$ " + strings.Repeat("y", 25) + "\x1b[2K\x1b[A\r\x1b[K$ ls\r\nfile1 file2", []string{"$ ls", "file1 file2"}},
		{"erase above through the last column", strings.Repeat("z", 30) + "\x1b[1;20H\x1b[1J", []string{"", "zzzzzzzzzz"}},
		{"erase to the start of a continuation row", strings.Repeat("w", 30) + "\x1b[2;1H\x1b[1K", []string{strings.Repeat("w", 20), " " + strings.Repeat("w", 9)}},
		{"erase characters through the last column", strings.Repeat("v", 30) + "\x1b[1;19H\x1b[5X", []string{strings.Repeat("v", 18), strings.Repeat("v", 10)}},
		{"screen alignment test over a wrapped line", strings.Repeat("u", 30) + "\x1b#8", slices.Repeat([]string{strings.Repeat("E", 20)}, 4)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(4, 20)
			writeString(t, s, tt.input)

			s.Resize(4, 40)

			if got := historyLines(s); !slices.Equal(got, tt.want) {
				t.Errorf("lines after widening to 40 = %q, want %q", got, tt.want)
			}
		})
	}
}

// An erase that cuts a wide character blanks its other half too, so the
// soft wrap it ends is the one an erase over both halves ends.
func TestResizeTreatsAnEraseThatCutsAWideCharacterAsOneOverBothHalves(t *testing.T) {
	const endsWide = "abcdef中xyz"     // 中 in row 0's last two columns
	const startsWide = "abcdefgh中xyz" // 中 in row 1's first two columns
	tests := []struct {
		name, setup, cut, whole string
	}{
		{"ECH ending on a left half in the last two columns", endsWide, "\x1b[1;6H\x1b[2X", "\x1b[1;6H\x1b[3X"},
		{"EL 1 ending on a left half in the last two columns", endsWide, "\x1b[1;7H\x1b[1K", "\x1b[1;8H\x1b[1K"},
		{"ED 1 ending on a left half in the last two columns", endsWide, "\x1b[1;7H\x1b[1J", "\x1b[1;8H\x1b[1J"},
		{"ECH starting on a right half in column 1", startsWide, "\x1b[2;2H\x1b[X", "\x1b[2;1H\x1b[2X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cut, whole := New(6, 8), New(6, 8)
			writeString(t, cut, tt.setup+tt.cut)
			writeString(t, whole, tt.setup+tt.whole)

			cut.Resize(6, 20)
			whole.Resize(6, 20)

			if !slices.Equal(cut.wrapped, whole.wrapped) {
				t.Errorf("wrapped after widening = %v, want %v", cut.wrapped, whole.wrapped)
			}
			if got, want := historyLines(cut), historyLines(whole); !slices.Equal(got, want) {
				t.Errorf("lines after widening = %q, want %q", got, want)
			}
		})
	}
}

func TestResizeCarriesSpacesARectangleFillDrew(t *testing.T) {
	s := New(4, 10)
	writeString(t, s, "abc\x1b[32;1;4;1;6$x\x1b[3;1H")

	s.Resize(4, 4)

	if !s.wrapped[1] {
		t.Error("wrapped[1] = false, want true: the filled spaces are written text that continues onto row 1")
	}
	if y, x := s.CursorPos(); y != 3 || x != 0 {
		t.Errorf("cursor = (%d, %d), want (3, 0): two rows for the six-cell line, one for the empty line", y, x)
	}
}

func TestResizeShorterAndNarrowerKeepsEveryLine(t *testing.T) {
	s := New(24, 80)
	var want []string
	for _, n := range []string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10", "11", "12"} {
		line := "line " + n + " " + strings.Repeat("z", 60)
		want = append(want, line)
		writeString(t, s, line+"\r\n")
	}

	s.Resize(8, 20)

	if got := historyLines(s); !slices.Equal(got, want) {
		t.Errorf("scrollback and screen after resizing 24x80 to 8x20 hold %q, want every line: %q", got, want)
	}
	if y, x := s.CursorPos(); y != 7 || x != 0 {
		t.Errorf("cursor = (%d, %d), want (7, 0): on the empty line after the last one", y, x)
	}
}

func TestResizeShorterDrainsTheTopAndKeepsTheCursorRow(t *testing.T) {
	t.Run("rows above the cursor go to scrollback", func(t *testing.T) {
		s := New(6, 20)
		writeString(t, s, "a\r\nb\r\nc\r\nd\r\ne\r\n$ prompt")

		s.Resize(3, 20)

		if got, want := drainedText(s), []string{"a", "b", "c"}; !slices.Equal(got, want) {
			t.Errorf("drained = %q, want %q", got, want)
		}
		if got, want := screenRows(s), []string{"d", "e", "$ prompt"}; !slices.Equal(got, want) {
			t.Errorf("rows = %q, want %q", got, want)
		}
		if y, x := s.CursorPos(); y != 2 || x != 8 {
			t.Errorf("cursor = (%d, %d), want (2, 8): after the prompt", y, x)
		}
	})
	t.Run("blank rows below the cursor go first", func(t *testing.T) {
		s := New(6, 20)
		writeString(t, s, "a\r\nb\r\n$")

		s.Resize(3, 20)

		if got := drainedText(s); len(got) != 0 {
			t.Errorf("drained = %q, want none", got)
		}
		if got, want := screenRows(s), []string{"a", "b", "$"}; !slices.Equal(got, want) {
			t.Errorf("rows = %q, want %q", got, want)
		}
	})
	t.Run("the alternate screen keeps its top rows", func(t *testing.T) {
		s := New(10, 10)
		writeString(t, s, "\x1b[?1049hA\x1b[10;1HB")

		s.Resize(5, 10)

		if got := drainedText(s); len(got) != 0 {
			t.Errorf("drained = %q, want none: the alternate screen has no scrollback", got)
		}
		if got := s.RowString(0); got != "A" {
			t.Errorf("row 0 = %q, want %q", got, "A")
		}
		if y, _ := s.CursorPos(); y != 4 {
			t.Errorf("cursor row = %d, want 4 (clamped)", y)
		}
	})
}

// Rows below the cursor that overflow the screen push the rows above them,
// the cursor's row too, into scrollback, as kitty does: no text is lost, and
// the cursor stays on the top row.
func TestResizeSendsTheCursorsLineToScrollbackWhenTheRowsBelowOverflow(t *testing.T) {
	tests := []struct {
		name      string
		rows      int
		cols      int
		input     string
		toRows    int
		toCols    int
		wantLines []string
		wantRows  []string
		wantX     int
	}{
		{
			"narrower", 5, 10, "TOP!!!!!!!\r\n" + strings.Repeat("b", 40) + "\x1b[1;4H", 5, 5,
			[]string{"TOP!!!!!!!", strings.Repeat("b", 40)},
			slices.Repeat([]string{"bbbbb"}, 5), 3,
		},
		{
			"shorter", 6, 20, "top\r\nl1\r\nl2\r\nl3\r\nl4\r\nl5\x1b[1;2H", 3, 20,
			[]string{"top", "l1", "l2", "l3", "l4", "l5"},
			[]string{"l3", "l4", "l5"},
			1,
		},
		{
			"narrower with a wrap pending", 5, 10, "\x1b[2;1H" + strings.Repeat("b", 40) + "\x1b[HTOP!!!!!!!", 5, 5,
			[]string{"TOP!!!!!!!", strings.Repeat("b", 40)},
			slices.Repeat([]string{"bbbbb"}, 5), 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(tt.rows, tt.cols)
			writeString(t, s, tt.input)

			s.Resize(tt.toRows, tt.toCols)

			if got := historyLines(s); !slices.Equal(got, tt.wantLines) {
				t.Errorf("scrollback and screen = %q, want every line: %q", got, tt.wantLines)
			}
			if got := screenRows(s); !slices.Equal(got, tt.wantRows) {
				t.Errorf("rows = %q, want %q", got, tt.wantRows)
			}
			if y, x := s.CursorPos(); y != 0 || x != tt.wantX || s.pendingWrap {
				t.Errorf("cursor = (%d, %d) pending %v, want (0, %d) with no wrap pending", y, x, s.pendingWrap, tt.wantX)
			}
		})
	}
}

func TestResizeUnderTheAlternateScreenSendsTheSavedOverflowToScrollback(t *testing.T) {
	t.Run("narrowing", func(t *testing.T) {
		s := New(4, 20)
		lines := []string{"L1 " + strings.Repeat("a", 16), "L2 " + strings.Repeat("b", 16), "L3 " + strings.Repeat("c", 16), "L4"}
		writeString(t, s, strings.Join(lines, "\r\n"))
		writeString(t, s, "\x1b[?1049h")

		s.Resize(4, 10)
		writeString(t, s, "\x1b[?1049l")

		if got := historyLines(s); !slices.Equal(got, lines) {
			t.Errorf("scrollback and screen after the alternate screen = %q, want %q", got, lines)
		}
		if got, want := screenRows(s), []string{"bbbbbbbbb", "L3 ccccccc", "ccccccccc", "L4"}; !slices.Equal(got, want) {
			t.Errorf("rows = %q, want %q", got, want)
		}
		if y, x := s.CursorPos(); y != 3 || x != 2 {
			t.Errorf("cursor = (%d, %d), want (3, 2): after L4", y, x)
		}
	})
	t.Run("the top row continues the line in history", func(t *testing.T) {
		s := New(3, 8)
		writeString(t, s, "OLD-LINE\r\nOLD-LINE\r\nOLD-LINE\r\nabcdefghabcdefgh")
		s.DrainScrollback()
		writeString(t, s, "\x1b[?1049h")

		s.Resize(3, 4)
		writeString(t, s, "\x1b[?1049l")

		if got, want := historyLines(s), []string{"OLD-LINE", "abcdefghabcdefgh"}; !slices.Equal(got, want) {
			t.Errorf("scrollback and screen = %q, want %q", got, want)
		}
	})
}

func TestResizeTreatsAPaintedBlankAsPadding(t *testing.T) {
	const blue = "\x1b[44m\x1b[2Kab\x1b[0m"
	tests := []struct {
		name  string
		input string
		want  []string
		row   int // the painted line's row after the resize
	}{
		{"nothing else wrapped", blue + "\r\nnext", []string{"ab", "next", "", ""}, 0},
		{"another line re-wrapped", strings.Repeat("0123456789", 2) + "01\r\n" + blue + "\r\nnext", []string{"0123456789", "01", "ab", "next"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(4, 20)
			writeString(t, s, tt.input)

			s.Resize(4, 10)

			if got := screenRows(s); !slices.Equal(got, tt.want) {
				t.Errorf("rows at width 10 = %q, want %q: a background-painted blank is not text", got, tt.want)
			}
			if bg := s.Cells[tt.row][9].Style.BG; bg != (Color{Type: 1, Val: 4}) {
				t.Errorf("last cell of the painted row has background %v, want palette blue: the paint fills the row", bg)
			}
		})
	}
}

func TestResizeKeepsASpaceWrittenBeforeAWrappedWideCharacter(t *testing.T) {
	s := New(4, 4)
	writeString(t, s, "abc 中")

	s.Resize(4, 10)

	if got := s.RowString(0); got != "abc 中" {
		t.Errorf("row 0 = %q, want %q: the program wrote that space", got, "abc 中")
	}
}

func TestResizeDropsThePadAWrappedWideCharacterLeft(t *testing.T) {
	s := New(3, 5)
	writeString(t, s, "abcd中")

	s.Resize(3, 6)

	if got := s.RowString(0); got != "abcd中" {
		t.Errorf("row 0 = %q, want %q: the blank autowrap left in the last column is not text", got, "abcd中")
	}
}

// A wide character that does not fit before a DECSLRM right margin wraps
// inside the box, so its blank is a cell of a row that is not soft-wrapped:
// a re-wrap keeps it and the text right of the box keeps its column.
func TestResizeKeepsTheBlankAWideCharacterLeftAtARightMargin(t *testing.T) {
	for name, cols := range map[string]int{"widen": 20, "narrow": 9} {
		t.Run(name, func(t *testing.T) {
			s := New(3, 10)
			writeString(t, s, "abcdefghij\x1b[?69h\x1b[1;5s\x1b[1;1HABCD中\x1b[3;1H")

			s.Resize(3, cols)

			if got, want := historyLines(s), []string{"ABCD fghij", "中"}; !slices.Equal(got, want) {
				t.Errorf("lines after resizing to width %d = %q, want %q", cols, got, want)
			}
		})
	}
}

// The next character goes where the cursor is, so a pad the cursor sits on
// stays in the line: writing on it after a widen keeps the wide character.
func TestResizeKeepsThePadTheCursorIsOn(t *testing.T) {
	s := New(3, 5)
	writeString(t, s, "abcd中\x1b[1;5H")

	s.Resize(3, 10)
	writeString(t, s, "Z")

	if got, want := historyLines(s), []string{"abcdZ中"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q: Z goes on the pad, as it does without a resize", got, want)
	}
}

func TestResizeDropsAKeptPadOnceTheCursorLeavesIt(t *testing.T) {
	s := New(3, 5)
	writeString(t, s, "abcd中\x1b[1;5H")

	s.Resize(3, 10)
	writeString(t, s, "\r")
	s.Resize(3, 20)

	if got, want := historyLines(s), []string{"abcd中"}; !slices.Equal(got, want) {
		t.Errorf("lines = %q, want %q: the pad is layout, not text", got, want)
	}
}

// A row inserted, deleted or scrolled away ends the soft wrap through it: the
// row that lands below a new neighbour is not that neighbour's continuation, so
// a widen keeps the two apart.
func TestResizeKeepsLinesALineShiftSeparated(t *testing.T) {
	wrappedAB := strings.Repeat("a", 10) + strings.Repeat("b", 10)
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"delete the head of a wrapped line", "HELLO\r\n" + wrappedAB + "\x1b[2;1H\x1b[M", []string{"HELLO", "bbbbbbbbbb"}},
		{"insert in the middle of a wrapped line", wrappedAB + "\x1b[2;1H\x1b[L", []string{"aaaaaaaaaa", "", "bbbbbbbbbb"}},
		{"delete in a region whose bottom splits a wrapped line", "top\r\n" + wrappedAB + "\x1b[1;2r\x1b[1;1H\x1b[M\x1b[r", []string{"aaaaaaaaaa", "", "bbbbbbbbbb"}},
		{"scroll up a region whose bottom splits a wrapped line", "top\r\n" + wrappedAB + "\x1b[1;2r\x1b[S\x1b[r", []string{"aaaaaaaaaa", "", "bbbbbbbbbb"}},
		{"line feed at the bottom of a region that splits a wrapped line", "top\r\n" + wrappedAB + "\x1b[1;2r\x1b[2;1H\n\x1b[r", []string{"aaaaaaaaaa", "", "bbbbbbbbbb"}},
		{"scroll down a region whose bottom splits a wrapped line", "top\r\n" + wrappedAB + "\x1b[1;2r\x1b[T\x1b[r", []string{"", "top", "bbbbbbbbbb"}},
		{"insert in a region whose bottom splits a wrapped line", "top\r\n" + wrappedAB + "\x1b[1;2r\x1b[1;1H\x1b[L\x1b[r", []string{"", "top", "bbbbbbbbbb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(5, 10)
			writeString(t, s, tt.input)

			s.Resize(5, 40)

			if got := historyLines(s); !slices.Equal(got, tt.want) {
				t.Errorf("lines after widening to 40 = %q, want %q", got, tt.want)
			}
		})
	}
}

// SD keeps a pending wrap, as xterm's RevScroll does, but the row it blanks
// holds no text for the next character to continue.
func TestResizeKeepsTextAfterAScrollBlankedTheRowItWrapsFrom(t *testing.T) {
	s := New(8, 10)
	writeString(t, s, strings.Repeat("e", 10)+"\x1b[3Te")

	s.Resize(8, 40)

	if got, want := screenRows(s)[:4], []string{"", "e", "", strings.Repeat("e", 10)}; !slices.Equal(got, want) {
		t.Errorf("rows after widening to 40 = %q, want %q", got, want)
	}
}

// A full-screen scroll sends the top row to scrollback, so the row that
// becomes the top still continues it.
func TestResizeKeepsTheLinkAFullScreenScrollSendsToHistory(t *testing.T) {
	s := New(2, 10)
	writeString(t, s, "top\r\n"+strings.Repeat("a", 10)+strings.Repeat("b", 10)+"\r\nnext")

	s.Resize(2, 40)

	if got, want := historyLines(s), []string{"top", strings.Repeat("a", 10) + strings.Repeat("b", 10), "next"}; !slices.Equal(got, want) {
		t.Errorf("lines after widening to 40 = %q, want %q", got, want)
	}
}

func TestAlternateScreenKeepsTheMainScreensPendingWrap(t *testing.T) {
	t.Run("without a resize", func(t *testing.T) {
		s := New(4, 6)
		writeString(t, s, "abcdef\x1b[?1049hALT")
		if got := s.RowString(0); got != "ALT" {
			t.Errorf("alternate row 0 = %q, want %q: 1049 homes the cursor with no wrap pending", got, "ALT")
		}
		writeString(t, s, "\x1b[?1049lX")

		if got, want := screenRows(s), []string{"abcdef", "X", "", ""}; !slices.Equal(got, want) {
			t.Errorf("rows = %q, want %q: the next character wraps as it would have", got, want)
		}
	})
	t.Run("across a widening", func(t *testing.T) {
		s := New(4, 6)
		writeString(t, s, "abcdef\x1b[?1049hALT")
		s.Resize(4, 12)
		writeString(t, s, "\x1b[?1049lX")

		if got := s.RowString(0); got != "abcdefX" {
			t.Errorf("row 0 = %q, want %q", got, "abcdefX")
		}
	})
}

func TestResizeKeepsACursorPastTheContentOnItsRow(t *testing.T) {
	s := New(5, 80)
	writeString(t, s, "ab\r\nnext\x1b[1;71H")

	s.Resize(5, 20)

	if got, want := screenRows(s), []string{"ab", "next", "", "", ""}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q: blank space the cursor sat in is not a line", got, want)
	}
	if y, x := s.CursorPos(); y != 0 || x != 19 {
		t.Errorf("cursor = (%d, %d), want (0, 19): its row's last column", y, x)
	}
}

// A wide character a row too narrow for it left without its right half takes
// two columns again once the row is wide enough, as writing it there does.
func TestResizeGivesAWideCharacterItsRightHalfBack(t *testing.T) {
	t.Run("from a one-column screen", func(t *testing.T) {
		s := New(3, 1)
		writeString(t, s, "中")
		s.Resize(3, 16)

		written := New(3, 16)
		writeString(t, written, "中")
		if got, want := rowText(s.Cells[0]), rowText(written.Cells[0]); got != want {
			t.Errorf("row 0 = %q, want %q", got, want)
		}
		if y, x := s.CursorPos(); y != 0 || x != 2 {
			t.Errorf("cursor = (%d, %d), want (0, 2)", y, x)
		}
	})
	t.Run("from the last column with autowrap off", func(t *testing.T) {
		s := New(3, 3)
		writeString(t, s, "\x1b[?7lab中")
		s.Resize(3, 4)

		if got, want := rowText(s.Cells[0]), "ab中|"; got != want {
			t.Errorf("row 0 = %q, want %q", got, want)
		}
	})
	t.Run("from a re-wrap onto one column", func(t *testing.T) {
		s := New(3, 4)
		writeString(t, s, "中")
		s.Resize(3, 1)
		s.Resize(3, 16)

		if got, want := rowText(s.Cells[0]), "中|"; got != want {
			t.Errorf("row 0 = %q, want %q", got, want)
		}
	})
	t.Run("from a DECCRA of a left half into the last column", func(t *testing.T) {
		s := New(3, 4)
		writeString(t, s, "a中b\x1b[1;2;1;2;1;1;4$v")
		s.Resize(3, 8)

		if got, want := rowText(s.Cells[0]), "a中|中|"; got != want {
			t.Errorf("row 0 = %q, want %q", got, want)
		}
	})
}

// DECFRA fills every column of its box with one cell of a wide code point,
// as xterm's ScrnFillRectangle writes it, so a re-wrap keeps the glyph one
// cell wherever it sits, as filling at the new width does.
func TestResizeKeepsADECFRAWideGlyphOneCell(t *testing.T) {
	fill := func(cols, from, to int) string {
		return fmt.Sprintf("\x1b[20013;%d;%d;%d;%d$x", from/cols+1, from%cols+1, to/cols+1, to%cols+1)
	}
	for _, tc := range []struct {
		name     string
		from, to int
	}{
		{"mid-row", 0, 2},
		{"in the last column", 7, 7},
	} {
		for _, cols := range []int{16, 4} {
			t.Run(fmt.Sprintf("%s resized to %d", tc.name, cols), func(t *testing.T) {
				s := New(4, 8)
				writeString(t, s, "abcdefgh"+fill(8, tc.from, tc.to))
				s.Resize(4, cols)

				written := New(4, cols)
				writeString(t, written, "abcdefgh"+fill(cols, tc.from, tc.to))
				for y := range 3 {
					if got, want := rowText(s.Cells[y]), rowText(written.Cells[y]); got != want || s.wrapped[y] != written.wrapped[y] {
						t.Errorf("row %d = %q wrapped %v, want %q wrapped %v", y, got, s.wrapped[y], want, written.wrapped[y])
					}
				}
			})
		}
	}
}
