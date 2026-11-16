package vt

// reflowGrid is a main-screen grid with its soft-wrap flags, cursor and DECSC
// slot: what a width change lays out again. wrapped may be shorter than cells
// (a missing flag reads false).
type reflowGrid struct {
	cells   [][]Cell
	wrapped []bool
	cursor  gridPoint
	// saved is the DECSC slot's place, carried with the text when hasSaved.
	saved    gridPoint
	hasSaved bool
}

// gridPoint is a cell of a grid; a pending wrap puts the place one past it.
type gridPoint struct {
	y, x        int
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

// relayout lays g out at cols, with a flag for every row: unchanged at its
// own width, each row cut or padded when re-wrapping would move nothing, else
// re-wrapped.
func (g *reflowGrid) relayout(cols int) {
	if len(g.wrapped) < len(g.cells) {
		w := make([]bool, len(g.cells))
		copy(w, g.wrapped)
		g.wrapped = w
	}
	switch {
	case len(g.cells) == 0 || len(g.cells[0]) == cols:
	case g.layoutHolds(cols):
		for i, old := range g.cells {
			g.cells[i] = makeRow(cols, Color{})
			copy(g.cells[i], old)
		}
	default:
		lines, cursor, saved := g.lines()
		*g = layout(lines, cursor, saved, cols, len(g.cells))
	}
}

// layoutHolds reports whether re-wrapping g at cols would change nothing: no
// row continues the one above, every row's content fits with no pad in it
// but the cursor's, and the cursor and DECSC slot keep their columns with no
// wrap pending.
func (g *reflowGrid) layoutHolds(cols int) bool {
	if !g.cursor.holds(cols) || g.hasSaved && !g.saved.holds(cols) {
		return false
	}
	for y := range g.cells {
		if y > 0 && g.wrappedAt(y) || !g.rowFits(y, cols) {
			return false
		}
	}
	return true
}

// holds reports whether p keeps its column at cols with no wrap pending.
func (p gridPoint) holds(cols int) bool {
	return !p.pendingWrap && p.x < cols
}

// rowFits reports whether row y's content fits cols with no pad in it but the
// cursor's and no wide character cellSpan would give a right half.
func (g *reflowGrid) rowFits(y, cols int) bool {
	n := contentLen(g.cells[y])
	if n > cols {
		return false
	}
	for x, c := range g.cells[y][:n] {
		if c.widePad && (y != g.cursor.y || x != g.cursor.x) {
			return false
		}
		if w, span := cellSpan(g.cells[y], x, cols); span > w {
			return false
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

// logicalCursor is the cursor or DECSC slot as a place in a line: offset is
// the cell of the line the next character goes to, so a pending wrap reads one
// past the column. line is -1 for a slot never saved.
type logicalCursor struct {
	line, offset int
	pendingWrap  bool
}

// lines reads g as logical lines, one per row not continuing the row above,
// and the cursor and DECSC slot as places in them. The width does not enter
// any of them.
func (g *reflowGrid) lines() (lines []logicalLine, cursor, saved logicalCursor) {
	cursor = logicalCursor{pendingWrap: g.cursor.pendingWrap}
	saved = logicalCursor{line: -1, pendingWrap: g.saved.pendingWrap}
	lines = make([]logicalLine, 0, len(g.cells))
	for start := 0; start < len(g.cells); {
		end := start
		for end+1 < len(g.cells) && g.wrappedAt(end+1) {
			end++
		}
		l, at, savedAt := g.readLine(start, end)
		if at >= 0 {
			cursor.line, cursor.offset = len(lines), at
		}
		if savedAt >= 0 {
			saved.line, saved.offset = len(lines), savedAt
		}
		lines = append(lines, l)
		start = end + 1
	}
	return lines, cursor, saved
}

// readLine joins rows start to end into one line and returns the cursor's
// and the DECSC slot's offsets in it, each -1 when on another line.
func (g *reflowGrid) readLine(start, end int) (l logicalLine, at, savedAt int) {
	onRow := func(p gridPoint) int { return min(max(p.y, 0), len(g.cells)-1) }
	l.continuesHistory = start == 0 && g.wrappedAt(0)
	at, savedAt = -1, -1
	for r := start; r <= end; r++ {
		row := g.cells[r]
		if r == end {
			n := contentLen(row)
			row, l.tail = row[:n], row[n:]
		}
		keepPad := -1
		if r == onRow(g.cursor) {
			keepPad = g.cursor.x
			at = placeIn(l.cells, row, g.cursor, keepPad)
		}
		if g.hasSaved && r == onRow(g.saved) {
			savedAt = placeIn(l.cells, row, g.saved, keepPad)
		}
		l.cells = appendRow(l.cells, row, keepPad)
	}
	return l, at, savedAt
}

// appendRow appends row to cells without any wide character's pad but the one
// at column keepPad.
func appendRow(cells, row []Cell, keepPad int) []Cell {
	for x, c := range row {
		if !c.widePad || x == keepPad {
			cells = append(cells, c)
		}
	}
	return cells
}

// placeIn returns the offset of p in a line whose cells precede row, which
// appendRow is about to add keeping only keepPad's pad. A point on a dropped
// pad reads as the cell after it.
func placeIn(cells, row []Cell, p gridPoint, keepPad int) int {
	at := len(cells) + max(p.x-len(row), 0)
	for x, c := range row[:min(p.x, len(row))] {
		if !c.widePad || x == keepPad {
			at++
		}
	}
	if p.pendingWrap {
		at++
	}
	return at
}

// layout wraps lines onto rows of width cols, as writing them at that width
// would, and puts the cursor back at its place. Every line takes at least one
// row; the caller fits the result to the screen's height.
func layout(lines []logicalLine, cursor, saved logicalCursor, cols, rowsHint int) reflowGrid {
	g := reflowGrid{cells: make([][]Cell, 0, rowsHint), wrapped: make([]bool, 0, rowsHint), hasSaved: saved.line >= 0}
	mayPend := cursor.pendingWrap || !textAfter(lines, cursor.line)
	for i, l := range lines {
		at, savedAt := -1, -1
		if i == cursor.line {
			at = cursor.offset
		}
		if i == saved.line {
			savedAt = saved.offset
		}
		g.appendLine(l, lineMarks{at, savedAt}, mayPend, cols)
	}
	return g
}

// lineMarks are the cursor's and the DECSC slot's offsets in a line, each -1
// when on another line.
type lineMarks struct{ cursor, saved int }

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
// character, and places the cursor and DECSC slot whose offsets fall in it.
// mayPend is whether a cursor just past a full last row may pend a wrap.
func (g *reflowGrid) appendLine(l logicalLine, at lineMarks, mayPend bool, cols int) {
	row := g.newRow(cols, l.continuesHistory)
	x := 0
	for i := 0; i < len(l.cells); {
		w, span := cellSpan(l.cells, i, cols)
		if x+span > cols && x > 0 {
			if x < cols {
				// The pad autowrap leaves takes the wide character's paint, as put does.
				row[x] = Cell{Ch: ' ', Style: l.cells[i].Style, widePad: true}
			}
			if at.saved == i {
				g.saved = savedPastText(len(g.cells)-1, x, x, cols)
				at.saved = -1
			}
			row = g.newRow(cols, true)
			x = 0
		}
		g.markCell(at, i, w, x, cols)
		// A wide character on a one-column row keeps its glyph and loses its
		// spacer.
		x += copy(row[x:], l.cells[i:i+w])
		if span > w {
			half := l.cells[i]
			half.Ch = 0
			row[x] = half
			x++
		}
		i += w
	}
	copy(row[x:], l.tail)
	if at.saved >= len(l.cells) {
		g.saved = savedPastText(len(g.cells)-1, x+at.saved-len(l.cells), x, cols)
	}
	if at.cursor >= len(l.cells) {
		g.placePastText(x+at.cursor-len(l.cells), x, mayPend, cols, l.tail)
	}
}

// cellWidth is 2 for a wide character at cells[i], followed by its spacer,
// else 1.
func cellWidth(cells []Cell, i int) int {
	if i+1 < len(cells) && cells[i].Ch != 0 && cells[i+1].Ch == 0 {
		return 2
	}
	return 1
}

// cellSpan returns the cells of the character at cells[i] and the columns it
// takes on a row of cols: a wide character a one-column row left without its
// right half takes two again, as writing it there would.
func cellSpan(cells []Cell, i, cols int) (w, span int) {
	w = cellWidth(cells, i)
	if w == 1 && cols > 1 && runeWidth(cells[i].Ch) == 2 {
		return w, 2
	}
	return w, w
}

// markCell places the cursor and DECSC slot whose offsets fall in the w cells
// at offset i, copied to column x of the last row.
func (g *reflowGrid) markCell(at lineMarks, i, w, x, cols int) {
	y := len(g.cells) - 1
	if at.cursor >= i && at.cursor < i+w {
		g.cursor = gridPoint{y: y, x: min(x+at.cursor-i, cols-1)}
	}
	if at.saved >= i && at.saved < i+w {
		g.saved = gridPoint{y: y, x: min(x+at.saved-i, cols-1)}
	}
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
		g.cursor = gridPoint{y: last, x: col}
	case x < cols:
		g.cursor = gridPoint{y: last, x: cols - 1}
	case col == cols && mayPend:
		g.cursor = gridPoint{y: last, x: cols - 1, pendingWrap: true}
	default:
		copy(g.newRow(cols, true), tail)
		g.cursor = gridPoint{y: last + 1, x: min(col-cols, cols-1)}
	}
}

// savedPastText is the place of a DECSC slot at column col after text that
// ends at x on row y, the end of its line or a row a character wraps from, as
// writing at cols leaves it: a wrap pending just past a full row, else
// clamped to the row. Unlike the cursor it never adds a row, which would move
// text a restore does not write to.
func savedPastText(y, col, x, cols int) gridPoint {
	if col == cols && x == cols {
		return gridPoint{y: y, x: cols - 1, pendingWrap: true}
	}
	return gridPoint{y: y, x: min(col, cols-1)}
}

func (g *reflowGrid) newRow(cols int, wrapped bool) []Cell {
	row := makeRow(cols, Color{})
	g.cells = append(g.cells, row)
	g.wrapped = append(g.wrapped, wrapped)
	return row
}
