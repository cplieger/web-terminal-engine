package terminal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// listedInfo returns the List entry for id, failing t when it is absent.
func listedInfo(t *testing.T, m *SessionManager, id SessionID) SessionInfo {
	t.Helper()
	for _, info := range m.List() {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("List() has no session %s", LogID(id))
	return SessionInfo{}
}

func isMintedAlias(a string) bool {
	return len(a) == 8 && strings.Trim(a, "abcdefghijklmnopqrstuvwxyz234567") == ""
}

func TestCreateWith_zeroSpecMatchesCreate(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	info, err := m.CreateWith(CreateSpec{})
	if err != nil {
		t.Fatalf("CreateWith(zero) = %v, want nil", err)
	}
	got := listedInfo(t, m, info.ID)
	if got.Title != "cat" {
		t.Errorf("List() Title = %q, want %q (the factory's command)", got.Title, "cat")
	}
	if !isMintedAlias(info.Alias) || got.Alias != info.Alias {
		t.Errorf("alias = %q (listed %q), want one minted 8-char alias", info.Alias, got.Alias)
	}
	if info.PinnedTitle != "" || got.PinnedTitle != "" {
		t.Errorf("PinnedTitle = %q (listed %q), want none", info.PinnedTitle, got.PinnedTitle)
	}
}

func TestCreateWith_buildReplacesTheFactory(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	info, err := m.CreateWith(CreateSpec{Build: func(SessionID) *Handler {
		h := NewHandler([]string{"/bin/echo", "hi"}, WithLogger(nil))
		shutdownOnCleanup(t, h)
		return h
	}})
	if err != nil {
		t.Fatalf("CreateWith(Build) = %v, want nil", err)
	}
	if got := listedInfo(t, m, info.ID).Title; got != "echo" {
		t.Errorf("List() Title = %q, want %q (Build's command, not the factory's)", got, "echo")
	}
}

func TestCreateWith_appliesAliasAndPinnedTitle(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	held, err := m.CreateWith(CreateSpec{Alias: "held-alias"})
	if err != nil {
		t.Fatalf("CreateWith(held-alias) = %v", err)
	}
	if held.Alias != "held-alias" {
		t.Fatalf("seed alias = %q, want held-alias", held.Alias)
	}
	tests := []struct {
		name      string
		spec      CreateSpec
		wantAlias string // "" means a minted alias
		wantPin   string
	}{
		{"a free alias and a title", CreateSpec{Alias: "tab_free", PinnedTitle: "fix build"}, "tab_free", "fix build"},
		{"an alias a live session holds", CreateSpec{Alias: "held-alias"}, "", ""},
		{"an alias with a slash", CreateSpec{Alias: "a/b"}, "", ""},
		{"a 65-byte alias", CreateSpec{Alias: strings.Repeat("a", 65)}, "", ""},
		{"a title with a C0 control", CreateSpec{PinnedTitle: "fix\x07 build"}, "", "fix build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := m.CreateWith(tt.spec)
			if err != nil {
				t.Fatalf("CreateWith = %v, want nil", err)
			}
			listed := listedInfo(t, m, info.ID)
			for _, got := range []SessionInfo{info, listed} {
				if tt.wantAlias == "" && !isMintedAlias(got.Alias) {
					t.Errorf("Alias = %q, want a minted 8-char alias", got.Alias)
				}
				if tt.wantAlias != "" && got.Alias != tt.wantAlias {
					t.Errorf("Alias = %q, want %q", got.Alias, tt.wantAlias)
				}
				if got.PinnedTitle != tt.wantPin {
					t.Errorf("PinnedTitle = %q, want %q", got.PinnedTitle, tt.wantPin)
				}
			}
			if tt.wantPin != "" && listed.Title != tt.wantPin {
				t.Errorf("List() Title = %q, want the pin %q", listed.Title, tt.wantPin)
			}
		})
	}
}

func TestCreateWith_afterShutdown(t *testing.T) {
	t.Run("before the factory", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		shutdownManager(t, m)
		built := false
		_, err := m.CreateWith(CreateSpec{Build: func(id SessionID) *Handler {
			built = true
			return catFactory(id)
		}})
		if !errors.Is(err, ErrManagerClosed) {
			t.Errorf("CreateWith after Shutdown = %v, want ErrManagerClosed", err)
		}
		if built {
			t.Error("Build ran after Shutdown, want it never called")
		}
		if _, err := m.Create(); !errors.Is(err, ErrManagerClosed) {
			t.Errorf("Create after Shutdown = %v, want ErrManagerClosed", err)
		}
	})
	t.Run("while forking", func(t *testing.T) {
		m := NewSessionManager(catFactory)
		t.Cleanup(func() { shutdownManager(t, m) })
		prev := createStartedHook
		t.Cleanup(func() { createStartedHook = prev })
		createStartedHook = func() { shutdownManager(t, m) }
		var h *Handler
		_, err := m.CreateWith(CreateSpec{Build: func(SessionID) *Handler {
			h = NewHandler([]string{"/bin/cat"}, WithLogger(nil))
			shutdownOnCleanup(t, h)
			return h
		}})
		createStartedHook = prev
		if !errors.Is(err, ErrManagerClosed) {
			t.Errorf("CreateWith across Shutdown = %v, want ErrManagerClosed", err)
		}
		if n := len(m.List()); n != 0 {
			t.Errorf("List() after a create across Shutdown has %d sessions, want 0", n)
		}
		ctx, cancel := context.WithTimeout(context.Background(), waitPatience)
		defer cancel()
		if err := h.wait(ctx); err != nil {
			t.Errorf("the forked handler's teardown = %v, want done within %v", err, waitPatience)
		}
		if exited, _ := h.exitOutcome(); !exited {
			t.Error("the forked handler's process is still running, want it ended")
		}
	})
}
