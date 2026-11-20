package terminal

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/go/httpserver/v2"
	"github.com/cplieger/go/httpserver/v2/httpservertest"
)

// The package writes exactly codeSpecs plus httpserver's cross_origin_refused.
func TestCodeSpecs_matchSource(t *testing.T) {
	written := CodeSpecs()
	written[codeCrossOriginRefused] = httpserver.CodeSpecs()[codeCrossOriginRefused]
	httpservertest.CheckCodeSpecs(t, ".", written)
}

// CodeSpecs hands out a copy, so a caller's edit never reaches the default
// writer's remediation.
func TestCodeSpecs_isACopy(t *testing.T) {
	got := CodeSpecs()
	delete(got, codeSessionUnknown)
	if !maps.Equal(CodeSpecs(), codeSpecs) {
		t.Error("CodeSpecs() shares its map with the package")
	}
}

// Without WithErrors a refusal is httpserver's envelope with this package's
// remediation and a request id, and keeps the session routes' no-store policy.
func TestDefaultRefusalIsTheEnvelope(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	rec := httptest.NewRecorder()

	m.RESTHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, SessionsSubtreePath+"0123", nil))

	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body %q is not the envelope: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusNotFound || body.Code != codeSessionUnknown {
		t.Errorf("DELETE unknown session = %d %s, want 404 %s", rec.Code, body.Code, codeSessionUnknown)
	}
	if body.Remediation != codeSpecs[codeSessionUnknown].Remediation || body.RequestID == "" {
		t.Errorf("refusal = %+v, want this package's remediation and a request id", body)
	}
	if got := rec.Header().Get(cacheControlHeader); got != noStorePolicy {
		t.Errorf("Cache-Control = %q, want %q", got, noStorePolicy)
	}
}

// refusal is one call a recording ErrorResponder saw.
type refusal struct {
	code   httpserver.ErrorCode
	status int
}

func recordingResponder(seen *[]refusal) httpserver.ErrorResponder {
	return func(w http.ResponseWriter, _ *http.Request, status int, code httpserver.ErrorCode, _ string) {
		*seen = append(*seen, refusal{code: code, status: status})
		w.WriteHeader(status)
	}
}

// WithErrors writes the REST, status-stream and WebSocket-origin refusals,
// the last for an unknown session and for a session handler the manager built.
func TestWithErrorsWritesEveryRoute(t *testing.T) {
	var seen []refusal
	m := NewSessionManager(catFactory, WithErrors(recordingResponder(&seen)))
	t.Cleanup(func() { shutdownManager(t, m) })
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	foreign := func(target string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Origin", "https://elsewhere.example")
		return r
	}
	tests := []struct {
		name    string
		handler http.Handler
		req     *http.Request
		wrap    func(http.ResponseWriter) http.ResponseWriter
		want    refusal
	}{
		{"unknown session", m.RESTHandler(), httptest.NewRequest(http.MethodDelete, SessionsSubtreePath+"0123", nil), nil, refusal{codeSessionUnknown, http.StatusNotFound}},
		{"empty pinned title", m.RESTHandler(), httptest.NewRequest(http.MethodPut, SessionsSubtreePath+string(id)+"/pinned-title", strings.NewReader(`{"title":"  "}`)), nil, refusal{codeSessionTitleEmpty, http.StatusBadRequest}},
		{"invalid body", m.RESTHandler(), httptest.NewRequest(http.MethodPut, SessionsSubtreePath+"order", strings.NewReader(`{`)), nil, refusal{codeSessionBodyInvalid, http.StatusBadRequest}},
		{"stale order", m.RESTHandler(), httptest.NewRequest(http.MethodPut, SessionsSubtreePath+"order", strings.NewReader(`{"order":[]}`)), nil, refusal{codeSessionOrderStale, http.StatusConflict}},
		{"stream without flush", m.EventsHandler(), httptest.NewRequest(http.MethodGet, SessionEventsPath, nil), hideFlush, refusal{codeSessionEventsUnsupported, http.StatusInternalServerError}},
		{"origin on an unknown session", m.WebSocketHandler(), foreign(WSPath + "?session=0123"), nil, refusal{codeCrossOriginRefused, http.StatusForbidden}},
		{"origin on a live session", m.WebSocketHandler(), foreign(WSPath + "?session=" + string(id)), nil, refusal{codeCrossOriginRefused, http.StatusForbidden}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen = seen[:0]
			var w http.ResponseWriter = httptest.NewRecorder()
			if tt.wrap != nil {
				w = tt.wrap(w)
			}

			tt.handler.ServeHTTP(w, tt.req)

			if len(seen) != 1 || seen[0] != tt.want {
				t.Errorf("responder saw %+v, want one %+v", seen, tt.want)
			}
		})
	}
}

func hideFlush(http.ResponseWriter) http.ResponseWriter { return noFlushWriter{} }

// A standalone Handler, built with no manager, refuses a foreign Origin with
// the default envelope: httpserver's cross_origin_refused, its remediation
// and a request id.
func TestStandaloneHandlerOriginRefusal(t *testing.T) {
	h := catFactory("")
	t.Cleanup(h.Close)
	req := httptest.NewRequest(http.MethodGet, WSPath, nil)
	req.Header.Set("Origin", "https://elsewhere.example")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	var body httpserver.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("refusal body %q is not the envelope: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusForbidden || body.Code != codeCrossOriginRefused {
		t.Errorf("foreign Origin = %d %s, want 403 %s", rec.Code, body.Code, codeCrossOriginRefused)
	}
	if want := httpserver.CodeSpecs()[codeCrossOriginRefused].Remediation; body.Remediation != want || body.RequestID == "" {
		t.Errorf("refusal = %+v, want httpserver's remediation %q and a request id", body, want)
	}
}
