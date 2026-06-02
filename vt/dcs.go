package vt

import "fmt"

// DCS (Device Control String) handlers.
// Supported:
//   - DECRQSS (DCS $ q <selector> ST) — Request Status String
//
// Routing: intermediate='$', final='q' → DECRQSS

// dcsHook is called on entry to DcsPassthrough. Routes to the appropriate handler.
func (s *Screen) dcsHook(finalByte byte) {
	switch {
	case s.numInterm == 1 && s.pIntermed[0] == '$' && finalByte == 'q':
		s.dcsFunc = dcsDecrqss
		s.dcsBuf = s.dcsBuf[:0]
	default:
		s.dcsFunc = dcsIgnored
	}
}

// dcsPut receives data bytes during DcsPassthrough.
func (s *Screen) dcsPut(b byte) {
	switch s.dcsFunc {
	case dcsDecrqss:
		if len(s.dcsBuf) < maxDCSLen {
			s.dcsBuf = append(s.dcsBuf, b)
		}
	case dcsIgnored:
		// Don't buffer unknown DCS data
	}
}

// dcsUnhook is called on exit from DcsPassthrough. Dispatches the completed DCS.
func (s *Screen) dcsUnhook() {
	if s.dcsFunc == dcsDecrqss {
		s.handleDecrqss(s.dcsBuf)
	}
	s.dcsFunc = dcsNone
}

// handleDecrqss processes a DECRQSS query and appends the response to s.Response.
// Valid response: DCS 1 $ r <data> ST
// Invalid response: DCS 0 $ r ST
func (s *Screen) handleDecrqss(query []byte) {
	q := string(query)
	switch q {
	case "m": // SGR
		data := sgrParamsString(s.style)
		s.Response = fmt.Appendf(s.Response, "\x1bP1$r%sm\x1b\\", data)
	case "r": // DECSTBM (scroll region)
		top := s.scrollTop + 1
		bottom := s.scrollBottom + 1
		s.Response = fmt.Appendf(s.Response, "\x1bP1$r%d;%dr\x1b\\", top, bottom)
	case " q": // DECSCUSR (cursor style)
		s.Response = fmt.Appendf(s.Response, "\x1bP1$r%d q\x1b\\", s.CursorStyle)
	default:
		// Unrecognized query
		s.Response = append(s.Response, "\x1bP0$r\x1b\\"...)
	}
}
