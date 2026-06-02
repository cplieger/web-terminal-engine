package vt

import "testing"

// =============================================================
// REDTEAM2 PROBE 3: param/subparam edge cases
// =============================================================

func TestRT2_Params_EmptyParams_CSI_m(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("\x1b[1m")) // bold
	s.Write([]byte("\x1b[m"))  // reset (empty param = SGR 0)
	s.Write([]byte("X"))
	if s.Cells[0][0].Ch != 'X' {
		t.Fatal("X not written")
	}
	if s.Cells[0][0].Style.Bold {
		t.Error("empty SGR did not reset bold")
	}
}

func TestRT2_Params_LeadingColon(t *testing.T) {
	// Leading colon: ":1m" — colon before any digit
	s := New(1, 10)
	s.Write([]byte("\x1b[:1mX"))
	// Should not panic; behavior is implementation-defined but must not crash
	if s.Cells[0][0].Ch != 'X' {
		t.Error("leading colon: X not written")
	}
}

func TestRT2_Params_TrailingColon(t *testing.T) {
	// "1:m" — trailing colon
	s := New(1, 10)
	s.Write([]byte("\x1b[1:mX"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("trailing colon: X not written")
	}
}

func TestRT2_Params_MultipleSemicolons(t *testing.T) {
	// ";;;" = multiple empty params
	s := New(1, 10)
	s.Write([]byte("\x1b[;;;mX"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("multiple semicolons: X not written")
	}
}

func TestRT2_Params_ColonSemicolonMix(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("\x1b[1:2:3;4:5;6mX"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("colon/semicolon mix: X not written")
	}
}

func TestRT2_Params_FixedArrayOverflow_NoPanic(t *testing.T) {
	// Generate CSI with 100 params (way over maxParams=32)
	var seq []byte
	seq = append(seq, "\x1b["...)
	for i := range 100 {
		if i > 0 {
			seq = append(seq, ';')
		}
		seq = append(seq, '1')
	}
	seq = append(seq, 'H')
	s := New(10, 10)
	s.Write(seq)
	row, col := s.CursorPos()
	if row < 0 || row >= s.Height || col < 0 || col >= s.Width {
		t.Fatalf("OOB after param overflow: %d,%d", row, col)
	}
}

func TestRT2_Params_FixedArrayOverflow_Subparams(t *testing.T) {
	// 100 colon-separated subparams
	var seq []byte
	seq = append(seq, "\x1b[38"...)
	for range 100 {
		seq = append(seq, ':')
		seq = append(seq, '5')
	}
	seq = append(seq, 'm')
	s := New(1, 10)
	s.Write(seq)
	s.Write([]byte("X"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("subparam overflow: X not written")
	}
}

func TestRT2_Params_HugeValue_Clamped(t *testing.T) {
	s := New(10, 10)
	// CSI 99999 H — should clamp to maxCSIArgValue
	s.Write([]byte("\x1b[99999H"))
	row, _ := s.CursorPos()
	// Should clamp to bottom row (Height-1)
	if row >= s.Height {
		t.Fatalf("huge param: row=%d >= Height=%d", row, s.Height)
	}
}

func TestRT2_Params_MaxUint16Value(t *testing.T) {
	s := New(10, 10)
	// 65535 is max
	s.Write([]byte("\x1b[65535;65535H"))
	row, col := s.CursorPos()
	if row >= s.Height || col >= s.Width {
		t.Fatalf("65535 param: %d,%d OOB", row, col)
	}
}

func TestRT2_Params_OverflowThenValid(t *testing.T) {
	s := New(10, 20)
	// Overflow params, then follow up with a valid sequence
	var overflow []byte
	overflow = append(overflow, "\x1b["...)
	for i := range 50 {
		if i > 0 {
			overflow = append(overflow, ';')
		}
		overflow = append(overflow, '1')
	}
	overflow = append(overflow, 'm') // this SGR will be partially ignored
	s.Write(overflow)

	// Next sequence must work correctly (parser state resets on clear)
	s.Write([]byte("\x1b[3;5H"))
	row, col := s.CursorPos()
	if row != 2 || col != 4 {
		t.Errorf("post-overflow CUP: got %d,%d want 2,4", row, col)
	}
}

func TestRT2_Params_AllZeros(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[0;0;0;0;0m"))
	// All zeros = SGR reset, no panic
	s.Write([]byte("X"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("all-zero params: X not written")
	}
}

func TestRT2_Params_EmptyGroupHandling(t *testing.T) {
	s := New(1, 10)
	// "38;;2m" — empty param between 38 and 2
	s.Write([]byte("\x1b[38;;2mX"))
	// Should not panic
	if s.Cells[0][0].Ch != 'X' {
		t.Error("empty group: X not written")
	}
}

func TestRT2_Params_ColonOnly(t *testing.T) {
	s := New(1, 10)
	// Just colons
	s.Write([]byte("\x1b[:::mX"))
	if s.Cells[0][0].Ch != 'X' {
		t.Error("colon-only: X not written")
	}
}
