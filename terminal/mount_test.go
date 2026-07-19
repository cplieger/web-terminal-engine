package terminal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mountStubs builds a mux wired through MountSessionRoutes with three named
// stubs, returning the mux and a hit recorder keyed by stub name.
func mountStubs(t *testing.T, opts ...MountOption) (*http.ServeMux, map[string]int) {
	t.Helper()
	hits := make(map[string]int)
	stub := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits[name]++
			w.WriteHeader(http.StatusOK)
		})
	}
	mux := http.NewServeMux()
	MountSessionRoutes(mux, stub("ws"), stub("rest"), stub("events"), opts...)
	return mux, hits
}

// TestMountSessionRoutesTopology pins the mount contract: each of the four
// documented paths routes to its designated handler, including the two
// ServeMux subtleties the helper owns (the REST exact+subtree double mount,
// and the SSE path winning over the REST subtree by specificity).
func TestMountSessionRoutesTopology(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodGet, WSPath, "ws"},
		{http.MethodGet, WSPath + "?session=abc", "ws"},
		{http.MethodPost, SessionsPath, "rest"},
		{http.MethodGet, SessionsPath, "rest"},
		{http.MethodDelete, SessionsSubtreePath + "some-id", "rest"},
		{http.MethodPut, SessionsSubtreePath + "some-id/title", "rest"},
		{http.MethodGet, SessionEventsPath, "events"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			mux, hits := mountStubs(t)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s = %d, want 200", tc.method, tc.path, rec.Code)
			}
			if hits[tc.want] != 1 {
				t.Errorf("%s %s routed to %v, want exactly one hit on %q", tc.method, tc.path, hits, tc.want)
			}
		})
	}
}

// TestMountSessionRoutesCreateGate pins the WithCreateGate contract: the gate
// wraps the REST handler on both its mounts and never wraps the WebSocket or
// events handlers, and a nil gate is ignored.
func TestMountSessionRoutesCreateGate(t *testing.T) {
	gated := 0
	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gated++
			next.ServeHTTP(w, r)
		})
	}

	mux, hits := mountStubs(t, WithCreateGate(gate))
	serve := func(method, path string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	}

	serve(http.MethodPost, SessionsPath)
	if gated != 1 || hits["rest"] != 1 {
		t.Errorf("POST %s: gate hits = %d, rest hits = %d, want 1 and 1", SessionsPath, gated, hits["rest"])
	}
	serve(http.MethodDelete, SessionsSubtreePath+"id")
	if gated != 2 || hits["rest"] != 2 {
		t.Errorf("DELETE subtree: gate hits = %d, rest hits = %d, want 2 and 2 (gate wraps both REST mounts)", gated, hits["rest"])
	}
	serve(http.MethodGet, WSPath)
	serve(http.MethodGet, SessionEventsPath)
	if gated != 2 {
		t.Errorf("gate hits after /ws + events = %d, want 2 (gate must not wrap ws or events)", gated)
	}

	// A nil gate is skipped: routing still works.
	mux2, hits2 := mountStubs(t, WithCreateGate(nil))
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SessionsPath, nil))
	if rec.Code != http.StatusOK || hits2["rest"] != 1 {
		t.Errorf("nil gate: GET %s = %d with rest hits %d, want 200 and 1", SessionsPath, rec.Code, hits2["rest"])
	}
}

// TestMountAPIWiresManagerHandlers is the convenience-path smoke test: a real
// manager mounted via MountAPI answers the session list on SessionsPath.
func TestMountAPIWiresManagerHandlers(t *testing.T) {
	factory := func(id string) *Handler {
		return NewHandler([]string{"/bin/cat"})
	}
	mgr := NewSessionManager(factory)
	t.Cleanup(mgr.Shutdown)

	mux := http.NewServeMux()
	mgr.MountAPI(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SessionsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", SessionsPath, rec.Code)
	}
	var infos []SessionInfo
	if err := json.NewDecoder(rec.Body).Decode(&infos); err != nil {
		t.Fatalf("list response is not a JSON session array: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("fresh manager listed %d sessions, want 0", len(infos))
	}

	// A plain (non-upgrade) GET with an unknown session id gets Accept's 426
	// through the same mount — the SAME answer a known id gives, so a probe
	// cannot distinguish session existence (the old 404-vs-426 oracle). A
	// real WebSocket dial to an unknown id gets the accepted-then-closed 4004
	// treatment (TestWebSocketUnknownSessionClosesDefinitively).
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, WSPath+"?session=nope", nil))
	if rec2.Code != http.StatusUpgradeRequired {
		t.Errorf("GET %s?session=nope = %d, want 426 (upgrade required, matching the known-session answer)", WSPath, rec2.Code)
	}
}
