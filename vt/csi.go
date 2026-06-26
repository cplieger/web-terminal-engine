package vt

import (
	"fmt"
	"log/slog"
	"time"
)

//nolint:gocyclo,gocognit // CSI dispatch is inherently complex
func (s *Screen) dispatchCSI(final byte) {
	// Any cursor-affecting CSI clears pending wrap.
	switch final {
	case 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'f', 'd', 'e', '`', 'a', 'I', 'Z', 'u':
		s.pendingWrap = false
	}

	// SP-prefixed sequences (intermediate byte 0x20).
	if s.numInterm > 0 && s.pIntermed[0] == ' ' {
		switch final {
		case '@': // SL
			n := s.paramVal(0, 1)
			s.shiftLeft(n)
		case 'A': // SR
			n := s.paramVal(0, 1)
			s.shiftRight(n)
		case 'q': // DECSCUSR
			v := s.paramVal(0, 0)
			if v <= 6 {
				s.CursorStyle = uint8(v) // #nosec G115 -- v bounded [0,6]
				s.CursorBlink = v == 0 || v%2 == 1
			}
		default:
			slog.Info("vt: unhandled CSI SP", "cmd", string(final), "args", s.paramVal(0, 0))
		}
		return
	}

	// '!' intermediate — DECSTR (soft terminal reset).
	if s.numInterm > 0 && s.pIntermed[0] == '!' {
		if final == 'p' {
			s.softReset()
		}
		return
	}

	// '$' intermediate — DECRQM (Request Mode).
	if s.numInterm > 0 && s.pIntermed[0] == '$' {
		if final == 'p' {
			s.handleDECRQM()
		}
		return
	}

	switch final {
	case 'A':
		n := s.paramVal(0, 1)
		s.curY -= n
		if s.curY < 0 {
			s.curY = 0
		}
	case 'B':
		n := s.paramVal(0, 1)
		s.curY += n
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'C':
		n := s.paramVal(0, 1)
		s.curX += n
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 'D':
		n := s.paramVal(0, 1)
		s.curX -= n
		if s.curX < 0 {
			s.curX = 0
		}
	case 'E':
		n := s.paramVal(0, 1)
		s.curY += n
		s.curX = 0
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'F':
		n := s.paramVal(0, 1)
		s.curY -= n
		s.curX = 0
		if s.curY < 0 {
			s.curY = 0
		}
	case 'G':
		n := s.paramVal(0, 1)
		s.curX = max(n-1, 0)
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 'H', 'f':
		y := s.paramVal(0, 1) - 1
		x := s.paramVal(1, 1) - 1
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
		d := s.paramVal(0, 0)
		switch d {
		case 0:
			s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
			s.eraseRegion(s.curY+1, 0, s.Height-1, s.Width-1)
		case 1:
			s.eraseRegion(0, 0, s.curY-1, s.Width-1)
			s.eraseRegion(s.curY, 0, s.curY, s.curX)
		case 2:
			s.eraseRegion(0, 0, s.Height-1, s.Width-1)
			s.Drained = nil
		case 3:
			s.eraseRegion(0, 0, s.Height-1, s.Width-1)
			s.Drained = nil
		}
	case 'K':
		d := s.paramVal(0, 0)
		switch d {
		case 0:
			s.eraseRegion(s.curY, s.curX, s.curY, s.Width-1)
		case 1:
			s.eraseRegion(s.curY, 0, s.curY, s.curX)
		case 2:
			s.eraseRegion(s.curY, 0, s.curY, s.Width-1)
		}
	case '@': // ICH
		n := s.paramVal(0, 1)
		s.insertChars(n)
	case 'L': // IL
		n := s.paramVal(0, 1)
		s.insertLines(n)
	case 'M': // DL
		n := s.paramVal(0, 1)
		s.deleteLines(n)
	case 'P': // DCH
		n := s.paramVal(0, 1)
		s.deleteChars(n)
	case 'S': // SU
		n := s.paramVal(0, 1)
		regionH := s.scrollBottom - s.scrollTop + 1
		n = min(n, regionH)
		for range n {
			s.scrollUpOnce()
		}
	case 'T': // SD
		n := s.paramVal(0, 1)
		regionH := s.scrollBottom - s.scrollTop + 1
		n = min(n, regionH)
		for range n {
			s.scrollDownOnce()
		}
	case '^': // SD alternate
		n := s.paramVal(0, 1)
		regionH := s.scrollBottom - s.scrollTop + 1
		n = min(n, regionH)
		for range n {
			s.scrollDownOnce()
		}
	case 'X': // ECH
		n := s.paramVal(0, 1)
		end := s.curX + n - 1
		if end >= s.Width {
			end = s.Width - 1
		}
		s.eraseRegion(s.curY, s.curX, s.curY, end)
	case 'I': // CHT
		n := s.paramVal(0, 1)
		for range n {
			s.curX = s.nextTabStop(s.curX)
			if s.curX >= s.Width {
				s.curX = s.Width - 1
				break
			}
		}
	case 'Z': // CBT
		n := s.paramVal(0, 1)
		for range n {
			s.curX = s.prevTabStop(s.curX)
			if s.curX <= 0 {
				s.curX = 0
				break
			}
		}
	case '`', 'a': // HPA / HPR
		n := s.paramVal(0, 1)
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
	case 'd': // VPA
		n := s.paramVal(0, 1)
		s.curY = max(n-1, 0)
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'e': // VPR
		n := s.paramVal(0, 1)
		s.curY += n
		if s.curY >= s.Height {
			s.curY = s.Height - 1
		}
	case 'b': // REP
		n := s.paramVal(0, 1)
		if s.lastPrintedRune != 0 {
			saved := s.style
			s.style = s.lastPrintedStyle
			for range n {
				s.put(s.lastPrintedRune)
			}
			s.style = saved
		}
	case 'c': // Device Attributes
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
	case 'g': // TBC
		n := s.paramVal(0, 0)
		switch n {
		case 0:
			s.clearTabStop(s.curX)
		case 3:
			s.clearAllTabStops()
		}
	case 'r': // DECSTBM
		top := max(s.paramVal(0, 1)-1, 0)
		bottom := min(s.paramVal(1, s.Height)-1, s.Height-1)
		if top < bottom {
			s.scrollTop = top
			s.scrollBottom = bottom
		}
		s.curY, s.curX = 0, 0
	case 'm':
		s.applySGR()
	case 's':
		s.saveCursor()
	case 'u':
		s.restoreCursor()
	case 'h':
		s.applyModes(true)
	case 'l':
		s.applyModes(false)
	case 't': // Window manipulation
		n := s.paramVal(0, 0)
		if n == 18 {
			s.Response = fmt.Appendf(s.Response, "\x1b[8;%d;%dt", s.Height, s.Width)
		}
	case 'n': // DSR
		if s.privateMarker == '?' {
			n := s.paramVal(0, 0)
			if n == 6 {
				s.Response = fmt.Appendf(s.Response, "\x1b[?%d;%dR", s.curY+1, s.curX+1)
			}
		} else {
			n := s.paramVal(0, 0)
			switch n {
			case 5:
				s.Response = append(s.Response, "\x1b[0n"...)
			case 6:
				s.Response = fmt.Appendf(s.Response, "\x1b[%d;%dR", s.curY+1, s.curX+1)
			}
		}
	default:
		if final != 0 {
			slog.Info("vt: unhandled CSI", "cmd", string(final), "marker", s.privateMarker)
		}
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
