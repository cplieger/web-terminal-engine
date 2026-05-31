package vt

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

//nolint:gocyclo,gocognit // CSI dispatch is inherently complex
func (s *Screen) dispatchCSI(final byte) {
	args := string(s.pParams)
	// Any cursor-affecting CSI clears pending wrap.
	switch final {
	case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'f', 'd', 'e', '`', 'a', 'I', 'Z', 'u':
		s.pendingWrap = false
	}

	// SP-prefixed sequences (intermediate byte 0x20).
	if len(s.pIntermed) > 0 && s.pIntermed[0] == ' ' {
		switch final {
		case '@': // SL - Shift left Ps columns
			n := csiArg(args, 1)
			s.shiftLeft(n)
		case 'A': // SR - Shift right Ps columns
			n := csiArg(args, 1)
			s.shiftRight(n)
		case 'q': // DECSCUSR - Set cursor style
			v := csiArg(args, 0)
			if v <= 6 {
				s.CursorStyle = uint8(v) // #nosec G115 -- v bounded [0,6]
				// Odd values (1,3,5) = blinking; even (2,4,6) = steady; 0 = default (blink)
				s.CursorBlink = v == 0 || v%2 == 1
			}
		default:
			slog.Info("vt: unhandled CSI SP", "cmd", string(final), "args", args)
		}
		return
	}

	// '!' intermediate — DECSTR (soft terminal reset).
	if len(s.pIntermed) > 0 && s.pIntermed[0] == '!' {
		if final == 'p' {
			s.softReset()
		}
		return
	}

	switch final {
	case 'A':
		s.curY -= csiArg(args, 1)
		if s.curY < 0 {
			s.curY = 0
		}
	case 'B':
		n := csiArg(args, 1)
		s.curY += n
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'C':
		n := csiArg(args, 1)
		s.curX += n
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 'D':
		n := csiArg(args, 1)
		s.curX -= n
		if s.curX < 0 {
			s.curX = 0
		}
	case 'E':
		n := csiArg(args, 1)
		s.curY += n
		s.curX = 0
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'F':
		n := csiArg(args, 1)
		s.curY -= n
		s.curX = 0
		if s.curY < 0 {
			s.curY = 0
		}
	case 'G':
		n := csiArg(args, 1)
		s.curX = max(n-1, 0)
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 'H', 'f':
		params := csiArgs(args)
		y, x := 0, 0
		if len(params) >= 1 {
			y = params[0] - 1
		}
		if len(params) >= 2 {
			x = params[1] - 1
		}
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
	case 'J':
		d := csiArg(args, 0)
		switch d {
		case 0:
			s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
			s.eraseRegion(s.curY+1, 0, s.Height-1, s.Width-1)
		case 1:
			s.eraseRegion(0, 0, s.curY-1, s.Width-1)
			s.eraseRegion(s.curY, 0, s.curY, s.curX)
		case 2:
			s.eraseRegion(0, 0, s.Height-1, s.Width-1)
			// Discard any pending drained lines — the screen was just
			// cleared, so whatever was in the buffer is now blank and
			// shouldn't pollute scrollback history.
			s.Drained = nil
		case 3:
			// Erase display + scrollback (xterm extension)
			s.eraseRegion(0, 0, s.Height-1, s.Width-1)
			s.Drained = nil
		}
	case 'K':
		d := csiArg(args, 0)
		switch d {
		case 0:
			s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
		case 1:
			s.eraseRegion(s.curY, 0, s.curY, s.curX)
		case 2:
			s.eraseRegion(s.curY, 0, s.curY, s.Width-1)
		}
	case '@': // Insert n blank characters (ICH)
		n := csiArg(args, 1)
		s.insertChars(n)
	case 'L': // Insert n lines (IL)
		n := csiArg(args, 1)
		s.insertLines(n)
	case 'M': // Delete n lines (DL)
		n := csiArg(args, 1)
		s.deleteLines(n)
	case 'P': // Delete n characters (DCH)
		n := csiArg(args, 1)
		s.deleteChars(n)
	case 'S': // Scroll up n lines (SU)
		n := csiArg(args, 1)
		for range n {
			s.scrollUpOnce()
		}
	case 'T': // Scroll down n lines (SD)
		n := csiArg(args, 1)
		for range n {
			s.scrollDownOnce()
		}
	case '^': // SD alternate (ECMA-48 erratum, same as T)
		n := csiArg(args, 1)
		for range n {
			s.scrollDownOnce()
		}
	case 'X': // Erase n characters (ECH)
		n := csiArg(args, 1)
		end := s.curX + n - 1
		if end >= s.Width {
			end = s.Width - 1
		}
		s.eraseRegion(s.curY, s.curX, s.curY, end)
	case 'I': // Cursor forward tab (CHT)
		n := csiArg(args, 1)
		for range n {
			s.curX = (s.curX + 8) &^ 7
			if s.curX >= s.Width {
				s.curX = s.Width - 1
				break
			}
		}
	case 'Z': // Cursor backward tab (CBT)
		n := csiArg(args, 1)
		for range n {
			s.curX = ((s.curX - 1) &^ 7)
			if s.curX < 0 {
				s.curX = 0
				break
			}
		}
	case '`', 'a': // Char position absolute (HPA) / relative (HPR)
		n := csiArg(args, 1)
		if final == '`' {
			s.curX = n - 1
		} else {
			s.curX += n
		}
		if s.curX < 0 {
			s.curX = 0
		}
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 'd': // Line position absolute (VPA)
		n := csiArg(args, 1)
		s.curY = max(n-1, 0)
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'e': // Line position relative (VPR)
		n := csiArg(args, 1)
		s.curY += n
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'b': // Repeat preceding character (REP)
		n := csiArg(args, 1)
		if s.lastPrintedRune != 0 {
			saved := s.style
			s.style = s.lastPrintedStyle
			for range n {
				s.put(s.lastPrintedRune)
			}
			s.style = saved
		}
	case 'c': // Device Attributes
		switch {
		case strings.HasPrefix(args, ">"):
			// Secondary DA — respond as VT220.
			s.Response = append(s.Response, "\x1b[>1;10;0c"...)
		case strings.HasPrefix(args, "="):
			// Tertiary DA — respond with unit ID.
			s.Response = append(s.Response, "\x1bP!|00000000\x1b\\"...)
		case csiArg(args, 0) == 0:
			// Primary DA — respond as VT102.
			s.Response = append(s.Response, "\x1b[?6c"...)
		}
	case 'g': // Tab clear (TBC) — no-op (we don't track tab stops)
	case 'r': // Set scroll region (DECSTBM)
		params := csiArgs(args)
		top, bottom := 0, s.Height-1
		if len(params) >= 1 && params[0] > 0 {
			top = params[0] - 1
		}
		if len(params) >= 2 && params[1] > 0 {
			bottom = params[1] - 1
		}
		if top < 0 {
			top = 0
		}
		if bottom >= s.Height {
			bottom = s.Height - 1
		}
		if top < bottom {
			s.scrollTop = top
			s.scrollBottom = bottom
		}
		s.curY, s.curX = 0, 0
	case 'm':
		s.applySGR(args)
	case 's':
		s.savedY, s.savedX = s.curY, s.curX
	case 'u':
		s.curY, s.curX = s.savedY, s.savedX
	case 'h':
		modes := privateModes(args)
		if modes[1049] || modes[47] || modes[1047] {
			s.enterAltScreen()
			slog.Info("vt: alt-screen entered")
		}
		if modes[1048] {
			s.savedY, s.savedX = s.curY, s.curX
		}
		if modes[2026] {
			// Synchronized output mode: hold flushes for up to 1s while
			// the application sends an atomic frame batch.
			s.HoldFlush(time.Now().Add(time.Second))
		}
		if modes[2004] {
			s.BracketedPaste = true
		}
		if modes[1] {
			s.AppCursorKeys = true
		}
		if modes[6] {
			s.OriginMode = true
			s.curY, s.curX = s.scrollTop, 0
		}
		if modes[7] {
			s.AutoWrap = true
		}
		if modes[25] {
			s.CursorHidden = false
		}
		if modes[12] {
			s.CursorBlink = true
		}
	case 'l':
		modes := privateModes(args)
		if modes[1049] || modes[47] || modes[1047] {
			s.exitAltScreen()
			s.Drained = nil
			slog.Info("vt: alt-screen exited")
		}
		if modes[1048] {
			s.curY, s.curX = s.savedY, s.savedX
			s.pendingWrap = false
		}
		if modes[2026] {
			s.ReleaseFlush()
		}
		if modes[2004] {
			s.BracketedPaste = false
		}
		if modes[1] {
			s.AppCursorKeys = false
		}
		if modes[6] {
			s.OriginMode = false
			s.curY, s.curX = 0, 0
		}
		if modes[7] {
			s.AutoWrap = false
		}
		if modes[25] {
			s.CursorHidden = true
		}
		if modes[12] {
			s.CursorBlink = false
		}
	case 't': // Window manipulation
		n := csiArg(args, 0)
		if n == 18 {
			s.Response = fmt.Appendf(s.Response, "\x1b[8;%d;%dt", s.Height, s.Width)
		}
	case 'n': // Device Status Report
		if csiArg(args, 0) == 6 {
			s.Response = fmt.Appendf(s.Response, "\x1b[%d;%dR", s.curY+1, s.curX+1)
		}
	default:
		if final != 0 {
			slog.Info("vt: unhandled CSI", "cmd", string(final), "args", args)
		}
	}
}

// privateModes parses the param string of a CSI ?...{h,l} sequence and
// returns a set of mode numbers present. Replaces a previous
// strings.Contains check that imprecisely matched substrings (e.g.
// "1049" inside "10490"). The leading "?" / ">" / "!" intermediates
// are stripped; semicolon separates parameters.
func privateModes(args string) map[int]bool {
	out := map[int]bool{}
	for _, n := range csiArgs(args) {
		out[n] = true
	}
	return out
}

// --- Cell-level operations used by CSI handlers ---

// insertChars inserts n blank cells at the cursor position, shifting cells right.
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

// deleteChars removes n cells at the cursor, shifting cells left.
func (s *Screen) deleteChars(n int) {
	if s.curY < 0 || s.curY >= s.Height {
		return
	}
	row := s.Cells[s.curY]
	for x := s.curX; x < s.Width-n; x++ {
		row[x] = row[x+n]
	}
	for x := s.Width - n; x < s.Width; x++ {
		if x >= 0 {
			row[x] = Cell{Ch: ' '}
		}
	}
}

// insertLines inserts n blank lines at the cursor row, shifting following lines down.
func (s *Screen) insertLines(n int) {
	if s.curY < s.scrollTop || s.curY > s.scrollBottom {
		return
	}
	for range n {
		// Shift rows down within the scroll region.
		for y := s.scrollBottom; y > s.curY; y-- {
			s.Cells[y] = s.Cells[y-1]
		}
		s.Cells[s.curY] = makeRow(s.Width, s.style.BG)
	}
}

// deleteLines removes n lines at the cursor row, shifting following lines up.
func (s *Screen) deleteLines(n int) {
	if s.curY < s.scrollTop || s.curY > s.scrollBottom {
		return
	}
	for range n {
		for y := s.curY; y < s.scrollBottom; y++ {
			s.Cells[y] = s.Cells[y+1]
		}
		s.Cells[s.scrollBottom] = makeRow(s.Width, s.style.BG)
	}
}

// scrollUpOnce moves all lines in the scroll region up by 1, drains the top.
func (s *Screen) scrollUpOnce() {
	if s.scrollTop == 0 && s.scrollBottom == s.Height-1 {
		s.Drained = append(s.Drained, cellsToRuns(s.Cells[0]))
	}
	for y := s.scrollTop; y < s.scrollBottom; y++ {
		s.Cells[y] = s.Cells[y+1]
	}
	s.Cells[s.scrollBottom] = makeRow(s.Width, s.style.BG)
}

// lineDown advances the cursor down one row. At the bottom margin it
// scrolls the region up by one and leaves curY unchanged. Otherwise
// it increments curY (clamped to Height-1). Used by LF/IND/NEL.
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

// scrollDownOnce moves all lines in the scroll region down by 1.
func (s *Screen) scrollDownOnce() {
	for y := s.scrollBottom; y > s.scrollTop; y-- {
		s.Cells[y] = s.Cells[y-1]
	}
	s.Cells[s.scrollTop] = makeRow(s.Width, s.style.BG)
}

// shiftLeft shifts all content in the scroll region left by n columns.
func (s *Screen) shiftLeft(n int) {
	for y := s.scrollTop; y <= s.scrollBottom; y++ {
		row := s.Cells[y]
		for x := range s.Width - n {
			row[x] = row[x+n]
		}
		for x := s.Width - n; x < s.Width; x++ {
			if x >= 0 {
				row[x] = Cell{Ch: ' '}
			}
		}
	}
}

// shiftRight shifts all content in the scroll region right by n columns.
func (s *Screen) shiftRight(n int) {
	for y := s.scrollTop; y <= s.scrollBottom; y++ {
		row := s.Cells[y]
		for x := s.Width - 1; x >= n; x-- {
			row[x] = row[x-n]
		}
		for x := 0; x < n && x < s.Width; x++ {
			row[x] = Cell{Ch: ' '}
		}
	}
}

// softReset performs a DECSTR — resets style, scroll region, cursor, and modes.
func (s *Screen) softReset() {
	s.style = Style{}
	s.scrollTop = 0
	s.scrollBottom = s.Height - 1
	s.savedY, s.savedX = 0, 0
	s.pendingWrap = false
	s.CursorHidden = false
	s.CursorStyle = 0
	s.BracketedPaste = false
	s.AppCursorKeys = false
	s.OriginMode = false
	s.AutoWrap = true
}
