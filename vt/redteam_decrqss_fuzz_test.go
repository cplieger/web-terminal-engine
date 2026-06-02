package vt

import (
	"strings"
	"testing"
)

// =============================================================
// PROBE 4: DECRQSS — exact responses
// =============================================================

func TestDECRQSS_SGR_Default(t *testing.T) {
	s := New(24, 80)
	// Default style → SGR 0
	s.Write([]byte("\x1bP$qm\x1b\\"))
	want := "\x1bP1$r0m\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS SGR default: got %q, want %q", s.Response, want)
	}
}

func TestDECRQSS_SGR_Complex(t *testing.T) {
	s := New(24, 80)
	// Set bold + italic + 256-color FG (color 42) + RGB BG (10,20,30)
	s.Write([]byte("\x1b[1;3;38;5;42;48;2;10;20;30m"))
	s.Response = nil
	s.Write([]byte("\x1bP$qm\x1b\\"))
	got := string(s.Response)
	// Should contain valid response indicator "1$r"
	if !strings.HasPrefix(got, "\x1bP1$r") {
		t.Fatalf("DECRQSS SGR complex: bad prefix: %q", got)
	}
	if !strings.HasSuffix(got, "m\x1b\\") {
		t.Fatalf("DECRQSS SGR complex: bad suffix: %q", got)
	}
	// Check the params contain expected attributes
	params := got[len("\x1bP1$r") : len(got)-len("m\x1b\\")]
	if !strings.Contains(params, "1") {
		t.Errorf("missing bold in response: %q", params)
	}
	if !strings.Contains(params, "3") {
		t.Errorf("missing italic in response: %q", params)
	}
}

func TestDECRQSS_DECSTBM_Default(t *testing.T) {
	s := New(24, 80)
	// Default scroll region: 1;24
	s.Write([]byte("\x1bP$qr\x1b\\"))
	want := "\x1bP1$r1;24r\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS DECSTBM default: got %q, want %q", s.Response, want)
	}
}

func TestDECRQSS_DECSCUSR_Default(t *testing.T) {
	s := New(24, 80)
	// Default cursor style is 0
	s.Write([]byte("\x1bP$q q\x1b\\"))
	want := "\x1bP1$r0 q\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS DECSCUSR default: got %q, want %q", s.Response, want)
	}
}

func TestDECRQSS_Unsupported_InvalidResponse(t *testing.T) {
	// Various unsupported queries should all return invalid (0$r)
	queries := []string{"x", "\"p", "\"q", "$}", "HELLO", ""}
	for _, q := range queries {
		s := New(24, 80)
		s.Write([]byte("\x1bP$q" + q + "\x1b\\"))
		want := "\x1bP0$r\x1b\\"
		if string(s.Response) != want {
			t.Errorf("DECRQSS unsupported %q: got %q, want %q", q, s.Response, want)
		}
	}
}

// =============================================================
// PROBE 5: DCS per-handler bounds
// =============================================================

func TestDCS_DECRQSS_BoundedAt256(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$q")) // start DECRQSS
	// Feed 512 bytes of data
	chunk := make([]byte, 512)
	for i := range chunk {
		chunk[i] = byte('A' + i%26)
	}
	s.Write(chunk)
	if len(s.dcsBuf) > maxDCSLen {
		t.Errorf("DECRQSS buffer: %d > %d", len(s.dcsBuf), maxDCSLen)
	}
	// Terminate and check invalid response (query was garbage)
	s.Write([]byte("\x1b\\"))
	if !strings.HasPrefix(string(s.Response), "\x1bP0$r") {
		t.Errorf("oversized DECRQSS should still produce invalid response: %q", s.Response)
	}
}

func TestDCS_UnknownHandler_ZeroBuffered(t *testing.T) {
	s := New(24, 80)
	// Unknown DCS: ESC P + z (final byte, no recognized intermediate)
	s.Write([]byte("\x1bPz"))
	// Feed data
	s.Write([]byte("lots of data that should not be buffered at all"))
	if len(s.dcsBuf) != 0 {
		t.Errorf("unknown DCS buffered %d bytes, want 0", len(s.dcsBuf))
	}
	// Terminate
	s.Write([]byte("\x1b\\"))
	if s.pState != stGround {
		t.Errorf("not in ground after unknown DCS ST: state=%d", s.pState)
	}
}

func TestDCS_8bitST_Terminates(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS for SGR
	// Use 8-bit ST (0x9C) to terminate
	s.Write([]byte{0x9C})
	if s.pState != stGround {
		t.Errorf("8-bit ST(0x9C) did not terminate DCS: state=%d", s.pState)
	}
	// Should have produced a valid response
	if len(s.Response) == 0 {
		t.Error("DECRQSS with 8-bit ST produced no response")
	}
}

// =============================================================
// PROBE 6: Fuzz — random streams (run via `go test -fuzz`)
// Additional seeds for adversarial coverage
// =============================================================

func TestFuzzSeeds_NoPanic(t *testing.T) {
	// Manually run dangerous sequences that target edge cases
	seeds := [][]byte{
		// All C1 bytes in sequence
		{0x80, 0x81, 0x82, 0x90, 0x9B, 0x9C, 0x9D, 0x9E, 0x9F},
		// Nested ESC sequences
		[]byte("\x1b\x1b\x1b\x1b[[[[[1;1H"),
		// DCS inside DCS attempt
		[]byte("\x1bP$q\x1bP$qm\x1b\\\x1b\\"),
		// OSC with every byte value 0-255
		append([]byte("\x1b]2;"), func() []byte {
			b := make([]byte, 256)
			for i := range b {
				b[i] = byte(i)
			}
			return b
		}()...),
		// Max params with colons and semicolons mixed
		[]byte("\x1b[1:2:3;4:5:6;7:8:9;10;11;12;13;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34;35m"),
		// Param overflow then valid sequence
		func() []byte {
			var b []byte
			b = append(b, "\x1b["...)
			for i := range 100 {
				if i > 0 {
					b = append(b, ';')
				}
				b = append(b, '9', '9', '9', '9', '9')
			}
			b = append(b, "H\x1b[1;1H"...)
			return b
		}(),
		// Rapid state transitions
		[]byte("\x1b[\x1b]\x1bP\x1b\\\x1b[\x18\x1a\x1b[m"),
		// UTF-8 edge cases
		{0xF4, 0x8F, 0xBF, 0xBF}, // U+10FFFF
		{0xED, 0xA0, 0x80},       // surrogate (invalid)
		{0xC0, 0x80},             // overlong NUL
		{0xFE, 0xFF},             // invalid UTF-8 bytes
		// Wide chars + CAN interrupting UTF-8
		{0xE6, 0x18, 0xBC, 0xA2},
	}

	for i, seed := range seeds {
		s := New(24, 80)
		s.Write(seed)
		row, col := s.CursorPos()
		if row < 0 || row >= s.Height {
			t.Fatalf("seed %d: row %d OOB [0,%d)", i, row, s.Height)
		}
		if col < 0 || col >= s.Width {
			t.Fatalf("seed %d: col %d OOB [0,%d)", i, col, s.Width)
		}
	}
}

func TestFuzz_LargeRandomStream_NoPanic(t *testing.T) {
	// Deterministic pseudo-random stream to catch state machine issues
	s := New(24, 80)
	data := make([]byte, 10000)
	seed := uint32(0xDEADBEEF)
	for i := range data {
		seed = seed*1103515245 + 12345
		data[i] = byte(seed >> 16)
	}
	s.Write(data)
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height {
		t.Fatalf("random stream: row %d OOB", row)
	}
	if col < 0 || col >= s.Width {
		t.Fatalf("random stream: col %d OOB", col)
	}
}

func TestFuzz_RepeatedCSIOverflow(t *testing.T) {
	// Repeated CSI sequences with param overflow to detect memory growth
	s := New(24, 80)
	heavy := []byte("\x1b[1;2;3;4;5;6;7;8;9;10;11;12;13;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34;35m")
	for range 1000 {
		s.Write(heavy)
	}
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatalf("cursor OOB after repeated overflow: row=%d col=%d", row, col)
	}
}

// =============================================================
// PROBE 7: Pre-existing Screen behavior preserved
// =============================================================

func TestScreen_CursorMovement_Preserved(t *testing.T) {
	s := New(10, 20)
	s.Write([]byte("\x1b[5;10H"))
	row, col := s.CursorPos()
	if row != 4 || col != 9 {
		t.Errorf("CUP 5;10: got %d,%d want 4,9", row, col)
	}
	s.Write([]byte("\x1b[A")) // up
	row, _ = s.CursorPos()
	if row != 3 {
		t.Errorf("CUU: row=%d want 3", row)
	}
	s.Write([]byte("\x1b[B")) // down
	row, _ = s.CursorPos()
	if row != 4 {
		t.Errorf("CUD: row=%d want 4", row)
	}
}

func TestScreen_ScrollRegion_Preserved(t *testing.T) {
	s := New(10, 20)
	s.Write([]byte("\x1b[3;8r")) // set scroll region rows 3-8
	if s.scrollTop != 2 || s.scrollBottom != 7 {
		t.Errorf("scroll region: top=%d bot=%d, want 2,7", s.scrollTop, s.scrollBottom)
	}
}

func TestScreen_SGR_BasicPreserved(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("\x1b[1;31mA"))
	cell := s.Cells[0][0]
	if !cell.Style.Bold {
		t.Error("bold not set")
	}
	if cell.Style.FG.Type != 1 || cell.Style.FG.Val != 1 {
		t.Errorf("FG: got %+v, want red(type=1,val=1)", cell.Style.FG)
	}
}

func TestScreen_AltScreen_Preserved(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("Main"))
	s.Write([]byte("\x1b[?1049h")) // enter alt
	if !s.InAltScreen {
		t.Fatal("not in alt screen")
	}
	s.Write([]byte("Alt"))
	s.Write([]byte("\x1b[?1049l")) // exit alt
	if s.InAltScreen {
		t.Fatal("still in alt screen")
	}
	// Main content should be restored
	if s.Cells[0][0].Ch != 'M' {
		t.Errorf("main screen not restored: got %q", s.Cells[0][0].Ch)
	}
}

func TestScreen_OSCTitle_Preserved(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b]0;My Title\x07"))
	if s.Title != "My Title" {
		t.Errorf("OSC title: got %q, want %q", s.Title, "My Title")
	}
}

func TestScreen_DeviceAttributes_Preserved(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[c"))
	if string(s.Response) != "\x1b[?62;22c" {
		t.Errorf("DA1: got %q, want %q", s.Response, "\x1b[?62;22c")
	}
}

func TestScreen_DSR_CursorPosition(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[5;10H"))
	s.Write([]byte("\x1b[6n"))
	want := "\x1b[5;10R"
	if string(s.Response) != want {
		t.Errorf("DSR CPR: got %q, want %q", s.Response, want)
	}
}

func TestScreen_BracketedPaste_Preserved(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[?2004h"))
	if !s.BracketedPaste {
		t.Error("bracketed paste not enabled")
	}
	s.Write([]byte("\x1b[?2004l"))
	if s.BracketedPaste {
		t.Error("bracketed paste not disabled")
	}
}
