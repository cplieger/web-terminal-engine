package terminal

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// inputClassifier maps the two kiro-cli notification strings the way vibecli
// does, for exercising the latched needs-input/done state machine.
func inputClassifier(msg string) (string, bool) {
	switch msg {
	case "Permission required":
		return StatusInput, true
	case "Response complete":
		return StatusDone, true
	}
	return "", false
}

func handlerOf(t *testing.T, m *SessionManager, id string) *Handler {
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
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	// A prompt followed by a needs-input notification: latched input.
	h.handlePTYData([]byte("Allow? \x1b]9;Permission required\x07"))
	if st := m.computeStatus(h, tr); st != StatusInput {
		t.Fatalf("after notification, status = %q, want %q", st, StatusInput)
	}
	// No resume: the latch persists across sweeps.
	if st := m.computeStatus(h, tr); st != StatusInput {
		t.Fatalf("latch did not persist, status = %q, want %q", st, StatusInput)
	}
	// The turn resumes: an active progress signal (OSC 9;4;3) clears the latch
	// and reports working.
	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatus(h, tr); st != StatusWorking {
		t.Fatalf("after resume progress, status = %q, want %q", st, StatusWorking)
	}
}

// TestComputeStatusDoneSupersedesInput verifies a classified done notification
// ("Response complete") replaces an input latch with the done state.
func TestComputeStatusDoneSupersedesInput(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;Permission required\x07"))
	if st := m.computeStatus(h, tr); st != StatusInput {
		t.Fatalf("precondition: status = %q, want %q", st, StatusInput)
	}
	h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
	if st := m.computeStatus(h, tr); st != StatusDone {
		t.Fatalf("done did not supersede input latch; status = %q, want %q", st, StatusDone)
	}
}

// TestComputeStatusWorkingFromProgress verifies an active OSC 9;4 progress state
// (3 indeterminate) reports working, and clearing it (0) drops to idle when
// nothing is latched.
func TestComputeStatusWorkingFromProgress(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatus(h, tr); st != StatusWorking {
		t.Fatalf("progress 3: status = %q, want %q", st, StatusWorking)
	}
	h.handlePTYData([]byte("\x1b]9;4;0\x07"))
	if st := m.computeStatus(h, tr); st != StatusIdle {
		t.Fatalf("progress 0 with no latch: status = %q, want %q", st, StatusIdle)
	}
}

// TestComputeStatusDoneLatchPersistsThenClears verifies "Response complete"
// latches done through the quiet gap, and the next working progress clears it.
func TestComputeStatusDoneLatchPersistsThenClears(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;4;0\x07\x1b]9;Response complete\x07"))
	if st := m.computeStatus(h, tr); st != StatusDone {
		t.Fatalf("after done notification: status = %q, want %q", st, StatusDone)
	}
	// Persists across a quiet sweep (no progress, no output-driven flip).
	if st := m.computeStatus(h, tr); st != StatusDone {
		t.Fatalf("done latch did not persist: status = %q, want %q", st, StatusDone)
	}
	// Next turn starts working, clearing the done latch.
	h.handlePTYData([]byte("\x1b]9;4;3\x07"))
	if st := m.computeStatus(h, tr); st != StatusWorking {
		t.Fatalf("next working phase: status = %q, want %q", st, StatusWorking)
	}
}

// TestComputeStatusNoWorkingFromOutput verifies a program that never reports
// OSC 9;4 progress stays idle even while producing output: working now comes
// ONLY from OSC 9;4, so a plain shell under web-terminal-server never flaps to
// working merely because it (or the user typing at the prompt) produced bytes.
// The reveal gate then keeps such a session's tab dot hidden.
func TestComputeStatusNoWorkingFromOutput(t *testing.T) {
	m := NewSessionManager(catFactory) // no classifier: a generic shell
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("some output"))
	if st := m.computeStatus(h, tr); st != StatusIdle {
		t.Fatalf("output with no OSC 9;4 progress: status = %q, want %q (no output-activity fallback)", st, StatusIdle)
	}
}

// TestReportsActivitySticky verifies the reportsActivity flag the client uses to
// reveal the per-tab activity dot: false until an OSC 9;4 signal appears, then
// sticky true even after the progress is cleared (Progress stays >= 0), so the
// dot stays revealed while the session idles rather than flickering away.
func TestReportsActivitySticky(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)

	reports := func() bool {
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

// TestComputeStatusExited verifies an exited process reports exited regardless
// of activity or latch.
func TestComputeStatusExited(t *testing.T) {
	m := NewSessionManager(func(string) *Handler {
		return NewHandler([]string{"/bin/true"}, WithLogger(nil))
	})
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	deadline := time.Now().Add(2 * time.Second)
	for !h.Exited() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := m.computeStatus(h, &statusTracker{}); st != StatusExited {
		t.Fatalf("status = %q, want %q", st, StatusExited)
	}
}

// TestEventsHandlerInitialSync verifies a new SSE subscriber immediately
// receives the current status of existing sessions (initial sync).
func TestEventsHandlerInitialSync(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	srv := httptest.NewServer(m.EventsHandler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
		if strings.HasPrefix(line, "data:") && strings.Contains(line, id) {
			return // initial sync delivered the session
		}
	}
	t.Fatalf("initial sync did not include session %s (scan err: %v)", id, sc.Err())
}

// TestEventsHandlerSubscriberCap verifies the subscriber cap returns 503 once
// the limit is reached.
func TestEventsHandlerSubscriberCap(t *testing.T) {
	m := NewSessionManager(catFactory, WithMaxSubscribers(1))
	t.Cleanup(m.Shutdown)
	srv := httptest.NewServer(m.EventsHandler())
	t.Cleanup(srv.Close)

	req1, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("sub1: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("sub1 status = %d, want 200", resp1.StatusCode)
	}
	time.Sleep(30 * time.Millisecond) // let sub1 register

	ctx2, cancel2 := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodGet, srv.URL, nil)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("sub2: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sub2 status = %d, want 503", resp2.StatusCode)
	}
}

// unwrapOnlyWriter mimics an access-log middleware wrapper (web-terminal-server's
// statusWriter, vibecli's statusRecorder): it exposes the underlying
// ResponseWriter via Unwrap but does NOT itself implement http.Flusher
// (embedding the ResponseWriter interface does not promote Flush). It pins that
// EventsHandler finds the flusher through the Unwrap chain rather than a direct
// type assertion, which is what makes SSE work behind middleware.
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
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.EventsHandler().ServeHTTP(unwrapOnlyWriter{w}, r)
	})
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
		if line := sc.Text(); strings.HasPrefix(line, "data:") && strings.Contains(line, id) {
			return // stream flushed the initial sync through the middleware
		}
	}
	t.Fatalf("SSE stream delivered no data behind middleware (scan err: %v)", sc.Err())
}
