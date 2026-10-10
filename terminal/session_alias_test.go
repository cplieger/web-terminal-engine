package terminal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// aliasGrammar is the reference grammar SessionInfo.Alias documents; the UI's
// route codec pins the same table (web-terminal-ui src/features/tabs/route.ts).
var aliasGrammar = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var mintedAlias = regexp.MustCompile(`^[a-z2-7]{8}$`)

func TestCreate_mintsAUniqueEightCharacterAlias(t *testing.T) {
	// Only the session map: mintAliasLocked reads nothing else.
	m := &SessionManager{sessions: make(map[SessionID]*session)}
	for range 1000 {
		alias := m.mintAliasLocked()
		if !mintedAlias.MatchString(alias) {
			t.Fatalf("mintAliasLocked() = %q, want 8 characters of [a-z2-7]", alias)
		}
		if m.aliasHeldLocked(alias) {
			t.Fatalf("mintAliasLocked() = %q, already held by a live session", alias)
		}
		id := SessionID("fake-" + alias)
		m.sessions[id] = &session{id: id, alias: alias}
	}
}

func TestCreate_aliasIsCarriedByTheCreateBodyListAndStream(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	srv := httptest.NewServer(m.RESTHandler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+SessionsPath, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	var created SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode 201 body: %v", err)
	}
	if !mintedAlias.MatchString(created.Alias) {
		t.Fatalf("201 alias = %q, want a minted alias", created.Alias)
	}
	if list := m.List(); len(list) != 1 || list[0].Alias != created.Alias {
		t.Errorf("List() = %+v, want one session with alias %q", list, created.Alias)
	}
	if snap := m.snapshot(); len(snap) != 1 || snap[0].Alias != created.Alias {
		t.Errorf("snapshot() = %+v, want one event with alias %q", snap, created.Alias)
	}
	evs := m.diffStatuses()
	if len(evs) != 1 || evs[0].Alias != created.Alias {
		t.Errorf("diffStatuses() = %+v, want one event with alias %q", evs, created.Alias)
	}
}

func TestSetSessionAlias(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	a, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !m.SetSessionAlias(b, "held_by_b") {
		t.Fatal("SetSessionAlias(b, held_by_b) = false, want true")
	}

	cases := []struct {
		name  string
		id    SessionID
		alias string
		want  bool
	}{
		{name: "kiro_thread_id", id: a, alias: "sess_6f1c2b9e-0d4a-4e7b-9a51-3c2d1e0f9a8b", want: true},
		{name: "max_length", id: a, alias: strings.Repeat("x", 64), want: true},
		{name: "over_max_length", id: a, alias: strings.Repeat("x", 65)},
		{name: "empty", id: a, alias: ""},
		{name: "percent", id: a, alias: "a%41"},
		{name: "space", id: a, alias: "a b"},
		{name: "comma", id: a, alias: "a,b"},
		{name: "slash", id: a, alias: "a/b"},
		{name: "non_ascii", id: a, alias: "caf\u00e9"},
		{name: "unknown_id", id: "nope", alias: "fine"},
		{name: "held_by_another_session", id: a, alias: "held_by_b"},
		{name: "own_current_value", id: b, alias: "held_by_b", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := aliasOf(m, a)
			if got := m.SetSessionAlias(tc.id, tc.alias); got != tc.want {
				t.Fatalf("SetSessionAlias(%q) = %v, want %v", tc.alias, got, tc.want)
			}
			if !tc.want && tc.id == a && aliasOf(m, a) != before {
				t.Errorf("refused SetSessionAlias(%q) changed the alias to %q", tc.alias, aliasOf(m, a))
			}
		})
	}

	if !m.Close(b) {
		t.Fatal("Close(b) = false")
	}
	if !m.SetSessionAlias(a, "held_by_b") {
		t.Error("SetSessionAlias(a, held_by_b) after the holder closed = false, want true")
	}
}

func aliasOf(m *SessionManager, id SessionID) string {
	for _, s := range m.List() {
		if s.ID == id {
			return s.Alias
		}
	}
	return ""
}

func TestDiffStatuses_emitsAnAliasChangeAndNothingWhenUnchanged(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	m.diffStatuses()
	if evs := m.diffStatuses(); len(evs) != 0 {
		t.Fatalf("quiescent sweep emitted %+v", evs)
	}

	if !m.SetSessionAlias(id, "sess_thread") {
		t.Fatal("SetSessionAlias = false")
	}
	evs := m.diffStatuses()
	if len(evs) != 1 || evs[0].Alias != "sess_thread" {
		t.Fatalf("sweep after SetSessionAlias = %+v, want one event with alias sess_thread", evs)
	}

	if !m.SetSessionAlias(id, "sess_thread") {
		t.Fatal("SetSessionAlias(same value) = false")
	}
	if evs := m.diffStatuses(); len(evs) != 0 {
		t.Errorf("sweep after re-setting the same alias emitted %+v, want none", evs)
	}
}

func FuzzValidSessionAlias_matchesTheGrammar(f *testing.F) {
	for _, s := range []string{"", "a", "sess_0-9", "a,b", "a%41", "caf\u00e9", strings.Repeat("z", 64), strings.Repeat("z", 65), "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := validSessionAlias(s), aliasGrammar.MatchString(s); got != want {
			t.Fatalf("validSessionAlias(%q) = %v, want %v", s, got, want)
		}
	})
}
