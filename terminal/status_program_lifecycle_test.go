package terminal

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// lifecycleRow is a record state the OSC 7501 lifecycle table starts from: the
// reports that build it, the id of the record under test ("" is the root) and
// the record's own report again with new progress ("" when it has none).
type lifecycleRow struct {
	name  string
	id    string
	again string
	setup []string
}

// lifecycleRows is the table's row order; each column's want lists one token
// per row in this order.
var lifecycleRows = []lifecycleRow{
	{name: "absent"},
	{name: "cleared", setup: []string{"state=done", "state=clear"}},
	{name: "working", again: "state=working:progress=60", setup: []string{"state=working:progress=10"}},
	{name: "blocked permission", again: "state=blocked:kind=permission:progress=60", setup: []string{"state=blocked:kind=permission:progress=10"}},
	{name: "blocked question", again: "state=blocked:kind=question:progress=60", setup: []string{"state=blocked:kind=question"}},
	{name: "blocked auth", again: "state=blocked:kind=auth:progress=60", setup: []string{"state=blocked:kind=auth"}},
	{name: "blocked", again: "state=blocked:progress=60", setup: []string{"state=blocked"}},
	{name: "done", again: "state=done", setup: []string{"state=done"}},
	{name: "error", again: "state=error", setup: []string{"state=error"}},
	{name: "idle", again: "state=idle", setup: []string{"state=idle"}},
	{name: "blocked child", id: "ab/c", again: "state=blocked:kind=question:progress=60:app=t:id=ab/c", setup: []string{"state=blocked:kind=question:app=t:id=ab/c"}},
}

// childApp is the app every report for the child row carries, so a blocked
// report below the child shows, through app inheritance, whether it exists.
const childApp = "t"

func reportFor(r lifecycleRow, body string) []byte {
	if r.id != "" {
		body += ":app=" + childApp + ":id=" + r.id
	}
	return program7501(body)
}

// every is a column's record want with the same token for every row.
func every(token string) string {
	return strings.TrimSpace(strings.Repeat(token+" ", len(lifecycleRows)))
}

// observeRecord reads the row's record through the folded ProgramStatus:
// "none" when absent, else its state. The root's is the fold itself, since no
// root row has a blocked child; an idle fold is no record exactly when a root
// idle probe raises ProgramStatus.Seq, being a new record. The child shows
// itself only when blocked; otherwise a blocked probe below it inherits
// childApp exactly when it exists, and the result is "present". After process
// exit the probe is dropped as well, so a hidden child reads "".
func observeRecord(h *Handler, r lifecycleRow) string {
	ps := h.statusSnapshot().program
	if r.id == "" {
		if ps.State != vt.ProgramStateIdle {
			return ps.State
		}
		h.handlePTYData(program7501("state=idle"))
		if h.statusSnapshot().program.Seq > ps.Seq {
			return "none"
		}
		return ps.State
	}
	if ps.App == childApp {
		return ps.State
	}
	h.handlePTYData(program7501("state=blocked:id=" + r.id + "/p"))
	ps = h.statusSnapshot().program
	switch {
	case ps.State != vt.ProgramStateBlocked:
		return ""
	case ps.App == childApp:
		return "present"
	default:
		return "none"
	}
}

func fillRecords(h *Handler, from, n int) {
	for i := from; i < from+n; i++ {
		h.handlePTYData(program7501(fmt.Sprintf("state=idle:id=f%d", i)))
	}
}

// TestProgramRecordLifecycle is the OSC 7501 record lifecycle table of
// docs/sessions.md "Record lifecycle": every record state against every event
// that can reach it. A want token is the status after the event, with a "+"
// when statusSeq rose (a new occurrence), or "-" where the row has no such
// event. A record token is the row's record after it, as observeRecord reads
// it. absentReports is reportsActivity for the never-reported row; every other
// row has reported, and so stays true.
func TestProgramRecordLifecycle(t *testing.T) {
	const unchanged = "idle idle working input input input input done failed idle input"
	const dropped = "idle idle idle+ idle+ idle+ idle+ idle+ idle+ idle+ idle idle+"
	const kept = "none none working blocked blocked blocked blocked done error idle blocked"
	const finished = "none none none none none none none done error idle none"
	columns := []struct {
		act           func(h *Handler, r lifecycleRow)
		name          string
		want          string
		record        string
		noBaseline    bool
		absentReports bool
		exits         bool
	}{
		{
			name: "report done", absentReports: true,
			act:    func(h *Handler, r lifecycleRow) { h.handlePTYData(reportFor(r, "state=done")) },
			want:   "done+ done+ done+ done+ done+ done+ done+ done done+ done+ idle+",
			record: every("done"),
		},
		{
			name: "report working", absentReports: true,
			act:    func(h *Handler, r lifecycleRow) { h.handlePTYData(reportFor(r, "state=working:progress=50")) },
			want:   "working+ working+ working working+ working+ working+ working+ working+ working+ working+ idle+",
			record: every("working"),
		},
		{
			name: "report blocked question", absentReports: true,
			act:    func(h *Handler, r lifecycleRow) { h.handlePTYData(reportFor(r, "state=blocked:kind=question")) },
			want:   "input+ input+ input+ input+ input input+ input+ input+ input+ input+ input",
			record: every("blocked"),
		},
		{
			name: "report idle", absentReports: true,
			act:    func(h *Handler, r lifecycleRow) { h.handlePTYData(reportFor(r, "state=idle")) },
			want:   "idle idle idle+ idle+ idle+ idle+ idle+ idle+ idle+ idle idle+",
			record: every("idle"),
		},
		{
			name:   "the same report again",
			act:    func(h *Handler, r lifecycleRow) { h.handlePTYData(program7501(r.again)) },
			want:   "- - working input input input input done failed idle input",
			record: kept,
		},
		{
			name: "clear", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData(program7501("state=clear")) },
			want:   dropped,
			record: every("none"),
		},
		{
			name: "clear of the record's own id", absentReports: true,
			act: func(h *Handler, r lifecycleRow) {
				body := "state=clear"
				if r.id != "" {
					body += ":id=" + r.id
				}
				h.handlePTYData(program7501(body))
			},
			want:   dropped,
			record: every("none"),
		},
		{
			name: "clear of the parent id", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData(program7501("state=clear:id=ab")) },
			want:   "idle idle working input input input input done failed idle idle+",
			record: "none none working blocked blocked blocked blocked done error idle none",
		},
		{
			name: "clear of an id sharing the prefix", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData(program7501("state=clear:id=a")) },
			want:   unchanged,
			record: kept,
		},
		{
			// The report makes the records the status again after RIS handed it to OSC 9;4.
			name: "RIS, then a report for another id", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1bc\x1b]7501;state=idle:id=z\a")) },
			want:   dropped,
			record: every("none"),
		},
		{
			name: "RIS, then OSC 9;4 progress", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1bc\x1b]9;4;3\a")) },
			want:   "working+ working+ working working+ working+ working+ working+ working+ working+ working+ working+",
			record: every("none"),
		},
		{
			name: "RIS before the first sweep", noBaseline: true,
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1bc")) },
			want:   "idle idle idle idle idle idle idle idle idle idle idle",
			record: every("none"),
		},
		{
			name:   "DECSTR",
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1b[!p")) },
			want:   unchanged,
			record: kept,
		},
		{
			name:   "alternate screen switch",
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1b[?1049h\x1b[?1049l\x1b[?47h")) },
			want:   unchanged,
			record: kept,
		},
		{
			name:   "OSC 133 A",
			act:    func(h *Handler, _ lifecycleRow) { h.handlePTYData([]byte("\x1b]133;A;cl=m;aid=7\x1b\\")) },
			want:   "idle idle idle+ idle+ idle+ idle+ idle+ done failed idle idle+",
			record: finished,
		},
		{
			name: "another OSC 133 mark",
			act: func(h *Handler, _ lifecycleRow) {
				h.handlePTYData([]byte("\x1b]133;B\a\x1b]133;C\a\x1b]133;D;0\a\x1b]133;AB\a\x1b]133;a\a\x1b]133\a"))
			},
			want:   unchanged,
			record: kept,
		},
		{
			name: "process exit", exits: true,
			act:    func(h *Handler, _ lifecycleRow) { h.markProgramExited() },
			want:   "idle idle idle+ idle+ idle+ idle+ idle+ done failed idle idle+",
			record: finished,
		},
		{
			name: "a blocked report after process exit", absentReports: true, exits: true,
			act: func(h *Handler, r lifecycleRow) {
				h.markProgramExited()
				h.handlePTYData(reportFor(r, "state=blocked:kind=question"))
			},
			want:   dropped,
			record: every("none"),
		},
		{
			name: "64 newer records", absentReports: true,
			act:    func(h *Handler, _ lifecycleRow) { fillRecords(h, 1, 64) },
			want:   dropped,
			record: every("none"),
		},
		{
			name: "the same report again before the 64th newer record",
			act: func(h *Handler, r lifecycleRow) {
				fillRecords(h, 1, 63)
				h.handlePTYData(program7501(r.again))
				fillRecords(h, 64, 1)
			},
			want:   "- - working input input input input done failed idle input",
			record: kept,
		},
		{
			name: "a malformed pair over the key cap",
			act: func(h *Handler, r lifecycleRow) {
				h.handlePTYData(reportFor(r, "state=done:"+strings.Repeat("k", 17)+"=;"))
			},
			want:   unchanged,
			record: kept,
		},
		{
			name: "a 4097-byte report",
			act: func(h *Handler, r lifecycleRow) {
				body := "state=done"
				if r.id != "" {
					body += ":app=" + childApp + ":id=" + r.id
				}
				body += ":pad="
				h.handlePTYData([]byte("\x1b]7501;" + body + strings.Repeat("a", 4097-len("\x1b]7501;\x1b\\")-len(body)) + "\x1b\\"))
			},
			want:   unchanged,
			record: kept,
		},
	}
	for _, col := range columns {
		want, record := strings.Fields(col.want), strings.Fields(col.record)
		if len(want) != len(lifecycleRows) || len(record) != len(lifecycleRows) {
			t.Fatalf("column %q has %d status and %d record tokens for %d rows", col.name, len(want), len(record), len(lifecycleRows))
		}
		for i, row := range lifecycleRows {
			if want[i] == "-" {
				continue
			}
			t.Run(row.name+"|"+col.name, func(t *testing.T) {
				wantStatus, wantRise := strings.CutSuffix(want[i], "+")
				wantReports := row.name != "absent" || col.absentReports
				m, id, h := newProgramManager(t)
				for _, body := range row.setup {
					h.handlePTYData(program7501(body))
				}
				var before uint64
				if !col.noBaseline {
					_, before = sweepSeq(t, m, id)
				}
				col.act(h, row)
				events := m.diffStatuses()
				li := listedInfo(t, m, id)
				if li.Status != wantStatus {
					t.Errorf("Status = %q, want %q", li.Status, wantStatus)
				}
				if !col.noBaseline && (li.StatusSeq > before) != wantRise {
					t.Errorf("StatusSeq %d -> %d, want a rise = %v", before, li.StatusSeq, wantRise)
				}
				if li.ReportsActivity != wantReports {
					t.Errorf("List ReportsActivity = %v, want %v", li.ReportsActivity, wantReports)
				}
				emitted := false
				for _, ev := range events {
					if ev.ID == id {
						emitted = true
						if ev.Status != wantStatus || ev.ReportsActivity != wantReports {
							t.Errorf("event (%q, ReportsActivity %v), want (%q, %v)", ev.Status, ev.ReportsActivity, wantStatus, wantReports)
						}
					}
				}
				if col.noBaseline && !emitted {
					t.Error("the first sweep emitted no event")
				}
				for _, snap := range m.snapshot() {
					if snap.ID == id && snap.ReportsActivity != wantReports {
						t.Errorf("snapshot ReportsActivity = %v, want %v", snap.ReportsActivity, wantReports)
					}
				}
				got := observeRecord(h, row)
				hiddenMatch := got == "present" && record[i] != "none" && record[i] != vt.ProgramStateBlocked
				unobservable := got == "" && col.exits
				if got != record[i] && !hiddenMatch && !unobservable {
					t.Errorf("record = %q, want %q", got, record[i])
				}
			})
		}
	}
}

// TestStatusClassifierLatchLifecycle is the lifecycle of a classifier-supplied
// status latched after an OSC 7501 report at the root or a child: a prompt,
// the records' exit drop and RIS are no report, and the same report again
// changes no status, so all leave the latch; a report for the same id that
// changes the status clears it.
func TestStatusClassifierLatchLifecycle(t *testing.T) {
	const custom = "paused-custom"
	customClassifier := func(string) (string, bool) { return custom, true }
	positions := []struct{ name, suffix string }{{"root", ""}, {"child", ":id=a/b"}}
	events := []struct {
		act        func(h *Handler, suffix string)
		name, want string
		keepsLatch bool
	}{
		{name: "OSC 133 A", want: custom, keepsLatch: true, act: func(h *Handler, _ string) { h.handlePTYData([]byte("\x1b]133;A\a")) }},
		{name: "process exit", want: custom, keepsLatch: true, act: func(h *Handler, _ string) { h.markProgramExited() }},
		{name: "RIS", want: custom, keepsLatch: true, act: func(h *Handler, _ string) { h.handlePTYData([]byte("\x1bc")) }},
		{name: "the same report again", want: custom, keepsLatch: true, act: func(h *Handler, suffix string) {
			h.handlePTYData(program7501("state=working" + suffix))
		}},
		{name: "a report for the same id changing the status", want: StatusInput, act: func(h *Handler, suffix string) {
			h.handlePTYData(program7501("state=blocked:kind=question" + suffix))
		}},
	}
	for _, pos := range positions {
		for _, ev := range events {
			t.Run(pos.name+" "+ev.name, func(t *testing.T) {
				m, id, h := newProgramManager(t, WithStatusClassifier(customClassifier))
				h.handlePTYData(program7501("state=working" + pos.suffix))
				h.handlePTYData([]byte(approvalNeeded))
				if status, _ := sweepSeq(t, m, id); status != custom {
					t.Fatalf("after the notification Status = %q, want %q", status, custom)
				}
				ev.act(h, pos.suffix)
				if status, _ := sweepSeq(t, m, id); status != ev.want {
					t.Errorf("Status = %q, want %q", status, ev.want)
				}
				if latch, _, _ := m.StatusLatch(id); (latch == custom) != ev.keepsLatch {
					t.Errorf("StatusLatch = %q, want kept = %v", latch, ev.keepsLatch)
				}
			})
		}
	}
}

// heldExitSession creates one session running script under a manager with
// the background sweep stopped, and returns once the session's exit callback
// has started. The callback parks until release closes.
func heldExitSession(t *testing.T, script string) (m *SessionManager, h *Handler, id SessionID, release chan struct{}) {
	t.Helper()
	enteredCh := make(chan struct{})
	release = make(chan struct{})
	m = NewSessionManager(func(SessionID) *Handler {
		return NewHandler([]string{"/bin/sh", "-c", script},
			WithWorkDir("/"), WithLogger(nil),
			WithOnProcessExit(func(error) {
				close(enteredCh)
				<-release
			}),
		)
	})
	t.Cleanup(func() { shutdownManager(t, m) })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h = handlerOf(t, m, id)
	select {
	case <-enteredCh:
	case <-time.After(30 * time.Second):
		t.Fatal("the exit callback never ran")
	}
	return m, h, id, release
}

// awaitMonitorDone waits until the process monitor and every other handler
// goroutine has finished.
func awaitMonitorDone(t *testing.T, h *Handler) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the process monitor never finished")
	}
}

// TestStatusProgramStatusExitOutlivesALateReport runs the real process
// monitor: a background child keeps the PTY after the session's process
// exits and, while the exit callback is still running, writes a blocked
// report. Until the callback returns the session is live and its blocked
// records stand; then it is exited and both blocked records are gone.
func TestStatusProgramStatusExitOutlivesALateReport(t *testing.T) {
	const script = `printf '\033]7501;state=done\007\033]7501;state=blocked:kind=question:id=w\007'; ` +
		`trap '' HUP; exec 3<&0; (read -r _ <&3; printf '\033]7501;state=blocked:kind=permission:id=late\007') & exit 0`
	m, h, id, release := heldExitSession(t, script)
	if status, _ := sweepSeq(t, m, id); status != StatusInput {
		t.Fatalf("while the exit callback runs Status = %q, want %q: the process is not reported exited yet", status, StatusInput)
	}
	h.mu.Lock()
	ptmx := h.ptmx
	h.mu.Unlock()
	if _, err := ptmx.WriteString("\n"); err != nil {
		t.Fatalf("waking the background child: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for h.statusSnapshot().programSeq < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the late report never reached the screen (programSeq %d)", h.statusSnapshot().programSeq)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, _ := sweepSeq(t, m, id); status != StatusInput {
		t.Errorf("after the late report Status = %q, want %q", status, StatusInput)
	}
	close(release)
	awaitMonitorDone(t, h)
	if status, _ := sweepSeq(t, m, id); status != StatusExited {
		t.Errorf("after the exit callback Status = %q, want %q", status, StatusExited)
	}
	want := vt.ProgramStatus{State: vt.ProgramStateDone, Progress: -1, Seq: 3}
	if got := h.statusSnapshot().program; got != want {
		t.Errorf("after the exit ProgramStatus = %+v, want %+v: exit drops both blocked records", got, want)
	}
}

// TestStatusSweepPairsDroppedRecordsWithTheExit finishes the process exit
// inside a sweep, between its screen snapshot and its exit outcome: the
// sweep must report the exit, never the idle left by the dropped record.
func TestStatusSweepPairsDroppedRecordsWithTheExit(t *testing.T) {
	m, h, id, release := heldExitSession(t, `printf '\033]7501;state=blocked:kind=question\007'; exit 0`)
	var once sync.Once
	hold := func() {
		once.Do(func() {
			close(release)
			awaitMonitorDone(t, h)
		})
	}
	testStatusReadHold.Store(&hold)
	t.Cleanup(func() { testStatusReadHold.Store(nil) })
	if status, _ := sweepSeq(t, m, id); status != StatusExited {
		t.Errorf("Status = %q, want %q", status, StatusExited)
	}
	if got := h.statusSnapshot().program.State; got != vt.ProgramStateIdle {
		t.Errorf("after the exit ProgramStatus.State = %q, want idle: exit drops the blocked root", got)
	}
}
