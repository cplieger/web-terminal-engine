package terminal

import (
	"sync/atomic"
	"testing"
)

const permissionRequired = "\x1b]9;Permission required\x07"

func newLatchManager(t *testing.T) (*SessionManager, SessionID, *Handler) {
	t.Helper()
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return m, id, handlerOf(t, m, id)
}

func armInputLatch(t *testing.T, m *SessionManager, id SessionID, h *Handler) uint64 {
	t.Helper()
	h.handlePTYData([]byte(permissionRequired))
	if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusInput {
		t.Fatalf("after Permission required, event Status = %q, want %q", ev.Status, StatusInput)
	}
	status, seq, ok := m.StatusLatch(id)
	if !ok || status != StatusInput || seq == 0 {
		t.Fatalf("StatusLatch after arming = (%q, %d, %t), want (%q, >0, true)", status, seq, ok, StatusInput)
	}
	return seq
}

func TestWithdrawStatusLatchClearsInputAndTheSweepRecomputes(t *testing.T) {
	cases := []struct {
		name     string
		progress string
		want     string
	}{
		{name: "idle under a cleared progress state", progress: "\x1b]9;4;0\x07", want: StatusIdle},
		{name: "warning under the parked context percentage", progress: "\x1b]9;4;4;72\x07", want: StatusWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, id, h := newLatchManager(t)
			h.handlePTYData([]byte(tc.progress))
			seq := armInputLatch(t, m, id, h)
			if quiet := m.diffStatuses(); len(quiet) != 0 {
				t.Fatalf("a sweep with no news emitted %d event(s), want 0: the latch does not hold on its own", len(quiet))
			}

			if !m.WithdrawStatusLatch(id, StatusInput, seq) {
				t.Fatalf("WithdrawStatusLatch(%s, %q, %d) = false, want true", LogID(id), StatusInput, seq)
			}
			ev := eventFor(t, m.diffStatuses(), id)
			if ev.Status != tc.want {
				t.Errorf("sweep after the withdraw: event Status = %q, want %q", ev.Status, tc.want)
			}
			if !ev.ReportsActivity {
				t.Errorf("sweep after the withdraw: event ReportsActivity = false, want true (progress has been seen)")
			}
			if st, seq, ok := m.StatusLatch(id); st != "" || seq != 0 || !ok {
				t.Errorf("StatusLatch after the withdraw = (%q, %d, %t), want (\"\", 0, true)", st, seq, ok)
			}
			if infos := m.List(); len(infos) != 1 || infos[0].Status != tc.want {
				t.Errorf("List after the sweep reports %+v, want one session at %q", infos, tc.want)
			}
			if m.WithdrawStatusLatch(id, StatusInput, seq) {
				t.Errorf("WithdrawStatusLatch(%s, %q, %d) on a cleared latch = true, want false", LogID(id), StatusInput, seq)
			}
		})
	}
}

func TestWithdrawStatusLatchRefusals(t *testing.T) {
	m, asking, askH := newLatchManager(t)
	finished, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	askH.handlePTYData([]byte(permissionRequired))
	handlerOf(t, m, finished).handlePTYData([]byte("\x1b]9;Response complete\x07"))
	m.diffStatuses()
	askStatus, askSeq, askOK := m.StatusLatch(asking)
	if !askOK || askStatus != StatusInput {
		t.Fatalf("StatusLatch(asking) = (%q, %d, %t), want (%q, _, true)", askStatus, askSeq, askOK, StatusInput)
	}
	doneStatus, doneSeq, doneOK := m.StatusLatch(finished)
	if !doneOK || doneStatus != StatusDone {
		t.Fatalf("StatusLatch(finished) = (%q, %d, %t), want (%q, _, true)", doneStatus, doneSeq, doneOK, StatusDone)
	}

	cases := []struct {
		name    string
		id      SessionID
		want    string
		latch   string
		seq     uint64
		tracked bool
	}{
		{name: "an unknown session", id: "no-such-session", want: StatusInput, seq: askSeq},
		{name: "a done latch under an input withdrawal", id: finished, want: StatusInput, seq: doneSeq, latch: StatusDone, tracked: true},
		{name: "an input latch under a done withdrawal", id: asking, want: StatusDone, seq: askSeq, latch: StatusInput, tracked: true},
		{name: "a want outside the latchable pair", id: asking, want: StatusWorking, seq: askSeq, latch: StatusInput, tracked: true},
		{name: "an empty want", id: asking, want: "", seq: askSeq, latch: StatusInput, tracked: true},
		{name: "a seq the latch was not set by", id: asking, want: StatusInput, seq: askSeq + 1, latch: StatusInput, tracked: true},
		{name: "the zero seq", id: asking, want: StatusInput, seq: 0, latch: StatusInput, tracked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if m.WithdrawStatusLatch(tc.id, tc.want, tc.seq) {
				t.Fatalf("WithdrawStatusLatch(%s, %q, %d) = true, want false", LogID(tc.id), tc.want, tc.seq)
			}
			if st, _, ok := m.StatusLatch(tc.id); st != tc.latch || ok != tc.tracked {
				t.Errorf("after the refusal, StatusLatch(%s) = (%q, ok=%t), want (%q, ok=%t)",
					LogID(tc.id), st, ok, tc.latch, tc.tracked)
			}
		})
	}

	if !m.WithdrawStatusLatch(asking, StatusInput, askSeq) {
		t.Errorf("WithdrawStatusLatch(asking, %q, %d) = false, want true: the guards above refuse only the wrong call", StatusInput, askSeq)
	}
	if !m.WithdrawStatusLatch(finished, StatusDone, doneSeq) {
		t.Errorf("WithdrawStatusLatch(finished, %q, %d) = false, want true: a done latch is withdrawable as done", StatusDone, doneSeq)
	}
}

// TestWithdrawStatusLatchRefusesAWantOutsideThePairEvenWhenLatched is the one
// case the latch-equality guard cannot cover: a consumer classifier may latch
// any status string, and a withdrawal must still only ever clear input or done.
func TestWithdrawStatusLatchRefusesAWantOutsideThePairEvenWhenLatched(t *testing.T) {
	workingClassifier := func(string) (string, bool) { return StatusWorking, true }
	m := NewSessionManager(catFactory, WithStatusClassifier(workingClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handlerOf(t, m, id).handlePTYData([]byte(permissionRequired))
	m.diffStatuses()
	st, seq, ok := m.StatusLatch(id)
	if !ok || st != StatusWorking || seq != 1 {
		t.Fatalf("StatusLatch = (%q, %d, %t), want (%q, 1, true): the classifier's status did not latch", st, seq, ok, StatusWorking)
	}
	if m.WithdrawStatusLatch(id, StatusWorking, seq) {
		t.Fatalf("WithdrawStatusLatch(%s, %q, %d) = true, want false", LogID(id), StatusWorking, seq)
	}
	if st, _, _ := m.StatusLatch(id); st != StatusWorking {
		t.Errorf("after the refusal, StatusLatch = %q, want %q", st, StatusWorking)
	}
}

// TestWithdrawStatusLatchRefusesASeqARelatchSuperseded is the race the seq
// exists for: a consumer read (input, seq) and decided to withdraw, but a second
// ask was classified before its call landed.
func TestWithdrawStatusLatchRefusesASeqARelatchSuperseded(t *testing.T) {
	m, id, h := newLatchManager(t)
	stale := armInputLatch(t, m, id, h)

	h.handlePTYData([]byte(permissionRequired))
	if ev := eventFor(t, m.diffStatuses(), id); ev.NotificationSeq != stale+1 || ev.Status != StatusInput {
		t.Fatalf("second ask: event NotificationSeq = %d Status = %q, want %d / %q", ev.NotificationSeq, ev.Status, stale+1, StatusInput)
	}
	st, fresh, ok := m.StatusLatch(id)
	if !ok || st != StatusInput || fresh != stale+1 {
		t.Fatalf("StatusLatch after the second ask = (%q, %d, %t), want (%q, %d, true)", st, fresh, ok, StatusInput, stale+1)
	}

	if m.WithdrawStatusLatch(id, StatusInput, stale) {
		t.Fatalf("WithdrawStatusLatch(%s, %q, %d) with the superseded seq = true, want false", LogID(id), StatusInput, stale)
	}
	if st, seq, _ := m.StatusLatch(id); st != StatusInput || seq != fresh {
		t.Errorf("after the refused withdraw, StatusLatch = (%q, %d), want (%q, %d)", st, seq, StatusInput, fresh)
	}
	if !m.WithdrawStatusLatch(id, StatusInput, fresh) {
		t.Fatalf("WithdrawStatusLatch(%s, %q, %d) with the fresh seq = false, want true", LogID(id), StatusInput, fresh)
	}
	if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusIdle {
		t.Errorf("sweep after the fresh withdraw: event Status = %q, want %q", ev.Status, StatusIdle)
	}
}

func TestStatusLatchReportsTrackedSessionsOnly(t *testing.T) {
	m, id, h := newLatchManager(t)
	if st, seq, ok := m.StatusLatch("no-such-session"); ok || st != "" || seq != 0 {
		t.Errorf("StatusLatch(unknown) = (%q, %d, %t), want (\"\", 0, false)", st, seq, ok)
	}
	if st, seq, ok := m.StatusLatch(id); ok {
		t.Errorf("StatusLatch before the first sweep = (%q, %d, %t), want ok=false", st, seq, ok)
	}
	m.diffStatuses()
	if st, seq, ok := m.StatusLatch(id); st != "" || seq != 0 || !ok {
		t.Errorf("StatusLatch on a tracked, unlatched session = (%q, %d, %t), want (\"\", 0, true)", st, seq, ok)
	}

	h.handlePTYData([]byte(permissionRequired))
	m.diffStatuses()
	if st, seq, ok := m.StatusLatch(id); st != StatusInput || seq != 1 || !ok {
		t.Errorf("StatusLatch after the first ask = (%q, %d, %t), want (%q, 1, true)", st, seq, ok, StatusInput)
	}

	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if ev := eventFor(t, m.diffStatuses(), id); ev.Status != StatusWorking {
		t.Fatalf("after resume progress, event Status = %q, want %q", ev.Status, StatusWorking)
	}
	if st, seq, ok := m.StatusLatch(id); st != "" || seq != 0 || !ok {
		t.Errorf("StatusLatch after progress cleared the latch = (%q, %d, %t), want (\"\", 0, true)", st, seq, ok)
	}
}

// TestWithdrawStatusLatchBetweenSweepPhases lands the withdraw between a sweep's
// lock-free phase 2 and its phase 3, the only window in which manager state
// changes under a sweep in flight.
func TestWithdrawStatusLatchBetweenSweepPhases(t *testing.T) {
	cases := []struct {
		name       string
		inFlight   string
		wantStatus string
		wantLatch  string
		wantSeq    uint64
	}{
		{name: "nothing in flight", wantStatus: StatusIdle},
		{
			name: "a second ask captured by the same sweep", inFlight: permissionRequired,
			wantStatus: StatusInput, wantLatch: StatusInput, wantSeq: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, id, h := newLatchManager(t)
			armed := armInputLatch(t, m, id, h)
			h.handlePTYData([]byte(tc.inFlight))

			var fired atomic.Bool
			withdrew := make(chan bool, 1)
			hold := func() {
				if !fired.CompareAndSwap(false, true) {
					return
				}
				landed := make(chan bool)
				go func() { landed <- m.WithdrawStatusLatch(id, StatusInput, armed) }()
				withdrew <- <-landed
			}
			testDiffPhaseHold.Store(&hold)
			t.Cleanup(func() { testDiffPhaseHold.Store(nil) })

			ev := eventFor(t, m.diffStatuses(), id)
			if !fired.Load() {
				t.Fatal("the phase hold never ran; the seam is not wired")
			}
			if !<-withdrew {
				t.Errorf("WithdrawStatusLatch(%s, %q, %d) between the phases = false, want true", LogID(id), StatusInput, armed)
			}
			if ev.Status != tc.wantStatus {
				t.Errorf("the held sweep emitted Status = %q, want %q", ev.Status, tc.wantStatus)
			}
			if st, seq, ok := m.StatusLatch(id); st != tc.wantLatch || seq != tc.wantSeq || !ok {
				t.Errorf("StatusLatch after the held sweep = (%q, %d, %t), want (%q, %d, true)",
					st, seq, ok, tc.wantLatch, tc.wantSeq)
			}
			if m.WithdrawStatusLatch(id, StatusInput, armed) {
				t.Errorf("WithdrawStatusLatch(%s, %q, %d) after the held sweep = true, want false", LogID(id), StatusInput, armed)
			}
		})
	}
}
