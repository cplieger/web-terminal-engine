package vt

import (
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

func TestResizeKeepsTheCursorRowWhenTheRowsBelowOverflow(t *testing.T) {
	s := New(5, 10)
	writeString(t, s, "TOP!!!!!!!\r\n"+strings.Repeat("b", 40)+"\x1b[1;3H")

	s.Resize(5, 5)

	if y, x := s.CursorPos(); y != 0 || x != 2 {
		t.Fatalf("cursor = (%d, %d), want (0, 2): still on the 'P' of its line", y, x)
	}
	if got := s.RowString(0); got != "TOP!!" {
		t.Errorf("cursor row = %q, want %q", got, "TOP!!")
	}
	if got := drainedText(s); len(got) != 0 {
		t.Errorf("drained = %q, want none: the cursor's line is the top one", got)
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
