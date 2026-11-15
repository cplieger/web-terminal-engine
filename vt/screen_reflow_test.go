package vt

import (
	"slices"
	"strings"
	"testing"
)

// The expected layouts follow the reflow every current terminal applies to
// the main screen on a width change (xterm.js, kitty, VTE, Windows Terminal):
// rows joined by an autowrap are one line, re-wrapped at the new width, while
// a line the program ended with a newline stays its own line.

// screenRows returns every row's text with trailing blanks trimmed.
func screenRows(s *Screen) []string {
	rows := make([]string, s.Height)
	for y := range rows {
		rows[y] = s.RowString(y)
	}
	return rows
}

func writeString(t *testing.T, s *Screen, text string) {
	t.Helper()
	if _, err := s.Write([]byte(text)); err != nil {
		t.Fatalf("Screen.Write(%q) = %v", text, err)
	}
}

func TestResizeWiderJoinsASoftWrappedLine(t *testing.T) {
	s := New(5, 10)
	writeString(t, s, "abcdefghijKLMNOPQRSTuvw\r\nnext")

	s.Resize(5, 30)

	if got, want := screenRows(s), []string{"abcdefghijKLMNOPQRSTuvw", "next", "", "", ""}; !slices.Equal(got, want) {
		t.Errorf("rows after widening to 30 = %q, want %q", got, want)
	}
	if got := s.wrapped; slices.Contains(got, true) {
		t.Errorf("wrapped = %v after widening, want all false: every line now fits its row", got)
	}
	if y, x := s.CursorPos(); y != 1 || x != 4 {
		t.Errorf("cursor = (%d, %d), want (1, 4): after \"next\" on its own line", y, x)
	}
}

func TestResizeNarrowerRewrapsInsteadOfCutting(t *testing.T) {
	s := New(5, 20)
	writeString(t, s, "abcdefghijklmno\r\nxy")

	s.Resize(5, 6)

	if got, want := screenRows(s), []string{"abcdef", "ghijkl", "mno", "xy", ""}; !slices.Equal(got, want) {
		t.Errorf("rows after narrowing to 6 = %q, want %q", got, want)
	}
	if got, want := s.wrapped, []bool{false, true, true, false, false}; !slices.Equal(got, want) {
		t.Errorf("wrapped = %v, want %v: the re-wrapped rows continue the line above", got, want)
	}
	if y, x := s.CursorPos(); y != 3 || x != 2 {
		t.Errorf("cursor = (%d, %d), want (3, 2): after \"xy\"", y, x)
	}
}

func TestResizeNarrowerSendsOverflowToScrollback(t *testing.T) {
	s := New(2, 8)
	writeString(t, s, "aaaabbbbcccc")

	s.Resize(2, 4)

	if got, want := drainedText(s), []string{"aaaa", "bbbb"}; !slices.Equal(got, want) {
		t.Errorf("drained = %q, want %q: the rows the re-wrap pushed off the top are history", got, want)
	}
	// The line fills its last row with no wrap pending, so the cursor after it
	// takes a continuation row of its own: a pending wrap could land the next
	// character on another line's row.
	if got, want := screenRows(s), []string{"cccc", ""}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
	if got, want := s.wrapped, []bool{true, true}; !slices.Equal(got, want) {
		t.Errorf("wrapped = %v, want %v: both rows continue the line now in history", got, want)
	}
	if y, x := s.CursorPos(); y != 1 || x != 0 || s.pendingWrap {
		t.Errorf("cursor = (%d, %d) pending %v, want (1, 0) with no wrap pending", y, x, s.pendingWrap)
	}
	writeString(t, s, "d")
	if got, want := screenRows(s), []string{"cccc", "d"}; !slices.Equal(got, want) {
		t.Errorf("rows after writing one more character = %q, want %q", got, want)
	}
}

func TestResizeNarrowerGivesUpBlankRowsBeforeHistory(t *testing.T) {
	s := New(3, 10)
	writeString(t, s, "abcdefghijk")

	s.Resize(3, 4)

	if got := drainedText(s); len(got) != 0 {
		t.Errorf("drained = %q, want none: the blank row below the cursor makes room first", got)
	}
	if got, want := screenRows(s), []string{"abcd", "efgh", "ijk"}; !slices.Equal(got, want) {
		t.Errorf("rows = %q, want %q", got, want)
	}
}

func TestResizeWiderMovesAPendingWrapBackOntoTheLine(t *testing.T) {
	s := New(2, 5)
	writeString(t, s, "abcde")

	s.Resize(2, 10)
	writeString(t, s, "f")

	if got, want := screenRows(s), []string{"abcdef", ""}; !slices.Equal(got, want) {
		t.Errorf("rows after widening and writing one character = %q, want %q", got, want)
	}
}

func TestResizeCarriesTheCursorThroughItsLine(t *testing.T) {
	s := New(3, 10)
	writeString(t, s, "abcdefghijklm\x1b[2;2H") // the cursor on 'l', offset 11 in the line

	s.Resize(3, 5)

	if y, x := s.CursorPos(); y != 2 || x != 1 {
		t.Errorf("cursor = (%d, %d) at width 5, want (2, 1): still on 'l'", y, x)
	}
	s.Resize(3, 20)
	if y, x := s.CursorPos(); y != 0 || x != 11 {
		t.Errorf("cursor = (%d, %d) at width 20, want (0, 11): still on 'l'", y, x)
	}
}

func TestResizeKeepsAWideCharacterWhole(t *testing.T) {
	s := New(3, 6)
	writeString(t, s, "abcd中")

	s.Resize(3, 5)

	if got, want := screenRows(s), []string{"abcd", "中", ""}; !slices.Equal(got, want) {
		t.Errorf("rows at width 5 = %q, want %q: the wide character moves whole to the next row", got, want)
	}
	if s.Cells[1][1].Ch != 0 {
		t.Errorf("Cells[1][1].Ch = %q, want the wide character's spacer", s.Cells[1][1].Ch)
	}
	if !s.wrapped[1] {
		t.Error("wrapped[1] = false, want true")
	}

	s.Resize(3, 6)

	if got, want := screenRows(s), []string{"abcd中", "", ""}; !slices.Equal(got, want) {
		t.Errorf("rows back at width 6 = %q, want %q: the padding cell before the wide character is not content", got, want)
	}
}

func TestResizeReflowsTheMainScreenUnderTheAlternateScreen(t *testing.T) {
	s := New(4, 10)
	writeString(t, s, "abcdefghijklm")
	writeString(t, s, "\x1b[?1049h")
	writeString(t, s, "ALTALTALTALT")

	s.Resize(4, 20)

	// A full-screen program repaints its own rows on SIGWINCH; the alternate
	// screen is left as it was drawn.
	if got, want := screenRows(s), []string{"ALTALTALTA", "LT", "", ""}; !slices.Equal(got, want) {
		t.Errorf("alternate screen rows = %q, want %q", got, want)
	}
	writeString(t, s, "\x1b[?1049l")
	if got, want := screenRows(s), []string{"abcdefghijklm", "", "", ""}; !slices.Equal(got, want) {
		t.Errorf("main screen rows after leaving the alternate screen = %q, want %q", got, want)
	}
	if y, x := s.CursorPos(); y != 0 || x != 13 {
		t.Errorf("cursor = (%d, %d), want (0, 13): after the main screen's line", y, x)
	}
}

func TestResizeKeepsCellAttributesThroughAReflow(t *testing.T) {
	s := New(3, 4)
	writeString(t, s, "ab\x1b[1mcd\x1b]8;;https://example.com\x1b\\ef\x1b]8;;\x1b\\\x1b[0m")

	s.Resize(3, 10)

	row := s.Cells[0]
	if got := strings.TrimRight(s.RowString(0), " "); got != "abcdef" {
		t.Fatalf("row 0 = %q, want %q", got, "abcdef")
	}
	if row[1].Style.Bold || !row[2].Style.Bold || !row[5].Style.Bold {
		t.Errorf("bold = %v %v %v at columns 1, 2, 5, want false true true", row[1].Style.Bold, row[2].Style.Bold, row[5].Style.Bold)
	}
	if row[4].Hyperlink != "https://example.com" || row[3].Hyperlink != "" {
		t.Errorf("hyperlinks = %q at column 3, %q at column 4, want none then the link", row[3].Hyperlink, row[4].Hyperlink)
	}
}
