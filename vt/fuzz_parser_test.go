package vt

import "testing"

// FuzzParser exercises the parser table with arbitrary byte sequences.
// The invariant: no panic, no OOB, cursor stays in bounds.
func FuzzParser(f *testing.F) {
	// Seed corpus
	f.Add([]byte("\x1b[38:2:255:0:128m"))   // colon subparams
	f.Add([]byte("\x1b[38;2;100;150;200m")) // semicolon extended color
	f.Add([]byte("\x1bP$qm\x1b\\"))         // DECRQSS SGR
	f.Add([]byte("\x1bP$qr\x1b\\"))         // DECRQSS DECSTBM
	f.Add([]byte("\x1bP$q q\x1b\\"))        // DECRQSS DECSCUSR
	f.Add([]byte("\x1bP+q\x1b\\"))          // unknown DCS
	f.Add([]byte("\x90$q\x9c"))             // 8-bit DCS
	f.Add([]byte("\x98hello\x9c"))          // SOS
	f.Add([]byte("\x1b[38:2:255:0:255m"))   // colon RGB
	f.Add([]byte{0x9B, '3', '1', 'm'})      // 0x9B in Ground (C1 suppressed)
	f.Add([]byte("\x1b]2;title\x07"))       // OSC
	f.Add([]byte("\x1b]2;title\x1b\\"))     // OSC with ST
	f.Add([]byte("\x1b[?1049h\x1b[?1049l")) // alt screen enter/exit
	f.Add([]byte("\x1b[1;1H"))              // cursor positioning
	f.Add([]byte("\x1b[65535S"))            // large scroll
	f.Add([]byte("Hello\r\nWorld"))         // basic text
	f.Add([]byte("\xE6\xBC\xA2"))           // UTF-8 (漢)
	f.Add([]byte{0xE6, 'A'})                // invalid UTF-8

	f.Fuzz(func(t *testing.T, data []byte) {
		s := New(24, 80)
		s.Write(data)
		row, col := s.CursorPos()
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d OOB [0,%d)", row, s.Height)
		}
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d OOB [0,%d)", col, s.Width)
		}
	})
}
