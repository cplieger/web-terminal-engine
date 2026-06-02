// OSC (Operating System Command) dispatch.
//
// Supported:
//   - OSC 0 ; Pt BEL/ST — Set icon name and window title to Pt
//   - OSC 1 ; Pt BEL/ST — Set icon name to Pt (treated same as title)
//   - OSC 2 ; Pt BEL/ST — Set window title to Pt
//   - OSC 8 ; params ; URI BEL/ST — Set/clear hyperlink (URI empty = clear)
//
// Out-of-scope (buffered then ignored):
//   - OSC 4  (Change Color Number)
//   - OSC 7  (Current directory)
//   - OSC 10 (Set foreground color)
//   - OSC 11 (Set background color)
//   - OSC 52 (Clipboard manipulation)
//
// The OSC payload format is: <numeric-id> ; <string-data>
// The numeric prefix is parsed as decimal digits up to the first ';'.
package vt

import "strings"

// dispatchOsc processes the buffered OSC payload and resets the buffer.
func (s *Screen) dispatchOsc() {
	payload := s.oscBuf
	s.oscBuf = s.oscBuf[:0]

	if len(payload) == 0 {
		return
	}

	// Parse numeric prefix (digits before first ';').
	var id int
	i := 0
	for i < len(payload) && payload[i] >= '0' && payload[i] <= '9' {
		id = id*10 + int(payload[i]-'0')
		i++
	}

	// Skip the ';' separator if present.
	var data string
	if i < len(payload) && payload[i] == ';' {
		data = string(payload[i+1:])
	}

	switch id {
	case 0, 1, 2:
		// OSC 0: set icon name + title; OSC 1: icon name; OSC 2: title.
		// We treat all three as setting the title (icon name is not
		// separately exposed — matches xterm.js behavior).
		s.Title = data
	case 8:
		// OSC 8 ; params ; URI — set/clear hyperlink.
		// Format: "params;URI" where params is key=value pairs separated
		// by ':' (the 'id=' param is parsed but not used). Empty URI clears.
		s.handleOsc8(data)
	default:
		// Unknown/out-of-scope OSC: consumed and ignored.
	}
}

// handleOsc8 processes the OSC 8 payload (after "8;").
// Format: params;URI — params may contain id=... separated by ':'.
// Empty URI closes the current hyperlink.
func (s *Screen) handleOsc8(data string) {
	// Split on first ';' to separate params from URI.
	_, uri, ok := strings.Cut(data, ";")
	if !ok {
		// Malformed: no second semicolon. Ignore.
		return
	}
	// Empty URI closes the hyperlink; non-empty sets it.
	s.hyperlink = uri
}
