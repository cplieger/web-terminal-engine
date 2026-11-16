package terminal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sizeReporter builds sessions that print the size their pty had at start,
// in stty's rows-then-columns order, and then never redraw.
func sizeReporter(SessionID) *Handler {
	return NewHandler([]string{"/bin/sh", "-c", `printf 'size=%s\n' "$(stty size)"; exec sleep 60`},
		WithLogger(nil), WithWorkDir("/"))
}

// sizeReporterServer serves a sizeReporter manager's REST API.
func sizeReporterServer(t *testing.T) (*SessionManager, *httptest.Server) {
	t.Helper()
	m := NewSessionManager(sizeReporter)
	t.Cleanup(func() { shutdownManager(t, m) })
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)
	return m, srv
}

// postCreate POSTs body to the create route; "" sends no body.
func postCreate(t *testing.T, srv *httptest.Server, body string) (status int, respBody []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", r)
	if err != nil {
		t.Fatalf("POST /api/sessions: %v", err)
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read create response: %v", err)
	}
	return resp.StatusCode, respBody
}

// createdHandler POSTs body, asserts a 201 and returns the created session's handler.
func createdHandler(t *testing.T, m *SessionManager, srv *httptest.Server, body string) *Handler {
	t.Helper()
	status, respBody := postCreate(t, srv, body)
	if status != http.StatusCreated {
		t.Fatalf("POST %q status = %d, want 201", body, status)
	}
	var info SessionInfo
	if err := json.Unmarshal(respBody, &info); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return sessionHandler(t, m, info.ID)
}

// sessionHandler returns the live handler of session id.
func sessionHandler(t *testing.T, m *SessionManager, id SessionID) *Handler {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		t.Fatalf("session %s is not live", LogID(id))
	}
	return s.handler
}

func TestCreateREST_spawnsAtTheBodySize(t *testing.T) {
	m, srv := sizeReporterServer(t)
	h := createdHandler(t, m, srv, `{"cols":50,"rows":20}`)
	awaitMainScreenText(t, h, "size=20 50")
}

// TestCreateREST_withoutABodySpawnsAt80x24 pins the fallback for a create no
// client sized: an API create, a smoke test's bare POST.
func TestCreateREST_withoutABodySpawnsAt80x24(t *testing.T) {
	m, srv := sizeReporterServer(t)
	h := createdHandler(t, m, srv, "")
	awaitMainScreenText(t, h, "size=24 80")
}

func TestCreateREST_clampsAnOutOfRangeSize(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"below the minimum floors to 20x5", `{"cols":5,"rows":2}`, "size=5 20"},
		{"columns above the maximum cap at 1000", `{"cols":1500,"rows":10}`, "size=10 1000"},
		{"rows above the maximum cap at 1000", `{"cols":30,"rows":1500}`, "size=1000 30"},
		{"zero columns start at the default", `{"cols":0,"rows":30}`, "size=30 80"},
		{"negative values start at the default", `{"cols":-3,"rows":-3}`, "size=24 80"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := sizeReporterServer(t)
			h := createdHandler(t, m, srv, tc.body)
			awaitMainScreenText(t, h, tc.want)
		})
	}
}

func TestCreateREST_refusesAnUnreadableBody(t *testing.T) {
	prefix := `{"cols":50,"rows":20,"pad":"`
	oversized := prefix + strings.Repeat("x", 300-len(prefix)-2) + `"}`
	tests := []struct {
		name string
		body string
	}{
		{"a string field", `{"cols":"wide"}`},
		{"a fraction", `{"cols":80.5,"rows":24}`},
		{"not JSON", `not json`},
		{"data after the object", `{"cols":50,"rows":20}{}`},
		{"over 256 bytes", oversized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, srv := sizeReporterServer(t)
			if status, _ := postCreate(t, srv, tc.body); status != http.StatusBadRequest {
				t.Errorf("POST %q status = %d, want 400", tc.body, status)
			}
			if got := m.List(); len(got) != 0 {
				t.Errorf("List() after a refused create = %+v, want no session", got)
			}
		})
	}
}

func TestCreateWith_spawnsAtTheSpecSize(t *testing.T) {
	m := NewSessionManager(sizeReporter)
	t.Cleanup(func() { shutdownManager(t, m) })
	info, err := m.CreateWith(CreateSpec{Cols: 50, Rows: 20})
	if err != nil {
		t.Fatalf("CreateWith(Cols: 50, Rows: 20) = %v, want nil", err)
	}
	awaitMainScreenText(t, sessionHandler(t, m, info.ID), "size=20 50")
}
