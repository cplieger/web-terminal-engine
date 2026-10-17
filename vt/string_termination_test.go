package vt

import (
	"strings"
	"testing"
)

// stringOutcome is what one byte class does to an open OSC, DCS, SOS, PM or
// APC string. applied is the only outcome whose handler runs; dropped,
// cancelled and ignored all end the string without one, and payload keeps it
// open.
type stringOutcome int

const (
	applied stringOutcome = iota
	dropped
	cancelled
	ignored
	payload
)

// TestStringTermination is the source of the "String terminators" table in
// docs/non-goals.md: one row per string kind, one cell per byte class.
func TestStringTermination(t *testing.T) {
	type kind struct {
		name string
		open string // introducer plus a payload that applies when it ends
		over string // introducer plus a payload past any cap
	}
	pad := strings.Repeat("a", maxOSCLen+1)
	rows := []struct {
		name  string
		kinds []kind
		cells [6]stringOutcome // in the order of columns below
	}{
		{"OSC", []kind{
			{"osc", "\x1b]2;t", "\x1b]2;" + pad},
		}, [6]stringOutcome{applied, applied, applied, payload, cancelled, dropped}},
		{"DCS_query", []kind{
			{"decrqss", "\x1bP$qm", "\x1bP$q" + strings.Repeat("m", maxDCSLen+1)},
			{"xtgettcap", "\x1bP+q436F", "\x1bP+q" + strings.Repeat("436F;", maxDCSLen/5+1)},
		}, [6]stringOutcome{payload, applied, applied, payload, cancelled, dropped}},
		{"other_DCS", []kind{
			{"unknown_final", "\x1bPzabc", "\x1bPz" + pad},
			{"malformed_header", "\x1bP1<abc", "\x1bP1<" + pad},
		}, [6]stringOutcome{payload, ignored, ignored, payload, cancelled, ignored}},
		{"SOS_PM_APC", []kind{
			{"sos", "\x1bXabc", "\x1bX" + pad},
			{"pm", "\x1b^abc", "\x1b^" + pad},
			{"apc", "\x1b_abc", "\x1b_" + pad},
		}, [6]stringOutcome{payload, ignored, ignored, payload, cancelled, ignored}},
	}
	// Each case writes the string, a terminator and "X"; endedRow is row 0 once
	// the string has ended.
	type term struct{ name, bytes string }
	columns := []struct {
		terms    []term
		overCap  bool
		endedRow string
	}{
		{[]term{{"BEL", "\x07"}}, false, "X"},
		{[]term{{"ST", "\x1b\\"}}, false, "X"},
		{[]term{{"ESC_starting_a_CSI", "\x1b[2C"}}, false, "  X"},
		{[]term{{"raw_0x9C", "\x9c"}}, false, "X"},
		{[]term{{"CAN", "\x18"}, {"SUB", "\x1a"}}, false, "X"},
		{[]term{{"over_cap_then_ST", "\x1b\\"}}, true, "X"},
	}

	for _, row := range rows {
		for _, k := range row.kinds {
			for ci, col := range columns {
				want := row.cells[ci]
				for _, tm := range col.terms {
					t.Run(row.name+"_"+k.name+"_"+tm.name, func(t *testing.T) {
						s := New(2, 10)
						in := k.open
						if col.overCap {
							in = k.over
						}
						s.Write([]byte(in + tm.bytes + "X"))
						gotApplied := s.Title != "" || len(s.response) > 0
						if gotApplied != (want == applied) {
							t.Errorf("handler ran = %v, want %v (Title %q, reply %q)", gotApplied, want == applied, s.Title, s.response)
						}
						wantRow := col.endedRow
						if want == payload {
							wantRow = ""
						}
						if got := s.RowString(0); got != wantRow {
							t.Errorf("RowString(0) = %q, want %q (string still open = %v)", got, wantRow, want == payload)
						}
					})
				}
			}
		}
	}
}
