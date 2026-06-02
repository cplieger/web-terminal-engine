package vt

// Buffer size limits to prevent unbounded memory growth from malformed
// or adversarial input streams missing terminators.
const (
	maxOSCLen      = 4096 // max bytes buffered for an OSC payload
	maxCSIParams   = 256  // max parameter bytes in a CSI sequence
	maxCSIIntermed = 16   // max intermediate bytes in a CSI sequence
)

// VT500-style state machine parser. Processes raw PTY bytes one at a time,
// maintaining state across Write() calls so partial sequences split across
// reads are handled correctly.

func (s *Screen) feed(b byte) {
	// CAN/SUB abort any sequence
	if b == 0x18 || b == 0x1A {
		s.pState = stateGround
		s.utf8Len = 0
		return
	}

	switch s.pState {
	case stateGround:
		s.feedGround(b)
	case stateEscape:
		s.feedEscape(b)
	case stateEscapeIntermediate:
		s.designateCharset(s.pEscIntermed, b)
		s.pState = stateGround
	case stateCsiEntry, stateCsiParam:
		s.feedCsi(b)
	case stateCsiIntermediate:
		s.feedCsiIntermediate(b)
	case stateOscString:
		s.feedOsc(b)
	case stateOscEsc:
		if b == '\\' {
			s.dispatchOsc()
			s.pState = stateGround
		} else {
			s.pState = stateEscape
			s.feedEscape(b)
		}
	}
}

func (s *Screen) feedGround(b byte) {
	if s.utf8Len > 0 {
		if b&0xC0 == 0x80 {
			s.utf8Buf[s.utf8Got] = b
			s.utf8Got++
			if s.utf8Got == s.utf8Len {
				r := decodeUTF8Bytes(s.utf8Buf, s.utf8Len)
				s.utf8Len = 0
				s.put(r)
			}
			return
		}
		s.utf8Len = 0
	}

	switch {
	case b == 0x1B:
		s.pState = stateEscape
	case b < 0x20:
		s.execControl(b)
	case b >= 0xC0 && b < 0xE0:
		s.utf8Buf[0] = b
		s.utf8Len = 2
		s.utf8Got = 1
	case b >= 0xE0 && b < 0xF0:
		s.utf8Buf[0] = b
		s.utf8Len = 3
		s.utf8Got = 1
	case b >= 0xF0 && b < 0xF8:
		s.utf8Buf[0] = b
		s.utf8Len = 4
		s.utf8Got = 1
	default:
		s.put(s.translateChar(b))
	}
}

func (s *Screen) feedEscape(b byte) {
	switch {
	case b == '[':
		s.pState = stateCsiEntry
		s.pParams = s.pParams[:0]
		s.pIntermed = s.pIntermed[:0]
	case b == ']' || b == 'P' || b == '^' || b == '_':
		s.pState = stateOscString
		s.oscBuf = s.oscBuf[:0]
	case b == '(' || b == ')' || b == '*' || b == '+' || b == '#':
		s.pEscIntermed = b
		s.pState = stateEscapeIntermediate
	case b == '=' || b == '>' || b == '7' || b == '8':
		// DECKPAM (ESC =), DECKPNM (ESC >), DECSC (ESC 7), DECRC (ESC 8)
		// — final bytes below 0x40 that are valid ESC sequence terminators.
		s.dispatchEsc(b)
		s.pState = stateGround
	case b >= 0x40 && b <= 0x7E:
		s.dispatchEsc(b)
		s.pState = stateGround
	case b == 0x1B:
		// Repeated ESC — stay in escape state
	default:
		s.pState = stateGround
	}
}

func (s *Screen) feedCsi(b byte) {
	switch {
	case b == 0x1B:
		s.pState = stateEscape
	case b >= 0x30 && b <= 0x3F:
		// Cap parameter bytes to prevent unbounded memory growth.
		if len(s.pParams) < maxCSIParams {
			s.pParams = append(s.pParams, b)
		}
		s.pState = stateCsiParam
	case b >= 0x20 && b <= 0x2F:
		if len(s.pIntermed) < maxCSIIntermed {
			s.pIntermed = append(s.pIntermed, b)
		}
		s.pState = stateCsiIntermediate
	case b >= 0x40 && b <= 0x7E:
		s.dispatchCSI(b)
		s.pState = stateGround
	}
}

func (s *Screen) feedCsiIntermediate(b byte) {
	switch {
	case b == 0x1B:
		s.pState = stateEscape
	case b >= 0x20 && b <= 0x2F:
		if len(s.pIntermed) < maxCSIIntermed {
			s.pIntermed = append(s.pIntermed, b)
		}
	case b >= 0x40 && b <= 0x7E:
		s.dispatchCSI(b)
		s.pState = stateGround
	default:
		s.pState = stateGround
	}
}

func (s *Screen) feedOsc(b byte) {
	switch b {
	case 0x07: // BEL terminates OSC
		s.dispatchOsc()
		s.pState = stateGround
	case 0x1B:
		s.pState = stateOscEsc
	default:
		// Cap OSC buffer to prevent unbounded memory growth from a
		// missing terminator. 4096 bytes is generous for any real OSC
		// payload (titles, hyperlinks, etc.).
		if len(s.oscBuf) < maxOSCLen {
			s.oscBuf = append(s.oscBuf, b)
		}
	}
}

func (s *Screen) execControl(b byte) {
	switch b {
	case 0x07:
		s.BellRing = true
	case '\b':
		s.pendingWrap = false
		if s.curX > 0 {
			s.curX--
		}
	case '\n', 0x0B, 0x0C: // LF, VT, FF — all treated as line feed per xterm spec
		s.pendingWrap = false
		s.lineDown()
	case '\r':
		s.curX = 0
		s.pendingWrap = false
	case '\t':
		s.pendingWrap = false
		s.curX = s.nextTabStop(s.curX)
		if s.curX >= s.Width {
			s.curX = s.Width - 1
		}
	case 0x0E: // SO — Shift Out: activate G1 in GL
		s.gl = 1
	case 0x0F: // SI — Shift In: activate G0 in GL
		s.gl = 0
	}
}

func (s *Screen) dispatchEsc(b byte) {
	switch b {
	case '7':
		s.saveCursor()
	case '8':
		s.restoreCursor()
	case 'H': // HTS — Horizontal Tab Set: set tab stop at current column
		s.setTabStop(s.curX)
	case 'D': // IND — Index: move cursor down, scroll if at bottom margin
		s.pendingWrap = false
		s.lineDown()
	case 'E': // NEL — Next Line: CR + LF
		s.pendingWrap = false
		s.curX = 0
		s.lineDown()
	case 'M': // RI — Reverse Index
		if s.curY == s.scrollTop {
			s.scrollDownOnce()
		} else if s.curY > 0 {
			s.curY--
		}
	case '=': // DECKPAM — Application Keypad Mode
		s.AppKeypad = true
	case '>': // DECKPNM — Normal Keypad Mode
		s.AppKeypad = false
	case 'N': // SS2 — Single Shift G2
		s.singleShft = 2
	case 'O': // SS3 — Single Shift G3
		s.singleShft = 3
	case 'c': // RIS — Full Reset
		s.softReset()
		s.eraseRegion(0, 0, s.Height-1, s.Width-1)
		s.Drained = nil
		s.InAltScreen = false
		s.savedMainCells = nil
		s.BracketedPaste = false
		s.AppCursorKeys = false
		s.AppKeypad = false
		s.CursorHidden = false
		s.CursorStyle = 0
		s.ReverseVideo = false
		s.tabStops = nil
	}
}

func decodeUTF8Bytes(buf [4]byte, n uint8) rune {
	switch n {
	case 2:
		return rune(buf[0]&0x1F)<<6 | rune(buf[1]&0x3F)
	case 3:
		return rune(buf[0]&0x0F)<<12 | rune(buf[1]&0x3F)<<6 | rune(buf[2]&0x3F)
	case 4:
		return rune(buf[0]&0x07)<<18 | rune(buf[1]&0x3F)<<12 | rune(buf[2]&0x3F)<<6 | rune(buf[3]&0x3F)
	}
	return '?'
}
