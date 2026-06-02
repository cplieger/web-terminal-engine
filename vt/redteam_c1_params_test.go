package vt

import (
	"testing"
)

// =============================================================
// PROBE 2: C1 bytes 0x80-0x9F behavior
// =============================================================

func TestC1_Ground_AllEmitReplacement(t *testing.T) {
	for b := byte(0x80); b <= 0x9F; b++ {
		s := New(1, 5)
		s.Write([]byte{b})
		if s.Cells[0][0].Ch != 0xFFFD {
			t.Errorf("byte 0x%02x in Ground: got %U, want U+FFFD", b, s.Cells[0][0].Ch)
		}
		if s.pState != stGround {
			t.Errorf("byte 0x%02x moved parser to state %d, want Ground", b, s.pState)
		}
	}
}

func TestC1_0x9B_DoesNotInitiateCSI_InGround(t *testing.T) {
	s := New(1, 10)
	// 0x9B is CSI in 8-bit mode but in UTF-8 Ground it should NOT start CSI
	s.Write([]byte{0x9B, '3', '1', 'm'})
	// Should have printed U+FFFD then '3', '1', 'm'
	if s.Cells[0][0].Ch != 0xFFFD {
		t.Errorf("0x9B in Ground: got %U, want U+FFFD", s.Cells[0][0].Ch)
	}
	if s.Cells[0][1].Ch != '3' {
		t.Errorf("after 0x9B: cell[1] got %U, want '3'", s.Cells[0][1].Ch)
	}
}

func TestC1_0x90_DoesNotInitiateDCS_InGround(t *testing.T) {
	s := New(1, 10)
	// 0x90 is DCS in 8-bit, but in Ground UTF-8 → U+FFFD
	s.Write([]byte{0x90})
	if s.Cells[0][0].Ch != 0xFFFD {
		t.Errorf("0x90 in Ground: got %U, want U+FFFD", s.Cells[0][0].Ch)
	}
	if s.pState != stGround {
		t.Errorf("0x90 in Ground: state=%d, want Ground", s.pState)
	}
}

func TestC1_HonoredInNonGround_CSIEntry(t *testing.T) {
	s := New(24, 80)
	// Enter CSI state
	s.Write([]byte("\x1b["))
	if s.pState != stCsiEntry {
		t.Fatalf("not in CsiEntry: state=%d", s.pState)
	}
	// 0x9C (ST) in non-Ground should act as C1 → Ground
	s.Write([]byte{0x9C})
	if s.pState != stGround {
		t.Errorf("0x9C in CsiEntry: state=%d, want Ground", s.pState)
	}
}

func TestC1_HonoredInNonGround_EscapeState(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte{0x1B}) // ESC → Escape state
	if s.pState != stEscape {
		t.Fatalf("not in Escape: state=%d", s.pState)
	}
	// 0x9B in Escape should transition to CsiEntry (C1 CSI)
	s.Write([]byte{0x9B})
	if s.pState != stCsiEntry {
		t.Errorf("0x9B in Escape: state=%d, want CsiEntry", s.pState)
	}
}

func TestC1_0x9D_InNonGround_TransitionsToOSC(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte{0x1B}) // Enter Escape
	// 0x9D = OSC
	s.Write([]byte{0x9D})
	if s.pState != stOscString {
		t.Errorf("0x9D in Escape: state=%d, want OscString", s.pState)
	}
}

// =============================================================
// PROBE 3: Param/subparam edge cases
// =============================================================

func TestParams_EmptyParams_DefaultsToZero(t *testing.T) {
	s := New(5, 80)
	// CSI H with no params → defaults to 1,1 (cursor home)
	s.Write([]byte("\x1b[5;5H")) // move to 5,5
	s.Write([]byte("\x1b[H"))    // no params → home
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Errorf("CSI H with no params: got row=%d col=%d, want 0,0", row, col)
	}
}

func TestParams_EmptyMiddleParam(t *testing.T) {
	s := New(24, 80)
	// CSI ;5H → row=default(1), col=5
	s.Write([]byte("\x1b[;5H"))
	row, col := s.CursorPos()
	if row != 0 || col != 4 { // 1-indexed to 0-indexed
		t.Errorf("CSI ;5H: got row=%d col=%d, want 0,4", row, col)
	}
}

func TestParams_LeadingColon(t *testing.T) {
	s := New(1, 10)
	// Leading colon: \x1b[:2:255:0:128m — should not panic
	s.Write([]byte("\x1b[:2:255:0:128mX"))
	// May or may not apply color, but must not panic
	if s.pState != stGround {
		t.Errorf("parser stuck in state %d after leading-colon SGR", s.pState)
	}
}

func TestParams_TrailingColon(t *testing.T) {
	s := New(1, 10)
	// Trailing colon: \x1b[38:2:255:0:128:m — extra colon before final
	s.Write([]byte("\x1b[38:2:255:0:128:mX"))
	if s.pState != stGround {
		t.Errorf("parser stuck in state %d after trailing-colon SGR", s.pState)
	}
}

func TestParams_OnlyColons(t *testing.T) {
	s := New(1, 10)
	// All colons, no digits
	s.Write([]byte("\x1b[:::mX"))
	if s.pState != stGround {
		t.Errorf("parser stuck in state %d after all-colon params", s.pState)
	}
}

func TestParams_FixedArrayOverflow_NoPanic(t *testing.T) {
	s := New(5, 80)
	// Build CSI with 64 params (well over maxParams=32)
	var seq []byte
	seq = append(seq, "\x1b["...)
	for i := range 64 {
		if i > 0 {
			seq = append(seq, ';')
		}
		seq = append(seq, '1')
	}
	seq = append(seq, 'H')
	s.Write(seq) // must not panic
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatalf("cursor OOB after param overflow: row=%d col=%d", row, col)
	}
}

func TestParams_FixedArrayOverflow_Subparams(t *testing.T) {
	s := New(5, 80)
	// Build CSI with 64 subparams via colons (over maxParams=32)
	var seq []byte
	seq = append(seq, "\x1b[38"...)
	for range 60 {
		seq = append(seq, ':')
		seq = append(seq, '1')
	}
	seq = append(seq, 'm')
	s.Write(seq) // must not panic
	if s.pState != stGround {
		t.Errorf("parser stuck in state %d after subparam overflow", s.pState)
	}
}

func TestParams_HugeValue_Clamped(t *testing.T) {
	s := New(5, 80)
	// CSI with param value 9999999999 (exceeds uint16)
	s.Write([]byte("\x1b[9999999999A"))
	// Should be clamped to maxCSIArgValue=65535, cursor at row 0
	row, _ := s.CursorPos()
	if row != 0 {
		t.Errorf("huge param cursor up: row=%d, want 0", row)
	}
}

func TestParams_MaxUint16Value(t *testing.T) {
	s := New(5, 80)
	// Exactly 65535
	s.Write([]byte("\x1b[65535B"))
	row, _ := s.CursorPos()
	if row != s.Height-1 {
		t.Errorf("65535 cursor down: row=%d, want %d", row, s.Height-1)
	}
}

func TestParams_ZeroParam_UsesDefault(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[3;3H")) // move to 3,3
	// CSI 0 A → cursor up by default 1 (0 treated as default)
	s.Write([]byte("\x1b[0A"))
	row, _ := s.CursorPos()
	if row != 1 { // was at row 2, up by 1 = row 1
		t.Errorf("CSI 0A: row=%d, want 1", row)
	}
}
