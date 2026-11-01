package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// handlerRunning builds a session whose command is argv, torn down with t.
func handlerRunning(t *testing.T, argv ...string) func(SessionID) *Handler {
	t.Helper()
	return func(SessionID) *Handler {
		h := NewHandler(argv, WithLogger(nil))
		shutdownOnCleanup(t, h)
		return h
	}
}

// awaitStatus polls List until id reports want, failing t after waitPatience.
func awaitStatus(t *testing.T, m *SessionManager, id SessionID, want string) {
	t.Helper()
	deadline := time.Now().Add(waitPatience)
	for {
		if listedInfo(t, m, id).Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s never reported status %q", LogID(id), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fmtIndex(p *int) string {
	if p == nil {
		return "nil"
	}
	return fmt.Sprint(*p)
}

func fmtSetLayout(l SessionSetLayout) string {
	return fmt.Sprintf("{left:%s right:%s selected:%s handle:%v open:%v}",
		fmtIndex(l.Left), fmtIndex(l.Right), l.Selected, l.Handle, l.Open)
}

func sameIndex(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func sameSetLayout(a, b SessionSetLayout) bool {
	return sameIndex(a.Left, b.Left) && sameIndex(a.Right, b.Right) &&
		a.Selected == b.Selected && a.Handle == b.Handle && a.Open == b.Open
}

func sameSessionSet(a, b SessionSet) bool {
	return a.Version == b.Version && slices.Equal(a.Sessions, b.Sessions) && sameSetLayout(a.Layout, b.Layout)
}

func TestCaptureSessionSet(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	create := func(spec CreateSpec) SessionID {
		t.Helper()
		info, err := m.CreateWith(spec)
		if err != nil {
			t.Fatalf("CreateWith: %v", err)
		}
		return info.ID
	}
	a := create(CreateSpec{Alias: "alias-a", PinnedTitle: "fix build"})
	exited := create(CreateSpec{Alias: "alias-exited", Build: handlerRunning(t, "/bin/true")})
	crashed := create(CreateSpec{Alias: "alias-crashed", Build: handlerRunning(t, "/bin/sh", "-c", "exit 3")})
	b := create(CreateSpec{Alias: "alias-b"})
	c := create(CreateSpec{Alias: "alias-c"})
	awaitStatus(t, m, exited, StatusExited)
	awaitStatus(t, m, crashed, StatusCrashed)
	if !m.SetSessionOrder([]SessionID{b, exited, a, crashed, c}) {
		t.Fatal("SetSessionOrder = false, want true")
	}
	setLayout(t, m, PaneLayout{Left: new(a), Right: new(exited), Selected: PaneRight, Handle: 0.3, Open: true})

	tokens := map[SessionID]string{
		a: "tok-a", b: strings.Repeat("x", MaxResumeTokenBytes+1), crashed: "tok-crashed", c: "r\xffr",
	}
	type captured struct{ set SessionSet }
	done := make(chan captured, 1)
	go func() {
		done <- captured{m.CaptureSessionSet(func(info SessionInfo) string {
			_ = m.List() // a resume callback may enumerate the manager
			return tokens[info.ID]
		})}
	}()
	var got SessionSet
	select {
	case c := <-done:
		got = c.set
	case <-time.After(waitPatience):
		t.Fatal("CaptureSessionSet did not return: the resume callback ran under the manager lock")
	}

	want := SessionSet{
		Version: SessionSetVersion,
		Sessions: []SessionSetEntry{
			{Alias: "alias-b"},
			{Alias: "alias-a", PinnedTitle: "fix build", Resume: "tok-a"},
			{Alias: "alias-crashed", Resume: "tok-crashed"},
			{Alias: "alias-c"},
		},
		Layout: SessionSetLayout{Left: new(1), Selected: PaneLeft, Handle: 0.3, Open: true},
	}
	if !slices.Equal(got.Sessions, want.Sessions) {
		t.Errorf("Sessions = %+q, want %+q (exited dropped, crashed kept, oversize and invalid-UTF-8 tokens dropped)",
			got.Sessions, want.Sessions)
	}
	if !sameSetLayout(got.Layout, want.Layout) {
		t.Errorf("Layout = %s, want %s (the exited right pane emptied and the selection moved)",
			fmtSetLayout(got.Layout), fmtSetLayout(want.Layout))
	}
	if got.Version != SessionSetVersion {
		t.Errorf("Version = %d, want %d", got.Version, SessionSetVersion)
	}
}

// Capture drops a cleanly exited session as soon as it has exited, before any
// sweep commits the exit, while List still serves the committed status.
func TestCaptureSessionSet_readsExitLive(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	ended, err := m.CreateWith(CreateSpec{Alias: "alias-ended", Build: handlerRunning(t, "/bin/true")})
	if err != nil {
		t.Fatalf("CreateWith: %v", err)
	}
	if _, err := m.CreateWith(CreateSpec{Alias: "alias-live"}); err != nil {
		t.Fatalf("CreateWith: %v", err)
	}
	waitExited(t, handlerOf(t, m, ended.ID))

	if st := listedInfo(t, m, ended.ID).Status; st != StatusIdle {
		t.Errorf("List status = %q, want %q (no sweep has committed the exit)", st, StatusIdle)
	}
	got := m.CaptureSessionSet(func(SessionInfo) string { return "" })
	want := []SessionSetEntry{{Alias: "alias-live"}}
	if !slices.Equal(got.Sessions, want) {
		t.Errorf("Sessions = %+q, want %+q (the exited session left out before any sweep)", got.Sessions, want)
	}
}

// At the session cap, tokens of escapable control bytes marshal past the byte
// limit; whole tokens are dropped from the end until the set fits, and no more.
func TestCaptureSessionSet_fitsTheByteLimit(t *testing.T) {
	list := make([]SessionInfo, MaxSessionSetSessions)
	for i := range list {
		list[i] = SessionInfo{ID: SessionID(fmt.Sprintf("id-%03d", i)), Alias: fmt.Sprintf("alias-%03d", i), Status: StatusIdle}
	}
	token := strings.Repeat("\x00", MaxResumeTokenBytes)
	set := captureSessionSet(list, make([]bool, len(list)), PaneLayout{Left: new(list[0].ID), Selected: PaneLeft, Handle: 0.5},
		func(SessionInfo) string { return token })

	data, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(data) > MaxSessionSetBytes {
		t.Fatalf("captured set marshals to %d bytes, want at most %d", len(data), MaxSessionSetBytes)
	}
	if _, err := ParseSessionSet(data); err != nil {
		t.Errorf("ParseSessionSet(captured) = %v, want nil", err)
	}
	kept := 0
	for kept < len(set.Sessions) && set.Sessions[kept].Resume == token {
		kept++
	}
	for i := kept; i < len(set.Sessions); i++ {
		if set.Sessions[i].Resume != "" {
			t.Fatalf("entry %d kept a token after entry %d lost one, want tokens dropped from the end", i, kept)
		}
	}
	if kept == 0 || kept == len(set.Sessions) {
		t.Fatalf("kept %d of %d tokens, want some kept and some dropped", kept, len(set.Sessions))
	}
	set.Sessions[kept].Resume = token
	if more, _ := json.Marshal(set); len(more) <= MaxSessionSetBytes {
		t.Errorf("keeping token %d still fits (%d bytes), want only as many tokens dropped as needed", kept, len(more))
	}
}

func TestParseSessionSet_accepts(t *testing.T) {
	in := `{"version":1,"future":true,"sessions":[{"alias":"tab_a","pinnedTitle":"fix build","resume":"r"},{"alias":""}],` +
		`"layout":{"left":0,"right":1,"selected":"right","handle":0.25,"open":true}}`
	got, err := ParseSessionSet([]byte(in))
	if err != nil {
		t.Fatalf("ParseSessionSet = %v, want nil", err)
	}
	want := SessionSet{
		Version:  1,
		Sessions: []SessionSetEntry{{Alias: "tab_a", PinnedTitle: "fix build", Resume: "r"}, {}},
		Layout:   SessionSetLayout{Left: new(0), Right: new(1), Selected: PaneRight, Handle: 0.25, Open: true},
	}
	if !sameSessionSet(got, want) {
		t.Errorf("ParseSessionSet = %+v, want %+v", got, want)
	}
}

func TestParseSessionSet_refuses(t *testing.T) {
	entries := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"alias":"a%d"}`, i)
		}
		return strings.Join(parts, ",")
	}
	set := func(sessions, layout string) string {
		return `{"version":1,"sessions":[` + sessions + `],"layout":` + layout + `}`
	}
	closedLeft := `{"left":0,"right":null,"selected":"left","handle":0.5,"open":false}`
	tests := []struct {
		name string
		data string
	}{
		{"version 2", `{"version":2,"sessions":[],"layout":{"selected":"left","handle":0.5}}`},
		{"oversize", set(`{"alias":"a","pinnedTitle":"`+strings.Repeat("x", MaxSessionSetBytes)+`"}`, closedLeft)},
		{"513 entries", set(entries(MaxSessionSetSessions+1), closedLeft)},
		{"duplicate alias", set(`{"alias":"a"},{"alias":"a"}`, closedLeft)},
		{"a bad alias character", set(`{"alias":"a/b"}`, closedLeft)},
		{"an index out of range", set(entries(1), `{"left":1,"selected":"left","handle":0.5,"open":false}`)},
		{"one index on both sides", set(entries(2), `{"left":0,"right":0,"selected":"left","handle":0.5,"open":true}`)},
		{"a closed split with a right side", set(entries(2), `{"left":0,"right":1,"selected":"left","handle":0.5,"open":false}`)},
		{"handle below 0", set(entries(2), `{"left":0,"right":1,"selected":"left","handle":-0.1,"open":true}`)},
		{"handle above 1", set(entries(2), `{"left":0,"right":1,"selected":"left","handle":1.5,"open":true}`)},
		{"a NaN token", set(entries(2), `{"left":0,"right":1,"selected":"left","handle":NaN,"open":true}`)},
		{"trailing data", set(entries(1), closedLeft) + `{}`},
		{"an unsanitized title", set(`{"alias":"a","pinnedTitle":"fix\u0007 build"}`, closedLeft)},
		{"an oversize resume token", set(`{"alias":"a","resume":"`+strings.Repeat("r", MaxResumeTokenBytes+1)+`"}`, closedLeft)},
		{"invalid UTF-8 in a resume token", set("{\"alias\":\"a\",\"resume\":\"r\xffr\"}", closedLeft)},
		{"selected on an empty pane", set(entries(2), `{"left":null,"right":1,"selected":"left","handle":0.5,"open":true}`)},
		{"an invalid selected", set(entries(1), `{"left":0,"selected":"middle","handle":0.5,"open":false}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseSessionSet([]byte(tt.data)); !errors.Is(err, ErrInvalidSessionSet) {
				t.Errorf("ParseSessionSet = %v, want an ErrInvalidSessionSet refusal", err)
			}
		})
	}
}

// restoreAsync runs RestoreSessionSet on its own goroutine.
func restoreAsync(ctx context.Context, m *SessionManager, set SessionSet, plan RestorePlan) <-chan restoreOutcome {
	out := make(chan restoreOutcome, 1)
	go func() {
		res, err := m.RestoreSessionSet(ctx, &set, plan)
		out <- restoreOutcome{res: res, err: err}
	}()
	return out
}

type restoreOutcome struct {
	err error
	res RestoreResult
}

func awaitRestore(t *testing.T, out <-chan restoreOutcome) restoreOutcome {
	t.Helper()
	select {
	case o := <-out:
		return o
	case <-time.After(waitPatience):
		t.Fatal("RestoreSessionSet did not return")
		return restoreOutcome{}
	}
}

// specFor restores an entry under its stored alias and pin with the factory.
func specFor(e SessionSetEntry) CreateSpec {
	return CreateSpec{Alias: e.Alias, PinnedTitle: e.PinnedTitle}
}

func restoredIDs(res RestoreResult) map[int]SessionID {
	ids := make(map[int]SessionID, len(res.Restored))
	for _, r := range res.Restored {
		ids[r.Index] = r.Info.ID
	}
	return ids
}

// blockingPlan is specFor, except it parks at index `at` until release closes,
// signalling reached first.
func blockingPlan(at int, reached chan<- struct{}, release <-chan struct{}) RestorePlan {
	return func(_ context.Context, i int, e SessionSetEntry) (CreateSpec, bool) {
		if i == at {
			reached <- struct{}{}
			<-release
		}
		return specFor(e), true
	}
}

func fourEntrySet() SessionSet {
	return SessionSet{
		Version: SessionSetVersion,
		Sessions: []SessionSetEntry{
			{Alias: "e0", PinnedTitle: "zero"}, {Alias: "e1"}, {Alias: "e2"}, {Alias: "e3", PinnedTitle: "three"},
		},
		Layout: SessionSetLayout{Left: new(3), Right: new(0), Selected: PaneRight, Handle: 0.3, Open: true},
	}
}

func TestRestoreSessionSet(t *testing.T) {
	t.Run("restores in order, passes the index and honours a skip", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		var seen []int
		res, err := m.RestoreSessionSet(t.Context(), new(fourEntrySet()), func(_ context.Context, i int, e SessionSetEntry) (CreateSpec, bool) {
			seen = append(seen, i)
			return specFor(e), i != 2
		})
		if err != nil {
			t.Fatalf("RestoreSessionSet = %v, want nil", err)
		}
		if !slices.Equal(seen, []int{0, 1, 2, 3}) {
			t.Errorf("plan saw indices %v, want [0 1 2 3]", seen)
		}
		if res.Skipped != 1 || res.Failed != 0 || len(res.Restored) != 3 {
			t.Fatalf("result = %d restored, %d skipped, %d failed; want 3, 1, 0", len(res.Restored), res.Skipped, res.Failed)
		}
		ids := restoredIDs(res)
		if got, want := listIDs(m), []SessionID{ids[0], ids[1], ids[3]}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want %v", short(got), short(want))
		}
		for i, alias := range map[int]string{0: "e0", 1: "e1", 3: "e3"} {
			if got := listedInfo(t, m, ids[i]); got.Alias != alias {
				t.Errorf("entry %d alias = %q, want %q", i, got.Alias, alias)
			}
		}
		if got := listedInfo(t, m, ids[3]).PinnedTitle; got != "three" {
			t.Errorf("entry 3 pin = %q, want %q", got, "three")
		}
		want := flatLayout{left: string(ids[3]), right: string(ids[0]), selected: PaneRight, handle: 0.3, open: true}
		if got := flat(m.PaneLayout()); got != want {
			t.Errorf("layout = %+v, want %+v", got, want)
		}
	})

	t.Run("a pre-existing session goes after the restored ones", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		pre := createSessions(t, m, 1)[0]
		set := fourEntrySet()
		set.Sessions = set.Sessions[:2]
		set.Layout = SessionSetLayout{Left: new(1), Selected: PaneLeft, Handle: 0.5}
		res, err := m.RestoreSessionSet(t.Context(), &set, func(_ context.Context, _ int, e SessionSetEntry) (CreateSpec, bool) {
			return specFor(e), true
		})
		if err != nil {
			t.Fatalf("RestoreSessionSet = %v, want nil", err)
		}
		ids := restoredIDs(res)
		if got, want := listIDs(m), []SessionID{ids[0], ids[1], pre}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want %v", short(got), short(want))
		}
		if got, want := flat(m.PaneLayout()), (flatLayout{left: string(ids[1]), selected: PaneLeft, handle: 0.5}); got != want {
			t.Errorf("layout = %+v, want %+v", got, want)
		}
	})

	t.Run("a client layout written during the restore wins", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		pre := createSessions(t, m, 1)[0]
		reached, release := make(chan struct{}), make(chan struct{})
		out := restoreAsync(t.Context(), m, fourEntrySet(), blockingPlan(1, reached, release))
		<-reached
		client := PaneLayout{Left: new(pre), Selected: PaneLeft, Handle: 0.5}
		setLayout(t, m, client)
		close(release)
		o := awaitRestore(t, out)
		if o.err != nil {
			t.Fatalf("RestoreSessionSet = %v, want nil", o.err)
		}
		if got, want := flat(m.PaneLayout()), flat(client); got != want {
			t.Errorf("layout = %+v, want the client's %+v", got, want)
		}
		ids := restoredIDs(o.res)
		if got, want := listIDs(m), []SessionID{ids[0], ids[1], ids[2], ids[3], pre}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want the stored %v (no client order write)", short(got), short(want))
		}
	})

	t.Run("a client order written during the restore wins", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		pre := createSessions(t, m, 1)[0]
		reached, release := make(chan struct{}), make(chan struct{})
		out := restoreAsync(t.Context(), m, fourEntrySet(), blockingPlan(1, reached, release))
		<-reached
		live := listIDs(m) // [pre, entry 0]: the client keeps the pre-existing tab first
		if !m.SetSessionOrder(live) {
			t.Fatal("SetSessionOrder = false, want true")
		}
		close(release)
		o := awaitRestore(t, out)
		if o.err != nil {
			t.Fatalf("RestoreSessionSet = %v, want nil", o.err)
		}
		ids := restoredIDs(o.res)
		if got, want := listIDs(m), []SessionID{pre, ids[0], ids[1], ids[2], ids[3]}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want the client's arrangement %v", short(got), short(want))
		}
		want := flatLayout{left: string(ids[3]), right: string(ids[0]), selected: PaneRight, handle: 0.3, open: true}
		if got := flat(m.PaneLayout()); got != want {
			t.Errorf("layout = %+v, want the stored %+v (no client layout write)", got, want)
		}
	})

	t.Run("a failed create is counted and the rest continue", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		set := fourEntrySet()
		set.Sessions = set.Sessions[:3]
		set.Layout = SessionSetLayout{Left: new(2), Selected: PaneLeft, Handle: 0.5}
		res, err := m.RestoreSessionSet(t.Context(), &set, func(_ context.Context, i int, e SessionSetEntry) (CreateSpec, bool) {
			spec := specFor(e)
			if i == 1 {
				spec.Build = handlerRunning(t, "/nonexistent/web-terminal-engine-test")
			}
			return spec, true
		})
		if err != nil {
			t.Fatalf("RestoreSessionSet = %v, want nil", err)
		}
		if res.Failed != 1 || res.Skipped != 0 || len(res.Restored) != 2 {
			t.Fatalf("result = %d restored, %d skipped, %d failed; want 2, 0, 1", len(res.Restored), res.Skipped, res.Failed)
		}
		if res.Restored[0].Index != 0 || res.Restored[1].Index != 2 {
			t.Errorf("restored indices = %d, %d; want 0, 2", res.Restored[0].Index, res.Restored[1].Index)
		}
		ids := restoredIDs(res)
		if got, want := listIDs(m), []SessionID{ids[0], ids[2]}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want %v", short(got), short(want))
		}
		if got, want := flat(m.PaneLayout()), (flatLayout{left: string(ids[2]), selected: PaneLeft, handle: 0.5}); got != want {
			t.Errorf("layout = %+v, want %+v", got, want)
		}
	})

	t.Run("a restore that brings nothing back keeps the live arrangement", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		pre := createSessions(t, m, 2)
		live := PaneLayout{Left: new(pre[1]), Right: new(pre[0]), Selected: PaneRight, Handle: 0.3, Open: true}
		setLayout(t, m, live)
		res, err := m.RestoreSessionSet(t.Context(), new(fourEntrySet()), func(context.Context, int, SessionSetEntry) (CreateSpec, bool) {
			return CreateSpec{}, false
		})
		if err != nil || len(res.Restored) != 0 || res.Skipped != 4 {
			t.Fatalf("RestoreSessionSet = %d restored, %d skipped, %v; want 0, 4, nil", len(res.Restored), res.Skipped, err)
		}
		if got, want := flat(m.PaneLayout()), flat(live); got != want {
			t.Errorf("layout = %+v, want the live %+v", got, want)
		}
	})

	t.Run("a cancelled context stops early and still arranges", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		set := fourEntrySet()
		set.Layout = SessionSetLayout{Left: new(2), Right: new(0), Selected: PaneLeft, Handle: 0.3, Open: true}
		res, err := m.RestoreSessionSet(ctx, &set, func(_ context.Context, i int, e SessionSetEntry) (CreateSpec, bool) {
			if i == 1 {
				cancel()
			}
			return specFor(e), true
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RestoreSessionSet = %v, want context.Canceled", err)
		}
		if len(res.Restored) != 2 || res.Skipped != 2 {
			t.Fatalf("result = %d restored, %d skipped; want 2, 2", len(res.Restored), res.Skipped)
		}
		ids := restoredIDs(res)
		if got, want := listIDs(m), []SessionID{ids[0], ids[1]}; !slices.Equal(got, want) {
			t.Errorf("order = %v, want %v", short(got), short(want))
		}
		want := flatLayout{right: string(ids[0]), selected: PaneRight, handle: 0.3, open: true}
		if got := flat(m.PaneLayout()); got != want {
			t.Errorf("layout = %+v, want %+v (the unrestored left emptied, selection moved)", got, want)
		}
	})

	t.Run("a Shutdown during the restore stops it with ErrManagerClosed", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		reached, release := make(chan struct{}), make(chan struct{})
		set := fourEntrySet()
		set.Sessions = set.Sessions[:3]
		set.Layout = SessionSetLayout{Left: new(0), Selected: PaneLeft, Handle: 0.5}
		out := restoreAsync(t.Context(), m, set, blockingPlan(1, reached, release))
		<-reached
		shutdownManager(t, m)
		close(release)
		o := awaitRestore(t, out)
		if !errors.Is(o.err, ErrManagerClosed) {
			t.Fatalf("RestoreSessionSet = %v, want ErrManagerClosed", o.err)
		}
		if len(o.res.Restored) != 1 || o.res.Skipped != 2 {
			t.Errorf("result = %d restored, %d skipped; want 1, 2", len(o.res.Restored), o.res.Skipped)
		}
		if n := len(m.List()); n != 0 {
			t.Errorf("List() after Shutdown has %d sessions, want 0", n)
		}
	})
}

func doRequest(t *testing.T, method, url string) (status int, header http.Header, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, string(raw)
}

func TestRestoreSessionSet_gatesCreateAndLayoutRead(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	reached, release := make(chan struct{}), make(chan struct{})
	set := fourEntrySet()
	set.Sessions = set.Sessions[:1]
	set.Layout = SessionSetLayout{Left: new(0), Selected: PaneLeft, Handle: 0.5}
	out := restoreAsync(t.Context(), m, set, blockingPlan(0, reached, release))
	<-reached

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, SessionsPath},
		{http.MethodGet, SessionsPath + "/layout"},
	} {
		status, header, body := doRequest(t, route.method, srv.URL+route.path)
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s %s while restoring = %d, want 503", route.method, route.path, status)
		}
		if got := header.Get("Retry-After"); got != "1" {
			t.Errorf("%s %s Retry-After = %q, want 1", route.method, route.path, got)
		}
		var msg map[string]string
		if err := json.Unmarshal([]byte(body), &msg); err != nil || msg["error"] != RestoringMessage {
			t.Errorf("%s %s body = %q, want {\"error\":%q}", route.method, route.path, body, RestoringMessage)
		}
	}
	if status, _, _ := doRequest(t, http.MethodGet, srv.URL+SessionsPath); status != http.StatusOK {
		t.Errorf("GET %s while restoring = %d, want 200", SessionsPath, status)
	}
	if _, err := m.RestoreSessionSet(t.Context(), &set, blockingPlan(-1, nil, nil)); !errors.Is(err, ErrRestoreRunning) {
		t.Errorf("a second RestoreSessionSet = %v, want ErrRestoreRunning", err)
	}

	close(release)
	if o := awaitRestore(t, out); o.err != nil {
		t.Fatalf("RestoreSessionSet = %v, want nil", o.err)
	}
	if status, _, _ := doRequest(t, http.MethodGet, srv.URL+SessionsPath+"/layout"); status != http.StatusOK {
		t.Errorf("GET layout after the restore = %d, want 200", status)
	}
	if status, _, _ := doRequest(t, http.MethodPost, srv.URL+SessionsPath); status != http.StatusCreated {
		t.Errorf("POST after the restore = %d, want 201", status)
	}
}

func TestCreateRoute_afterShutdownAnswers503(t *testing.T) {
	m := NewSessionManager(catFactory)
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)
	shutdownManager(t, m)
	status, _, body := doRequest(t, http.MethodPost, srv.URL+SessionsPath)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "server shutting down") {
		t.Errorf("POST after Shutdown = %d %q, want 503 server shutting down", status, body)
	}
}
