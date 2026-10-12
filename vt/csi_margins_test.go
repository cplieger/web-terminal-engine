package vt

import (
	"strings"
	"testing"
)

// marginScreen returns a 5x10 screen with DECLRMM on and the left/right
// margins at columns 3..6 (0-based), every row filled with "0123456789".
func marginScreen(t *testing.T) *Screen {
	t.Helper()
	s := New(5, 10)
	for y := range 5 {
		s.Write([]byte("\x1b[" + itoa(y+1) + ";1H0123456789"))
	}
	s.Write([]byte("\x1b[?69h\x1b[4;7s"))
	return s
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func screenText(s *Screen) string {
	rows := make([]string, s.Height)
	for y := range s.Height {
		rows[y] = s.RowString(y)
	}
	return strings.Join(rows, "|")
}

func TestCUFStopsAtRightMarginOnlyFromInsideTheBox(t *testing.T) {
	cases := []struct {
		name    string
		margins bool
		startX  int // 0-based
		seq     string
		wantX   int
	}{
		{"no_margins_moves_n", false, 2, "\x1b[3C", 5},
		{"no_margins_clamps_at_edge", false, 2, "\x1b[100C", 9},
		{"inside_box_stops_at_margin", true, 4, "\x1b[10C", 6},
		{"at_margin_stays_at_margin", true, 6, "\x1b[C", 6},
		{"right_of_box_reaches_edge", true, 7, "\x1b[5C", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(5, 10)
			if tc.margins {
				s.Write([]byte("\x1b[?69h\x1b[4;7s"))
			}
			s.Write([]byte("\x1b[2;" + itoa(tc.startX+1) + "H" + tc.seq))
			if row, col := s.CursorPos(); row != 1 || col != tc.wantX {
				t.Errorf("CUF %q from col %d = (%d,%d), want (1,%d)", tc.seq, tc.startX, row, col, tc.wantX)
			}
		})
	}
}

func TestCUBStopsAtLeftMarginOnlyFromInsideTheBox(t *testing.T) {
	cases := []struct {
		name    string
		margins bool
		startX  int
		seq     string
		wantX   int
	}{
		{"no_margins_moves_n", false, 7, "\x1b[3D", 4},
		{"no_margins_clamps_at_zero", false, 7, "\x1b[100D", 0},
		{"inside_box_stops_at_margin", true, 5, "\x1b[10D", 3},
		{"at_margin_stays_at_margin", true, 3, "\x1b[D", 3},
		{"left_of_box_reaches_zero", true, 2, "\x1b[5D", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(5, 10)
			if tc.margins {
				s.Write([]byte("\x1b[?69h\x1b[4;7s"))
			}
			s.Write([]byte("\x1b[2;" + itoa(tc.startX+1) + "H" + tc.seq))
			if row, col := s.CursorPos(); row != 1 || col != tc.wantX {
				t.Errorf("CUB %q from col %d = (%d,%d), want (1,%d)", tc.seq, tc.startX, row, col, tc.wantX)
			}
		})
	}
}

// CUB under reverse-wraparound must land where the same number of Backspaces
// lands, wrapping back onto the previous line.
func TestCUBUnderReverseWrapMatchesBackspaces(t *testing.T) {
	cub := New(5, 10)
	bs := New(5, 10)
	for _, s := range []*Screen{cub, bs} {
		s.Write([]byte("\x1b[?7h\x1b[?45h\x1b[3;2H"))
	}
	cub.Write([]byte("\x1b[3D"))
	bs.Write([]byte("\b\b\b"))
	cr, cc := cub.CursorPos()
	br, bc := bs.CursorPos()
	if cr != br || cc != bc {
		t.Errorf("CUB 3 = (%d,%d), three BS = (%d,%d); they must agree", cr, cc, br, bc)
	}
	if cr != 1 || cc != 8 {
		t.Errorf("CUB 3 from (2,1) under reverse wrap = (%d,%d), want (1,8)", cr, cc)
	}
}

func TestDECBIAndDECFIScrollTheMarginBoxAtItsEdge(t *testing.T) {
	cases := []struct {
		name  string
		col   int // 1-based column to place the cursor
		seq   string
		row0  string
		wantX int
	}{
		{"decbi_inside_moves_left", 6, "\x1b6", "0123456789", 4},
		{"decbi_at_left_margin_scrolls_right", 4, "\x1b6", "012 345789", 3},
		{"decbi_left_of_box_moves_left", 3, "\x1b6", "0123456789", 1},
		{"decfi_inside_moves_right", 5, "\x1b9", "0123456789", 5},
		{"decfi_at_right_margin_scrolls_left", 7, "\x1b9", "012456 789", 6},
		{"decfi_right_of_box_moves_right", 9, "\x1b9", "0123456789", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := marginScreen(t)
			s.Write([]byte("\x1b[1;" + itoa(tc.col) + "H" + tc.seq))
			if got := s.RowString(0); got != tc.row0 {
				t.Errorf("row 0 after %q at col %d = %q, want %q", tc.seq, tc.col, got, tc.row0)
			}
			if got := s.RowString(4); got != tc.row0 {
				t.Errorf("row 4 after %q at col %d = %q, want %q (every region row shifts)", tc.seq, tc.col, got, tc.row0)
			}
			if _, x := s.CursorPos(); x != tc.wantX {
				t.Errorf("cursor col after %q at col %d = %d, want %d", tc.seq, tc.col, x, tc.wantX)
			}
		})
	}
}

func TestDECBIAndDECFIAreIgnoredAtTheScreenEdge(t *testing.T) {
	s := New(3, 5)
	s.Write([]byte("abcde\x1b[1;1H\x1b6"))
	if got := s.RowString(0); got != " abcd" {
		t.Errorf("DECBI at col 0 with no margins = %q, want %q (column 0 is the margin)", got, " abcd")
	}
	s = New(3, 5)
	s.Write([]byte("abcde\x1b[1;5H\x1b9"))
	if got := s.RowString(0); got != "bcde" {
		t.Errorf("DECFI at last col with no margins = %q, want %q", got, "bcde")
	}
}

func TestBoxedLineOpsShiftOnlyInsideTheMargins(t *testing.T) {
	cases := []struct {
		name string
		seq  string // applied with the cursor at row 2 (1-based), col 5
		want string
	}{
		{"il_one", "\x1b[L", "0123456789|012    789|0123456789|0123456789|0123456789"},
		{"dl_one", "\x1b[M", "0123456789|0123456789|0123456789|0123456789|012    789"},
		{"su_one", "\x1b[S", "0123456789|0123456789|0123456789|0123456789|012    789"},
		{"sd_one", "\x1b[T", "012    789|0123456789|0123456789|0123456789|0123456789"},
		{"il_two_from_row_two", "\x1b[2L", "0123456789|012    789|012    789|0123456789|0123456789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := marginScreen(t)
			s.Write([]byte("\x1b[2;5H"))
			s.Write([]byte(tc.seq))
			if got := screenText(s); got != tc.want {
				t.Errorf("after %q screen = %q, want %q", tc.seq, got, tc.want)
			}
		})
	}
}

// Distinct row content makes a boxed shift's direction and source row visible.
func TestBoxedLineOpsMoveTheRightRows(t *testing.T) {
	s := New(4, 6)
	for y, line := range []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd"} {
		s.Write([]byte("\x1b[" + itoa(y+1) + ";1H" + line))
	}
	s.Write([]byte("\x1b[?69h\x1b[2;4s\x1b[2;2H\x1b[M"))
	if got, want := screenText(s), "aaaaaa|bcccbb|cdddcc|d   dd"; got != want {
		t.Errorf("boxed DL at row 1 = %q, want %q", got, want)
	}
	s.Write([]byte("\x1b[2;2H\x1b[L"))
	if got, want := screenText(s), "aaaaaa|b   bb|cccccc|dddddd"; got != want {
		t.Errorf("boxed IL at row 1 = %q, want %q", got, want)
	}
}

func TestDECICAndDECDCShiftColumnsInsideTheBox(t *testing.T) {
	cases := []struct {
		name string
		seq  string // cursor at row 3, col 5 (1-based)
		want string
	}{
		{"decic_one", "\x1b['}", "0123 45789"},
		{"decic_clamped_to_box", "\x1b[9'}", "0123   789"},
		{"decdc_one", "\x1b['~", "012356 789"},
		{"decdc_clamped_to_box", "\x1b[9'~", "0123   789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := marginScreen(t)
			s.Write([]byte("\x1b[3;5H" + tc.seq))
			for y := range 5 {
				if got := s.RowString(y); got != tc.want {
					t.Errorf("row %d after %q = %q, want %q", y, tc.seq, got, tc.want)
				}
			}
		})
	}
}

func TestShiftLeftByScreenWidthBlanksTheRegion(t *testing.T) {
	s := New(3, 4)
	s.Write([]byte("abcd\r\nefgh\r\nijkl\x1b[2;3r\x1b[4 @"))
	if got := screenText(s); got != "abcd||" {
		t.Errorf("SL 4 on a 4-wide screen = %q, want %q", got, "abcd||")
	}
	s = New(3, 4)
	s.Write([]byte("abcd\r\nefgh\r\nijkl\x1b[2;3r\x1b[3 @"))
	if got := screenText(s); got != "abcd|h|l" {
		t.Errorf("SL 3 on a 4-wide screen = %q, want %q", got, "abcd|h|l")
	}
}
