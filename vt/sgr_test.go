package vt

import "testing"

// TestSGRRoundTrip verifies sgrSequence faithfully re-emits every Style
// attribute: parsing the emitted sequence reproduces the original Style.
func TestSGRRoundTrip(t *testing.T) {
	want := Style{
		Bold:            true,
		Dim:             true,
		Italic:          true,
		DoubleUnderline: true,
		Overline:        true,
		Blink:           true,
		Inverse:         true,
		Strikethrough:   true,
		Hidden:          true,
		FG:              Color{Type: 3, R: 10, G: 20, B: 30},
		BG:              Color{Type: 2, Val: 200},
		UnderlineColor:  Color{Type: 3, R: 1, G: 2, B: 3},
	}
	s := New(1, 4)
	s.Write([]byte(sgrSequence(want) + "X"))
	if got := s.Cells[0][0].Style; got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}
