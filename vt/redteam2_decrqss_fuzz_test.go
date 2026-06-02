package vt

import (
	"strings"
	"testing"
)

// =============================================================
// REDTEAM2 PROBE 4: DECRQSS exact responses
// =============================================================

func TestRT2_DECRQSS_SGR_AllAttribs(t *testing.T) {
	s := New(24, 80)
	// Set all attributes
	s.Write([]byte("\x1b[1;2;3;4;5;7;8;9;53m"))
	s.Response = nil
	s.Write([]byte("\x1bP$qm\x1b\\"))
	got := string(s.Response)
	if !strings.HasPrefix(got, "\x1bP1$r") || !strings.HasSuffix(got, "m\x1b\\") {
		t.Fatalf("response format: %q", got)
	}
	params := got[len("\x1bP1$r") : len(got)-len("m\x1b\\")]
	for _, want := range []string{"1", "2", "3", "4", "5", "7", "8", "9", "53"} {
		if !strings.Contains(params, want) {
			t.Errorf("missing %q in %q", want, params)
		}
	}
}

func TestRT2_DECRQSS_DECSTBM_Custom(t *testing.T) {
	s := New(50, 80)
	s.Write([]byte("\x1b[10;40r"))
	s.Write([]byte("\x1bP$qr\x1b\\"))
	want := "\x1bP1$r10;40r\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECSTBM: got %q want %q", s.Response, want)
	}
}

func TestRT2_DECRQSS_DECSCUSR_AllStyles(t *testing.T) {
	for style := range 7 {
		s := New(24, 80)
		s.Write([]byte{0x1b, '[', byte('0' + style), ' ', 'q'})
		s.Write([]byte("\x1bP$q q\x1b\\"))
		want := "\x1bP1$r" + string(byte('0'+style)) + " q\x1b\\"
		if string(s.Response) != want {
			t.Errorf("DECSCUSR %d: got %q want %q", style, s.Response, want)
		}
	}
}

func TestRT2_DECRQSS_InvalidQueryTypes(t *testing.T) {
	badQueries := []string{
		"z", "Z", "\"p", "\"q", "$}", "!p",
		"mm", "rr", " qq", "123",
		strings.Repeat("x", 200),
	}
	for _, q := range badQueries {
		s := New(24, 80)
		s.Write([]byte("\x1bP$q" + q + "\x1b\\"))
		want := "\x1bP0$r\x1b\\"
		if string(s.Response) != want {
			t.Errorf("unsupported %q: got %q want %q", q, s.Response, want)
		}
	}
}

// =============================================================
// REDTEAM2 PROBE 5: DCS per-handler bounds
// =============================================================

func TestRT2_DCS_DECRQSS_ExactlyAt256(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$q"))
	// Feed exactly 256 bytes
	s.Write(make([]byte, 256))
	if len(s.dcsBuf) > maxDCSLen {
		t.Errorf("dcsBuf len %d > %d", len(s.dcsBuf), maxDCSLen)
	}
}

func TestRT2_DCS_DECRQSS_AtBoundary(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$q"))
	// Fill up to exactly maxDCSLen, then one more
	data := make([]byte, maxDCSLen+10)
	for i := range data {
		data[i] = 'A'
	}
	s.Write(data)
	if len(s.dcsBuf) != maxDCSLen {
		t.Errorf("dcsBuf: got %d want %d", len(s.dcsBuf), maxDCSLen)
	}
}

func TestRT2_DCS_Unknown_NeverBuffers(t *testing.T) {
	s := New(24, 80)
	// Unknown DCS: final byte 'z' with no matching intermediate
	s.Write([]byte("\x1bPz"))
	s.Write([]byte("this should not be buffered at all whatsoever"))
	if len(s.dcsBuf) != 0 {
		t.Errorf("unknown DCS buffered %d bytes", len(s.dcsBuf))
	}
}

func TestRT2_DCS_MultipleSequential(t *testing.T) {
	s := New(24, 80)
	// Two DECRQSS in sequence: each should produce independent response
	s.Write([]byte("\x1bP$qm\x1b\\"))
	first := string(s.Response)
	s.Response = nil
	s.Write([]byte("\x1b[1m")) // set bold
	s.Write([]byte("\x1bP$qm\x1b\\"))
	second := string(s.Response)
	if first == second {
		t.Error("two DECRQSS with different state gave same response")
	}
}

// =============================================================
// REDTEAM2 PROBE 6: Fuzz-like adversarial streams
// =============================================================

func TestRT2_Fuzz_AllByteValues(t *testing.T) {
	s := New(24, 80)
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}
	s.Write(data)
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatalf("all-bytes: cursor OOB %d,%d", row, col)
	}
}

func TestRT2_Fuzz_RepeatedESC(t *testing.T) {
	s := New(24, 80)
	data := make([]byte, 1000)
	for i := range data {
		data[i] = 0x1B
	}
	s.Write(data)
	if s.pState != stEscape {
		t.Logf("state after 1000 ESC: %d", s.pState)
	}
}

func TestRT2_Fuzz_InterleavedDCS_OSC(t *testing.T) {
	s := New(24, 80)
	for range 500 {
		s.Write([]byte("\x1bP$q"))
		s.Write([]byte("x"))
		s.Write([]byte{0x18}) // CAN
		s.Write([]byte("\x1b]2;t"))
		s.Write([]byte{0x1A}) // SUB
	}
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatal("interleaved DCS/OSC: cursor OOB")
	}
}

func TestRT2_Fuzz_MemoryBounded(t *testing.T) {
	s := New(24, 80)
	// Feed 100KB of OSC data without terminator, then CAN
	s.Write([]byte("\x1b]2;"))
	big := make([]byte, 100000)
	for i := range big {
		big[i] = 'A'
	}
	s.Write(big)
	// oscBuf should be capped at maxOSCLen
	if len(s.oscBuf) > maxOSCLen {
		t.Errorf("oscBuf: %d > %d", len(s.oscBuf), maxOSCLen)
	}
	s.Write([]byte{0x18}) // abort
}

func TestRT2_Fuzz_RapidStateTransitions(t *testing.T) {
	s := New(24, 80)
	// Rapidly alternate between states
	pattern := []byte("\x1b[\x1b]\x1bP\x18\x1a\x1b[m\x1b]0;\x07\x1bP$qm\x1b\\")
	for range 200 {
		s.Write(pattern)
	}
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatal("rapid transitions: cursor OOB")
	}
}

func TestRT2_Fuzz_DeterministicRandom50K(t *testing.T) {
	s := New(24, 80)
	data := make([]byte, 50000)
	seed := uint32(0xCAFEBABE)
	for i := range data {
		seed = seed*1103515245 + 12345
		data[i] = byte(seed >> 16)
	}
	s.Write(data)
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatalf("50K random: cursor OOB %d,%d", row, col)
	}
}

// =============================================================
// REDTEAM2 PROBE 7: Screen behavior preserved
// =============================================================

func TestRT2_Screen_EraseInDisplay(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("AAAAAAAAAA"))
	s.Write([]byte("\x1b[1;1H"))
	s.Write([]byte("\x1b[2J")) // erase all
	for x := range s.Width {
		if s.Cells[0][x].Ch != ' ' {
			t.Fatalf("ED 2: cell[0][%d]=%U want space", x, s.Cells[0][x].Ch)
		}
	}
}

func TestRT2_Screen_InsertDeleteChars(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("ABCDE"))
	s.Write([]byte("\x1b[1;2H")) // cursor at col 1
	s.Write([]byte("\x1b[2@"))   // insert 2 chars
	if s.Cells[0][1].Ch != ' ' || s.Cells[0][2].Ch != ' ' {
		t.Error("ICH did not insert spaces")
	}
	if s.Cells[0][3].Ch != 'B' {
		t.Errorf("ICH shifted wrong: got %U want B", s.Cells[0][3].Ch)
	}
}

func TestRT2_Screen_ScrollUp(t *testing.T) {
	s := New(3, 5)
	s.Write([]byte("Line1\r\nLine2\r\nLine3"))
	s.Write([]byte("\x1b[1S")) // scroll up 1
	if s.RowString(0) != "Line2" {
		t.Errorf("after SU: row0=%q want Line2", s.RowString(0))
	}
}

func TestRT2_Screen_TabStops(t *testing.T) {
	s := New(1, 40)
	s.Write([]byte("\t"))
	_, col := s.CursorPos()
	if col != 8 {
		t.Errorf("tab: col=%d want 8", col)
	}
}

func TestRT2_Screen_SaveRestore(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[5;5H"))
	s.Write([]byte("\x1b[s")) // save
	s.Write([]byte("\x1b[1;1H"))
	s.Write([]byte("\x1b[u")) // restore
	row, col := s.CursorPos()
	if row != 4 || col != 4 {
		t.Errorf("save/restore: %d,%d want 4,4", row, col)
	}
}

func TestRT2_Screen_OriginMode(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[3;8r")) // scroll region 3-8
	s.Write([]byte("\x1b[?6h"))  // origin mode on
	s.Write([]byte("\x1b[1;1H")) // should be relative to region
	row, col := s.CursorPos()
	if row != 2 || col != 0 {
		t.Errorf("origin mode: %d,%d want 2,0", row, col)
	}
}

func TestRT2_Screen_WideChar(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("漢"))
	if s.Cells[0][0].Ch != '漢' {
		t.Errorf("wide char: %U", s.Cells[0][0].Ch)
	}
}

func TestRT2_Screen_MouseMode(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[?1000h"))
	if s.MouseMode != 1000 {
		t.Errorf("mouse mode: %d want 1000", s.MouseMode)
	}
	s.Write([]byte("\x1b[?1000l"))
	if s.MouseMode != 0 {
		t.Errorf("mouse mode off: %d want 0", s.MouseMode)
	}
}

func TestRT2_Screen_RIS(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[1;31m")) // bold + red
	s.Write([]byte("\x1b[?25l"))  // hide cursor
	s.Write([]byte("\x1bc"))      // RIS
	if s.CursorHidden {
		t.Error("RIS did not unhide cursor")
	}
	if s.style != (Style{}) {
		t.Errorf("RIS did not reset style: %+v", s.style)
	}
}
