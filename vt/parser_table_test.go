package vt

import "testing"

// --- Table completeness test ---

func TestTableComplete(t *testing.T) {
	for s := range numStates {
		for b := range 256 {
			if stateTable[s][b] == noTransition {
				t.Errorf("state %d byte 0x%02x: uninitialized (sentinel)", s, b)
			}
		}
	}
}

// --- Subparam parsing tests ---

func TestSubparamColonSGR38_2_RGB(t *testing.T) {
	s := New(1, 10)
	// SGR 38:2:255:0:128 m — colon-separated RGB foreground
	s.Write([]byte("\x1b[38:2:255:0:128mX"))
	cell := s.Cells[0][0]
	if cell.Style.FG.Type != 3 {
		t.Fatalf("expected RGB color type 3, got %d", cell.Style.FG.Type)
	}
	if cell.Style.FG.R != 255 || cell.Style.FG.G != 0 || cell.Style.FG.B != 128 {
		t.Errorf("RGB: got R=%d G=%d B=%d, want 255,0,128",
			cell.Style.FG.R, cell.Style.FG.G, cell.Style.FG.B)
	}
}

func TestSubparamColonSGR38_5_256Color(t *testing.T) {
	s := New(1, 10)
	// SGR 38:5:196 m — colon-separated 256-color
	s.Write([]byte("\x1b[38:5:196mX"))
	cell := s.Cells[0][0]
	if cell.Style.FG.Type != 2 {
		t.Fatalf("expected 256-color type 2, got %d", cell.Style.FG.Type)
	}
	if cell.Style.FG.Val != 196 {
		t.Errorf("256-color: got %d, want 196", cell.Style.FG.Val)
	}
}

func TestSubparamSemicolonLegacySGR38(t *testing.T) {
	s := New(1, 10)
	// Legacy semicolon form: SGR 38;2;144;70;255
	s.Write([]byte("\x1b[38;2;144;70;255mX"))
	cell := s.Cells[0][0]
	if cell.Style.FG.Type != 3 {
		t.Fatalf("expected RGB color type 3, got %d", cell.Style.FG.Type)
	}
	if cell.Style.FG.R != 144 || cell.Style.FG.G != 70 || cell.Style.FG.B != 255 {
		t.Errorf("RGB: got R=%d G=%d B=%d, want 144,70,255",
			cell.Style.FG.R, cell.Style.FG.G, cell.Style.FG.B)
	}
}

func TestSubparamMixed(t *testing.T) {
	s := New(1, 10)
	// Mix: bold + colon-RGB fg + semicolon-256 bg
	s.Write([]byte("\x1b[1;38:2:10:20:30;48;5;200mX"))
	cell := s.Cells[0][0]
	if !cell.Style.Bold {
		t.Error("expected Bold")
	}
	if cell.Style.FG.Type != 3 || cell.Style.FG.R != 10 || cell.Style.FG.G != 20 || cell.Style.FG.B != 30 {
		t.Errorf("FG RGB: got %+v", cell.Style.FG)
	}
	if cell.Style.BG.Type != 2 || cell.Style.BG.Val != 200 {
		t.Errorf("BG 256: got %+v", cell.Style.BG)
	}
}

// --- C1 bytes in Ground state (UTF-8 mode) ---

func TestC1InGroundEmitsReplacementChar(t *testing.T) {
	s := New(1, 10)
	// Byte 0x9B in Ground should NOT initiate CSI — should emit U+FFFD
	s.Write([]byte{0x9B})
	if s.Cells[0][0].Ch != 0xFFFD {
		t.Errorf("0x9B in Ground: got %U, want U+FFFD", s.Cells[0][0].Ch)
	}
	if s.pState != stGround {
		t.Errorf("parser left Ground after 0x9B: state=%d", s.pState)
	}
}

func TestC1InGroundRange(t *testing.T) {
	// All C1 bytes 0x80-0x9F should emit U+FFFD in Ground
	for b := byte(0x80); b <= 0x9F; b++ {
		s := New(1, 5)
		s.Write([]byte{b})
		if s.Cells[0][0].Ch != 0xFFFD {
			t.Errorf("byte 0x%02x in Ground: got %U, want U+FFFD", b, s.Cells[0][0].Ch)
		}
	}
}

func TestC1InNonGroundHonored(t *testing.T) {
	s := New(1, 10)
	// Start a CSI sequence, then 0x9C (ST) should abort to Ground
	s.Write([]byte("\x1b["))
	s.Write([]byte{0x9C}) // ST — should transition to Ground
	if s.pState != stGround {
		t.Errorf("0x9C in CsiEntry should → Ground: state=%d", s.pState)
	}
}

// --- DECRQSS tests ---

func TestDECRQSS_SGR(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[1;31m")) // bold + red
	s.Write([]byte("\x1bP$qm\x1b\\"))
	want := "\x1bP1$r0;1;31m\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS SGR: got %q, want %q", string(s.Response), want)
	}
}

func TestDECRQSS_DECSTBM(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[5;20r")) // set scroll region
	s.Write([]byte("\x1bP$qr\x1b\\"))
	want := "\x1bP1$r5;20r\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS DECSTBM: got %q, want %q", string(s.Response), want)
	}
}

func TestDECRQSS_DECSCUSR(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[2 q")) // steady block cursor
	s.Write([]byte("\x1bP$q q\x1b\\"))
	want := "\x1bP1$r2 q\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS DECSCUSR: got %q, want %q", string(s.Response), want)
	}
}

func TestDECRQSS_Unknown(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qz\x1b\\")) // unknown query 'z'
	want := "\x1bP0$r\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS unknown: got %q, want %q", string(s.Response), want)
	}
}

func TestDECRQSS_EmptyQuery(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$q\x1b\\")) // empty query
	want := "\x1bP0$r\x1b\\"
	if string(s.Response) != want {
		t.Errorf("DECRQSS empty: got %q, want %q", string(s.Response), want)
	}
}

// --- DCS abort by CAN/SUB ---

func TestDCSAbortByCAN(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // start DECRQSS for SGR
	// Abort with CAN before ST
	s.Write([]byte{0x18})
	if s.pState != stGround {
		t.Errorf("DCS not aborted by CAN: state=%d", s.pState)
	}
	// No response should have been generated
	if len(s.Response) != 0 {
		t.Errorf("DCS aborted by CAN produced response: %q", s.Response)
	}
}

// --- Parser state: private marker routing ---

func TestPrivateMarkerInIntermed(t *testing.T) {
	s := New(24, 80)
	// CSI ? 25 l → cursor hidden
	s.Write([]byte("\x1b[?25l"))
	if !s.CursorHidden {
		t.Error("CSI ?25l should hide cursor")
	}
	// Verify private marker is used correctly for DA
	s.Write([]byte("\x1b[>c"))
	if string(s.Response) != "\x1b[>1;10;0c" {
		t.Errorf("secondary DA: got %q", s.Response)
	}
}
