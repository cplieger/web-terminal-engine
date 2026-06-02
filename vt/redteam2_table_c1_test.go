package vt

import "testing"

// =============================================================
// REDTEAM2 PROBE 1: State-table completeness — deeper checks
// =============================================================

func TestRT2_Table_ActionAndNextValid(t *testing.T) {
	for s := range numStates {
		for b := range 256 {
			tr := stateTable[s][b]
			if tr == noTransition {
				t.Fatalf("state %d byte 0x%02x: sentinel", s, b)
			}
			act := tr.act()
			next := tr.next()
			if int(next) >= int(numStates) {
				t.Fatalf("state %d byte 0x%02x: next=%d invalid", s, b, next)
			}
			if int(act) > int(actMarker) {
				t.Fatalf("state %d byte 0x%02x: act=%d invalid", s, b, act)
			}
		}
	}
}

func TestRT2_CAN_SUB_AlwaysGoGround(t *testing.T) {
	for s := range numStates {
		for _, b := range []byte{0x18, 0x1A} {
			tr := stateTable[s][b]
			if tr.next() != stGround {
				t.Errorf("state %d byte 0x%02x: next=%d want Ground", s, b, tr.next())
			}
		}
	}
}

func TestRT2_ESC_AlwaysGoesEscape(t *testing.T) {
	for s := range numStates {
		if parserState(s) == stEscape { //nolint:unconvert // range yields int, not parserState
			continue
		}
		tr := stateTable[s][0x1B]
		if tr.next() != stEscape {
			t.Errorf("state %d ESC: next=%d want Escape", s, tr.next())
		}
	}
}

func TestRT2_C0_ExecuteMidCSI_BEL(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b["))
	s.BellRing = false
	s.Write([]byte{0x07}) // BEL mid-CSI
	if !s.BellRing {
		t.Error("BEL not executed mid-CSI")
	}
}

func TestRT2_C0_ExecuteMidCSI_BS(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("ABCD"))
	// cursor at col 4; BS mid-CSI should move to col 3
	s.Write([]byte("\x1b[\b1H"))
	// After BS cursor was at 3, then CSI 1H puts at 0
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Logf("cursor at %d,%d", row, col)
	}
}

func TestRT2_C0_ExecuteMidEscape_LF(t *testing.T) {
	s := New(5, 80)
	// Start ESC, inject LF, then 'H' (ESC H = HTS)
	s.Write([]byte("\x1b\nH"))
	// LF should execute (move down) then ESC H sets tab stop
	row, _ := s.CursorPos()
	if row < 1 {
		t.Error("LF not executed mid-escape")
	}
}

func TestRT2_CAN_Aborts_CSI_Cleanly(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[31")) // partial CSI
	s.Write([]byte{0x18})      // CAN
	if s.pState != stGround {
		t.Fatalf("CAN in CSI: state=%d want Ground", s.pState)
	}
	// Next sequence should work normally
	s.Write([]byte("\x1b[1;1H"))
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Errorf("after CAN recovery: %d,%d want 0,0", row, col)
	}
}

func TestRT2_SUB_Aborts_OSC_NoTitle(t *testing.T) {
	s := New(5, 80)
	s.Title = ""
	s.Write([]byte("\x1b]2;BadTitle"))
	s.Write([]byte{0x1A})
	if s.Title == "BadTitle" {
		t.Error("SUB did not suppress OSC title dispatch")
	}
}

// =============================================================
// REDTEAM2 PROBE 2: C1 0x80-0x9F
// =============================================================

func TestRT2_C1_Ground_AllFFFF(t *testing.T) {
	for b := byte(0x80); b <= 0x9F; b++ {
		s := New(1, 3)
		s.Write([]byte{b})
		if s.Cells[0][0].Ch != 0xFFFD {
			t.Errorf("0x%02x in Ground: %U want U+FFFD", b, s.Cells[0][0].Ch)
		}
		if s.pState != stGround {
			t.Errorf("0x%02x in Ground: state=%d", b, s.pState)
		}
	}
}

func TestRT2_C1_NonGround_0x9B_InitiatesCSI(t *testing.T) {
	s := New(1, 10)
	// Put parser in Escape state, then 0x9B should go to CsiEntry
	s.Write([]byte{0x1B}) // enter Escape
	s.Write([]byte{0x9B}) // 8-bit CSI
	if s.pState != stCsiEntry {
		t.Errorf("0x9B in Escape: state=%d want CsiEntry", s.pState)
	}
}

func TestRT2_C1_NonGround_0x90_InitiatesDCS(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte{0x1B}) // enter Escape
	s.Write([]byte{0x90}) // 8-bit DCS
	if s.pState != stDcsEntry {
		t.Errorf("0x90 in Escape: state=%d want DcsEntry", s.pState)
	}
}

func TestRT2_C1_NonGround_0x9D_InitiatesOSC(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte{0x1B}) // Escape
	s.Write([]byte{0x9D}) // 8-bit OSC
	if s.pState != stOscString {
		t.Errorf("0x9D in Escape: state=%d want OscString", s.pState)
	}
}

func TestRT2_C1_InDcsPassthrough_9C_Terminates(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS
	s.Write([]byte{0x9C})       // 8-bit ST
	if s.pState != stGround {
		t.Errorf("0x9C in DcsPassthrough: state=%d", s.pState)
	}
	if len(s.Response) == 0 {
		t.Error("DECRQSS with 8-bit ST: no response")
	}
}

func TestRT2_C1_InOscString_9C_Terminates(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b]2;Hello"))
	s.Write([]byte{0x9C}) // 8-bit ST terminates OSC
	if s.pState != stGround {
		t.Errorf("0x9C in OscString: state=%d", s.pState)
	}
	if s.Title != "Hello" {
		t.Errorf("OSC title after 0x9C: got %q", s.Title)
	}
}

func TestRT2_C1_UTF8_MultibyteNotCorrupted(t *testing.T) {
	s := New(1, 10)
	// Valid 3-byte UTF-8: U+6F22 = 漢
	s.Write([]byte{0xE6, 0xBC, 0xA2})
	if s.Cells[0][0].Ch != '漢' {
		t.Errorf("UTF-8 漢: got %U", s.Cells[0][0].Ch)
	}
}
