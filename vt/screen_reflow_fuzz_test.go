package vt

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// printableLines keeps data's printable ASCII and turns each newline into
// CR LF, so the written text has soft wraps and line ends but no cursor
// movement and no wide characters.
func printableLines(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for _, b := range data {
		switch {
		case b == '\n':
			out = append(out, '\r', '\n')
		case b >= 0x20 && b < 0x7f:
			out = append(out, b)
		}
	}
	return out
}

// logicalScreen is what a width change must keep: the text of each line, with
// rows joined by a soft wrap and the blanks no character was written to
// dropped from its end, and the cursor's offset in its line, one past the
// last column while a wrap is pending.
type logicalScreen struct {
	lines        []string
	cursorLine   int
	cursorOffset int
}

func logicalOf(s *Screen) logicalScreen {
	var ls logicalScreen
	var line []Cell
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
		line = line[:0]
	}
	for y, row := range s.Cells {
		if y > 0 && !s.wrapped[y] {
			flush()
		}
		if y == s.curY {
			ls.cursorLine = len(ls.lines)
			ls.cursorOffset = len(line) + s.curX
			if s.pendingWrap {
				ls.cursorOffset++
			}
		}
		line = append(line, row...)
	}
	flush()
	for len(ls.lines)-1 > ls.cursorLine && ls.lines[len(ls.lines)-1] == "" {
		ls.lines = ls.lines[:len(ls.lines)-1]
	}
	return ls
}

// rowsNeeded is how many rows the lines take at cols, one at least each.
func (ls logicalScreen) rowsNeeded(cols int) int {
	n := 0
	for _, line := range ls.lines {
		n += max(1, (len(line)+cols-1)/cols)
	}
	return n
}

// assertKept fails unless after holds before's lines and cursor line, and
// the cursor's offset too when the cursor was in its line's text or just
// after it. A cursor further out in blank space keeps its row only.
func assertKept(t *testing.T, before, after logicalScreen, what string) {
	t.Helper()
	inText := before.cursorOffset <= len(before.lines[before.cursorLine])
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
	if fits {
		assertKept(t, before, logicalOf(s), fmt.Sprintf("width %d -> %d", w1, w2))
	}
	s.Resize(s.Height, w1)
	assertGridShape(t, s)
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

	f.Fuzz(func(t *testing.T, data []byte, from, to uint8) {
		s := New(40, int(from)%60+1)
		writeString(t, s, string(printableLines(data)))
		s.DrainScrollback()
		roundTrip(t, s, int(to)%60+1)
	})
}

// writeEditing writes data to s as letters, line ends, cursor moves and
// erases: what a line editor or a full redraw writes, with no blank written as
// text.
func writeEditing(t *testing.T, s *Screen, data []byte) {
	t.Helper()
	erases := [6]string{1: "\x1b[J", 2: "\x1b[1J", 3: "\x1b[K", 4: "\x1b[1K", 5: "\x1b[2K"}
	for i := 0; i < len(data); i++ {
		b := data[i]
		op := b % 16
		switch {
		case op == 0:
			writeString(t, s, "\r\n")
		case op <= 5:
			writeString(t, s, erases[op])
		case op == 6 && i+2 < len(data):
			writeString(t, s, fmt.Sprintf("\x1b[%d;%dH", int(data[i+1])%s.Height+1, int(data[i+2])%s.Width+1))
			i += 2
		case op == 7:
			writeString(t, s, fmt.Sprintf("\x1b[%dX", int(b>>4)+1))
		case op > 7:
			writeString(t, s, string(rune('a'+b%26)))
		}
	}
}

// FuzzWrapped_onlyIntactRowsStayLinked pins the soft-wrap flag a reflow joins
// rows on to the content it describes. With no blank written as text, a row
// that continues the one above starts with a glyph, and the row above ends
// with one; an erase that blanks either end cuts the link. A width round trip
// then keeps the lines.
func FuzzWrapped_onlyIntactRowsStayLinked(f *testing.F) {
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdefghijklmnop\x06\x00\x00\x01hij\x00klm"), uint8(9))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdef\x05\x06\x00\x00\x03zz"), uint8(30))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyzabcdef\x06\x00\x13\x02"), uint8(3))
	f.Add([]byte("abcdefghijklmnopqrstuvwxyz\x06\x01\x02\x04\x17"), uint8(40))
	f.Add([]byte("00&0088118"), uint8(1))
	f.Add([]byte("&&208"), uint8(0))

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

// widthOps writes data to s as a width-independent sequence: letters, spaces,
// a wide character, line ends and DECSC, which saves a place in the text. A
// cursor move or an erase names a row or a column, which mean different text
// at another width, so they are left out.
func widthOps(t *testing.T, s *Screen, data []byte) {
	t.Helper()
	var b strings.Builder
	for _, c := range data {
		switch c % 8 {
		case 0:
			b.WriteString("\r\n")
		case 1:
			b.WriteByte(' ')
		case 2:
			b.WriteString("中")
		case 3:
			b.WriteString("\x1b7")
		default:
			b.WriteByte('a' + c%26)
		}
	}
	writeString(t, s, b.String())
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

// FuzzResize_matchesWritingAtTheNewWidth pins the reflow to its definition:
// text written at one width and resized to another reads as the text written
// at the new width, the cursor at the same place in the same line. data[0]'s
// high bit first restores the last DECSC, putting the cursor mid-text or
// above other lines. Only a cursor left at the end must keep the same pending
// wrap and land the next text the same way: a restored cursor after a full
// last row with text below takes a continuation row, where writing at the new
// width pends a wrap onto the next line's row.
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

	f.Fuzz(func(t *testing.T, data []byte, from, to uint8) {
		const rows = 120
		if len(data) == 0 || len(data) > 200 {
			return
		}
		a, b := int(from)%40+2, int(to)%40+2
		restore := data[0]&0x80 != 0
		resized, written := New(rows, a), New(rows, b)
		for _, s := range []*Screen{resized, written} {
			widthOps(t, s, data)
			if restore {
				writeString(t, s, "\x1b8")
			}
		}
		if len(resized.Drained) > 0 || len(written.Drained) > 0 {
			return
		}

		resized.Resize(rows, b)
		if len(resized.Drained) > 0 {
			return
		}

		gotLines, gotLine, gotOffset := logicalText(resized)
		wantLines, wantLine, wantOffset := logicalText(written)
		if !slices.Equal(gotLines, wantLines) || gotLine != wantLine || gotOffset != wantOffset {
			t.Fatalf("width %d resized to %d reads %q with the cursor at line %d offset %d; written at %d it reads %q, line %d offset %d",
				a, b, gotLines, gotLine, gotOffset, b, wantLines, wantLine, wantOffset)
		}
		if restore {
			return
		}
		if resized.pendingWrap != written.pendingWrap {
			t.Fatalf("width %d resized to %d leaves the wrap pending %v; written at %d it is %v", a, b, resized.pendingWrap, b, written.pendingWrap)
		}
		widthOps(t, resized, []byte("ZYX\x00Q"))
		widthOps(t, written, []byte("ZYX\x00Q"))
		gotLines, _, _ = logicalText(resized)
		wantLines, _, _ = logicalText(written)
		if !slices.Equal(gotLines, wantLines) {
			t.Fatalf("after more text, width %d resized to %d reads %q; written at %d it reads %q", a, b, gotLines, b, wantLines)
		}
	})
}
