package vt

import "testing"

// TestParams_NoParamsDefaultsToHome verifies CSI H with no parameters moves the
// cursor home (defaults to 1;1).
func TestParams_NoParamsDefaultsToHome(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[5;5H")) // move away
	s.Write([]byte("\x1b[H"))    // no params -> home
	if row, col := s.CursorPos(); row != 0 || col != 0 {
		t.Errorf("CSI H with no params = %d,%d, want 0,0", row, col)
	}
}

// TestParams_EmptyMiddleParam verifies an empty leading parameter takes its
// default while a later parameter is honored (CSI ;5H).
func TestParams_EmptyMiddleParam(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[;5H"))
	if row, col := s.CursorPos(); row != 0 || col != 4 {
		t.Errorf("CSI ;5H = %d,%d, want 0,4", row, col)
	}
}

// TestParams_ZeroTreatedAsDefault verifies a zero parameter is treated as the
// default (CSI 0A moves up by one).
func TestParams_ZeroTreatedAsDefault(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[3;3H")) // row 2
	s.Write([]byte("\x1b[0A"))   // up by default 1
	if row, _ := s.CursorPos(); row != 1 {
		t.Errorf("CSI 0A: row=%d, want 1", row)
	}
}

// TestParams_MaxUint16Value verifies the maximum representable parameter value
// is accepted and the cursor clamps to the screen.
func TestParams_MaxUint16Value(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b[65535B"))
	if row, _ := s.CursorPos(); row != s.Height-1 {
		t.Errorf("CSI 65535 B: row=%d, want %d", row, s.Height-1)
	}
}

// TestParams_OverflowThenValidRecovers verifies the parser recovers after a
// parameter-count overflow: a following well-formed sequence works correctly.
func TestParams_OverflowThenValidRecovers(t *testing.T) {
	s := New(10, 20)
	var overflow []byte
	overflow = append(overflow, "\x1b["...)
	for i := range 50 {
		if i > 0 {
			overflow = append(overflow, ';')
		}
		overflow = append(overflow, '1')
	}
	overflow = append(overflow, 'm')
	s.Write(overflow)

	s.Write([]byte("\x1b[3;5H"))
	if row, col := s.CursorPos(); row != 2 || col != 4 {
		t.Errorf("post-overflow CUP = %d,%d, want 2,4", row, col)
	}
}

// TestParams_SubparamOverflowNoPanic verifies a CSI with far more colon
// subparams than the fixed array holds does not panic and the parser recovers.
func TestParams_SubparamOverflowNoPanic(t *testing.T) {
	var seq []byte
	seq = append(seq, "\x1b[38"...)
	for range 100 {
		seq = append(seq, ':', '5')
	}
	seq = append(seq, 'm')
	s := New(1, 10)
	s.Write(seq)
	s.Write([]byte("X"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("subparam overflow: X not written")
	}
}

// TestParams_MalformedSequencesRecover verifies a range of malformed parameter
// shapes leave the parser in Ground with the following printable applied.
func TestParams_MalformedSequencesRecover(t *testing.T) {
	cases := []struct {
		name string
		seq  string
	}{
		{"leading colon", "\x1b[:1mX"},
		{"trailing colon", "\x1b[1:mX"},
		{"only colons", "\x1b[:::mX"},
		{"multiple semicolons", "\x1b[;;;mX"},
		{"colon-semicolon mix", "\x1b[1:2:3;4:5;6mX"},
		{"empty group", "\x1b[38;;2mX"},
		{"all zeros", "\x1b[0;0;0;0;0mX"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(1, 10)
			s.Write([]byte(tc.seq))
			// The trailing 'X' printing at column 0 is the observable proof the
			// parser recovered to Ground: had it stayed mid-sequence, 'X' would
			// have been consumed as a parameter or final byte instead of printed.
			if s.Cells[0][0].Ch != 'X' {
				t.Errorf("after %q cell[0][0] = %q, want 'X' (parser recovered and printed the trailing char)", tc.seq, s.Cells[0][0].Ch)
			}
		})
	}
}

// TestPushParamZeroGroupsGuard verifies pushParam on a fresh screen (numGroups
// == 0) does not bump any group length.
func TestPushParamZeroGroupsGuard(t *testing.T) {
	s := New(1, 10)
	s.curParam = 7
	s.pushParam(false)
	if s.pGroupLen[0] != 0 {
		t.Errorf("pushParam with numGroups==0: pGroupLen[0] = %d, want 0", s.pGroupLen[0])
	}
}

// TestPushParamNewGroupCap verifies pushParam will not open a new group past the
// group cap.
func TestPushParamNewGroupCap(t *testing.T) {
	s := New(1, 10)
	s.numGroups = maxParams
	s.numParams = 0
	s.pushParam(true)
	if s.numGroups != maxParams {
		t.Errorf("pushParam(true) at numGroups==maxParams: numGroups = %d, want %d", s.numGroups, maxParams)
	}
}

// TestFinalizeParamsFullArrayGuard verifies finalizeParams returns early when
// the parameter array is full rather than writing out of range.
func TestFinalizeParamsFullArrayGuard(t *testing.T) {
	s := New(1, 10)
	s.numParams = maxParams
	s.finalizeParams()
	if s.numParams != maxParams {
		t.Errorf("finalizeParams at numParams==maxParams: numParams = %d, want %d", s.numParams, maxParams)
	}
}

// TestFinalizeParamsFreshScreen verifies finalizeParams pushes exactly one param
// on a fresh screen and skips the group bump when there are no groups.
func TestFinalizeParamsFreshScreen(t *testing.T) {
	s := New(1, 10)
	s.curParam = 9
	s.finalizeParams()
	if s.numParams != 1 {
		t.Errorf("finalizeParams: numParams = %d, want 1", s.numParams)
	}
	if s.pGroupLen[0] != 0 {
		t.Errorf("finalizeParams with numGroups==0: pGroupLen[0] = %d, want 0", s.pGroupLen[0])
	}
}

// TestParamGroupStartIdxClamp verifies paramGroup clamps a group's length so
// startIdx+length stays within the fixed array even with a corrupt stored
// length.
func TestParamGroupStartIdxClamp(t *testing.T) {
	s := New(1, 80)
	s.numGroups = 2
	s.pGroupLen[0] = 30 // group 1 begins at index 30
	s.pGroupLen[30] = 5 // raw length 5 -> 30+5=35 must clamp to 2
	g := s.paramGroup(1)
	if g.Len != 2 {
		t.Errorf("paramGroup(1): Len = %d, want 2 (length clamped within maxParams)", g.Len)
	}
}
