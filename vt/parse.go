package vt

// VT500-style table-driven state machine parser. Processes raw PTY bytes one
// at a time via a [14][256]uint16 flat transition table. Maintains state across
// Write() calls so partial sequences split across reads are handled correctly.

func (s *Screen) feed(b byte) {
	// UTF-8 continuation handling (only in Ground state).
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
		// Invalid continuation — abort UTF-8 sequence, re-process byte
		s.utf8Len = 0
	}

	t := stateTable[s.pState][b]
	act := t.act()
	next := t.next()

	// CAN (0x18) and SUB (0x1A) abort sequences without firing exit actions.
	isCancelByte := b == 0x18 || b == 0x1A

	// Exit action of current state (if transitioning), but NOT on CAN/SUB.
	if next != s.pState && !isCancelByte {
		if ea := exitAction[s.pState]; ea != actNone {
			s.doAction(ea, b)
		}
	}

	// Transition action.
	if act != actNone {
		s.doAction(act, b)
	}

	// Transition and entry action.
	if next != s.pState {
		s.pState = next
		if ea := entryAction[next]; ea != actNone {
			s.doAction(ea, b)
		}
	}
}

//nolint:gocyclo // action dispatch is inherently branchy
func (s *Screen) doAction(act action, b byte) {
	switch act {
	case actPrint:
		s.handlePrint(b)
	case actExecute:
		s.execControl(b)
	case actClear:
		s.parserClear()
	case actCollect:
		if s.numInterm < maxIntermed {
			s.pIntermed[s.numInterm] = b
			s.numInterm++
		}
	case actParam:
		s.handleParam(b)
	case actSubparam:
		s.handleSubparam()
	case actMarker:
		s.privateMarker = b
	case actEscDispatch:
		s.dispatchEsc(b)
	case actCsiDispatch:
		s.finalizeParams()
		s.dispatchCSI(b)
	case actHook:
		s.finalizeParams()
		s.dcsHook(b)
	case actPut:
		s.dcsPut(b)
	case actUnhook:
		s.dcsUnhook()
	case actOscStart:
		s.oscBuf = s.oscBuf[:0]
	case actOscPut:
		if len(s.oscBuf) < maxOSCLen {
			s.oscBuf = append(s.oscBuf, b)
		}
	case actOscEnd:
		s.dispatchOsc()
	case actIgnore:
		// do nothing
	}
}

func (s *Screen) handlePrint(b byte) {
	switch {
	case b < 0x80:
		s.put(s.translateChar(b))
	case b >= 0x80 && b < 0xC0:
		// Orphan continuation byte or C1 range (0x80-0x9F) arriving in Ground
		// when not inside a multi-byte sequence — emit replacement character.
		s.put(0xFFFD)
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
		s.put(0xFFFD)
	}
}

func (s *Screen) handleParam(b byte) {
	if s.ignoring {
		return
	}
	if b == ';' {
		// Semicolon: finalize current param, start new group
		s.pushParam(true)
	} else {
		// Digit
		s.paramSeen = true
		v := min(uint32(s.curParam)*10+uint32(b-'0'), maxCSIArgValue)
		s.curParam = uint16(v) //nolint:gosec // v capped at 65535
	}
}

func (s *Screen) handleSubparam() {
	if s.ignoring {
		return
	}
	// Colon: finalize current param value as part of same group (subparam)
	s.pushParam(false)
}

// pushParam stores curParam in the flat array. newGroup=true means start a new
// semicolon-separated group after this value.
func (s *Screen) pushParam(newGroup bool) {
	if s.numParams >= maxParams {
		s.ignoring = true
		return
	}
	s.pParams[s.numParams] = s.curParam
	s.numParams++
	// Increment the length of the current group
	if s.numGroups > 0 {
		gStart := s.groupStartIdx(s.numGroups - 1)
		s.pGroupLen[gStart]++
	}
	s.curParam = 0
	s.paramSeen = false

	if newGroup {
		// Start a new group — next pushParam will be into it
		if s.numGroups < maxParams && s.numParams < maxParams {
			// The new group starts at numParams
			s.pGroupLen[s.numParams] = 0
			s.numGroups++
		}
	}
}

// finalizeParams pushes the last accumulated param (if any digits were seen or
// the param string was non-empty).
func (s *Screen) finalizeParams() {
	if s.ignoring {
		return
	}
	// Always push the trailing param (even if 0/default)
	if s.numParams >= maxParams {
		return
	}
	s.pParams[s.numParams] = s.curParam
	s.numParams++
	if s.numGroups > 0 {
		gStart := s.groupStartIdx(s.numGroups - 1)
		s.pGroupLen[gStart]++
	}
}

func (s *Screen) parserClear() {
	s.numParams = 0
	s.numGroups = 0
	s.curParam = 0
	s.paramSeen = false
	s.numInterm = 0
	s.ignoring = false
	s.privateMarker = 0
	// Initialize first group
	s.pGroupLen[0] = 0
	s.numGroups = 1
}

// groupStartIdx returns the index in pParams where group g starts.
func (s *Screen) groupStartIdx(g uint8) uint8 {
	var idx uint8
	for range g {
		idx += s.pGroupLen[s.groupStartForGroup(idx)]
	}
	return idx
}

// groupStartForGroup returns the pGroupLen index for group i (which stores its length).
// The first group's length is at pGroupLen[0], second at pGroupLen[sum of first group's len], etc.
func (s *Screen) groupStartForGroup(g uint8) uint8 {
	var idx uint8
	for range g {
		idx += s.pGroupLen[idx]
	}
	return idx
}

// ParamGroup represents one semicolon-separated parameter group.
type ParamGroup struct {
	Params [8]uint16 // Params[0] = main param; Params[1:Len] = subparams
	Len    uint8
}

// paramGroup returns the i-th semicolon-separated group (0-indexed).
func (s *Screen) paramGroup(i int) ParamGroup {
	var g ParamGroup
	if i >= int(s.numGroups) {
		return g
	}
	// Find start index of group i
	var startIdx uint8
	for range i {
		if int(startIdx) >= maxParams {
			return g
		}
		startIdx += s.pGroupLen[startIdx]
	}
	if int(startIdx) >= maxParams {
		return g
	}
	length := s.pGroupLen[startIdx]
	if length == 0 {
		return g
	}
	length = min(length, 8, uint8(maxParams-int(startIdx)))
	g.Len = length
	for j := range length {
		g.Params[j] = s.pParams[startIdx+j]
	}
	return g
}

// paramCount returns the number of semicolon-separated groups.
func (s *Screen) paramCount() int {
	return int(s.numGroups)
}

// paramVal returns the main value of the i-th group, or def if absent/zero.
func (s *Screen) paramVal(i, def int) int {
	g := s.paramGroup(i)
	if g.Len == 0 {
		return def
	}
	v := int(g.Params[0])
	if v == 0 && def > 0 {
		return def
	}
	if v > maxCSIArgValue {
		return maxCSIArgValue
	}
	return v
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
	case '\n', 0x0B, 0x0C: // LF, VT, FF
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
	// Handle ESC intermediates (charset designation)
	if s.numInterm > 0 {
		s.designateCharset(s.pIntermed[0], b)
		return
	}
	switch b {
	case '7':
		s.saveCursor()
	case '8':
		s.restoreCursor()
	case 'H': // HTS
		s.setTabStop(s.curX)
	case 'D': // IND
		s.pendingWrap = false
		s.lineDown()
	case 'E': // NEL
		s.pendingWrap = false
		s.curX = 0
		s.lineDown()
	case 'M': // RI
		if s.curY == s.scrollTop {
			s.scrollDownOnce()
		} else if s.curY > 0 {
			s.curY--
		}
	case '=': // DECKPAM
		s.AppKeypad = true
	case '>': // DECKPNM
		s.AppKeypad = false
	case 'N': // SS2
		s.singleShft = 2
	case 'O': // SS3
		s.singleShft = 3
	case 'c': // RIS
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
