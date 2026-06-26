package vt

import "testing"

// TestStateTableComplete verifies the parser transition table is fully
// initialized: every (state, byte) pair has a real transition (not the
// uninitialized sentinel), with an in-range next state and a valid action.
func TestStateTableComplete(t *testing.T) {
	for s := range numStates {
		for b := range 256 {
			tr := stateTable[s][b]
			if tr == noTransition {
				t.Fatalf("state %d byte 0x%02x: uninitialized (sentinel)", s, b)
			}
			next := tr.next()
			if int(next) >= int(numStates) {
				t.Fatalf("state %d byte 0x%02x: next=%d invalid", s, b, next)
			}
			if int(tr.act()) > int(actMarker) {
				t.Fatalf("state %d byte 0x%02x: act=%d invalid", s, b, tr.act())
			}
		}
	}
}

// TestStateTableCANSUBGoToGround verifies CAN (0x18) and SUB (0x1A) transition
// to Ground from every state (they abort any in-progress sequence).
func TestStateTableCANSUBGoToGround(t *testing.T) {
	for s := range numStates {
		for _, b := range []byte{0x18, 0x1A} {
			if got := stateTable[s][b].next(); got != stGround {
				t.Errorf("state %d byte 0x%02x: next=%d, want Ground", s, b, got)
			}
		}
	}
}

// TestStateTableESCGoesToEscape verifies ESC (0x1B) transitions to the Escape
// state from every state except Escape itself.
func TestStateTableESCGoesToEscape(t *testing.T) {
	for s := range numStates {
		if s == stEscape {
			continue
		}
		if got := stateTable[s][0x1B].next(); got != stEscape {
			t.Errorf("state %d byte ESC: next=%d, want Escape", s, got)
		}
	}
}

// TestStateTableSosPmApcTerminatesOnST verifies the SOS/PM/APC string state
// transitions to Ground on the 8-bit ST (0x9C), as does the OSC string state.
func TestStateTableSosPmApcTerminatesOnST(t *testing.T) {
	if got := stateTable[stSosPmApcString][0x9C].next(); got != stGround {
		t.Errorf("stateTable[SosPmApcString][0x9C].next() = %d, want stGround (%d)", got, stGround)
	}
	if got := stateTable[stOscString][0x9C].next(); got != stGround {
		t.Errorf("stateTable[OscString][0x9C].next() = %d, want stGround (%d)", got, stGround)
	}
}
