package vt

import "testing"

// FuzzParser feeds arbitrary byte sequences through the full VT parser. The
// invariant: the parser never panics or accesses out of bounds, and the cursor
// always stays within [0,Width) x [0,Height).
func FuzzParser(f *testing.F) {
	seeds := [][]byte{
		// Plain text and basic sequences.
		{},
		[]byte("hello world"),
		[]byte("Hello\r\nWorld"),
		[]byte("\x1b[1;31mred\x1b[0m"),
		[]byte("\x1b[H\x1b[2J"),
		[]byte("\x1b[1;1H"),              // cursor positioning
		[]byte("\x1b[65535S"),            // large scroll
		[]byte("\x1b[?1049h\x1b[?1049l"), // alt screen enter/exit
		// Color / SGR forms.
		[]byte("\x1b[38:2:255:0:128m"),   // colon subparams
		[]byte("\x1b[38;2;100;150;200m"), // semicolon extended color
		[]byte("\x1b[38:2:255:0:255m"),   // colon RGB
		[]byte("\x1b[38;2;255;128;0mrgb\x1b[0m"),
		// DCS / DECRQSS.
		[]byte("\x1bP$qm\x1b\\"),              // DECRQSS SGR
		[]byte("\x1bP$qr\x1b\\"),              // DECRQSS DECSTBM
		[]byte("\x1bP$q q\x1b\\"),             // DECRQSS DECSCUSR
		[]byte("\x1bP+q\x1b\\"),               // unknown DCS
		[]byte("\x90$q\x9c"),                  // 8-bit DCS
		[]byte("\x98hello\x9c"),               // SOS
		[]byte("\x1bP$q\x1bP$qm\x1b\\\x1b\\"), // DCS inside DCS
		// OSC.
		[]byte("\x1b]2;title\x07"),   // OSC with BEL
		[]byte("\x1b]2;title\x1b\\"), // OSC with ST
		// C1 and control bytes.
		{0x9B, '3', '1', 'm'}, // 0x9B in Ground (C1 suppressed)
		{0x80, 0x81, 0x82, 0x90, 0x9B, 0x9C, 0x9D, 0x9E, 0x9F}, // all C1 bytes
		[]byte("\x1b\x1b\x1b\x1b[[[[[1;1H"),                    // nested ESC
		[]byte("\x1b[\x1b]\x1bP\x1b\\\x1b[\x18\x1a\x1b[m"),     // rapid state transitions
		// UTF-8: valid, multi-byte, and adversarial.
		[]byte("\xE6\xBC\xA2"),                         // 漢
		[]byte("\xc3\xa9\xe2\x9c\x93\xf0\x9f\x98\x80"), // multi-byte mix
		{0xE6, 'A'},              // invalid continuation
		{0xF4, 0x8F, 0xBF, 0xBF}, // U+10FFFF
		{0xED, 0xA0, 0x80},       // surrogate (invalid)
		{0xC0, 0x80},             // overlong NUL
		{0xFE, 0xFF},             // invalid UTF-8 bytes
		{0xE6, 0x18, 0xBC, 0xA2}, // CAN interrupting a wide-char UTF-8 sequence
		// Many params, mixing colons and semicolons.
		[]byte("\x1b[1:2:3;4:5:6;7:8:9;10;11;12;13;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34;35m"),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	// All 256 byte values in one stream.
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	f.Add(all)

	// OSC payload containing every byte value.
	f.Add(append([]byte("\x1b]2;"), all...))

	f.Fuzz(func(t *testing.T, data []byte) {
		s := New(24, 80)
		s.Write(data)
		row, col := s.CursorPos()
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d out of bounds [0,%d)", row, s.Height)
		}
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d out of bounds [0,%d)", col, s.Width)
		}
		// Grid structural invariant: the buffer always holds exactly Height
		// rows of exactly Width cells. A parser bug in scroll / alt-screen /
		// erase handling can corrupt the grid without tripping the cursor check.
		if len(s.Cells) != s.Height {
			t.Fatalf("len(Cells) = %d, want Height %d", len(s.Cells), s.Height)
		}
		for y := range s.Cells {
			if len(s.Cells[y]) != s.Width {
				t.Fatalf("len(Cells[%d]) = %d, want Width %d", y, len(s.Cells[y]), s.Width)
			}
		}
		// Buffer-bounds invariant: OSC/DCS accumulation never exceeds its cap.
		if len(s.oscBuf) > maxOSCLen {
			t.Fatalf("oscBuf = %d bytes, want <= %d", len(s.oscBuf), maxOSCLen)
		}
		if len(s.dcsBuf) > maxDCSLen {
			t.Fatalf("dcsBuf = %d bytes, want <= %d", len(s.dcsBuf), maxDCSLen)
		}
	})
}

// FuzzScreenWriteResize feeds arbitrary bytes through the parser with a resize
// interleaved partway through. Invariant: the cursor stays in bounds across the
// resize.
func FuzzScreenWriteResize(f *testing.F) {
	f.Add([]byte("\x1b[?1049h\x1b[2J\x1b[H漢字テスト\x1b[?1049l"), uint8(10), uint8(20))
	f.Add([]byte("\x1b[5S\x1b[5T\x1b[10L\x1b[10M"), uint8(3), uint8(5))
	f.Add([]byte("\x1b[38;2;255;0;0m\xf0\x9f\x98\x80\x1b[0m"), uint8(1), uint8(2))

	f.Fuzz(func(t *testing.T, data []byte, rows, cols uint8) {
		r := int(rows)%50 + 1
		c := int(cols)%100 + 1
		s := New(24, 80)
		mid := len(data) / 2
		s.Write(data[:mid])
		s.Resize(r, c)
		s.Write(data[mid:])
		row, col := s.CursorPos()
		if row < 0 || row >= s.Height {
			t.Errorf("cursor row %d out of bounds [0, %d)", row, s.Height)
		}
		if col < 0 || col >= s.Width {
			t.Errorf("cursor col %d out of bounds [0, %d)", col, s.Width)
		}
	})
}
