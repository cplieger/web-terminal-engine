package terminal

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// inputClassifier maps one needs-input and one done notification string, the
// shape a consumer's classifier takes, for exercising the latched state machine.
func inputClassifier(msg string) (string, bool) {
	switch msg {
	case "Approval needed":
		return StatusInput, true
	case "Task finished":
		return StatusDone, true
	}
	return "", false
}

// computeStatusFromHandler adapts the diffStatuses split for the state-machine
// tests: capture h's status inputs (the production phase-2 read) and run the
// tracker state machine on them, as one call.
func (m *SessionManager) computeStatusFromHandler(h *Handler, tr *statusTracker) string {
	in := statusRaw{handler: h}
	in.read()
	st, _ := m.computeStatus(&in, tr)
	return st
}

func handlerOf(t *testing.T, m *SessionManager, id SessionID) *Handler {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		t.Fatalf("session %s not found", id)
	}
	return s.handler
}

// TestComputeStatusLatchesInput verifies a classified needs-input notification
// latches input and persists (the process is blocked, so no progress and no
// output), then an active progress signal (the turn resuming) clears it.
func TestComputeStatusLatchesInput(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	// A prompt followed by a needs-input notification: latched input.
	h.handlePTYData([]byte("Allow? \x1b]9;Approval needed\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusInput {
		t.Fatalf("after notification, status = %q, want %q", st, StatusInput)
	}
	// No resume: the latch persists across sweeps.
	if st := m.computeStatusFromHandler(h, tr); st != StatusInput {
		t.Fatalf("latch did not persist, status = %q, want %q", st, StatusInput)
	}
	// The turn resumes: an active progress signal (OSC 9;4;3) clears the latch
	// and reports working.
	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusWorking {
		t.Fatalf("after resume progress, status = %q, want %q", st, StatusWorking)
	}
}

// TestComputeStatusDoneSupersedesInput verifies a classified done notification
// ("Task finished") replaces an input latch with the done state.
func TestComputeStatusDoneSupersedesInput(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;Approval needed\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusInput {
		t.Fatalf("precondition: status = %q, want %q", st, StatusInput)
	}
	h.handlePTYData([]byte("\x1b]9;Task finished\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusDone {
		t.Fatalf("done did not supersede input latch; status = %q, want %q", st, StatusDone)
	}
}

// TestComputeStatusWorkingFromProgress verifies an active OSC 9;4 progress state
// (3 indeterminate) reports working, and clearing it (0) drops to idle when
// nothing is latched.
func TestComputeStatusWorkingFromProgress(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusWorking {
		t.Fatalf("progress 3: status = %q, want %q", st, StatusWorking)
	}
	h.handlePTYData([]byte("\x1b]9;4;0\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusIdle {
		t.Fatalf("progress 0 with no latch: status = %q, want %q", st, StatusIdle)
	}
}

// TestComputeStatusDoneLatchPersistsThenClears verifies "Task finished"
// latches done through the quiet gap, and the next working progress clears it.
func TestComputeStatusDoneLatchPersistsThenClears(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;4;0\x07\x1b]9;Task finished\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusDone {
		t.Fatalf("after done notification: status = %q, want %q", st, StatusDone)
	}
	// Persists across a quiet sweep (no progress, no output-driven flip).
	if st := m.computeStatusFromHandler(h, tr); st != StatusDone {
		t.Fatalf("done latch did not persist: status = %q, want %q", st, StatusDone)
	}
	// Next turn starts working, clearing the done latch.
	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusWorking {
		t.Fatalf("next working phase: status = %q, want %q", st, StatusWorking)
	}
}

// TestComputeStatusFreshLatchOutranksActiveProgress pins the turn boundary:
// when one sweep's snapshot pairs a NEW classified notification with a progress
// value that still reads active (an emitter flushes its done notification a
// chunk ahead of the OSC 9;4;0 progress-off), the fresh latch wins and persists
// once progress clears, instead of every later sweep landing on idle. The
// pairing cannot be produced through handlePTYData (a chunk parses atomically
// under h.mu), so the snapshot is constructed directly.
func TestComputeStatusFreshLatchOutranksActiveProgress(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{name: "turn-end done", msg: "Task finished", want: StatusDone},
		{name: "needs-input", msg: "Approval needed", want: StatusInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
			t.Cleanup(func() { shutdownManager(t, m) })
			tr := &statusTracker{}

			// Sweep N: the poisoned snapshot — fresh notification, progress
			// still reading active.
			in := &statusRaw{progress: 3, notifMsg: tc.msg, notifSeq: 1}
			if st, _ := m.computeStatus(in, tr); st != tc.want {
				t.Errorf("poisoned snapshot: status = %q, want %q", st, tc.want)
			}
			// Sweep N+1: progress cleared, nothing new — the latch persists.
			in = &statusRaw{progress: 0, notifMsg: tc.msg, notifSeq: 1}
			if st, _ := m.computeStatus(in, tr); st != tc.want {
				t.Errorf("after progress clears: status = %q, want %q", st, tc.want)
			}
		})
	}
}

// TestComputeStatusStaleLatchStillClearedByWorking verifies the fresh-latch
// precedence lasts one sweep only (self-correcting): when progress remains
// active on the NEXT sweep with no new notification — the program genuinely kept
// working, e.g. a queued follow-up turn — the now-stale latch clears and the
// session reports working.
func TestComputeStatusStaleLatchStillClearedByWorking(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	tr := &statusTracker{}

	in := &statusRaw{progress: 3, notifMsg: "Task finished", notifSeq: 1}
	if st, _ := m.computeStatus(in, tr); st != StatusDone {
		t.Fatalf("poisoned snapshot: status = %q, want %q", st, StatusDone)
	}
	// Same active progress, no new notification: the latch is stale now and
	// the genuine working phase supersedes it.
	if st, _ := m.computeStatus(in, tr); st != StatusWorking {
		t.Fatalf("next sweep: status = %q, want %q", st, StatusWorking)
	}
	if tr.latched != "" {
		t.Fatalf("latch = %q, want cleared", tr.latched)
	}
	// That working phase later ends with no notification replay: idle is
	// correct (in production the queued turn's own end brings its own
	// "Task finished" with a new sequence).
	in = &statusRaw{progress: 0, notifMsg: "Task finished", notifSeq: 1}
	if st, _ := m.computeStatus(in, tr); st != StatusIdle {
		t.Fatalf("after the working phase ends: status = %q, want %q", st, StatusIdle)
	}
}

// TestComputeStatusNoWorkingFromOutput verifies a program that reports neither
// OSC 7501 status nor OSC 9;4 progress stays idle even while producing output:
// working comes only from those reports, so a plain shell with no classifier
// never flaps to working because it (or the user typing at the prompt)
// produced bytes. The reveal gate then keeps such a session's tab dot hidden.
func TestComputeStatusNoWorkingFromOutput(t *testing.T) {
	m := NewSessionManager(catFactory) // no classifier: a generic shell
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("some output"))
	if st := m.computeStatusFromHandler(h, tr); st != StatusIdle {
		t.Fatalf("output with no status report: status = %q, want %q (no output-activity fallback)", st, StatusIdle)
	}
}

// TestReportsActivitySticky verifies the reportsActivity flag the client uses to
// reveal the per-tab activity dot, here through OSC 9;4: false until the
// program reports progress, then sticky true even after the progress is
// cleared, so the dot stays revealed while the session idles rather than
// flickering away. OSC 7501 status and a classified OSC 9 notification set it
// too.
func TestReportsActivitySticky(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep() // List serves what the last sweep emitted, so sweep by hand
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)

	reports := func() bool {
		_ = m.diffStatuses()
		for _, info := range m.List() {
			if info.ID == id {
				return info.ReportsActivity
			}
		}
		t.Fatalf("session %s not in List()", id)
		return false
	}

	if reports() {
		t.Fatalf("fresh session (no OSC 9;4) reportsActivity = true, want false")
	}
	h.handlePTYData([]byte("\x1b]9;4;3\x07")) // active progress
	if !reports() {
		t.Fatalf("after OSC 9;4;3 reportsActivity = false, want true")
	}
	h.handlePTYData([]byte("\x1b]9;4;0\x07")) // clear progress
	if !reports() {
		t.Fatalf("after clearing progress reportsActivity = false, want true (sticky: Progress stays >= 0)")
	}
}

// TestListCarriesCommittedStatus is the reload regression guard: GET
// /api/sessions must report the same status the SSE stream pushes. A list that
// computed only coarse liveness (idle/exited) would make a page reload, whose
// stream-open reconcile repaints every tab dot from it, downgrade a latched
// done/input dot back to the hollow idle state until the next transition.
func TestListCarriesCommittedStatus(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep() // drive the sweep by hand, deterministically

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	statusInList := func() string {
		t.Helper()
		for _, info := range m.List() {
			if info.ID == id {
				return info.Status
			}
		}
		t.Fatalf("session %s not in List()", id)
		return ""
	}

	// Before any sweep has computed a status: the coarse new-session default.
	if got := statusInList(); got != StatusIdle {
		t.Fatalf("fresh session List status = %q, want %q", got, StatusIdle)
	}

	// A turn completes: progress clears, the done notification latches, and one
	// sweep records it. The list must then agree with what the stream pushes.
	handlerOf(t, m, id).handlePTYData([]byte("\x1b]9;4;0\x07\x1b]9;Task finished\x07"))
	_ = m.diffStatuses()
	if got := statusInList(); got != StatusDone {
		t.Fatalf("after swept done notification, List status = %q, want %q", got, StatusDone)
	}

	// The next turn starts working: the latch clears and the list follows.
	handlerOf(t, m, id).handlePTYData([]byte("\x1b]9;4;3\x07"))
	_ = m.diffStatuses()
	if got := statusInList(); got != StatusWorking {
		t.Fatalf("after resume progress, List status = %q, want %q", got, StatusWorking)
	}
}

// TestPointInTimeReadsAgreeAcrossACrash pins that List and the snapshot serve
// the sweep's committed (status, statusSeq) pair while a crash is not yet swept:
// a crashed status beside the previous status's number would let a reader's ack
// cover the crash before it is numbered. The crash then takes its own number.
func TestPointInTimeReadsAgreeAcrossACrash(t *testing.T) {
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler([]string{"/bin/sh", "-c", "read x; exit 3"}, WithWorkDir("/"), WithLogger(nil))
	}, WithStatusClassifier(inputClassifier))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	h.handlePTYData([]byte("\x1b]9;Task finished\x07"))
	n := eventFor(t, m.diffStatuses(), id).StatusSeq
	if _, err := h.ptmx.WriteString("\n"); err != nil {
		t.Fatalf("write to pty: %v", err)
	}
	waitExited(t, h)

	li := listedInfo(t, m, id)
	if li.Status != StatusDone || li.StatusSeq != n {
		t.Errorf("List = (%q, %d), want the committed (%q, %d)", li.Status, li.StatusSeq, StatusDone, n)
	}
	for _, ev := range m.snapshot() {
		if ev.ID == id && (ev.Status != StatusDone || ev.StatusSeq != n) {
			t.Errorf("snapshot = (%q, %d), want the committed (%q, %d)", ev.Status, ev.StatusSeq, StatusDone, n)
		}
	}

	m.markSessionSeen(id, li.StatusSeq)
	crash := eventFor(t, m.diffStatuses(), id)
	if crash.Status != StatusCrashed || crash.StatusSeq != n+1 || crash.SeenSeq != n {
		t.Fatalf("crash event = (%q, %d, seen %d), want (%q, %d, seen %d)",
			crash.Status, crash.StatusSeq, crash.SeenSeq, StatusCrashed, n+1, n)
	}
	m.markSessionSeen(id, crash.StatusSeq)
	read := eventFor(t, m.diffStatuses(), id)
	if read.StatusSeq != n+1 || read.SeenSeq != n+1 {
		t.Errorf("after acking the crash = (%d, seen %d), want (%d, seen %d)", read.StatusSeq, read.SeenSeq, n+1, n+1)
	}
}

// TestComputeStatusExited verifies an exited process reports exited regardless
// of activity or latch.
func TestComputeStatusExited(t *testing.T) {
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler([]string{"/bin/true"}, WithLogger(nil))
	})
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	waitExited(t, h)
	if st := m.computeStatusFromHandler(h, &statusTracker{}); st != StatusExited {
		t.Fatalf("status = %q, want %q", st, StatusExited)
	}
}

// TestEventsHandlerInitialSync verifies a new SSE subscriber immediately
// receives the current status of existing sessions (initial sync).
func TestEventsHandlerInitialSync(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	srv := httptest.NewServer(m.EventsHandler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") && strings.Contains(line, string(id)) {
			return // initial sync delivered the session
		}
	}
	t.Fatalf("initial sync did not include session %s (scan err: %v)", id, sc.Err())
}

// TestEventsHandlerSubscriberCap verifies the fixed subscriber cap: exactly
// maxSubscribers concurrent status-stream (SSE) subscribers are admitted, and
// the next one is rejected with HTTP 503. Driven deterministically — the
// background sweep is stopped so its broadcasts cannot drop an unread
// subscriber mid-test, the maxSubscribers slots are filled by driving
// subscribe() directly (no HTTP, no sleeps), and the over-cap request goes
// through EventsHandler to assert the real 503. It references the const (not a
// literal 10) so it tracks maxSubscribers if the cap ever changes.
func TestEventsHandlerSubscriberCap(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep() // deterministic: no background broadcast racing on m.subs

	// Fill exactly maxSubscribers slots; each must be admitted.
	for i := range maxSubscribers {
		if _, ok := m.subscribe(); !ok {
			t.Fatalf("subscriber %d/%d rejected below the cap; want admitted", i+1, maxSubscribers)
		}
	}

	// The cap is full (len(subs) == maxSubscribers): the next subscribe is
	// rejected, and EventsHandler surfaces that as HTTP 503.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/events", nil)
	m.EventsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("over-cap request status = %d, want %d (too many subscribers)", rec.Code, http.StatusServiceUnavailable)
	}
}

// unwrapOnlyWriter mimics an access-log middleware wrapper that records the
// response status: it exposes the underlying ResponseWriter via Unwrap but does
// NOT itself implement http.Flusher (embedding the ResponseWriter interface
// does not promote Flush). It pins that EventsHandler finds the flusher through
// the Unwrap chain rather than a direct type assertion, which is what makes SSE
// work behind middleware.
type unwrapOnlyWriter struct {
	http.ResponseWriter
}

func (u unwrapOnlyWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

// noFlushWriter is a bare ResponseWriter that supports neither Flush nor Unwrap.
type noFlushWriter struct{}

func (noFlushWriter) Header() http.Header         { return http.Header{} }
func (noFlushWriter) Write(b []byte) (int, error) { return len(b), nil }
func (noFlushWriter) WriteHeader(int)             {}

// TestSupportsFlush pins the Unwrap-chain walk: a direct flusher is supported, a
// wrapper that only implements Unwrap to a flusher is supported (the case a
// direct w.(http.Flusher) assertion misses), and a writer with neither is not.
func TestSupportsFlush(t *testing.T) {
	rec := httptest.NewRecorder() // implements http.Flusher directly
	if !supportsFlush(rec) {
		t.Error("httptest.ResponseRecorder should support flush directly")
	}
	if !supportsFlush(unwrapOnlyWriter{rec}) {
		t.Error("a wrapper that Unwraps to a flusher must be reported as supporting flush")
	}
	if supportsFlush(noFlushWriter{}) {
		t.Error("a writer with neither Flush nor Unwrap must not be reported as supporting flush")
	}
}

// TestEventsHandlerFlushesBehindMiddleware is the regression guard for the SSE
// status stream served behind a ResponseWriter-wrapping middleware. The engine
// wraps every request in an access-log recorder that implements Unwrap but not
// Flush; a direct w.(http.Flusher) assertion would fail there and 500. This
// serves EventsHandler behind exactly such a wrapper and asserts the stream
// opens (200 + text/event-stream) and delivers the initial sync.
func TestEventsHandlerFlushesBehindMiddleware(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.EventsHandler().ServeHTTP(unwrapOnlyWriter{w}, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a direct flusher assertion would 500 here)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data:") && strings.Contains(line, string(id)) {
			return // stream flushed the initial sync through the middleware
		}
	}
	t.Fatalf("SSE stream delivered no data behind middleware (scan err: %v)", sc.Err())
}

// stopSweep cancels the background status sweep so a test can drive
// diffStatuses deterministically without the 250ms ticker racing on the shared
// trackers. Safe to call once before the first tick; Shutdown then finds
// sweepCancel already nil.
func (m *SessionManager) stopSweep() {
	m.mu.Lock()
	if m.sweepCancel != nil {
		m.sweepCancel()
		m.sweepCancel = nil
	}
	m.mu.Unlock()
}

// TestDiffStatusesEmitsClientTitleChange verifies the status sweep detects a
// change to the raw client title even when the OSC-derived effective title,
// status, and reported activity are all unchanged: a PUT /title on a session
// whose program already emits an OSC window title moves only clientTitle, and a
// consumer that reads clientTitle directly must still get that pushed. It also
// pins that the emitted event carries ClientTitle (the raw label) alongside the
// unchanged OSC-derived Title.
func TestDiffStatusesEmitsClientTitleChange(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	// Drive diffStatuses by hand: stop the background sweep (it has not ticked
	// yet — its first tick is 250ms out) so it cannot race on m.trackers between
	// the change and the assertion.
	m.stopSweep()

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// An OSC window title fixes the effective title; a later client-title change
	// then moves ONLY clientTitle (OSC-first keeps Title == "osc label").
	handlerOf(t, m, id).handlePTYData([]byte("\x1b]2;osc label\x07"))

	findEvent := func(events []statusEvent) (statusEvent, bool) {
		t.Helper()
		for _, ev := range events {
			if ev.ID == id {
				return ev, true
			}
		}
		return statusEvent{}, false
	}

	// First sweep establishes the tracker baseline (Title="osc label",
	// ClientTitle="").
	base, ok := findEvent(m.diffStatuses())
	if !ok {
		t.Fatalf("baseline sweep emitted no event for session %s", id)
	}
	if base.Title != "osc label" || base.ClientTitle != "" {
		t.Fatalf("baseline event = {Title:%q ClientTitle:%q}, want {\"osc label\", \"\"}", base.Title, base.ClientTitle)
	}

	// A quiescent sweep (nothing changed) emits nothing for the session, so the
	// next emit is attributable to the client-title change alone.
	if ev, ok := findEvent(m.diffStatuses()); ok {
		t.Fatalf("quiescent sweep unexpectedly emitted an event: %+v", ev)
	}

	// A client-title-only change: OSC title, status, and activity all unchanged.
	if !m.SetSessionTitle(id, "msg") {
		t.Fatal("SetSessionTitle(known id) = false, want true")
	}
	ev, ok := findEvent(m.diffStatuses())
	if !ok {
		t.Fatal("sweep after client-title-only change emitted no event; the clientTitle change was not detected")
	}
	if ev.ClientTitle != "msg" {
		t.Fatalf("event ClientTitle = %q, want %q", ev.ClientTitle, "msg")
	}
	if ev.Title != "osc label" {
		t.Fatalf("event Title = %q, want %q (OSC-derived title must be unchanged)", ev.Title, "osc label")
	}
}

// TestBroadcastDropsSlowSubscriber pins the non-blocking fan-out: a subscriber
// whose bounded buffer overflows is dropped (removed from m.subs and its channel
// closed) instead of blocking the sweep. Without the select-default drop in
// broadcast, the overflowing send would block this goroutine forever.
func TestBroadcastDropsSlowSubscriber(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep() // no background sweep broadcasts racing on m.subs

	ch, ok := m.subscribe()
	if !ok {
		t.Fatal("subscribe = false, want a fresh subscriber channel")
	}

	ev := statusEvent{ID: "s", Status: StatusIdle}
	// Exactly subscriberBuffer sends fill the buffer without overflowing.
	for range subscriberBuffer {
		m.broadcast(&ev)
	}
	m.subsMu.Lock()
	_, present := m.subs[ch]
	m.subsMu.Unlock()
	if !present {
		t.Fatal("subscriber dropped before overflow; want still present after exactly subscriberBuffer sends")
	}

	// One more send overflows: the select default branch drops and closes it.
	m.broadcast(&ev)
	m.subsMu.Lock()
	_, present = m.subs[ch]
	m.subsMu.Unlock()
	if present {
		t.Fatal("subscriber not dropped after buffer overflow; want removed from m.subs")
	}

	// The dropped channel is closed: drain the buffered events, then a receive
	// reports closed (the signal the handler goroutine uses to unsubscribe).
	for range subscriberBuffer {
		<-ch
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after drop; want closed once buffered events drained")
	}
}

// TestDiffStatusesWedgedHandlerDoesNotStallManager pins the lock split in
// diffStatuses: a handler stuck under its own h.mu (a wedged PTY callback, a
// stalled classifier input) stalls only the sweep, which must read THAT
// handler's state, never the manager lock itself, and never List or a new
// subscriber's snapshot, which serve the committed record and read no handler.
func TestDiffStatusesWedgedHandlerDoesNotStallManager(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)

	// Wedge the handler: hold its h.mu so every status getter blocks.
	h.mu.Lock()
	unwedged := false
	defer func() {
		if !unwedged {
			h.mu.Unlock()
		}
	}()
	sweepDone := make(chan struct{})
	go func() {
		_ = m.diffStatuses() // blocks in phase 2 on the wedged handler
		close(sweepDone)
	}()
	select {
	case <-sweepDone:
		t.Fatal("diffStatuses returned while the handler was wedged; the test lost its premise")
	case <-time.After(50 * time.Millisecond):
	}

	// With the sweep stuck in phase 2, a pure-m.mu path (the title PUT), List and
	// the snapshot all complete promptly.
	for name, read := range map[string]func(){
		"SetSessionTitle": func() { m.SetSessionTitle(id, "still responsive") },
		"List":            func() { _ = m.List() },
		"snapshot":        func() { _ = m.snapshot() },
	} {
		done := make(chan struct{})
		go func() {
			read()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("%s blocked while a handler was wedged: it reads handler state or holds m.mu across a getter", name)
		}
	}

	// Unwedge; the stuck sweep completes.
	h.mu.Unlock()
	unwedged = true
	select {
	case <-sweepDone:
	case <-time.After(2 * time.Second):
		t.Fatal("diffStatuses did not finish after the handler was released")
	}
}

// TestReportsActivityAgreesAcrossReadPaths pins the activity flag on every
// surface a client can read it from — the status diff the stream pushes, the
// snapshot a fresh stream opens with, and the session list a reload GETs — and
// pins the distinction the flag rests on: a CLEARED progress (OSC 9;4;0) is a
// signal that arrived, while the absent value means none ever did. Only the
// second is "no activity".
//
// The three surfaces have to agree because a client mixes them: it repaints the
// tab dots from List on reload and from the snapshot on stream open, then
// follows the diff. One surface reading the flag differently makes the dot
// appear or disappear on reload rather than when something happened.
func TestReportsActivityAgreesAcrossReadPaths(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	// Drive the sweep by hand: the tracker List reads is created by the sweep,
	// so a background tick would decide whether List sees one.
	m.stopSweep()

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	diffReports := func() bool {
		t.Helper()
		for _, ev := range m.diffStatuses() {
			if ev.ID == id {
				return ev.ReportsActivity
			}
		}
		t.Fatalf("status sweep emitted no event for session %s, so the flag it carries is unobservable", id)
		return false
	}
	snapshotReports := func() bool {
		t.Helper()
		for _, ev := range m.snapshot() {
			if ev.ID == id {
				return ev.ReportsActivity
			}
		}
		t.Fatalf("snapshot omitted session %s", id)
		return false
	}
	listReports := func() bool {
		t.Helper()
		for _, info := range m.List() {
			if info.ID == id {
				return info.ReportsActivity
			}
		}
		t.Fatalf("List omitted session %s", id)
		return false
	}

	t.Run("a session that never emitted OSC 9;4 reports no activity", func(t *testing.T) {
		if got := diffReports(); got {
			t.Errorf("status event ReportsActivity = %v, want false: no OSC 9;4 has been seen", got)
		}
		if got := snapshotReports(); got {
			t.Errorf("snapshot ReportsActivity = %v, want false: no OSC 9;4 has been seen", got)
		}
		if got := listReports(); got {
			t.Errorf("List ReportsActivity = %v, want false: no OSC 9;4 has been seen", got)
		}
	})

	t.Run("a cleared progress still reports activity", func(t *testing.T) {
		handlerOf(t, m, id).handlePTYData([]byte("\x1b]9;4;0\x07")) // state 0: cleared, not absent
		if got := diffReports(); !got {
			t.Errorf("status event ReportsActivity = %v, want true: state 0 is a cleared signal, not an absent one", got)
		}
		if got := snapshotReports(); !got {
			t.Errorf("snapshot ReportsActivity = %v, want true: state 0 is a cleared signal, not an absent one", got)
		}
		if got := listReports(); !got {
			t.Errorf("List ReportsActivity = %v, want true: state 0 is a cleared signal, not an absent one", got)
		}
	})
}

// activitySource is a settable WithSessionActivity source: a test moves the
// value and sweeps, the way a host's own watcher would. Guarded, because the
// manager may read it from the sweep goroutine.
type activitySource struct {
	byID map[SessionID]SessionActivity
	mu   sync.Mutex
}

func newActivitySource() *activitySource {
	return &activitySource{byID: make(map[SessionID]SessionActivity)}
}

func (s *activitySource) set(id SessionID, act SessionActivity) {
	s.mu.Lock()
	s.byID[id] = act
	s.mu.Unlock()
}

func (s *activitySource) read(id SessionID) SessionActivity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[id]
}

// trackerOf returns a session's tracker, so a test can assert on state the
// public surface deliberately does not expose (the notification latch).
func trackerOf(t *testing.T, m *SessionManager, id SessionID) *statusTracker {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	tr := m.trackers[id]
	if tr == nil {
		t.Fatalf("session %s has no tracker; the sweep has not seen it yet", LogID(id))
	}
	return tr
}

// TestSessionActivityReachesEveryReadPath verifies the secondary activity a host
// supplies through WithSessionActivity reaches all three surfaces a client reads:
// the status sweep's event, the SSE initial-sync snapshot (a reload repaints from
// it, so a mark absent there disappears until the value next changes), and the
// REST list. It also pins the no-source default: unset, both fields are zero,
// which is the state every existing consumer stays in.
func TestSessionActivityReachesEveryReadPath(t *testing.T) {
	src := newActivitySource()
	m := NewSessionManager(catFactory, WithSessionActivity(src.read))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep() // drive the sweep by hand, deterministically

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	src.set(id, SessionActivity{State: ActivityWorking, Count: 2})

	ev := eventFor(t, m.diffStatuses(), id)
	if ev.Activity != ActivityWorking || ev.ActivityCount != 2 {
		t.Errorf("status event Activity = %q count = %d, want %q / 2",
			ev.Activity, ev.ActivityCount, ActivityWorking)
	}

	snapped := false
	for _, sev := range m.snapshot() {
		if sev.ID != id {
			continue
		}
		snapped = true
		if sev.Activity != ActivityWorking || sev.ActivityCount != 2 {
			t.Errorf("snapshot Activity = %q count = %d, want %q / 2",
				sev.Activity, sev.ActivityCount, ActivityWorking)
		}
	}
	if !snapped {
		t.Errorf("snapshot omitted session %s, so the fields it carries are unobservable", LogID(id))
	}

	listed := false
	for _, info := range m.List() {
		if info.ID != id {
			continue
		}
		listed = true
		if info.Activity != ActivityWorking || info.ActivityCount != 2 {
			t.Errorf("List Activity = %q count = %d, want %q / 2",
				info.Activity, info.ActivityCount, ActivityWorking)
		}
	}
	if !listed {
		t.Errorf("List omitted session %s", LogID(id))
	}

	// No source installed: the fields stay zero rather than inventing a state.
	plain := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, plain) })
	plain.stopSweep()
	plainID, err := plain.Create()
	if err != nil {
		t.Fatalf("Create (no source): %v", err)
	}
	nilEv := eventFor(t, plain.diffStatuses(), plainID)
	if nilEv.Activity != "" || nilEv.ActivityCount != 0 {
		t.Errorf("with no activity source, status event Activity = %q count = %d, want \"\" / 0",
			nilEv.Activity, nilEv.ActivityCount)
	}
}

// TestSweepEmitsOnActivityChangeAlone is the emission proof: a secondary activity
// change moves nothing else about the session — not its status, not a title, not
// the percentage — so unless the sweep's change-detection conjunction has a
// clause of its own for it, the value changes silently and a client's mark stays
// at whatever it first saw. Deleting either clause (state or count) from
// sweepSession must turn this red.
func TestSweepEmitsOnActivityChangeAlone(t *testing.T) {
	src := newActivitySource()
	m := NewSessionManager(catFactory, WithSessionActivity(src.read))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Settle: the first sweep emits because the status itself moves off "", then a
	// quiet manager must emit nothing, or every assertion below is vacuous.
	_ = eventFor(t, m.diffStatuses(), id)
	if quiet := m.diffStatuses(); len(quiet) != 0 {
		t.Fatalf("a quiet manager emitted %d events, want 0", len(quiet))
	}

	src.set(id, SessionActivity{State: ActivityInput, Count: 1})
	if ev := eventFor(t, m.diffStatuses(), id); ev.Activity != ActivityInput {
		t.Errorf("after the state appeared, event Activity = %q, want %q", ev.Activity, ActivityInput)
	}
	if quiet := m.diffStatuses(); len(quiet) != 0 {
		t.Errorf("the sweep after the change emitted %d events, want 0 (the value was not recorded as delivered)",
			len(quiet))
	}

	// The COUNT alone: same state, more sources. Without its own clause the tally
	// a client renders is stuck at 1 for the life of the run.
	src.set(id, SessionActivity{State: ActivityInput, Count: 3})
	if ev := eventFor(t, m.diffStatuses(), id); ev.ActivityCount != 3 {
		t.Errorf("after the count changed, event ActivityCount = %d, want 3", ev.ActivityCount)
	}

	// Withdrawal is a change too: the host stops reporting, and the mark has to be
	// told to go away.
	src.set(id, SessionActivity{})
	ev := eventFor(t, m.diffStatuses(), id)
	if ev.Activity != "" || ev.ActivityCount != 0 {
		t.Errorf("after withdrawal, event Activity = %q count = %d, want \"\" / 0",
			ev.Activity, ev.ActivityCount)
	}
}

// TestRemovedEventCarriesNoActivity verifies a removed session reports no
// secondary activity. The session is gone, so there is nothing left to be busy;
// carrying the last value would leave a client that reads the fields before it
// checks Removed painting a mark on a tab it is about to drop.
func TestRemovedEventCarriesNoActivity(t *testing.T) {
	src := newActivitySource()
	m := NewSessionManager(catFactory, WithSessionActivity(src.read))
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()

	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	src.set(id, SessionActivity{State: ActivityWorking, Count: 1})
	if ev := eventFor(t, m.diffStatuses(), id); ev.Activity != ActivityWorking {
		t.Fatalf("precondition: event Activity = %q, want %q", ev.Activity, ActivityWorking)
	}

	if !m.Close(id) {
		t.Fatalf("Close(%s) = false, want true", LogID(id))
	}
	ev := eventFor(t, m.diffStatuses(), id)
	// The removal marker first, or the assertions below pass on a live event.
	if !ev.Removed {
		t.Fatalf("event for the closed session has Removed = false, want true")
	}
	if ev.Activity != "" || ev.ActivityCount != 0 {
		t.Errorf("removed event Activity = %q count = %d, want \"\" / 0",
			ev.Activity, ev.ActivityCount)
	}
}
