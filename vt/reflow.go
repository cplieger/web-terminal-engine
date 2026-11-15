package vt

// reflowGrid is a main-screen grid with its soft-wrap flags and cursor: what a
// width change lays out again. wrapped may be shorter than cells (a missing
// flag reads false).
type reflowGrid struct {
	cells       [][]Cell
	wrapped     []bool
	curY        int
	curX        int
	pendingWrap bool
}

func (g *reflowGrid) wrappedAt(y int) bool {
	return y < len(g.wrapped) && g.wrapped[y]
}

// contentLen is the length of row up to its last cell holding text: a written
// cell, a glyph or a link. The blanks an erase or a new row leave are padding
// whatever their background, while a space the program wrote is text.
func contentLen(row []Cell) int {
	n := len(row)
	for n > 0 && !row[n-1].drawn && row[n-1].Ch == ' ' && row[n-1].Hyperlink == "" {
		n--
	}
	return n
}

// relayout returns g laid out at cols, with a flag for every row: unchanged
// at its own width, each row cut or padded when re-wrapping would move
// nothing, else re-wrapped.
func (g reflowGrid) relayout(cols int) reflowGrid {
	if len(g.wrapped) < len(g.cells) {
		w := make([]bool, len(g.cells))
		copy(w, g.wrapped)
		g.wrapped = w
	}
	switch {
	case len(g.cells) == 0 || len(g.cells[0]) == cols:
		return g
	case g.layoutHolds(cols):
		for i, old := range g.cells {
			g.cells[i] = makeRow(cols, Color{})
			copy(g.cells[i], old)
		}
		return g
	default:
		lines, cursor := g.lines()
		return layout(lines, cursor, cols, len(g.cells))
	}
}

// layoutHolds reports whether re-wrapping g at cols would change nothing: no
// row continues the one above, every row's content fits with no pad in it
// but the cursor's, and the cursor keeps its column with no wrap pending.
func (g *reflowGrid) layoutHolds(cols int) bool {
	if g.pendingWrap || g.curX >= cols {
		return false
	}
	for y, row := range g.cells {
		if y > 0 && g.wrappedAt(y) {
			return false
		}
		n := contentLen(row)
		if n > cols {
			return false
		}
		for x, c := range row[:n] {
			if c.widePad && (y != g.curY || x != g.curX) {
				return false
			}
		}
	}
	return true
}

// logicalLine is one line of a grid, its rows joined by soft wraps.
type logicalLine struct {
	// cells is the text, without its last row's padding or any wide
	// character's pad but the one the cursor is on.
	cells []Cell
	// tail is the padding after the text on its last row, kept for its paint.
	tail []Cell
	// continuesHistory marks a first line that began in scrollback.
	continuesHistory bool
}

// logicalCursor is the cursor as a place in a line: offset is the cell of the
// line the next character goes to, so a pending wrap reads one past the
// cursor's column.
type logicalCursor struct {
	line, offset int
	pendingWrap  bool
}

// lines reads g as logical lines, one per row not continuing the row above,
// and the cursor as a place in them. The width does not enter either.
func (g *reflowGrid) lines() ([]logicalLine, logicalCursor) {
	cursor := logicalCursor{pendingWrap: g.pendingWrap}
	lines := make([]logicalLine, 0, len(g.cells))
	for start := 0; start < len(g.cells); {
		end := start
		for end+1 < len(g.cells) && g.wrappedAt(end+1) {
			end++
		}
		l, at := g.readLine(start, end)
		if at >= 0 {
			cursor.line, cursor.offset = len(lines), at
		}
		lines = append(lines, l)
		start = end + 1
	}
	return lines, cursor
}

// readLine joins rows start to end into one line and returns the cursor's
// offset in it, or -1 when the cursor is on another line.
func (g *reflowGrid) readLine(start, end int) (l logicalLine, at int) {
	curY := min(max(g.curY, 0), len(g.cells)-1)
	l.continuesHistory = start == 0 && g.wrappedAt(0)
	at = -1
	for r := start; r <= end; r++ {
		row := g.cells[r]
		if r == end {
			n := contentLen(row)
			row, l.tail = row[:n], row[n:]
		}
		cursorX := -1
		if r == curY {
			cursorX = g.curX
		}
		for x, c := range row {
			if x == cursorX {
				at = len(l.cells)
			}
			if c.widePad && x != cursorX {
				continue
			}
			l.cells = append(l.cells, c)
		}
		if cursorX >= len(row) {
			at = len(l.cells) + cursorX - len(row)
		}
		if r == curY && g.pendingWrap {
			at++
		}
	}
	return l, at
}

// layout wraps lines onto rows of width cols, as writing them at that width
// would, and puts the cursor back at its place. Every line takes at least one
// row; the caller fits the result to the screen's height.
func layout(lines []logicalLine, cursor logicalCursor, cols, rowsHint int) reflowGrid {
	g := reflowGrid{cells: make([][]Cell, 0, rowsHint), wrapped: make([]bool, 0, rowsHint)}
	mayPend := cursor.pendingWrap || !textAfter(lines, cursor.line)
	for i, l := range lines {
		at := -1
		if i == cursor.line {
			at = cursor.offset
		}
		g.appendLine(l, at, mayPend, cols)
	}
	return g
}

// textAfter reports whether a line after lines[i] holds text.
func textAfter(lines []logicalLine, i int) bool {
	for _, l := range lines[i+1:] {
		if len(l.cells) > 0 {
			return true
		}
	}
	return false
}

// appendLine wraps l onto new rows of width cols, without splitting a wide
// character, and places the cursor when at, its offset in l or -1, falls in
// it. mayPend is whether a cursor just past a full last row may pend a wrap.
func (g *reflowGrid) appendLine(l logicalLine, at int, mayPend bool, cols int) {
	row := g.newRow(cols, l.continuesHistory)
	x := 0
	for i := 0; i < len(l.cells); {
		w := 1
		if i+1 < len(l.cells) && l.cells[i].Ch != 0 && l.cells[i+1].Ch == 0 {
			w = 2
		}
		if x+w > cols && x > 0 {
			if x < cols {
				// The pad autowrap leaves takes the wide character's paint, as put does.
				row[x] = Cell{Ch: ' ', Style: l.cells[i].Style, widePad: true}
			}
			row = g.newRow(cols, true)
			x = 0
		}
		if at >= i && at < i+w {
			g.curY, g.curX = len(g.cells)-1, min(x+at-i, cols-1)
		}
		// A wide character on a one-column row keeps its glyph and loses its
		// spacer.
		x += copy(row[x:], l.cells[i:i+w])
		i += w
	}
	copy(row[x:], l.tail)
	if at < len(l.cells) {
		return
	}
	g.placePastText(x+at-len(l.cells), x, mayPend, cols, l.tail)
}

// placePastText puts a cursor that sat after its line's text, at column col
// of the line's last row whose text ends at x. Blank space with room keeps the
// cursor on the row. A cursor just past a full row pends a wrap when mayPend:
// a wrap was pending, which still lands on the next row, or no later line
// holds text for it to land on. Otherwise the row gains a continuation row
// for the cursor, moving the rows below down by one.
func (g *reflowGrid) placePastText(col, x int, mayPend bool, cols int, tail []Cell) {
	last := len(g.cells) - 1
	switch {
	case col < cols:
		g.curY, g.curX = last, col
	case x < cols:
		g.curY, g.curX = last, cols-1
	case col == cols && mayPend:
		g.curY, g.curX, g.pendingWrap = last, cols-1, true
	default:
		copy(g.newRow(cols, true), tail)
		g.curY, g.curX = last+1, min(col-cols, cols-1)
	}
}

func (g *reflowGrid) newRow(cols int, wrapped bool) []Cell {
	row := makeRow(cols, Color{})
	g.cells = append(g.cells, row)
	g.wrapped = append(g.wrapped, wrapped)
	return row
}
