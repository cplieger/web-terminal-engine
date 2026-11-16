package vt

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// wrappingText keeps data's printable ASCII and turns each newline into
// CR LF, 0x01 into a wide character, 0x02 and the next three bytes into a
// DECSLRM box the cursor moves into, and 0x03 into the box's end: soft wraps,
// wraps inside a box and the blank a wide character leaves at either edge,
// with no cursor movement outside a box.
func wrappingText(data []byte, rows, cols int) string {
	var b strings.Builder
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case c == '\n':
			b.WriteString("\r\n")
		case c == 0x01:
			b.WriteString("中")
		case c == 0x02 && i+3 < len(data) && cols > 1:
			left := int(data[i+1]) % (cols - 1)
			right := left + 1 + int(data[i+2])%(cols-left-1)
			fmt.Fprintf(&b, "\x1b[?69h\x1b[%d;%ds\x1b[%d;%dH", left+1, right+1, int(data[i+3])%rows+1, left+1)
			i += 3
		case c == 0x03:
			b.WriteString("\x1b[?69l")
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// logicalScreen is what a width change must keep: the text of each line, with
// rows joined by a soft wrap, the blanks no character was written to dropped
// from its end and the pad a wrapped wide character leaves in a row's last
// column left out, and the cursor's offset in its line, one past the last
// column while a wrap is pending. blanks counts, per line, the dropped blanks
// a re-wrap still lays out: those on rows before the line's last row, which
// xterm.js's reflow also keeps (getWrappedLineTrimmedLength trims the last
// row only).
type logicalScreen struct {
	lines        []string
	blanks       []int
	cursorLine   int
	cursorOffset int
}

func logicalOf(s *Screen) logicalScreen {
	var ls logicalScreen
	var line []Cell
	lastRow := 0
	flush := func() {
		n := len(line)
		for n > 0 && line[n-1].Ch == ' ' && !line[n-1].drawn {
			n--
		}
		var b strings.Builder
		for _, c := range line[:n] {
			b.WriteRune(c.Ch)
		}
		ls.lines = append(ls.lines, b.String())
		ls.blanks = append(ls.blanks, max(lastRow-n, 0))
		line = line[:0]
	}
	for y, row := range s.Cells {
		if y > 0 && !s.wrapped[y] {
			flush()
		}
		lastRow = len(line)
		for x, c := range row {
			if y == s.curY && x == s.curX {
				ls.cursorLine, ls.cursorOffset = len(ls.lines), len(line)
				if s.pendingWrap {
					ls.cursorOffset++
				}
			}
			if !c.widePad || x < len(row)-1 || y == s.curY && x == s.curX {
				line = append(line, c)
			}
		}
	}
	flush()
	for len(ls.lines)-1 > ls.cursorLine && ls.lines[len(ls.lines)-1] == "" {
		ls.lines = ls.lines[:len(ls.lines)-1]
		ls.blanks = ls.blanks[:len(ls.blanks)-1]
	}
	return ls
}

// rowsNeeded is how many rows the lines and their kept blanks take at cols,
// one at least each, a wide character that does not fit a row's end going to
// the next row.
func (ls logicalScreen) rowsNeeded(cols int) int {
	n := 0
	for i, line := range ls.lines {
		n++
		x := 0
		for _, r := range line + strings.Repeat(" ", ls.blanks[i]) {
			w := runeWidth(r)
			switch {
			case r == 0:
				continue
			case w > cols:
				return math.MaxInt
			case x+w > cols:
				n++
				x = 0
			}
			x += w
		}
	}
	return n
}

// assertKept fails unless after holds before's lines and cursor line, and
// the cursor's offset too when the cursor was in its line's text or just
// after it. A cursor further out in blank space keeps its row only.
func assertKept(t *testing.T, before, after logicalScreen, what string) {
	t.Helper()
	inText := before.cursorOffset <= utf8.RuneCountInString(before.lines[before.cursorLine])
	if !slices.Equal(after.lines, before.lines) || after.cursorLine != before.cursorLine || inText && after.cursorOffset != before.cursorOffset {
		t.Fatalf("%s changed the lines:\nbefore %+v\nafter  %+v", what, before, after)
	}
}

// roundTrip resizes s to w2 and back to its width, checking the grid shape
// each time and, when the lines fit the screen at both widths so nothing
// leaves it, that both resizes keep the lines and the cursor. The fit leaves
// a row spare: a cursor after a line that fills its last row, with text
// below, takes a row of its own.
func roundTrip(t *testing.T, s *Screen, w2 int) {
	t.Helper()
	w1 := s.Width
	before := logicalOf(s)
	fits := before.rowsNeeded(w2) < s.Height && before.rowsNeeded(w1) < s.Height
	s.Resize(s.Height, w2)
	assertGridShape(t, s)
	assertWidePairs(t, s)
	if fits {
		assertKept(t, before, logicalOf(s), fmt.Sprintf("width %d -> %d", w1, w2))
	}
	s.Resize(s.Height, w1)
	assertGridShape(t, s)
	assertWidePairs(t, s)
	if fits {
		assertKept(t, before, logicalOf(s), fmt.Sprintf("width %d -> %d -> %d", w1, w2, w1))
	}
}

// FuzzResize_widthRoundTrip pins re-wrapping as lossless: text written at one
// width, resized to another width and back, keeps every line and the cursor's
// place in its line whenever nothing left the screen. Every resize also leaves
// a grid of exactly Height rows of Width cells with a flag per row and the
// cursor on it.
func FuzzResize_widthRoundTrip(f *testing.F) {
	f.Add([]byte("abcdefghijklmnop\nqr st\n\nuvwxyz"), uint8(20), uint8(7))
	f.Add([]byte("aaaaaaaaaaaaaaaaaaaa"), uint8(10), uint8(3))
	f.Add([]byte("abcde"), uint8(5), uint8(3))
	f.Add([]byte("abcde \ncd"), uint8(5), uint8(2))
	f.Add([]byte(" "), uint8(0), uint8(6))
	f.Add([]byte(strings.Repeat("0", 40)), uint8(0), uint8(21))
	f.Add([]byte(strings.Repeat("0", 78)+"  "), uint8(2), uint8(1))
	f.Add([]byte("abcdefghij\x02\x00\x03\x00ABCD\x01\x03"), uint8(9), uint8(19))
	f.Add([]byte("abcd\x01ef\x01\x01g"), uint8(4), uint8(6))

	f.Fuzz(func(t *testing.T, data []byte, from, to uint8) {
		s := New(40, int(from)%60+1)
		writeChecked(t, s, wrappingText(data, s.Height, s.Width))
		s.DrainScrollback()
		roundTrip(t, s, int(to)%60+1)
	})
}

// writeEditing writes data to s as letters, line ends, cursor moves, erases
// and line shifts: what a line editor, a full redraw or a pager writes, with
// no blank written as text. A byte from 0x80 up is a line shift: IL, DL, SU
// or SD, a scroll region (DECSTBM), RI or IND.
func writeEditing(t *testing.T, s *Screen, data []byte) {
	t.Helper()
	erases := [6]string{1: "\x1b[J", 2: "\x1b[1J", 3: "\x1b[K", 4: "\x1b[1K", 5: "\x1b[2K"}
	shifts := [8]string{0: "L", 1: "M", 3: "S", 4: "T", 5: "r"}
	for i := 0; i < len(data); i++ {
		b := data[i]
		op := b % 16
		switch {
		case b >= 0x80 && b%8 == 2:
			if i+2 < len(data) {
				writeChecked(t, s, fmt.Sprintf("\x1b[%d;%dr", int(data[i+1])%s.Height+1, int(data[i+2])%s.Height+1))
				i += 2
			}
		case b >= 0x80 && b%8 == 6:
			writeChecked(t, s, "\x1bM")
		case b >= 0x80 && b%8 == 7:
			writeChecked(t, s, "\x1bD")
		case b >= 0x80:
			writeChecked(t, s, fmt.Sprintf("\x1b[%d%s", int(b>>3)%3+1, shifts[b%8]))
		case op == 0:
			writeChecked(t, s, "\r\n")
		case op <= 5:
			writeChecked(t, s, erases[op])
		case op == 6 && i+2 < len(data):
			writeChecked(t, s, fmt.Sprintf("\x1b[%d;%dH", int(data[i+1])%s.Height+1, int(data[i+2])%s.Width+1))
			i += 2
		case op == 7:
			writeChecked(t, s, fmt.Sprintf("\x1b[%dX", int(b>>4)+1))
		case op > 7:
			writeChecked(t, s, string(rune('a'+b%26)))
		}
	}
}

// FuzzWrapped_onlyIntactRowsStayLinked pins the soft-wrap flag a reflow joins
// rows on to the content it describes. With no blank written as text, a row
// that continues the one above starts with a glyph, and the row above ends
// with one; an erase that blanks either end, or a line shift that moves a
// blank or another row next to it, cuts the link. A width round trip then
// keeps the lines.
func FuzzWrapped_onlyIntactRowsStayLinked(f *testing.F) {
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdefghijklmnop\x06\x00\x00\x01hij\x00klm"), uint8(9))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdef\x05\x06\x00\x00\x03zz"), uint8(30))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdef\x06\x00\x13\x02"), uint8(3))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyz\x06\x01\x02\x04\x17"), uint8(40))
	f.Add([]byte("00&0088118"), uint8(1))
	f.Add([]byte("&&208"), uint8(0))
	f.Add([]byte(strings.Repeat("h", 20)+"\x06\x00\x00\x91"), uint8(40))
	f.Add([]byte("hh\x00"+strings.Repeat("h", 20)+"\x06\x01\x00\x91"), uint8(40))
	f.Add([]byte(strings.Repeat("h", 25)+"\x06\x01\x00\x90"), uint8(40))
	f.Add([]byte("hh\x00"+strings.Repeat("h", 20)+"\x82\x00\x01\x91"), uint8(40))
	f.Add([]byte("hh\x00"+strings.Repeat("h", 20)+"\x82\x00\x01\x93"), uint8(40))
	f.Add([]byte("hh\x00"+strings.Repeat("h", 20)+"\x82\x00\x01\x00\x00hhh\x94"), uint8(40))

	f.Fuzz(func(t *testing.T, data []byte, to uint8) {
		const rows, cols = 8, 10
		s := New(rows, cols)
		writeEditing(t, s, data)
		for y := 1; y < s.Height; y++ {
			if s.wrapped[y] && (s.Cells[y][0].Ch == ' ' || s.Cells[y-1][cols-1].Ch == ' ') {
				t.Fatalf("row %d is linked to row %d, but %q and %q no longer meet at the wrap", y, y-1, s.RowString(y-1), s.RowString(y))
			}
		}
		s.DrainScrollback()
		roundTrip(t, s, int(to)%60+1)
	})
}

func assertGridShape(t *testing.T, s *Screen) {
	t.Helper()
	if len(s.Cells) != s.Height || len(s.wrapped) != s.Height {
		t.Fatalf("%d rows and %d wrap flags on a %d-row screen", len(s.Cells), len(s.wrapped), s.Height)
	}
	for y, row := range s.Cells {
		if len(row) != s.Width {
			t.Fatalf("row %d has %d cells on a %d-column screen", y, len(row), s.Width)
		}
	}
	if s.curY < 0 || s.curY >= s.Height || s.curX < 0 || s.curX >= s.Width {
		t.Fatalf("cursor (%d, %d) outside a %dx%d screen", s.curY, s.curX, s.Height, s.Width)
	}
}

// assertWidePairs fails unless, on the screen in use and on the main screen
// saved beneath the alternate one, every right half follows a wide character
// and a wide character cut off from its right half sits in a row's last
// column. A wide glyph with no right half elsewhere is one cell DECFRA filled.
func assertWidePairs(t *testing.T, s *Screen) {
	t.Helper()
	for _, grid := range []struct {
		name  string
		cells [][]Cell
	}{{"screen", s.Cells}, {"saved main screen", s.savedMainCells}} {
		for y, row := range grid.cells {
			for x, c := range row {
				switch {
				case c.Ch == 0 && (x == 0 || row[x-1].Ch == 0 || runeWidth(row[x-1].Ch) != 2):
					t.Fatalf("%s row %d %q: column %d is a right half with no wide character before it", grid.name, y, rowText(row), x)
				case c.wideCut && x != len(row)-1:
					t.Fatalf("%s row %d %q: the wide character in column %d is cut off from its right half before the last column", grid.name, y, rowText(row), x)
				}
			}
		}
	}
}

// rowText renders row with a wide character's right half as '|'.
func rowText(row []Cell) string {
	var b strings.Builder
	for _, c := range row {
		if c.Ch == 0 {
			b.WriteRune('|')
			continue
		}
		b.WriteRune(c.Ch)
	}
	return strings.TrimRight(b.String(), " ")
}

// writeChecked writes text to s a byte at a time and runs assertWidePairs
// after each byte that leaves the parser in ground state, so every edit,
// erase and write a sequence makes keeps the wide pairs, not only the last.
func writeChecked(t *testing.T, s *Screen, text string) {
	t.Helper()
	var b [1]byte
	for i := range len(text) {
		b[0] = text[i]
		_, _ = s.Write(b[:])
		if s.pState == stGround {
			assertWidePairs(t, s)
		}
	}
}

// maxCheckedLen bounds the input writeFuzzed checks after every operation: a
// check scans the grid, about 5 µs on 24x80, so a 1 MiB OSC 52 mutation
// written as text would take seconds per exec.
const maxCheckedLen = 1 << 14

// writeFuzzed writes arbitrary bytes to s through writeChecked, or, past
// maxCheckedLen, whole and then checks the wide pairs once.
func writeFuzzed(t *testing.T, s *Screen, data []byte) {
	t.Helper()
	if len(data) <= maxCheckedLen {
		writeChecked(t, s, string(data))
		return
	}
	_, _ = s.Write(data)
	assertWidePairs(t, s)
}

// widthOps writes data to s as a width-independent sequence: letters, spaces,
// a wide character, line ends, DECSC, which saves a place in the text, and
// 0x7f, a letter DECFRA then fills with a wide code point in its one cell.
func widthOps(t *testing.T, s *Screen, data []byte) {
	t.Helper()
	var b strings.Builder
	for _, c := range data {
		switch {
		case c == 0x7f:
			writeChecked(t, s, b.String()+"a")
			b.Reset()
			y, x := s.curY, s.curX
			if !s.pendingWrap {
				x--
			}
			fmt.Fprintf(&b, "\x1b[20013;%d;%d;%d;%d$x", y+1, x+1, y+1, x+1)
		case c%8 == 0:
			b.WriteString("\r\n")
		case c%8 == 1:
			b.WriteByte(' ')
		case c%8 == 2:
			b.WriteString("中")
		case c%8 == 3:
			b.WriteString("\x1b7")
		default:
			b.WriteByte('a' + c%26)
		}
	}
	writeChecked(t, s, b.String())
}

// logicalText is logicalOf with every wide character's pad left out and its
// spacer read as '.', so lines written at different widths compare. A cursor
// on a pad reads as the offset of the cell after it.
func logicalText(s *Screen) (lines []string, cursorLine, cursorOffset int) {
	var line []rune
	var kept int // len(line) up to its last cell a character was written to
	flush := func() {
		lines = append(lines, string(line[:kept]))
		line, kept = line[:0], 0
	}
	for y, row := range s.Cells {
		if y > 0 && !s.wrapped[y] {
			flush()
		}
		for x, c := range row {
			if y == s.curY && x == s.curX {
				cursorLine, cursorOffset = len(lines), len(line)
				if s.pendingWrap {
					cursorOffset++
				}
			}
			switch {
			case c.widePad:
				continue
			case c.Ch == 0:
				line = append(line, '.')
			default:
				line = append(line, c.Ch)
			}
			if c.Ch != ' ' || c.drawn {
				kept = len(line)
			}
		}
	}
	flush()
	for len(lines)-1 > cursorLine && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, cursorLine, cursorOffset
}

// lineStarts returns the first row of each of s's lines, then s.Height.
func lineStarts(s *Screen) []int {
	var starts []int
	for y := range s.Height {
		if y == 0 || !s.wrapped[y] {
			starts = append(starts, y)
		}
	}
	return append(starts, s.Height)
}

// emptyLines reports whether rows from to to of s are each an empty line,
// which takes one row at every width.
func emptyLines(s *Screen, from, to int) bool {
	for y := from; y <= to; y++ {
		if y < 0 || contentLen(s.Cells[y]) > 0 || s.wrapped[y] || y+1 < s.Height && s.wrapped[y+1] {
			return false
		}
	}
	return true
}

// lineEdit is one edit of whole lines as one screen writes it: seq, and the
// number of empty lines it lets into a scroll region, which every screen must
// agree on.
type lineEdit struct {
	seq   string
	letIn int
}

// planLineEdit lays out edit op on s's own rows, at line j, the at-th line up
// to the cursor's: op%8 picks IL or DL at line j, or SU, SD, IL, DL, IND at
// the bottom or RI inside a scroll region (DECSTBM) spanning lines j to k
// above the cursor's line. Bit 3 is the count, bits 4 and 5 the region's span,
// and bit 6 turns DL into ED 0 from line j's start, which leaves the cursor
// there. It refuses an edit that would push text off the screen or its region
// or cut a line, so the same edit means the same text at every width.
func planLineEdit(s *Screen, op, at byte) (lineEdit, bool) {
	kind, n := op%8, int(op>>3)%2+1
	starts := lineStarts(s)
	cur := 0
	for cur+1 < len(starts) && starts[cur+1] <= s.curY {
		cur++
	}
	j := int(at) % (cur + 1)
	k := min(j+int(op>>4)%4, cur-1)
	rows := func(m int) int { return starts[m+1] - starts[m] }
	cup := func(y, x int) string { return fmt.Sprintf("\x1b[%d;%dH", y+1, x+1) }
	y, x := s.curY, s.curX
	var e lineEdit
	var b strings.Builder
	top, bottom := 0, s.Height-1
	if kind >= 2 {
		if j >= cur || starts[k+1]-1 <= starts[j] {
			return e, false
		}
		top, bottom = starts[j], starts[k+1]-1
		fmt.Fprintf(&b, "\x1b[%d;%dr", top+1, bottom+1)
	}
	switch kind {
	case 0: // IL before line j
		if y+n >= s.Height || !emptyLines(s, s.Height-n, s.Height-1) {
			return e, false
		}
		fmt.Fprintf(&b, "%s\x1b[%dL", cup(starts[j], 0), n)
		y += n
	case 1:
		if op&0x40 != 0 {
			e.seq = cup(starts[j], 0) + "\x1b[J"
			return e, true
		}
		if j >= cur {
			return e, false
		}
		fmt.Fprintf(&b, "%s\x1b[%dM", cup(starts[j], 0), rows(j))
		y -= rows(j)
	case 2: // SU of line j
		e.letIn = rows(j)
		fmt.Fprintf(&b, "\x1b[%dS", e.letIn)
	case 3, 4, 7: // SD, IL before line k, RI
		if kind == 7 {
			n = 1
		}
		if bottom-n+1 <= top || !emptyLines(s, bottom-n+1, bottom) {
			return e, false
		}
		switch kind {
		case 3:
			fmt.Fprintf(&b, "\x1b[%dT", n)
		case 4:
			fmt.Fprintf(&b, "%s\x1b[%dL", cup(starts[k], 0), n)
		default:
			b.WriteString(cup(top, 0) + "\x1bM")
		}
	case 5: // DL of line k
		e.letIn = rows(k)
		fmt.Fprintf(&b, "%s\x1b[%dM", cup(starts[k], 0), e.letIn)
	case 6: // LF at the region's bottom, scrolling line j out
		if rows(j) != 1 {
			return e, false
		}
		b.WriteString(cup(bottom, 0) + "\n")
	}
	if kind >= 2 {
		b.WriteString("\x1b[r")
	}
	b.WriteString(cup(y, x))
	if s.pendingWrap {
		// Writing the last character again pends the wrap the edit cleared. A
		// wide glyph in one cell is DECFRA's: written, it would wrap whole.
		switch c := s.Cells[s.curY][x]; {
		case c.Ch == 0:
			b.WriteString(cup(y, x-1) + string(s.Cells[s.curY][x-1].Ch))
		case runeWidth(c.Ch) == 2:
			fmt.Fprintf(&b, "a\x1b[%d;%d;%d;%d;%d$x", c.Ch, y+1, x+1, y+1, x+1)
		default:
			b.WriteRune(c.Ch)
		}
	}
	e.seq = b.String()
	return e, true
}

// editLines applies line edit op to every screen at the same line of their
// text, then puts each cursor back where the edit moved its text. It skips an
// edit some screen refuses and reports whether it applied it.
func editLines(t *testing.T, screens []*Screen, op, at byte) bool {
	t.Helper()
	edits := make([]lineEdit, len(screens))
	for i, s := range screens {
		e, ok := planLineEdit(s, op, at)
		if !ok || e.letIn != edits[0].letIn && i > 0 {
			return false
		}
		edits[i] = e
	}
	for i, s := range screens {
		writeChecked(t, s, edits[i].seq)
	}
	return true
}

// FuzzResize_matchesWritingAtTheNewWidth pins the reflow to its definition:
// text written at one width and resized to another reads as the text written
// at the new width, the cursor at the same place in the same line. A byte
// from 0x80 up after the first is a line edit (planLineEdit). Unless an edit
// moved rows, data[0]'s high bit restores the last DECSC after the resize, and
// with bit 6 before it too. A cursor restored before the resize keeps neither
// pending wrap nor next text: after a full last row with text below it takes a
// continuation row, where writing at the new width pends a wrap.
func FuzzResize_matchesWritingAtTheNewWidth(f *testing.F) {
	f.Add([]byte("abcdefghijklmnop\x00qr\x01st\x00\x00uvwxyz"), uint8(20), uint8(7))
	f.Add([]byte("ab\x01\x01cd\x01\x01"), uint8(8), uint8(4))
	f.Add([]byte("abc\x02\x02\x01d"), uint8(4), uint8(10))
	f.Add([]byte("abcdefghijk"), uint8(11), uint8(5))
	f.Add([]byte("aaaa\x01"), uint8(10), uint8(5))
	f.Add([]byte("\x84bcd\x03\x00next"), uint8(4), uint8(6))
	f.Add([]byte("\x84bcd\x03\x00next"), uint8(6), uint8(2))
	f.Add([]byte("\xc4b\x01\x01cd\x01\x01\x03\x00more"), uint8(6), uint8(2))
	f.Add([]byte("\x04\x05\x06\x07\x04\x05\x06\x07"), uint8(18), uint8(6))
	f.Add([]byte("dddddddddd\x00ee\x00ff\x80\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddddddddd\x00ee\x00ff\x81\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddddddddd\x00ee\x00ff\xc1\x01"), uint8(8), uint8(2))
	f.Add([]byte("dd\x00dddddddddd\x00ff\x92\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddddddddd\x00\x00ff\x93\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddddddddd\x00\x00ff\x94\x00"), uint8(2), uint8(8))
	f.Add([]byte("dddddddddd\x00ee\x00ff\x95\x00"), uint8(8), uint8(2))
	f.Add([]byte("dd\x00dddddddddd\x00ff\x96\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddddddddd\x00\x00ff\x97\x00"), uint8(8), uint8(2))
	f.Add([]byte("dddd\x00eeeeeeee\x80\x00"), uint8(6), uint8(2))
	f.Add([]byte("eeeeee\x02\x80\x00"), uint8(6), uint8(2))
	f.Add([]byte("dd\x02e\x00fg\x02\x80\x00"), uint8(1), uint8(6))
	f.Add([]byte("\x84dd\x03\x02e\x00f"), uint8(1), uint8(6))
	f.Add([]byte("\x84dd\x03\x02e\x00f"), uint8(2), uint8(4))
	f.Add([]byte("\x84dd\x03\x02e\x00f"), uint8(4), uint8(2))
	f.Add([]byte("\x84ddddddddddddddddddd\x00f\x01\x03\x00eeeeee"), uint8(18), uint8(8))
	f.Add([]byte("\x84ddddddddddddddddddd\x00f\x01\x03\x00eeeeee"), uint8(8), uint8(18))
	f.Add([]byte("\x84ddddddddd\x03\x00eeeeee"), uint8(18), uint8(8))
	f.Add([]byte("\x84ddddddddd\x03\x00eeeeee"), uint8(8), uint8(18))
	f.Add([]byte("\x7f\x7f\x7fddddd"), uint8(6), uint8(14))
	f.Add([]byte("\x7f\x7f\x7fddddd"), uint8(6), uint8(2))
	f.Add([]byte("ddddddd\x7f"), uint8(6), uint8(14))
	f.Add([]byte("ddddddd\x7f"), uint8(6), uint8(2))

	f.Fuzz(func(t *testing.T, data []byte, from, to uint8) {
		const rows = 120
		if len(data) == 0 || len(data) > 200 {
			return
		}
		a, b := int(from)%40+2, int(to)%40+2
		resized, written := New(rows, a), New(rows, b)
		screens := []*Screen{resized, written}
		shifted := false
		for start, i := 0, 1; i <= len(data); i++ {
			if i < len(data) && data[i] < 0x80 {
				continue
			}
			for _, s := range screens {
				widthOps(t, s, data[start:i])
			}
			if i+1 < len(data) {
				shifted = editLines(t, screens, data[i], data[i+1]) || shifted
			}
			start = min(i+2, len(data))
			i++
		}
		restoreAfter := data[0]&0x80 != 0 && !shifted
		restoreBefore := restoreAfter && data[0]&0x40 != 0
		if restoreBefore {
			for _, s := range screens {
				writeChecked(t, s, "\x1b8")
			}
		}
		if len(resized.Drained) > 0 || len(written.Drained) > 0 {
			return
		}

		resized.Resize(rows, b)
		assertWidePairs(t, resized)
		if len(resized.Drained) > 0 {
			return
		}

		compare := func(what string) {
			t.Helper()
			gotLines, gotLine, gotOffset := logicalText(resized)
			wantLines, wantLine, wantOffset := logicalText(written)
			if !slices.Equal(gotLines, wantLines) || gotLine != wantLine || gotOffset != wantOffset {
				t.Fatalf("%s, width %d resized to %d reads %q with the cursor at line %d offset %d; written at %d it reads %q, line %d offset %d",
					what, a, b, gotLines, gotLine, gotOffset, b, wantLines, wantLine, wantOffset)
			}
		}
		compare("after the resize")
		if restoreAfter {
			for _, s := range screens {
				writeChecked(t, s, "\x1b8")
			}
			compare("after ESC 8")
		}
		if restoreBefore {
			return
		}
		if resized.pendingWrap != written.pendingWrap {
			t.Fatalf("width %d resized to %d leaves the wrap pending %v; written at %d it is %v", a, b, resized.pendingWrap, b, written.pendingWrap)
		}
		widthOps(t, resized, []byte("ZYX\x00Q"))
		widthOps(t, written, []byte("ZYX\x00Q"))
		gotLines, _, _ := logicalText(resized)
		wantLines, _, _ := logicalText(written)
		if !slices.Equal(gotLines, wantLines) {
			t.Fatalf("after more text, width %d resized to %d reads %q; written at %d it reads %q", a, b, gotLines, b, wantLines)
		}
	})
}
