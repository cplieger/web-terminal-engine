package vt

import (
	"fmt"
	"log/slog"
	"time"
)

//nolint:gocyclo // wide CSI final-byte dispatch; cognitively flat (each case is a one-line handler call)
func (s *Screen) dispatchCSI(final byte) {
	// Any cursor-affecting CSI clears pending wrap.
	switch final {
	case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'f', 'd', 'e', '`', 'a', 'I', 'Z', 'u':
		s.pendingWrap = false
	}

	// Intermediate-byte sequences (SP, '!', '$') dispatch separately. An
	// unrecognized intermediate returns false and falls through to the main
	// final-byte dispatch below, preserving the original behavior.
	if s.numInterm > 0 && s.dispatchCSIIntermediate(final) {
		return
	}

	switch final {
	case 'A': // CUU — cursor up
		s.curY -= s.paramVal(0, 1)
		s.clampCursor()
	case 'B': // CUD — cursor down
		s.curY += s.paramVal(0, 1)
		s.clampCursor()
	case 'C': // CUF — cursor forward
		s.curX += s.paramVal(0, 1)
		s.clampCursor()
	case 'D': // CUB — cursor back
		s.curX -= s.paramVal(0, 1)
		s.clampCursor()
	case 'E': // CNL — cursor next line
		s.curY += s.paramVal(0, 1)
		s.curX = 0
		s.clampCursor()
	case 'F': // CPL — cursor previous line
		s.curY -= s.paramVal(0, 1)
		s.curX = 0
		s.clampCursor()
	case 'G': // CHA — cursor horizontal absolute
		s.curX = s.paramVal(0, 1) - 1
		s.clampCursor()
	case 'H', 'f': // CUP / HVP — cursor position
		s.cursorPosition(s.paramVal(0, 1)-1, s.paramVal(1, 1)-1)
	case 'J': // ED — erase in display
		s.eraseInDisplay(s.paramVal(0, 0))
	case 'K': // EL — erase in line
		s.eraseInLine(s.paramVal(0, 0))
	case '@': // ICH — insert characters
		s.insertChars(s.paramVal(0, 1))
	case 'L': // IL — insert lines
		s.insertLines(s.paramVal(0, 1))
	case 'M': // DL — delete lines
		s.deleteLines(s.paramVal(0, 1))
	case 'P': // DCH — delete characters
		s.deleteChars(s.paramVal(0, 1))
	case 'S': // SU — scroll up
		s.scrollUp(s.paramVal(0, 1))
	case 'T', '^': // SD — scroll down
		s.scrollDown(s.paramVal(0, 1))
	case 'X': // ECH — erase characters
		s.eraseChars(s.paramVal(0, 1))
	case 'I': // CHT — cursor forward tabulation
		s.cursorTabForward(s.paramVal(0, 1))
	case 'Z': // CBT — cursor backward tabulation
		s.cursorTabBackward(s.paramVal(0, 1))
	case '`': // HPA — horizontal position absolute
		s.curX = s.paramVal(0, 1) - 1
		s.clampCursor()
	case 'a': // HPR — horizontal position relative
		s.curX += s.paramVal(0, 1)
		s.clampCursor()
	case 'd': // VPA — vertical position absolute
		s.curY = s.paramVal(0, 1) - 1
		s.clampCursor()
	case 'e': // VPR — vertical position relative
		s.curY += s.paramVal(0, 1)
		s.clampCursor()
	case 'b': // REP — repeat last printed character
		s.repeatLastChar(s.paramVal(0, 1))
	case 'c': // DA — device attributes
		s.deviceAttributes()
	case 'g': // TBC — tab clear
		s.tabClear(s.paramVal(0, 0))
	case 'r': // DECSTBM — set scroll region
		s.setScrollRegion()
	case 'm': // SGR — select graphic rendition
		s.applySGR()
	case 's':
		s.saveCursor()
	case 'u':
		s.restoreCursor()
	case 'h':
		s.applyModes(true)
	case 'l':
		s.applyModes(false)
	case 't': // window manipulation
		s.windowManipulation()
	case 'n': // DSR — device status report
		s.deviceStatusReport()
	default:
		if final != 0 {
			slog.Info("vt: unhandled CSI", "cmd", string(final), "marker", s.privateMarker)
		}
	}
}

// clampCursor confines the cursor to the screen bounds. The cursor-movement
// handlers move the cursor freely and then call this; clamping the axis that
// did not move is a no-op, so one helper serves every movement command.
func (s *Screen) clampCursor() {
	s.curX = min(max(s.curX, 0), s.Width-1)
	s.curY = min(max(s.curY, 0), s.Height-1)
}

// cursorPosition implements CUP/HVP. The coordinates are 0-based (the caller
// converts from the 1-based CSI parameters). In origin mode the row is
// relative to the scroll region and clamped to its bottom.
func (s *Screen) cursorPosition(y, x int) {
	y = max(y, 0)
	x = max(x, 0)
	if s.OriginMode {
		y += s.scrollTop
		if y > s.scrollBottom {
			y = s.scrollBottom
		}
	} else if y >= s.Height {
		y = s.Height - 1
	}
	if x >= s.Width {
		x = s.Width - 1
	}
	s.curY, s.curX = y, x
}

// dispatchCSIIntermediate handles CSI sequences carrying an intermediate byte
// (SP, '!', or '$'). It returns true when it consumed the sequence; an
// unrecognized intermediate returns false so the caller falls through to the
// main final-byte dispatch. Called only when s.numInterm > 0.
func (s *Screen) dispatchCSIIntermediate(final byte) bool {
	switch s.pIntermed[0] {
	case ' ': // SP-prefixed
		s.dispatchCSISpace(final)
		return true
	case '!': // DECSTR — soft terminal reset
		if final == 'p' {
			s.softReset()
		}
		return true
	case '$': // DECRQM — request mode
		if final == 'p' {
			s.handleDECRQM()
		}
		return true
	}
	return false
}

// dispatchCSISpace handles the SP-intermediate CSI sequences (SL, SR,
// DECSCUSR).
func (s *Screen) dispatchCSISpace(final byte) {
	switch final {
	case '@': // SL — shift left
		s.shiftLeft(s.paramVal(0, 1))
	case 'A': // SR — shift right
		s.shiftRight(s.paramVal(0, 1))
	case 'q': // DECSCUSR — set cursor style
		v := s.paramVal(0, 0)
		if v <= 6 {
			s.CursorStyle = uint8(v) // #nosec G115 -- v bounded [0,6]
			s.CursorBlink = v == 0 || v%2 == 1
		}
	default:
		slog.Info("vt: unhandled CSI SP", "cmd", string(final), "args", s.paramVal(0, 0))
	}
}

// eraseInDisplay implements ED (CSI J).
func (s *Screen) eraseInDisplay(mode int) {
	switch mode {
	case 0:
		s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
		s.eraseRegion(s.curY+1, 0, s.Height-1, s.Width-1)
	case 1:
		s.eraseRegion(0, 0, s.curY-1, s.Width-1)
		s.eraseRegion(s.curY, 0, s.curY, s.curX)
	case 2, 3:
		s.eraseRegion(0, 0, s.Height-1, s.Width-1)
		s.Drained = nil
	}
}

// eraseInLine implements EL (CSI K).
func (s *Screen) eraseInLine(mode int) {
	switch mode {
	case 0:
		s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
	case 1:
		s.eraseRegion(s.curY, 0, s.curY, s.curX)
	case 2:
		s.eraseRegion(s.curY, 0, s.curY, s.Width-1)
	}
}

// eraseChars implements ECH (CSI X): erase n cells from the cursor rightward,
// clamped to the row end.
func (s *Screen) eraseChars(n int) {
	end := s.curX + n - 1
	if end >= s.Width {
		end = s.Width - 1
	}
	s.eraseRegion(s.curY, s.curX, s.curY, end)
}

// scrollUp implements SU (CSI S): scroll the region up n lines (clamped to the
// region height).
func (s *Screen) scrollUp(n int) {
	n = min(n, s.scrollBottom-s.scrollTop+1)
	for range n {
		s.scrollUpOnce()
	}
}

// scrollDown implements SD (CSI T / CSI ^): scroll the region down n lines.
func (s *Screen) scrollDown(n int) {
	n = min(n, s.scrollBottom-s.scrollTop+1)
	for range n {
		s.scrollDownOnce()
	}
}

// cursorTabForward implements CHT (CSI I): advance the cursor n tab stops,
// stopping at the right margin.
func (s *Screen) cursorTabForward(n int) {
	for range n {
		s.curX = s.nextTabStop(s.curX)
		if s.curX >= s.Width {
			s.curX = s.Width - 1
			break
		}
	}
}

// cursorTabBackward implements CBT (CSI Z): move the cursor back n tab stops,
// stopping at column 0.
func (s *Screen) cursorTabBackward(n int) {
	for range n {
		s.curX = s.prevTabStop(s.curX)
		if s.curX <= 0 {
			s.curX = 0
			break
		}
	}
}

// repeatLastChar implements REP (CSI b): reprint the last printed rune n times
// using the style it was printed with.
func (s *Screen) repeatLastChar(n int) {
	if s.lastPrintedRune == 0 {
		return
	}
	saved := s.style
	s.style = s.lastPrintedStyle
	for range n {
		s.put(s.lastPrintedRune)
	}
	s.style = saved
}

// deviceAttributes implements DA (CSI c) for the primary, secondary ('>') and
// tertiary ('=') variants.
func (s *Screen) deviceAttributes() {
	switch s.privateMarker {
	case '>':
		s.Response = append(s.Response, "\x1b[>1;10;0c"...)
	case '=':
		s.Response = append(s.Response, "\x1bP!|00000000\x1b\\"...)
	default:
		if s.paramVal(0, 0) == 0 {
			s.Response = append(s.Response, "\x1b[?62;22c"...)
		}
	}
}

// tabClear implements TBC (CSI g): clear the stop at the cursor (Ps=0) or all
// stops (Ps=3).
func (s *Screen) tabClear(mode int) {
	switch mode {
	case 0:
		s.clearTabStop(s.curX)
	case 3:
		s.clearAllTabStops()
	}
}

// setScrollRegion implements DECSTBM (CSI r) and homes the cursor.
func (s *Screen) setScrollRegion() {
	top := max(s.paramVal(0, 1)-1, 0)
	bottom := min(s.paramVal(1, s.Height)-1, s.Height-1)
	if top < bottom {
		s.scrollTop = top
		s.scrollBottom = bottom
	}
	s.curY, s.curX = 0, 0
}

// windowManipulation answers the subset of CSI t we support: report the text
// area size in characters (Ps=18).
func (s *Screen) windowManipulation() {
	if s.paramVal(0, 0) == 18 {
		s.Response = fmt.Appendf(s.Response, "\x1b[8;%d;%dt", s.Height, s.Width)
	}
}

// deviceStatusReport implements DSR (CSI n / CSI ? n): cursor-position report
// (Ps=6) and, for the ANSI form, terminal-status report (Ps=5).
func (s *Screen) deviceStatusReport() {
	if s.privateMarker == '?' {
		if s.paramVal(0, 0) == 6 {
			s.Response = fmt.Appendf(s.Response, "\x1b[?%d;%dR", s.curY+1, s.curX+1)
		}
		return
	}
	switch s.paramVal(0, 0) {
	case 5:
		s.Response = append(s.Response, "\x1b[0n"...)
	case 6:
		s.Response = fmt.Appendf(s.Response, "\x1b[%d;%dR", s.curY+1, s.curX+1)
	}
}

// applyModes handles CSI ? Ps h/l (set/reset DEC private modes).
func (s *Screen) applyModes(set bool) {
	if s.privateMarker != '?' {
		// ANSI modes — not implemented, ignore
		return
	}
	for i := range s.paramCount() {
		mode := s.paramVal(i, 0)
		if set {
			s.setMode(mode)
		} else {
			s.resetMode(mode)
		}
	}
}

func (s *Screen) setMode(mode int) {
	switch mode {
	case 1:
		s.AppCursorKeys = true
	case 5:
		s.ReverseVideo = true
	case 6:
		s.OriginMode = true
		s.curY, s.curX = s.scrollTop, 0
	case 7:
		s.AutoWrap = true
	case 12:
		s.CursorBlink = true
	case 25:
		s.CursorHidden = false
	case 47, 1047, 1049:
		s.enterAltScreen()
	case 1048:
		s.saveCursor()
	case 1000, 1002, 1003:
		s.MouseMode = uint16(mode) // #nosec G115 -- mode is one of 1000/1002/1003, fits uint16
	case 1004:
		s.FocusReporting = true
	case 1006:
		s.MouseSGR = true
	case 2004:
		s.BracketedPaste = true
	case 2026:
		s.HoldFlush(time.Now().Add(time.Second))
	}
}

//nolint:gocyclo // mode dispatch is inherently branchy
func (s *Screen) resetMode(mode int) {
	switch mode {
	case 1:
		s.AppCursorKeys = false
	case 5:
		s.ReverseVideo = false
	case 6:
		s.OriginMode = false
		s.curY, s.curX = 0, 0
	case 7:
		s.AutoWrap = false
	case 12:
		s.CursorBlink = false
	case 25:
		s.CursorHidden = true
	case 47, 1047, 1049:
		s.exitAltScreen()
		s.Drained = nil
	case 1048:
		s.restoreCursor()
	case 1000:
		if s.MouseMode == 1000 {
			s.MouseMode = 0
		}
	case 1002:
		if s.MouseMode == 1002 {
			s.MouseMode = 0
		}
	case 1003:
		if s.MouseMode == 1003 {
			s.MouseMode = 0
		}
	case 1004:
		s.FocusReporting = false
	case 1006:
		s.MouseSGR = false
	case 2004:
		s.BracketedPaste = false
	case 2026:
		s.ReleaseFlush()
	}
}

// --- DECRQM handler (uses new param structure) ---

func (s *Screen) handleDECRQM() {
	if s.privateMarker == '?' {
		mode := s.paramVal(0, 0)
		ps := s.decModeStatus(mode)
		s.Response = fmt.Appendf(s.Response, "\x1b[?%d;%d$y", mode, ps)
	} else {
		mode := s.paramVal(0, 0)
		ps := s.ansiModeStatus(mode)
		s.Response = fmt.Appendf(s.Response, "\x1b[%d;%d$y", mode, ps)
	}
}

// --- Cell-level operations used by CSI handlers ---

func (s *Screen) insertChars(n int) {
	if s.curY < 0 || s.curY >= s.Height {
		return
	}
	row := s.Cells[s.curY]
	for x := s.Width - 1; x >= s.curX+n; x-- {
		row[x] = row[x-n]
	}
	for x := s.curX; x < s.curX+n && x < s.Width; x++ {
		row[x] = Cell{Ch: ' '}
	}
}

func (s *Screen) deleteChars(n int) {
	if s.curY < 0 || s.curY >= s.Height {
		return
	}
	row := s.Cells[s.curY]
	n = min(n, s.Width-s.curX)
	for x := s.curX; x < s.Width-n; x++ {
		row[x] = row[x+n]
	}
	for x := s.Width - n; x < s.Width; x++ {
		row[x] = Cell{Ch: ' '}
	}
}

func (s *Screen) insertLines(n int) {
	if s.curY < s.scrollTop || s.curY > s.scrollBottom {
		return
	}
	avail := s.scrollBottom - s.curY + 1
	n = min(n, avail)
	for range n {
		for y := s.scrollBottom; y > s.curY; y-- {
			s.Cells[y] = s.Cells[y-1]
		}
		s.Cells[s.curY] = makeRow(s.Width, s.style.BG)
	}
}

func (s *Screen) deleteLines(n int) {
	if s.curY < s.scrollTop || s.curY > s.scrollBottom {
		return
	}
	avail := s.scrollBottom - s.curY + 1
	n = min(n, avail)
	for range n {
		for y := s.curY; y < s.scrollBottom; y++ {
			s.Cells[y] = s.Cells[y+1]
		}
		s.Cells[s.scrollBottom] = makeRow(s.Width, s.style.BG)
	}
}

func (s *Screen) scrollUpOnce() {
	if s.scrollTop == 0 && s.scrollBottom == s.Height-1 {
		s.Drained = append(s.Drained, cellsToRuns(s.Cells[0]))
	}
	for y := s.scrollTop; y < s.scrollBottom; y++ {
		s.Cells[y] = s.Cells[y+1]
	}
	s.Cells[s.scrollBottom] = makeRow(s.Width, s.style.BG)
}

func (s *Screen) lineDown() {
	if s.curY == s.scrollBottom {
		s.scrollUpOnce()
		return
	}
	s.curY++
	if s.curY >= s.Height {
		s.curY = s.Height - 1
	}
}

func (s *Screen) scrollDownOnce() {
	for y := s.scrollBottom; y > s.scrollTop; y-- {
		s.Cells[y] = s.Cells[y-1]
	}
	s.Cells[s.scrollTop] = makeRow(s.Width, s.style.BG)
}

func (s *Screen) shiftLeft(n int) {
	if n >= s.Width {
		for y := s.scrollTop; y <= s.scrollBottom; y++ {
			for x := range s.Width {
				s.Cells[y][x] = Cell{Ch: ' '}
			}
		}
		return
	}
	for y := s.scrollTop; y <= s.scrollBottom; y++ {
		row := s.Cells[y]
		for x := range s.Width - n {
			row[x] = row[x+n]
		}
		for x := s.Width - n; x < s.Width; x++ {
			row[x] = Cell{Ch: ' '}
		}
	}
}

func (s *Screen) shiftRight(n int) {
	if n >= s.Width {
		for y := s.scrollTop; y <= s.scrollBottom; y++ {
			for x := range s.Width {
				s.Cells[y][x] = Cell{Ch: ' '}
			}
		}
		return
	}
	for y := s.scrollTop; y <= s.scrollBottom; y++ {
		row := s.Cells[y]
		for x := s.Width - 1; x >= n; x-- {
			row[x] = row[x-n]
		}
		for x := range n {
			row[x] = Cell{Ch: ' '}
		}
	}
}

func (s *Screen) softReset() {
	s.style = Style{}
	s.scrollTop = 0
	s.scrollBottom = s.Height - 1
	s.savedY, s.savedX = 0, 0
	s.cursorStateSaved = false
	s.pendingWrap = false
	s.CursorHidden = false
	s.CursorStyle = 0
	s.BracketedPaste = false
	s.AppCursorKeys = false
	s.AppKeypad = false
	s.OriginMode = false
	s.AutoWrap = true
	s.ReverseVideo = false
	s.MouseMode = 0
	s.MouseSGR = false
	s.FocusReporting = false
	s.tabStops = nil
	s.resetCharsets()
}

// maxCSIArgValue is the maximum value a single CSI parameter can take.
const maxCSIArgValue = 65535

// csiArg is a backward-compatible helper that parses the first numeric
// parameter from a raw CSI parameter string. Used only by tests.
func csiArg(args string, def int) int {
	clean := args
	for clean != "" && (clean[0] == '?' || clean[0] == '>' || clean[0] == '!') {
		clean = clean[1:]
	}
	if clean == "" {
		return def
	}
	// Extract first number (up to ; or end)
	end := 0
	for end < len(clean) && clean[end] >= '0' && clean[end] <= '9' {
		end++
	}
	if end == 0 {
		return def
	}
	var n int
	for _, ch := range clean[:end] {
		n = n*10 + int(ch-'0')
		if n > maxCSIArgValue {
			return maxCSIArgValue
		}
	}
	return n
}
