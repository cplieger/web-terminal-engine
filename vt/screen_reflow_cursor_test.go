package vt

import (
	"slices"
	"strings"
	"testing"
)

// A cursor after its line's text keeps the wrap that was pending, gains none
// that was not, and leaves the next character on its own line without
// overwriting the text.
func TestResizePlacesTheCursorAfterItsLineWhereTheNextCharacterBelongs(t *testing.T) {
	tests := []struct {
		name        string
		rows, cols  int
		input       string
		widths      []int
		wantRows    []string
		wantLines   []string
		wantPending bool
	}{
		{
			name: "a pending wrap after written spaces stays pending", rows: 4, cols: 8,
			input: "ab  cd  ", widths: []int{4}, wantPending: true,
			wantRows: []string{"ab", "cd", "Z", ""}, wantLines: []string{"ab  cd  Z"},
		},
		{
			name: "written spaces survive a round trip through a width they do not fill", rows: 4, cols: 8,
			input: "ab  cd  ", widths: []int{5, 8}, wantPending: true,
			wantRows: []string{"ab  cd", "Z", "", ""}, wantLines: []string{"ab  cd  Z"},
		},
		{
			name: "past a line that fills its last row, no wrap is invented", rows: 4, cols: 20,
			input: "? Pick one:\r\n> first\x1b[1;14H", widths: []int{11},
			wantRows: []string{"? Pick one:", "  Z", "> first", ""}, wantLines: []string{"? Pick one:  Z", "> first"},
		},
		{
			name: "just after a line that fills its last row, no wrap is invented", rows: 4, cols: 20,
			input: "? Pick one:\r\n> first\x1b[1;12H", widths: []int{11},
			wantRows: []string{"? Pick one:", "Z", "> first", ""}, wantLines: []string{"? Pick one:Z", "> first"},
		},
		{
			name: "after a typed space the next character follows it", rows: 4, cols: 10,
			input: "$ echo ", widths: []int{6},
			wantRows: []string{"$ echo", " Z", "", ""}, wantLines: []string{"$ echo Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(tt.rows, tt.cols)
			writeString(t, s, tt.input)
			for _, w := range tt.widths {
				s.Resize(tt.rows, w)
			}
			if s.pendingWrap != tt.wantPending {
				t.Errorf("pending wrap after the resize = %v, want %v", s.pendingWrap, tt.wantPending)
			}

			writeString(t, s, "Z")

			if got := screenRows(s); !slices.Equal(got, tt.wantRows) {
				t.Errorf("rows after writing Z = %q, want %q", got, tt.wantRows)
			}
			if got := historyLines(s); !slices.Equal(got, tt.wantLines) {
				t.Errorf("lines after writing Z = %q, want %q", got, tt.wantLines)
			}
		})
	}
}

// With no text below, a line that fills its last row at the new width leaves
// the wrap pending, as writing it at that width does, so a shell's next
// output and its carriage-return redraw land where they would have.
func TestResizePendsTheWrapAfterALineThatFillsTheLastRowWithNothingBelow(t *testing.T) {
	tests := []struct {
		name, before, after string
		wantRows            []string
		wantDrained         []string
	}{
		{
			name: "the next output follows the line", before: "out1\r\nout2\r\n$ ls -la", after: "\r\nfile",
			wantRows: []string{"out2", "$ ls -la", "file"}, wantDrained: []string{"out1"},
		},
		{
			name: "a carriage-return redraw replaces the line", before: "$ abcdef", after: "\r\x1b[K$ abcdef",
			wantRows: []string{"$ abcdef", "", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(3, 20)
			writeString(t, s, tt.before)

			s.Resize(3, 8)
			if y, x := s.CursorPos(); x != 7 || !s.pendingWrap {
				t.Errorf("cursor = (%d, %d) pending %v, want column 7 with the wrap pending", y, x, s.pendingWrap)
			}
			writeString(t, s, tt.after)

			if got := screenRows(s); !slices.Equal(got, tt.wantRows) {
				t.Errorf("rows = %q, want %q", got, tt.wantRows)
			}
			if got := drainedText(s); !slices.Equal(got, tt.wantDrained) {
				t.Errorf("drained = %q, want %q", got, tt.wantDrained)
			}
		})
	}
}

func TestResizeGivesTheWideCharacterPadItsPaint(t *testing.T) {
	s := New(3, 4)
	writeString(t, s, "\x1b[41mab中")

	s.Resize(3, 3)

	if got := s.Cells[0][2]; !got.widePad || got.Style.BG != (Color{Type: 1, Val: 1}) {
		t.Errorf("cell (0, 2) = %+v, want the wide character's pad with its red background", got)
	}
}

// The scrolled-off head of a soft-wrapped link belongs to the main screen, so
// an erase on the alternate screen leaves it to finish the link.
func TestAlternateScreenEraseKeepsTheMainScreensLinkHistory(t *testing.T) {
	const url = "https://ex.com/aaaaabbbbbcccccdd"
	for name, erase := range map[string]string{
		"erase below from home": "\x1b[H\x1b[J",
		"erase the top row":     "\x1b[H\x1b[2K",
		"erase the screen":      "\x1b[2J",
	} {
		t.Run(name, func(t *testing.T) {
			s := New(2, 20)
			writeString(t, s, url+"\r\n")
			writeString(t, s, "\x1b[?1049h"+erase+"\x1b[?1049l")

			if got := rowLink(s, 0); got != url {
				t.Errorf("link on row 0 = %q, want %q: its head is in scrollback", got, url)
			}
		})
	}
}

func TestMainScreenEraseOfTheTopRowEndsItsLinkHistory(t *testing.T) {
	s := New(2, 20)
	writeString(t, s, "https://ex.com/aaaaabbbbbcccccdd\r\n\x1b[H\x1b[K")

	if s.drainTail != nil {
		t.Errorf("drainTail = %q, want nil: the row that continued it is erased", s.drainTail)
	}
	if got := rowLink(s, 0); got != "" {
		t.Errorf("link on row 0 = %q, want none", got)
	}
}

func rowLink(s *Screen, y int) string {
	for _, r := range s.RenderRowWire(y) {
		if r.U != "" {
			return r.U
		}
	}
	return ""
}

// An erase that reaches the cursor ends a pending wrap, as xterm's ResetWrap
// does in ClearRight, ClearInLine and ClearScreen: the next character
// overwrites the last column instead of wrapping onto a row that would
// continue a blank.
func TestEraseFromTheCursorEndsAPendingWrap(t *testing.T) {
	keptRow := []string{"aaaaaaaaaX", "", "", ""}
	erasedRow := []string{"         X", "", "", ""}
	tests := []struct {
		name, erase string
		want        []string
	}{
		{"EL 0", "\x1b[K", keptRow},
		{"ECH", "\x1b[X", keptRow},
		{"ED 0", "\x1b[J", keptRow},
		{"EL 1", "\x1b[1K", erasedRow},
		{"EL 2", "\x1b[2K", erasedRow},
		{"ED 1", "\x1b[1J", erasedRow},
		{"ED 2", "\x1b[2J", erasedRow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(4, 10)
			writeString(t, s, strings.Repeat("a", 10)+tt.erase+"X")

			if got := screenRows(s); !slices.Equal(got, tt.want) {
				t.Errorf("rows = %q, want %q", got, tt.want)
			}
			if s.wrapped[1] {
				t.Error("wrapped[1] = true, want false: nothing wrapped")
			}
		})
	}
}

// 47 and 1047 restore no cursor on exit, so the alternate session's cursor and
// its pending wrap carry over to the main screen.
func TestAlternateScreenExitWithoutRestoreKeepsTheAlternateCursor(t *testing.T) {
	for _, mode := range []string{"47", "1047"} {
		t.Run(mode, func(t *testing.T) {
			s := New(4, 10)
			writeString(t, s, "main\x1b[?"+mode+"h\x1b[4;7Hx\x1b[?"+mode+"l")
			if y, x := s.CursorPos(); y != 3 || x != 7 || s.pendingWrap {
				t.Errorf("cursor = (%d, %d) pending %v, want (3, 7) with no wrap pending", y, x, s.pendingWrap)
			}

			writeString(t, s, "\x1b[?"+mode+"h\x1b[2;1H"+strings.Repeat("y", 10)+"\x1b[?"+mode+"l")
			if y, x := s.CursorPos(); y != 1 || x != 9 || !s.pendingWrap {
				t.Errorf("cursor = (%d, %d) pending %v, want (1, 9) with the wrap pending", y, x, s.pendingWrap)
			}
		})
	}
}
