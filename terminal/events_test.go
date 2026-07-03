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
// will, for exercising the latched needs-input state machine.
func inputClassifier(msg string) (string, bool) {
	switch msg {
	case "Permission required":
		return StatusInput, true
	case "Response complete":
		return StatusIdle, true
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
// latches input and persists (the process is blocked, so no output), then the
// latch clears once output resumes after the prompt (the turn continued).
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
	if st := m.computeStatus(h, tr, time.Now()); st != StatusInput {
		t.Fatalf("after notification, status = %q, want %q", st, StatusInput)
	}
	// No resume output: the latch persists.
	if st := m.computeStatus(h, tr, time.Now()); st != StatusInput {
		t.Fatalf("latch did not persist, status = %q, want %q", st, StatusInput)
	}
	// Turn resumes: with the latch set in the past, new output clears it.
	tr.latchAt = time.Now().Add(-time.Second)
	h.handlePTYData([]byte("continuing..."))
	if st := m.computeStatus(h, tr, time.Now()); st != StatusWorking {
		t.Fatalf("after resume, status = %q, want %q", st, StatusWorking)
	}
}

// TestComputeStatusDoneClearsLatch verifies a classified done notification
// ("Response complete") clears an input latch.
func TestComputeStatusDoneClearsLatch(t *testing.T) {
	m := NewSessionManager(catFactory, WithStatusClassifier(inputClassifier))
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := handlerOf(t, m, id)
	tr := &statusTracker{}

	h.handlePTYData([]byte("\x1b]9;Permission required\x07"))
	if st := m.computeStatus(h, tr, time.Now()); st != StatusInput {
		t.Fatalf("precondition: status = %q, want %q", st, StatusInput)
	}
	h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
	if st := m.computeStatus(h, tr, time.Now()); st == StatusInput {
		t.Fatalf("done did not clear the input latch; status = %q", st)
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
	if st := m.computeStatus(h, &statusTracker{}, time.Now()); st != StatusExited {
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
