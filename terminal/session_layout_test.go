package terminal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type flatLayout struct {
	left     string
	right    string
	selected PaneSide
	handle   float64
	open     bool
}

func flat(l PaneLayout) flatLayout {
	f := flatLayout{selected: l.Selected, handle: l.Handle, open: l.Open}
	if l.Left != nil {
		f.left = string(*l.Left)
	}
	if l.Right != nil {
		f.right = string(*l.Right)
	}
	return f
}

func createSessions(t *testing.T, m *SessionManager, n int) []SessionID {
	t.Helper()
	ids := make([]SessionID, 0, n)
	for range n {
		id, err := m.Create()
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func setLayout(t *testing.T, m *SessionManager, l PaneLayout) {
	t.Helper()
	if verdict, reason := m.SetPaneLayout(l); verdict != LayoutOK {
		t.Fatalf("SetPaneLayout(%+v) = %d (%s), want LayoutOK", flat(l), verdict, reason)
	}
}

func putLayout(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		srv.URL+SessionsPath+"/layout", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	text, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read PUT response: %v", err)
	}
	return resp.StatusCode, string(text)
}

func getLayout(t *testing.T, srv *httptest.Server) (PaneLayout, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		srv.URL+SessionsPath+"/layout", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET layout status = %d, want 200", resp.StatusCode)
	}
	var l PaneLayout
	if err := json.NewDecoder(resp.Body).Decode(&l); err != nil {
		t.Fatalf("decode GET layout body: %v", err)
	}
	return l, resp.Header
}

func TestGetLayoutDefault(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	got, header := getLayout(t, srv)
	want := flatLayout{selected: PaneLeft, handle: 0.5}
	if flat(got) != want {
		t.Errorf("GET layout on a fresh manager = %+v, want %+v", flat(got), want)
	}
	if cc := header.Get(cacheControlHeader); !hasDirective(cc, noStorePolicy) {
		t.Errorf("Cache-Control = %q, want no-store (the body carries session ids)", cc)
	}
}

// The body carries session ids, so an outer max-age must not win over no-store,
// as TestSessionRESTNoStorePrecedence pins for the list.
func TestGetLayoutOverridesAnOuterCachePolicy(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	inner := m.RESTHandler()
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(cacheControlHeader, "max-age=60")
		inner.ServeHTTP(w, r)
	})

	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SessionsPath+"/layout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET layout = %d, want 200", rec.Code)
	}
	if cc := rec.Result().Header.Get(cacheControlHeader); !hasDirective(cc, noStorePolicy) {
		t.Errorf("Cache-Control = %q, want no-store: the body carries the shown session ids", cc)
	}
}

func TestLayoutFirstCreateFillsLeft(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })

	first := createSessions(t, m, 1)[0]
	want := flatLayout{left: string(first), selected: PaneLeft, handle: 0.5}
	if got := flat(m.PaneLayout()); got != want {
		t.Errorf("after the first Create, PaneLayout() = %+v, want %+v", got, want)
	}
	createSessions(t, m, 1)
	if got := flat(m.PaneLayout()); got != want {
		t.Errorf("after the second Create, PaneLayout() = %+v, want it unchanged at %+v", got, want)
	}
}

func TestLayoutEmptySetAcceptsAnEmptyRecord(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })

	setLayout(t, m, PaneLayout{Selected: PaneRight, Handle: 0.3, Open: true})
	want := flatLayout{selected: PaneRight, handle: 0.3, open: true}
	if got := flat(m.PaneLayout()); got != want {
		t.Fatalf("PaneLayout() with no sessions = %+v, want %+v", got, want)
	}

	id := createSessions(t, m, 1)[0]
	want = flatLayout{left: string(id), selected: PaneLeft, handle: 0.3, open: true}
	if got := flat(m.PaneLayout()); got != want {
		t.Errorf("after the first Create, PaneLayout() = %+v, want %+v", got, want)
	}
}

func TestSetLayoutRoute(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	ids := createSessions(t, m, 3)
	a, b, closed := ids[0], ids[1], ids[2]
	if !m.Close(closed) {
		t.Fatal("Close = false, want true")
	}
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)
	q := func(id SessionID) string { return `"` + string(id) + `"` }

	cases := []struct {
		name     string
		body     string
		want     int
		wantBody string
		stored   flatLayout
	}{
		{
			"both panes shown, right selected",
			`{"left":` + q(a) + `,"right":` + q(b) + `,"selected":"right","handle":0.3,"open":true}`,
			http.StatusNoContent, "",
			flatLayout{left: string(a), right: string(b), selected: PaneRight, handle: 0.3, open: true},
		},
		{
			"closed, left shown",
			`{"left":` + q(a) + `,"right":null,"selected":"left","handle":0.5,"open":false}`,
			http.StatusNoContent, "",
			flatLayout{left: string(a), selected: PaneLeft, handle: 0.5},
		},
		{"selected middle", `{"left":` + q(a) + `,"right":null,"selected":"middle","handle":0.5,"open":false}`, http.StatusBadRequest, "left or right", flatLayout{}},
		{"handle 1.5", `{"left":` + q(a) + `,"right":` + q(b) + `,"selected":"left","handle":1.5,"open":true}`, http.StatusBadRequest, "handle", flatLayout{}},
		{"handle -0.1", `{"left":` + q(a) + `,"right":` + q(b) + `,"selected":"left","handle":-0.1,"open":true}`, http.StatusBadRequest, "handle", flatLayout{}},
		{"closed with a right pane", `{"left":` + q(a) + `,"right":` + q(b) + `,"selected":"left","handle":0.5,"open":false}`, http.StatusBadRequest, "closed", flatLayout{}},
		{"closed with right selected", `{"left":` + q(a) + `,"right":null,"selected":"right","handle":0.5,"open":false}`, http.StatusBadRequest, "closed", flatLayout{}},
		{"the same session on both sides", `{"left":` + q(a) + `,"right":` + q(a) + `,"selected":"left","handle":0.5,"open":true}`, http.StatusBadRequest, "same session", flatLayout{}},
		{"open with only the right shown and left selected", `{"left":null,"right":` + q(b) + `,"selected":"left","handle":0.5,"open":true}`, http.StatusBadRequest, "other pane shows a session", flatLayout{}},
		{"closed with left null while a session is live", `{"left":null,"right":null,"selected":"left","handle":0.5,"open":false}`, http.StatusBadRequest, "while a session is live", flatLayout{}},
		{"open with both panes empty while a session is live", `{"left":null,"right":null,"selected":"left","handle":0.5,"open":true}`, http.StatusBadRequest, "while a session is live", flatLayout{}},
		{"no open field", `{"left":` + q(a) + `,"right":null,"selected":"left","handle":0.5}`, http.StatusBadRequest, "must carry", flatLayout{}},
		{"no handle field", `{"left":` + q(a) + `,"right":null,"selected":"left","open":false}`, http.StatusBadRequest, "must carry", flatLayout{}},
		{"no selected field", `{"left":` + q(a) + `,"right":null,"handle":0.5,"open":false}`, http.StatusBadRequest, "must carry", flatLayout{}},
		{"handle as a string", `{"left":` + q(a) + `,"right":null,"selected":"left","handle":"0.5","open":false}`, http.StatusBadRequest, "invalid body", flatLayout{}},
		{"not JSON", `<layout/>`, http.StatusBadRequest, "invalid body", flatLayout{}},
		{"empty body", ``, http.StatusBadRequest, "invalid body", flatLayout{}},
		{"left names an id that is not a session", `{"left":"nope","right":null,"selected":"left","handle":0.5,"open":false}`, http.StatusConflict, "not live", flatLayout{}},
		{"right names a closed session", `{"left":` + q(a) + `,"right":` + q(closed) + `,"selected":"right","handle":0.5,"open":true}`, http.StatusConflict, "not live", flatLayout{}},
	}
	// The first 204 must CHANGE the record, or every "last accepted record"
	// assertion below would pass against a handler that stores nothing.
	accepted := flatLayout{left: string(a), selected: PaneLeft, handle: 0.5}
	if got := flat(m.PaneLayout()); got != accepted {
		t.Fatalf("before any write, PaneLayout() = %+v, want %+v", got, accepted)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, text := putLayout(t, srv, tc.body)
			if status != tc.want {
				t.Fatalf("PUT %s: status = %d (%q), want %d", tc.body, status, text, tc.want)
			}
			if status == http.StatusNoContent {
				accepted = tc.stored
			} else if !strings.Contains(text, tc.wantBody) {
				t.Errorf("PUT %s: body = %q, want it to name the rule (%q)", tc.body, text, tc.wantBody)
			}
			got, _ := getLayout(t, srv)
			if flat(got) != accepted {
				t.Errorf("after PUT %s: GET layout = %+v, want the last accepted record %+v",
					tc.body, flat(got), accepted)
			}
		})
	}
}

func TestSetLayoutRouteRejectsAnOversizedBody(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	createSessions(t, m, 1)
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	flood := `{"left":"` + strings.Repeat("a", maxLayoutBodyBytes+1) +
		`","right":null,"selected":"left","handle":0.5,"open":false}`
	if status, _ := putLayout(t, srv, flood); status != http.StatusBadRequest {
		t.Errorf("PUT of %d bytes: status = %d, want 400", len(flood), status)
	}
}

func TestSetLayoutRouteRejectsTrailingData(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	id := createSessions(t, m, 1)[0]
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	valid := `{"left":"` + string(id) + `","right":null,"selected":"left","handle":0.5,"open":false}`
	if status, _ := putLayout(t, srv, valid); status != http.StatusNoContent {
		t.Fatalf("PUT of the valid record alone: status = %d, want 204", status)
	}
	for _, body := range []string{valid + valid, valid + " nope"} {
		status, text := putLayout(t, srv, body)
		if status != http.StatusBadRequest {
			t.Errorf("PUT %s: status = %d, want 400", body, status)
		}
		if !strings.Contains(text, "after the body") {
			t.Errorf("PUT %s: body = %q, want it to name the trailing data", body, text)
		}
	}
}

func TestSetPaneLayoutDoesNotAliasCaller(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	ids := createSessions(t, m, 2)

	caller := PaneLayout{Left: new(ids[0]), Right: new(ids[1]), Selected: PaneRight, Handle: 0.4, Open: true}
	setLayout(t, m, caller)
	*caller.Left, *caller.Right = "overwritten", "overwritten"
	want := flatLayout{left: string(ids[0]), right: string(ids[1]), selected: PaneRight, handle: 0.4, open: true}
	if got := flat(m.PaneLayout()); got != want {
		t.Errorf("PaneLayout() followed the caller's pointers: %+v, want %+v", got, want)
	}
}

func TestPaneLayoutDoesNotExposeManagerPointers(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	ids := createSessions(t, m, 2)
	setLayout(t, m, PaneLayout{Left: new(ids[0]), Right: new(ids[1]), Selected: PaneLeft, Handle: 0.6, Open: true})

	read := m.PaneLayout()
	*read.Left, *read.Right = "overwritten", "overwritten"
	want := flatLayout{left: string(ids[0]), right: string(ids[1]), selected: PaneLeft, handle: 0.6, open: true}
	if got := flat(m.PaneLayout()); got != want {
		t.Errorf("a write through a PaneLayout() read reached the manager: %+v, want %+v", got, want)
	}
}

func TestLayoutTracksTheSessionSet(t *testing.T) {
	m := NewSessionManager(catFactory, WithIdleReaper(time.Hour))
	t.Cleanup(func() { shutdownManager(t, m) })
	ids := createSessions(t, m, 4)
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	check := func(t *testing.T, stage string, want flatLayout) {
		t.Helper()
		if got := flat(m.PaneLayout()); got != want {
			t.Errorf("%s: PaneLayout() = %+v, want %+v", stage, got, want)
		}
	}

	setLayout(t, m, PaneLayout{Left: new(a), Right: new(b), Selected: PaneLeft, Handle: 0.4, Open: true})
	if !m.Close(a) {
		t.Fatal("Close(a) = false, want true")
	}
	check(t, "closing the selected left session",
		flatLayout{right: string(b), selected: PaneRight, handle: 0.4, open: true})

	setLayout(t, m, PaneLayout{Left: new(c), Right: new(b), Selected: PaneRight, Handle: 0.4, Open: true})
	if !m.Close(b) {
		t.Fatal("Close(b) = false, want true")
	}
	check(t, "closing the selected right session with the left shown",
		flatLayout{left: string(c), selected: PaneLeft, handle: 0.4, open: true})

	if !m.Close(c) {
		t.Fatal("Close(c) = false, want true")
	}
	check(t, "closing the only shown session while an unshown one remains",
		flatLayout{left: string(d), selected: PaneLeft, handle: 0.4, open: true})

	if !m.Close(d) {
		t.Fatal("Close(d) = false, want true")
	}
	def := flatLayout{selected: PaneLeft, handle: 0.5}
	check(t, "closing the last session", def)

	e := createSessions(t, m, 1)[0]
	check(t, "the first Create after the set emptied",
		flatLayout{left: string(e), selected: PaneLeft, handle: 0.5})

	f := createSessions(t, m, 1)[0]
	setLayout(t, m, PaneLayout{Left: new(e), Right: new(f), Selected: PaneRight, Handle: 0.3, Open: true})
	m.mu.Lock()
	m.idleSince = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()
	m.maybeReap()
	check(t, "the idle reaper", def)

	g := createSessions(t, m, 1)[0]
	check(t, "a Create after the reap", flatLayout{left: string(g), selected: PaneLeft, handle: 0.5})
	shutdownManager(t, m) // the cleanup's second Shutdown is a no-op
	check(t, "Shutdown", def)
}

func TestLayoutClosedHandleIsHalf(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	id := createSessions(t, m, 1)[0]
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	body := `{"left":"` + string(id) + `","right":null,"selected":"left","handle":0.2,"open":false}`
	if status, text := putLayout(t, srv, body); status != http.StatusNoContent {
		t.Fatalf("PUT %s: status = %d (%q), want 204", body, status, text)
	}
	got, _ := getLayout(t, srv)
	if got.Handle != 0.5 {
		t.Errorf("a closed record sent with handle 0.2 reads back handle %v, want 0.5", got.Handle)
	}
}

func TestLayoutIsNotOnTheStream(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	ids := createSessions(t, m, 2)
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	if first := m.diffStatuses(); len(first) != 2 {
		t.Fatalf("the first sweep emitted %d events, want 2 (one per session)", len(first))
	}
	if quiet := m.diffStatuses(); len(quiet) != 0 {
		t.Fatalf("a quiet manager emitted %d events, want 0", len(quiet))
	}
	ch, ok := m.subscribe()
	if !ok {
		t.Fatal("subscribe = false, want a subscriber slot")
	}
	t.Cleanup(func() { m.unsubscribe(ch) })

	body := `{"left":"` + string(ids[0]) + `","right":"` + string(ids[1]) +
		`","selected":"right","handle":0.3,"open":true}`
	if status, text := putLayout(t, srv, body); status != http.StatusNoContent {
		t.Fatalf("PUT %s: status = %d (%q), want 204", body, status, text)
	}
	if got := m.diffStatuses(); len(got) != 0 {
		t.Errorf("the sweep after a layout write emitted %d events, want 0", len(got))
	}
	if n := len(ch); n != 0 {
		t.Errorf("a layout write pushed %d frames to a subscriber, want 0", n)
	}

	// The control: a reorder still emits, so the quiet sweep above was watching.
	if !m.SetSessionOrder([]SessionID{ids[1], ids[0]}) {
		t.Fatal("SetSessionOrder = false, want true")
	}
	if got := m.diffStatuses(); len(got) != 2 {
		t.Errorf("the sweep after a reorder emitted %d events, want 2 (one per moved session)", len(got))
	}
}

func TestLayoutRoundTripThroughTheRoutes(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	ids := createSessions(t, m, 2)
	a, b := ids[0], ids[1]
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	body := `{"left":"` + string(a) + `","right":"` + string(b) + `","selected":"right","handle":0.3,"open":true}`
	if status, text := putLayout(t, srv, body); status != http.StatusNoContent {
		t.Fatalf("PUT %s: status = %d (%q), want 204", body, status, text)
	}
	got, _ := getLayout(t, srv)
	want := flatLayout{left: string(a), right: string(b), selected: PaneRight, handle: 0.3, open: true}
	if flat(got) != want {
		t.Fatalf("GET after PUT = %+v, want the record as written %+v", flat(got), want)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete,
		srv.URL+SessionsPath+"/"+string(b), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
	got, _ = getLayout(t, srv)
	want = flatLayout{left: string(a), selected: PaneLeft, handle: 0.3, open: true}
	if flat(got) != want {
		t.Errorf("GET after DELETE of the right session = %+v, want %+v", flat(got), want)
	}
}
