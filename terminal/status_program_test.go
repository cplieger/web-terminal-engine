package terminal

import (
	"fmt"
	"strings"
	"testing"
)

// Status derivation for a program that speaks OSC 7501 (Program Status Protocol
// rev 0.4, https://www.superlogical.com/rex/docs/build/program-status).

func newProgramManager(t *testing.T, opts ...ManagerOption) (*SessionManager, SessionID, *Handler) {
	t.Helper()
	m := NewSessionManager(catFactory, opts...)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return m, id, handlerOf(t, m, id)
}

func program7501(body string) []byte { return []byte("\x1b]7501;" + body + "\x07") }

func TestStatusProgramStatusMapping(t *testing.T) {
	cases := []struct{ state, want string }{
		{"working", StatusWorking},
		{"blocked:kind=permission", StatusInput},
		{"done", StatusDone},
		{"error", StatusFailed},
		{"idle", StatusIdle},
		{"clear", StatusIdle},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			m, id, h := newProgramManager(t)
			h.handlePTYData(program7501("state=" + c.state))
			ev := eventFor(t, m.diffStatuses(), id)
			if ev.Status != c.want {
				t.Errorf("Status = %q, want %q", ev.Status, c.want)
			}
			if !ev.ReportsActivity {
				t.Error("ReportsActivity = false after an OSC 7501 report")
			}
		})
	}
}

func TestStatusProgramStatusOutranksProgress(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"idle beside a state-4 bar is idle, not warning", "\x1b]9;4;4;72\x07" + string(program7501("state=idle")), StatusIdle},
		{"done beside an active bar is done", "\x1b]9;4;3\x07" + string(program7501("state=done")), StatusDone},
		{"working beside an error bar is working", "\x1b]9;4;2\x07" + string(program7501("state=working")), StatusWorking},
		{"a 9;4 report after 7501 changes nothing", string(program7501("state=idle")) + "\x1b]9;4;1;50\x07", StatusIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
			h.handlePTYData([]byte(c.input))
			if ev := eventFor(t, m.diffStatuses(), id); ev.Status != c.want {
				t.Errorf("Status = %q, want %q", ev.Status, c.want)
			}
		})
	}
	t.Run("blocked falls to idle beside a cleared bar", func(t *testing.T) {
		m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
		h.handlePTYData([]byte("\x1b]9;4;0\x07"))
		h.handlePTYData(program7501("state=blocked:kind=question"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusInput {
			t.Fatalf("Status = %q, want %q", ev.Status, StatusInput)
		}
		h.handlePTYData(program7501("state=idle"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusIdle {
			t.Errorf("after idle Status = %q, want %q", ev.Status, StatusIdle)
		}
	})
}

func TestStatusProgramStatusKeepsTheProgressBar(t *testing.T) {
	cases := []struct {
		name, input string
		want        int
	}{
		{"a 9;4 percentage draws beside 7501 idle", "\x1b]9;4;4;72\x07" + string(program7501("state=idle")), 72},
		{"a 9;4 percentage draws beside 7501 done", "\x1b]9;4;1;40\x07" + string(program7501("state=done")), 40},
		{"the report's own progress wins", "\x1b]9;4;4;72\x07" + string(program7501("state=working:progress=30")), 30},
		{"no percentage anywhere is absent", string(program7501("state=working")), -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, id, h := newProgramManager(t)
			h.handlePTYData([]byte(c.input))
			if ev := eventFor(t, m.diffStatuses(), id); ev.ProgressValue != c.want {
				t.Errorf("ProgressValue = %d, want %d", ev.ProgressValue, c.want)
			}
		})
	}
}

func TestStatusClassifierNewerThanReportWins(t *testing.T) {
	m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
	h.handlePTYData(program7501("state=error"))
	if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusFailed {
		t.Fatalf("after error Status = %q, want %q", ev.Status, StatusFailed)
	}
	h.handlePTYData([]byte(approvalNeeded))
	ev := eventFor(t, m.diffStatuses(), id)
	if ev.Status != StatusInput {
		t.Fatalf("a notification after the report: Status = %q, want %q", ev.Status, StatusInput)
	}
	if quiet := m.diffStatuses(); len(quiet) != 0 {
		t.Fatalf("the latch did not hold: a quiet sweep emitted %+v", quiet)
	}
	h.handlePTYData(program7501("state=working"))
	if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusWorking {
		t.Fatalf("the next report: Status = %q, want %q", ev.Status, StatusWorking)
	}
	if st, _, _ := m.StatusLatch(id); st != "" {
		t.Errorf("StatusLatch after the next report = %q, want cleared", st)
	}

	t.Run("a notification older than the report loses", func(t *testing.T) {
		m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
		h.handlePTYData([]byte(approvalNeeded))
		h.handlePTYData(program7501("state=done"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusDone {
			t.Errorf("Status = %q, want %q", ev.Status, StatusDone)
		}
	})
	t.Run("a newer notification latches any status the classifier returns", func(t *testing.T) {
		warningClassifier := func(string) (string, bool) { return StatusWarning, true }
		m, id, h := newProgramManager(t, WithStatusClassifier(warningClassifier))
		h.handlePTYData(program7501("state=working"))
		h.handlePTYData([]byte(approvalNeeded))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusWarning {
			t.Errorf("Status = %q, want %q", ev.Status, StatusWarning)
		}
	})
	t.Run("an unclassified notification leaves the report", func(t *testing.T) {
		m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
		h.handlePTYData(program7501("state=error"))
		h.handlePTYData([]byte("\x1b]9;something else\x07"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusFailed {
			t.Errorf("Status = %q, want %q", ev.Status, StatusFailed)
		}
	})
	t.Run("a winning notification supersedes the older report's occurrence", func(t *testing.T) {
		m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
		h.handlePTYData(program7501("state=blocked:kind=question:id=a"))
		_ = m.diffStatuses()
		h.handlePTYData(program7501("state=blocked:kind=permission:id=b"))
		h.handlePTYData([]byte(approvalNeeded))
		status, seq := sweepSeq(t, m, id)
		if status != StatusInput {
			t.Fatalf("after the notification Status = %q, want %q", status, StatusInput)
		}
		if !m.markSessionSeen(id, seq) {
			t.Fatalf("markSessionSeen(%d) refused", seq)
		}
		h.handlePTYData(program7501("state=blocked:kind=permission:id=b:progress=5"))
		if status, after := sweepSeq(t, m, id); status != StatusInput || after != seq {
			t.Errorf("a progress-only report: (%q, StatusSeq %d), want (%q, %d): nothing new to attend to", status, after, StatusInput, seq)
		}
	})
}

// TestStatusClassifierLatchHoldsUntilAStatusChange starts from an OSC 7501
// error and a classified notification latched over it: a report that repeats
// the status keeps the latch, and one that changes it, a clear that leaves no
// record included, clears it.
func TestStatusClassifierLatchHoldsUntilAStatusChange(t *testing.T) {
	cases := []struct {
		name, report, want string
		keepsLatch         bool
	}{
		{"the same state again", "state=error", StatusInput, true},
		{"a new record the status does not come from", "state=working:id=w", StatusInput, true},
		{"another state", "state=working", StatusWorking, false},
		{"a clear that leaves no record", "state=clear", StatusIdle, false},
		{"a blocked child the status comes from", "state=blocked:kind=question:id=b", StatusInput, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
			h.handlePTYData(program7501("state=error"))
			h.handlePTYData([]byte(approvalNeeded))
			if status, _ := sweepSeq(t, m, id); status != StatusInput {
				t.Fatalf("after the notification Status = %q, want %q", status, StatusInput)
			}
			h.handlePTYData(program7501(c.report))
			if status, _ := sweepSeq(t, m, id); status != c.want {
				t.Errorf("Status = %q, want %q", status, c.want)
			}
			if latch, _, _ := m.StatusLatch(id); (latch == StatusInput) != c.keepsLatch {
				t.Errorf("StatusLatch = %q, want kept = %v", latch, c.keepsLatch)
			}
		})
	}
}

// TestStatusClassifierStatusOutsideTheBuiltInSet pins WithStatusClassifier's
// contract that any status it returns latches as given, not only a Status* value.
func TestStatusClassifierStatusOutsideTheBuiltInSet(t *testing.T) {
	const custom = "paused-custom"
	customClassifier := func(string) (string, bool) { return custom, true }
	cases := []struct{ name, before string }{
		{"with no OSC 7501 report", ""},
		{"newer than an OSC 7501 report", string(program7501("state=working"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, id, h := newProgramManager(t, WithStatusClassifier(customClassifier))
			h.handlePTYData([]byte(c.before + approvalNeeded))
			if ev := eventFor(t, m.diffStatuses(), id); ev.Status != custom {
				t.Fatalf("status event Status = %q, want %q", ev.Status, custom)
			}
			if quiet := m.diffStatuses(); len(quiet) != 0 {
				t.Errorf("the latch did not hold: a quiet sweep emitted %+v", quiet)
			}
			if list := m.List(); len(list) != 1 || list[0].Status != custom {
				t.Errorf("List() = %+v, want one session with Status %q", list, custom)
			}
		})
	}
}

// TestComputeStatusConsumesTheProgramOccurrence drives every return of
// computeStatus on one tracker: an occurrence a path does not show must not
// read as fresh on a later sweep.
func TestComputeStatusConsumesTheProgramOccurrence(t *testing.T) {
	m := &SessionManager{classifier: inputClassifier}
	tr := &statusTracker{}
	steps := []struct {
		name       string
		in         statusRaw
		wantStatus string
		wantLatch  string
		wantOcc    uint64
		wantFresh  bool
	}{
		{
			name:       "7501 wins",
			in:         statusRaw{programSeen: true, programState: "blocked", programSeq: 1, programOccurrence: 1, progress: -1},
			wantStatus: StatusInput, wantOcc: 1, wantFresh: true,
		},
		{
			name: "a newer notification wins over a newer blocked record",
			in: statusRaw{
				programSeen: true, programState: "blocked", programSeq: 2, programOccurrence: 2, progress: -1,
				notifMsg: "Approval needed", notifSeq: 3,
			},
			wantStatus: StatusInput, wantLatch: StatusInput, wantOcc: 2, wantFresh: true,
		},
		{
			name:       "a report that repeats the status keeps the latch",
			in:         statusRaw{programSeen: true, programState: "blocked", programSeq: 4, programOccurrence: 2, progress: -1, notifSeq: 3},
			wantStatus: StatusInput, wantLatch: StatusInput, wantOcc: 2,
		},
		{
			name:       "7501 idle",
			in:         statusRaw{programSeen: true, programState: "idle", programSeq: 5, programOccurrence: 5, progress: -1, notifSeq: 3},
			wantStatus: StatusIdle, wantOcc: 5,
		},
		{
			name:       "9;4 fallback after RIS",
			in:         statusRaw{programSeq: 5, progress: 1, notifSeq: 3},
			wantStatus: StatusWorking,
		},
		{
			name:       "a report after RIS",
			in:         statusRaw{programSeen: true, programState: "done", programSeq: 6, programOccurrence: 6, progress: -1, notifSeq: 3},
			wantStatus: StatusDone, wantOcc: 6, wantFresh: true,
		},
		{
			name:       "exit",
			in:         statusRaw{exited: true, programSeen: true, programState: "done", programSeq: 7, programOccurrence: 7, progress: -1, notifSeq: 3},
			wantStatus: StatusExited, wantOcc: 7,
		},
	}
	for _, st := range steps {
		status, fresh := m.computeStatus(&st.in, tr)
		if status != st.wantStatus || fresh != st.wantFresh {
			t.Errorf("%s: (%q, fresh %v), want (%q, %v)", st.name, status, fresh, st.wantStatus, st.wantFresh)
		}
		if tr.latched != st.wantLatch || tr.programOccurrence != st.wantOcc {
			t.Errorf("%s: tracker latch %q occurrence %d, want %q %d", st.name, tr.latched, tr.programOccurrence, st.wantLatch, st.wantOcc)
		}
	}
}

func TestStatusProgramStatusFallbackWithoutReport(t *testing.T) {
	m, id, h := newProgramManager(t, WithStatusClassifier(inputClassifier))
	steps := []struct {
		name, input, want string
	}{
		{"9;4 active is working", "\x1b]9;4;3\x07", StatusWorking},
		{"a classified notification latches", approvalNeeded, StatusInput},
		{"9;4 active clears the latch", "\x1b]9;4;1;10\x07", StatusWorking},
		{"9;4 state 4 is warning", "\x1b]9;4;4;72\x07", StatusWarning},
		{"7501 takes over", string(program7501("state=done")), StatusDone},
		{"RIS hands status back to 9;4", "\x1bc\x1b]9;4;2\x07", StatusFailed},
	}
	for _, st := range steps {
		h.handlePTYData([]byte(st.input))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Status != st.want {
			t.Errorf("%s: Status = %q, want %q", st.name, ev.Status, st.want)
		}
	}
}

// sweepSeq sweeps once and returns the session's committed status and number.
func sweepSeq(t *testing.T, m *SessionManager, id SessionID) (string, uint64) {
	t.Helper()
	_ = m.diffStatuses()
	li := listedInfo(t, m, id)
	return li.Status, li.StatusSeq
}

// occurrenceStep is one sweep of TestStatusOccurrence: the reports it parses
// (an entry starting with ESC is written as given), then the status,
// ProgramStatus.Seq and whether StatusSeq rose.
type occurrenceStep struct {
	reports []string
	status  string
	seq     uint64
	isNew   bool
}

// TestStatusOccurrence pins SessionInfo.StatusSeq's rule for OSC 7501
// reports, and ProgramStatus.Seq beneath it, one report sequence per case.
func TestStatusOccurrence(t *testing.T) {
	cases := []struct {
		name  string
		steps []occurrenceStep // the first sets up; isNew compares each later one with the step before
	}{
		{"a same-state rewrite retakes the blocked fold", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a"}, StatusInput, 1, true},
			{[]string{"state=blocked:kind=question:id=b"}, StatusInput, 2, true},
			{[]string{"state=blocked:kind=question:id=a"}, StatusInput, 2, false},
		}},
		{"an identical done resent", []occurrenceStep{
			{[]string{"state=done"}, StatusDone, 1, true},
			{[]string{"state=done"}, StatusDone, 1, false},
		}},
		{"a progress tick on a blocked root", []occurrenceStep{
			{[]string{"state=blocked:kind=auth:progress=10"}, StatusInput, 1, true},
			{[]string{"state=blocked:kind=auth:progress=20"}, StatusInput, 1, false},
		}},
		{"two blocked children trading the fold on progress ticks", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=permission:id=b"}, StatusInput, 2, true},
			{[]string{"state=blocked:kind=question:id=a:progress=5"}, StatusInput, 2, false},
			{[]string{"state=blocked:kind=permission:id=b:progress=5"}, StatusInput, 2, false},
		}},
		{"clearing the newest blocked child exposes an older one", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=permission:id=b"}, StatusInput, 2, true},
			{[]string{"state=clear:id=b"}, StatusInput, 3, true},
		}},
		{"the newest blocked child resuming exposes an older one", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=question:id=b"}, StatusInput, 2, true},
			{[]string{"state=working:id=b"}, StatusInput, 3, true},
		}},
		{"a blocked root resuming exposes a blocked child", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=auth"}, StatusInput, 2, true},
			{[]string{"state=working"}, StatusInput, 3, true},
		}},
		{"clearing a blocked child the status does not show", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=permission:id=b"}, StatusInput, 2, true},
			{[]string{"state=clear:id=a"}, StatusInput, 2, false},
		}},
		{"a new blocked id the status comes from", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a"}, StatusInput, 1, true},
			{[]string{"state=blocked:kind=permission:id=b"}, StatusInput, 2, true},
		}},
		{"a new id the status does not come from", []occurrenceStep{
			{[]string{"state=done"}, StatusDone, 1, true},
			{[]string{"state=working:id=worker"}, StatusDone, 1, false},
		}},
		{"a blocked root changing kind", []occurrenceStep{
			{[]string{"state=blocked:kind=auth"}, StatusInput, 1, true},
			{[]string{"state=blocked:kind=question"}, StatusInput, 2, true},
		}},
		{"an older blocked child changing kind retakes the fold", []occurrenceStep{
			{[]string{"state=blocked:kind=question:id=a", "state=blocked:kind=permission:id=b"}, StatusInput, 2, true},
			{[]string{"state=blocked:kind=auth:id=a"}, StatusInput, 3, true},
		}},
		{"done, working, done in one sweep", []occurrenceStep{
			{[]string{"state=done"}, StatusDone, 1, true},
			{[]string{"state=working", "state=done"}, StatusDone, 3, true},
		}},
		{"a child clear under a root error", []occurrenceStep{
			{[]string{"state=error", "state=working:id=w"}, StatusFailed, 1, true},
			{[]string{"state=clear:id=w"}, StatusFailed, 1, false},
		}},
		{"an unrelated record under a winning blocked child", []occurrenceStep{
			{[]string{"state=working", "state=blocked:kind=question:id=a"}, StatusInput, 2, true},
			{[]string{"state=idle:id=b"}, StatusInput, 2, false},
		}},
		{"a prompt drops a new blocked child before the sweep", []occurrenceStep{
			{[]string{"state=done"}, StatusDone, 1, true},
			{[]string{"state=blocked:kind=question:id=b", "\x1b]133;A\a"}, StatusDone, 2, true},
		}},
		{"an idle root cleared and reported again", []occurrenceStep{
			{[]string{"state=idle"}, StatusIdle, 1, true},
			{[]string{"state=clear"}, StatusIdle, 2, false},
			{[]string{"state=idle:app=x"}, StatusIdle, 3, false},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, id, h := newProgramManager(t)
			var last uint64
			for i, st := range c.steps {
				for _, r := range st.reports {
					if strings.HasPrefix(r, "\x1b") {
						h.handlePTYData([]byte(r))
					} else {
						h.handlePTYData(program7501(r))
					}
				}
				status, seq := sweepSeq(t, m, id)
				if got := h.statusSnapshot().program.Seq; status != st.status || got != st.seq {
					t.Errorf("step %d: (%q, ProgramStatus.Seq %d), want (%q, %d)", i, status, got, st.status, st.seq)
				}
				if i > 0 && (seq > last) != st.isNew {
					t.Errorf("step %d: StatusSeq %d -> %d, want a new occurrence %v", i, last, seq, st.isNew)
				}
				last = seq
			}
		})
	}
}

// statusTableRow is one row of docs/sessions.md's status table: the status it
// gives and its condition over one sweep's inputs and the latch that sweep
// leaves.
type statusTableRow struct {
	holds  func(in *statusRaw, latched string) bool
	status func(latched string) string
	name   string
}

func fixed(status string) func(string) string { return func(string) string { return status } }

func runningUnlatched(in *statusRaw, latched string) bool { return !in.exited && latched == "" }

// statusTable mirrors docs/sessions.md's status table row for row.
var statusTable = []statusTableRow{
	{name: "exited", status: fixed(StatusExited), holds: func(in *statusRaw, _ string) bool { return in.exited && !in.crashed }},
	{name: "crashed", status: fixed(StatusCrashed), holds: func(in *statusRaw, _ string) bool { return in.exited && in.crashed }},
	{name: "latched", status: func(l string) string { return l }, holds: func(in *statusRaw, l string) bool { return !in.exited && l != "" }},
	{name: "working", status: fixed(StatusWorking), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && (in.programSeen && in.programState == "working" ||
			!in.programSeen && (in.progress == 1 || in.progress == 3))
	}},
	{name: "failed", status: fixed(StatusFailed), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && (in.programSeen && in.programState == "error" || !in.programSeen && in.progress == 2)
	}},
	{name: "warning", status: fixed(StatusWarning), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && !in.programSeen && in.progress == 4
	}},
	{name: "input", status: fixed(StatusInput), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && in.programSeen && in.programState == "blocked"
	}},
	{name: "done", status: fixed(StatusDone), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && in.programSeen && in.programState == "done"
	}},
	{name: "idle", status: fixed(StatusIdle), holds: func(in *statusRaw, l string) bool {
		return runningUnlatched(in, l) && (in.programSeen && in.programState == "idle" ||
			!in.programSeen && (in.progress == 0 || in.progress == -1))
	}},
}

// TestStatusTableRowsAreExclusive runs computeStatus over every combination of
// liveness, OSC 7501 fold state, OSC 9;4 state and latch timing, and checks
// that exactly one statusTable row holds and gives the derived status. A
// folded state of idle stands for no record too: vt reports both alike.
func TestStatusTableRowsAreExclusive(t *testing.T) {
	type latchCase struct {
		name           string
		trSeq, notifSq uint64 // an earlier sweep's latch number (0: none); this sweep's notification number
	}
	latches := []latchCase{
		{"no latch", 0, 0},
		{"a notification this sweep after the report", 0, 7},
		{"an earlier latch after the report", 6, 6},
		{"an earlier latch before the report", 3, 3},
	}
	type live struct{ exited, crashed bool }
	m := &SessionManager{classifier: inputClassifier}
	for _, lv := range []live{{false, false}, {true, false}, {true, true}} {
		for _, state := range []string{"", "working", "blocked", "done", "error", "idle"} {
			for _, progress := range []int{-1, 0, 1, 2, 3, 4} {
				for _, lc := range latches {
					in := statusRaw{
						exited: lv.exited, crashed: lv.crashed, programSeen: state != "", programState: state,
						programSeq: 5, progress: progress, notifSeq: lc.notifSq, notifMsg: "Approval needed",
					}
					if in.programSeen {
						in.programOccurrence = 5
					}
					tr := &statusTracker{notifSeen: lc.trSeq, latchSeq: lc.trSeq}
					if lc.trSeq != 0 {
						tr.latched = StatusInput
					}
					status, _ := m.computeStatus(&in, tr)
					name := fmt.Sprintf("exited %v crashed %v, 7501 %q, 9;4 %d, %s", lv.exited, lv.crashed, state, progress, lc.name)
					var held []string
					for _, row := range statusTable {
						if row.holds(&in, tr.latched) {
							held = append(held, row.name)
							if want := row.status(tr.latched); status != want {
								t.Errorf("%s: status %q, but row %s gives %q", name, status, row.name, want)
							}
						}
					}
					if len(held) != 1 {
						t.Errorf("%s: rows %v hold, want exactly one", name, held)
					}
				}
			}
		}
	}
}
