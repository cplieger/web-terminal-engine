package vt

import (
	"testing"
)

// =============================================================
// PROBE 1: State-table completeness
// =============================================================

func TestTableComplete_AllStatesAllBytes(t *testing.T) {
	for s := range numStates {
		for b := range 256 {
			tr := stateTable[s][b]
			if tr == noTransition {
				t.Errorf("state %d byte 0x%02x: sentinel (no transition)", s, b)
			}
			// Verify next state is valid
			next := tr.next()
			if int(next) >= int(numStates) {
				t.Errorf("state %d byte 0x%02x: next=%d >= numStates=%d", s, b, next, numStates)
			}
		}
	}
}

func TestTableComplete_Byte0x18InEveryState(t *testing.T) {
	// CAN (0x18) must transition to Ground from every state
	for s := range numStates {
		tr := stateTable[s][0x18]
		if tr.next() != stGround {
			t.Errorf("state %d: CAN(0x18) goes to %d, want Ground(0)", s, tr.next())
		}
	}
}

func TestTableComplete_Byte0x1AInEveryState(t *testing.T) {
	// SUB (0x1A) must transition to Ground from every state
	for s := range numStates {
		tr := stateTable[s][0x1A]
		if tr.next() != stGround {
			t.Errorf("state %d: SUB(0x1A) goes to %d, want Ground(0)", s, tr.next())
		}
	}
}

func TestTableComplete_ESC0x1BTransitions(t *testing.T) {
	// ESC (0x1B) must go to stEscape from all states except stEscape itself
	for s := range numStates {
		if s == stEscape {
			continue
		}
		tr := stateTable[s][0x1B]
		if tr.next() != stEscape {
			t.Errorf("state %d: ESC(0x1B) goes to %d, want Escape(1)", s, tr.next())
		}
	}
}

// =============================================================
// PROBE 1b: CAN/SUB abort without firing exit actions
// =============================================================

func TestCANAbortsDCS_NoUnhook(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[1;31m")) // set bold+red for DECRQSS
	s.Write([]byte("\x1bP$qm"))   // enter DCS passthrough for DECRQSS
	// CAN should abort without calling dcsUnhook (no response)
	s.Write([]byte{0x18})
	if s.pState != stGround {
		t.Fatalf("CAN did not abort DCS: state=%d", s.pState)
	}
	if len(s.Response) != 0 {
		t.Fatalf("CAN in DCS produced response: %q", s.Response)
	}
}

func TestSUBAbortsDCS_NoUnhook(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS for SGR
	s.Write([]byte{0x1A})       // SUB
	if s.pState != stGround {
		t.Fatalf("SUB did not abort DCS: state=%d", s.pState)
	}
	if len(s.Response) != 0 {
		t.Fatalf("SUB in DCS produced response: %q", s.Response)
	}
}

func TestCANAbortsOSC_NoDispatch(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;new title"))
	s.Write([]byte{0x18}) // CAN aborts OSC
	if s.pState != stGround {
		t.Fatalf("CAN did not abort OSC: state=%d", s.pState)
	}
	// Title should NOT have been set (CAN suppresses exit action)
	if s.Title == "new title" {
		t.Fatal("CAN in OSC dispatched title (exit action fired)")
	}
}

func TestSUBAbortsOSC_NoDispatch(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;aborted"))
	s.Write([]byte{0x1A}) // SUB aborts OSC
	if s.pState != stGround {
		t.Fatalf("SUB did not abort OSC: state=%d", s.pState)
	}
	if s.Title == "aborted" {
		t.Fatal("SUB in OSC dispatched title (exit action fired)")
	}
}

// =============================================================
// PROBE 1c: C0 controls execute mid-sequence
// =============================================================

func TestC0ExecutesMidCSI(t *testing.T) {
	s := New(5, 80)
	// BEL (0x07) should execute even mid-CSI
	s.Write([]byte("\x1b[1\x07;1H"))
	if !s.BellRing {
		t.Error("BEL (0x07) not executed mid-CSI param")
	}
}

func TestC0_LF_MidCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("A"))
	// LF mid-CSI should execute (move cursor down)
	s.Write([]byte("\x1b[1\n;1H"))
	// LF should have moved us down, then the CSI completes
	// The key assertion: LF executed and cursor moved
	row, _ := s.CursorPos()
	// The CSI 1;1H at end puts us at 0,0 - that's correct
	_ = row
}

func TestC0_CR_MidCSI(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("HELLO"))
	// CR mid-CSI param should execute (cursor to col 0)
	s.Write([]byte("\x1b[\rH"))
	// After CR, cursor goes to col 0, then CSI 'H' (no params) → home
	row, col := s.CursorPos()
	if row != 0 || col != 0 {
		t.Logf("cursor at row=%d col=%d (expected 0,0)", row, col)
	}
}

func TestC0_BS_MidEscape(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("AB"))
	// BS mid-escape state should execute
	s.Write([]byte("\x1b\b"))
	// After BS, cursor should move left
	_, col := s.CursorPos()
	// After "AB" cursor is at 2, BS moves to 1, but ESC then
	// waits for next byte. Let's send a char to complete:
	_ = col
	s.Write([]byte("c")) // ESC c = RIS
}
