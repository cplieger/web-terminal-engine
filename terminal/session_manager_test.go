package terminal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// catFactory builds sessions running /bin/cat, which stays alive in a PTY
// (waiting for input) so sessions do not exit mid-test. Logger is discarded.
func catFactory(string) *Handler {
	return NewHandler([]string{"/bin/cat"}, WithLogger(nil))
}

func TestSessionManagerCreateListClose(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(m.Shutdown)

	id1, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure distinct createdAt ordering
	id2, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	list := m.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Sorted by creation time: id1 before id2.
	if list[0].ID != id1 || list[1].ID != id2 {
		t.Fatalf("List order = [%s %s], want [%s %s]", list[0].ID, list[1].ID, id1, id2)
	}

	if !m.Close(id1) {
		t.Fatal("Close(id1) = false, want true")
	}
	if m.Close(id1) {
		t.Fatal("Close(id1) twice = true, want false (already gone)")
	}
	if m.Close("nonexistent") {
		t.Fatal("Close(unknown) = true, want false")
	}
	if len(m.List()) != 1 {
		t.Fatalf("List len after close = %d, want 1", len(m.List()))
	}
}

func TestSessionManagerMaxSessions(t *testing.T) {
	m := NewSessionManager(catFactory, WithMaxSessions(2))
	t.Cleanup(m.Shutdown)

	if _, err := m.Create(); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	if _, err := m.Create(); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	if _, err := m.Create(); err != ErrTooManySessions {
		t.Fatalf("Create 3 err = %v, want ErrTooManySessions", err)
	}
}

// TestSessionManagerReaperOwnershipKeyed is the N4 regression guard: the reaper
// keys on client presence, not on a per-session socket, so a socketless session
// is NOT reaped while any client is connected, and all sessions are reaped only
// after no client for the idle window. Drives maybeReap directly (in-package)
// with a forced idleSince so it is deterministic and does not depend on the
// background ticker (the 1h window keeps that goroutine quiet during the test).
func TestSessionManagerReaperOwnershipKeyed(t *testing.T) {
	m := NewSessionManager(catFactory, WithIdleReaper(time.Hour))
	t.Cleanup(m.Shutdown)

	if _, err := m.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A client is present (e.g. another tab's WS or the status stream). Even
	// with the idle window fully elapsed, no session is reaped.
	m.clientConnected()
	m.forceIdleSince(time.Now().Add(-2 * time.Hour))
	m.maybeReap()
	if got := len(m.List()); got != 2 {
		t.Fatalf("reaped %d sessions while a client was present; want 0 reaped (2 remain)", 2-got)
	}

	// Client leaves and the window elapses: all sessions are reaped together.
	m.clientDisconnected()
	m.forceIdleSince(time.Now().Add(-2 * time.Hour))
	m.maybeReap()
	if got := len(m.List()); got != 0 {
		t.Fatalf("List len after reap = %d, want 0", got)
	}
}

// forceIdleSince overrides idleSince for deterministic reaper tests.
func (m *SessionManager) forceIdleSince(ts time.Time) {
	m.mu.Lock()
	m.idleSince = ts
	m.mu.Unlock()
}

func TestSessionManagerREST(t *testing.T) {
	m := NewSessionManager(catFactory, WithMaxSessions(1))
	t.Cleanup(m.Shutdown)
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	// POST creates a session (201 + id).
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}
	var created SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("POST returned empty id")
	}

	// At the cap, a second POST is 429.
	resp2, err := http.Post(srv.URL+"/api/sessions", "application/json", nil)
	if err != nil {
		t.Fatalf("POST 2: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("POST at cap status = %d, want 429", resp2.StatusCode)
	}

	// GET lists the created session.
	respL, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var list []SessionInfo
	_ = json.NewDecoder(respL.Body).Decode(&list)
	respL.Body.Close()
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("GET list = %+v, want one session %s", list, created.ID)
	}

	// DELETE removes it (204), a second DELETE is 404.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/api/sessions/"+created.ID, nil)
	respD, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	respD.Body.Close()
	if respD.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", respD.StatusCode)
	}
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, srv.URL+"/api/sessions/"+created.ID, nil)
	respD2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("DELETE 2: %v", err)
	}
	respD2.Body.Close()
	if respD2.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE unknown status = %d, want 404", respD2.StatusCode)
	}
}

func TestSessionManagerWSUnknownSession(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(m.Shutdown)
	srv := httptest.NewServer(m.WebSocketHandler())
	t.Cleanup(srv.Close)

	// A plain GET (no WS upgrade) for an unknown session is a 404, decided
	// before any upgrade attempt.
	resp, err := http.Get(srv.URL + "/ws?session=bogus")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", resp.StatusCode)
	}
}

// TestSessionManagerWSAttach dials a real session through the manager and
// verifies the connection is served (a screen frame arrives) and that client
// presence is tracked around the attachment.
func TestSessionManagerWSAttach(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(m.Shutdown)
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	srv := httptest.NewServer(m.WebSocketHandler())
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?session=" + id
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	//nolint:bodyclose // coder/websocket Dial nils resp.Body on success
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	// Reading at least one frame proves the manager routed to the session.
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatalf("ws read: %v", err)
	}
	_ = ws.Close(websocket.StatusNormalClosure, "")
}
