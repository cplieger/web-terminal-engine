package vt

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzOSC7501 feeds arbitrary report bodies. Beyond not panicking, the record
// set stays within its cap, the folded status holds only defined values, the
// only reply is the fixed probe answer, and splitting the sequence into single
// bytes yields the same result as one write.
func FuzzOSC7501(f *testing.F) {
	for _, seed := range []string{
		"state=working", "state=blocked:kind=permission:app=x", "?", "state=clear:id=a",
		"state=done:msg=" + b64("ok"), "state=working:id=a/b:progress=50", "state=done:msg=QQ=",
		" state = idle ", "state=working:kind=permission", "state=blocked:id=a//b", ":::", "state=working:state=",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		seq := append(append([]byte("\x1b]7501;"), body...), '\a')
		whole := New(24, 80)
		for range 3 {
			whole.Write(seq)
		}
		split := New(24, 80)
		for range 3 {
			for i := range seq {
				split.Write(seq[i : i+1])
			}
		}
		if len(whole.program.records) > programStatusMaxRecords {
			t.Fatalf("records = %d, over the cap", len(whole.program.records))
		}
		got := whole.ProgramStatus()
		switch got.State {
		case ProgramStateIdle, ProgramStateWorking, ProgramStateDone, ProgramStateBlocked, ProgramStateError:
		default:
			t.Fatalf("State = %q", got.State)
		}
		if got.Progress < -1 || got.Progress > 100 {
			t.Fatalf("Progress = %d", got.Progress)
		}
		if got.Kind != "" && got.State != ProgramStateBlocked {
			t.Fatalf("Kind = %q on State %q", got.Kind, got.State)
		}
		// A control byte can end the OSC early and start a sequence of its own
		// that answers (DA, DSR), so the reply is judged only without one.
		if !bytes.ContainsFunc(body, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			if r := whole.TakeResponse(); strings.ReplaceAll(string(r), programStatusReply, "") != "" {
				t.Fatalf("reply carries more than the probe answer: %q", r)
			}
		}
		if sg := split.ProgramStatus(); sg != got || split.ProgramReportSeq() != whole.ProgramReportSeq() ||
			len(split.program.records) != len(whole.program.records) {
			t.Fatalf("byte-split write %+v seq %d differs from one write %+v seq %d", sg, split.ProgramReportSeq(), got, whole.ProgramReportSeq())
		}
	})
}
