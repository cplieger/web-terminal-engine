package vt

// --- Tab stop management ---

// initTabStops lazily initializes the tab stop array with default stops every 8 columns.
func (s *Screen) initTabStops() {
	s.tabStops = make([]bool, s.Width)
	for i := 8; i < s.Width; i += 8 {
		s.tabStops[i] = true
	}
}

// setTabStop sets a tab stop at the given column.
func (s *Screen) setTabStop(col int) {
	if s.tabStops == nil {
		s.initTabStops()
	}
	if col >= 0 && col < len(s.tabStops) {
		s.tabStops[col] = true
	}
}

// clearTabStop clears the tab stop at the given column.
func (s *Screen) clearTabStop(col int) {
	if s.tabStops == nil {
		s.initTabStops()
	}
	if col >= 0 && col < len(s.tabStops) {
		s.tabStops[col] = false
	}
}

// clearAllTabStops removes all tab stops.
func (s *Screen) clearAllTabStops() {
	s.tabStops = make([]bool, s.Width)
}

// nextTabStop returns the next tab stop column after col.
// If no tab stops are set, uses default every-8 behavior.
func (s *Screen) nextTabStop(col int) int {
	if s.tabStops == nil {
		// Default: every 8 columns
		return (col + 8) &^ 7
	}
	for i := col + 1; i < s.Width; i++ {
		if s.tabStops[i] {
			return i
		}
	}
	return s.Width
}

// prevTabStop returns the previous tab stop column before col.
func (s *Screen) prevTabStop(col int) int {
	if s.tabStops == nil {
		// Default tab stops every 8 columns, floored at column 0:
		// prevTabStop(0) would otherwise compute (-1 &^ 7) == -8, a negative
		// cursor column that only a downstream clamp currently saves.
		return max((col-1)&^7, 0)
	}
	for i := col - 1; i >= 0; i-- {
		if s.tabStops[i] {
			return i
		}
	}
	return 0
}

// --- DECSC / DECRC cursor save/restore ---

// saveCursor saves the full cursor state per DECSC spec:
// position, style, origin mode, autowrap, and active charset.
func (s *Screen) saveCursor() {
	s.savedY = s.curY
	s.savedX = s.curX
	s.savedStyle = s.style
	s.savedOrigin = s.OriginMode
	s.savedAutoWrap = s.AutoWrap
	s.savedCharsets = s.gsets
	s.savedGL = s.gl
	s.cursorStateSaved = true
}

// restoreCursor restores the full cursor state per DECRC spec.
func (s *Screen) restoreCursor() {
	if !s.cursorStateSaved {
		// No saved state — move to home per xterm behavior.
		s.curY, s.curX = 0, 0
		s.pendingWrap = false
		return
	}
	s.curY = s.savedY
	s.curX = s.savedX
	s.style = s.savedStyle
	s.OriginMode = s.savedOrigin
	s.AutoWrap = s.savedAutoWrap
	s.gsets = s.savedCharsets
	s.gl = s.savedGL
	s.pendingWrap = false
	if s.curY >= s.Height {
		s.curY = s.Height - 1
	}
	if s.curX >= s.Width {
		s.curX = s.Width - 1
	}
}

// --- DECRQM (Request Mode) → DECRPM (Report Mode) ---

// decModeStatus returns the DECRPM Ps value for a DEC private mode.
// 1=set, 2=reset, 0=not recognized.
func (s *Screen) decModeStatus(mode int) int {
	switch mode {
	case 1: // DECCKM
		return boolToModeStatus(s.AppCursorKeys)
	case 5: // DECSCNM
		return boolToModeStatus(s.ReverseVideo)
	case 6: // DECOM
		return boolToModeStatus(s.OriginMode)
	case 7: // DECAWM
		return boolToModeStatus(s.AutoWrap)
	case 12: // Cursor blink (att610)
		return boolToModeStatus(s.CursorBlink)
	case 25: // DECTCEM
		return boolToModeStatus(!s.CursorHidden)
	case 47, 1047, 1049: // Alt screen
		return boolToModeStatus(s.InAltScreen)
	case 1000, 1002, 1003: // Mouse modes
		return boolToModeStatus(s.MouseMode == uint16(mode))
	case 1004: // Focus reporting
		return boolToModeStatus(s.FocusReporting)
	case 1006: // SGR mouse
		return boolToModeStatus(s.MouseSGR)
	case 2004: // Bracketed paste
		return boolToModeStatus(s.BracketedPaste)
	case 2026: // Synchronized output
		return boolToModeStatus(s.IsFlushHeld())
	default:
		return 0 // not recognized
	}
}

// ansiModeStatus returns the DECRPM Ps value for an ANSI mode.
func (s *Screen) ansiModeStatus(mode int) int {
	switch mode {
	case 4: // IRM (insert/replace) — we don't track IRM, always replace
		return 2 // reset
	case 20: // LNM (line feed/new line) — always reset (LF doesn't imply CR)
		return 2
	default:
		return 0 // not recognized
	}
}

func boolToModeStatus(v bool) int {
	if v {
		return 1
	}
	return 2
}
