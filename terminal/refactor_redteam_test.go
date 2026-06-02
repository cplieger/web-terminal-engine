package terminal

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// Refactor Red-Team: verify functional-options API applies defaults correctly,
// each WithX threads independently, option order doesn't matter, nil/zero cases
// are safe, and no behavior drifted from the pre-refactor config.

// TestNewHandler_DefaultsMatchPreRefactor verifies that calling NewHandler
// with only a command produces the exact same defaults as the pre-refactor
// hardcoded config: scrollbackCapacity=1000, logger=slog.Default(), env adds
// TERM/COLORTERM, no AcceptOptions, no onProcessExit, no workDir.
func TestNewHandler_DefaultsMatchPreRefactor(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"})

	// scrollbackCapacity should be 1000 (the const scrollbackCapacity)
	if h.cfg.scrollbackCapacity != 1000 {
		t.Fatalf("default scrollbackCapacity = %d, want 1000", h.cfg.scrollbackCapacity)
	}

	// logger should be slog.Default()
	if h.cfg.logger != slog.Default() {
		t.Fatal("default logger is not slog.Default()")
	}

	// acceptOptions should be nil (no origin restrictions by default)
	if h.cfg.acceptOptions != nil {
		t.Fatal("default acceptOptions should be nil")
	}

	// onProcessExit should be nil
	if h.cfg.onProcessExit != nil {
		t.Fatal("default onProcessExit should be nil")
	}

	// workDir should be empty
	if h.cfg.workDir != "" {
		t.Fatalf("default workDir = %q, want empty", h.cfg.workDir)
	}

	// env should be nil (TERM/COLORTERM injected at spawn time, not in cfg)
	if h.cfg.env != nil {
		t.Fatalf("default env = %v, want nil", h.cfg.env)
	}

	// screen should be initialized at defaultRows x defaultCols
	if h.screen.Height != defaultRows || h.screen.Width != defaultCols {
		t.Fatalf("screen %dx%d, want %dx%d", h.screen.Height, h.screen.Width, defaultRows, defaultCols)
	}

	// scrollback ring capacity
	if cap(h.scrollback.buf) != 1000 {
		t.Fatalf("scrollback ring cap = %d, want 1000", cap(h.scrollback.buf))
	}

	// registry should be non-nil
	if h.registry == nil {
		t.Fatal("registry is nil")
	}

	// builder should be non-nil
	if h.builder == nil {
		t.Fatal("builder is nil")
	}

	// command stored correctly
	if len(h.command) != 1 || h.command[0] != "/bin/sh" {
		t.Fatalf("command = %v, want [/bin/sh]", h.command)
	}

	// started should be false before any WS connect
	if h.started.Load() {
		t.Fatal("started should be false before any connection")
	}
}

// TestNewHandler_WithScrollbackCapacity verifies custom scrollback.
func TestNewHandler_WithScrollbackCapacity(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithScrollbackCapacity(500))
	if h.cfg.scrollbackCapacity != 500 {
		t.Fatalf("scrollbackCapacity = %d, want 500", h.cfg.scrollbackCapacity)
	}
	if cap(h.scrollback.buf) != 500 {
		t.Fatalf("scrollback ring cap = %d, want 500", cap(h.scrollback.buf))
	}
}

// TestNewHandler_WithLogger verifies custom logger injection.
func TestNewHandler_WithLogger(t *testing.T) {
	custom := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := NewHandler([]string{"/bin/sh"}, WithLogger(custom))
	if h.cfg.logger != custom {
		t.Fatal("WithLogger did not inject custom logger")
	}
}

// TestNewHandler_WithLogger_Nil verifies nil logger stores a discard logger (not nil).
func TestNewHandler_WithLogger_Nil(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithLogger(nil))
	if h.cfg.logger == nil {
		t.Fatal("WithLogger(nil) should store a discard logger, not nil")
	}
}

// TestNewHandler_WithWorkDir verifies working directory is threaded.
func TestNewHandler_WithWorkDir(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithWorkDir("/tmp"))
	if h.cfg.workDir != "/tmp" {
		t.Fatalf("workDir = %q, want /tmp", h.cfg.workDir)
	}
}

// TestNewHandler_WithEnv verifies additional env vars are stored.
func TestNewHandler_WithEnv(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux"}
	h := NewHandler([]string{"/bin/sh"}, WithEnv(env))
	if len(h.cfg.env) != 2 || h.cfg.env[0] != "FOO=bar" || h.cfg.env[1] != "BAZ=qux" {
		t.Fatalf("env = %v, want [FOO=bar BAZ=qux]", h.cfg.env)
	}
}

// TestNewHandler_WithAcceptOptions verifies websocket accept options.
func TestNewHandler_WithAcceptOptions(t *testing.T) {
	opts := &websocket.AcceptOptions{InsecureSkipVerify: true}
	h := NewHandler([]string{"/bin/sh"}, WithAcceptOptions(opts))
	if h.cfg.acceptOptions != opts {
		t.Fatal("WithAcceptOptions did not thread options")
	}
}

// TestNewHandler_WithOnProcessExit verifies callback is stored.
func TestNewHandler_WithOnProcessExit(t *testing.T) {
	called := false
	fn := func(err error) { called = true }
	h := NewHandler([]string{"/bin/sh"}, WithOnProcessExit(fn))
	if h.cfg.onProcessExit == nil {
		t.Fatal("WithOnProcessExit did not store callback")
	}
	h.cfg.onProcessExit(nil)
	if !called {
		t.Fatal("onProcessExit callback not invoked")
	}
	_ = called
}

// TestNewHandler_OptionOrderIndependent verifies that applying options in
// different orders produces the same result.
func TestNewHandler_OptionOrderIndependent(t *testing.T) {
	env := []string{"X=1"}
	custom := slog.New(slog.NewTextHandler(os.Stderr, nil))

	h1 := NewHandler([]string{"/bin/sh"},
		WithScrollbackCapacity(200),
		WithLogger(custom),
		WithWorkDir("/home"),
		WithEnv(env),
	)
	h2 := NewHandler([]string{"/bin/sh"},
		WithEnv(env),
		WithWorkDir("/home"),
		WithLogger(custom),
		WithScrollbackCapacity(200),
	)

	if h1.cfg.scrollbackCapacity != h2.cfg.scrollbackCapacity {
		t.Fatal("scrollbackCapacity differs with option order")
	}
	if h1.cfg.logger != h2.cfg.logger {
		t.Fatal("logger differs with option order")
	}
	if h1.cfg.workDir != h2.cfg.workDir {
		t.Fatal("workDir differs with option order")
	}
	if len(h1.cfg.env) != len(h2.cfg.env) || h1.cfg.env[0] != h2.cfg.env[0] {
		t.Fatal("env differs with option order")
	}
}

// TestNewHandler_NoOptions_NilSlice verifies passing nil options is safe.
func TestNewHandler_NoOptions_NilSlice(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, nil)
	// nil Option is a nil function pointer — calling it would panic.
	// Verify the constructor handles it gracefully.
	if h.cfg.scrollbackCapacity != 1000 {
		t.Fatalf("scrollbackCapacity = %d after nil option", h.cfg.scrollbackCapacity)
	}
}

// TestNewHandler_EmptyCommand verifies empty command produces a handler
// that fails gracefully on ensureStarted (existing TestEmptyCommandFails
// covers the WS path; this tests the direct internal call).
func TestNewHandler_EmptyCommand(t *testing.T) {
	h := NewHandler(nil)
	err := h.ensureStarted(80, 24)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNewHandler_EmptyStringCommand verifies []string{} is empty.
func TestNewHandler_EmptyStringCommand(t *testing.T) {
	h := NewHandler([]string{})
	err := h.ensureStarted(80, 24)
	if err == nil {
		t.Fatal("expected error for empty string slice command")
	}
}

// TestNewHandler_ENVInjection verifies TERM and COLORTERM are injected
// into the spawned process environment (not in cfg.env, but at spawn time).
func TestNewHandler_ENVInjection(t *testing.T) {
	// The TERM/COLORTERM injection happens in ensureStarted, not in config.
	// We verify by checking the command's env after start.
	h := NewHandler([]string{"/bin/sh", "-c", "true"}, WithWorkDir("/"))
	err := h.ensureStarted(80, 24)
	if err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	defer h.Shutdown()

	// Check cmd.Env contains TERM and COLORTERM
	env := h.cmd.Env
	var hasTerm, hasColorterm bool
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=xterm-256color") {
			hasTerm = true
		}
		if strings.HasPrefix(e, "COLORTERM=truecolor") {
			hasColorterm = true
		}
	}
	if !hasTerm {
		t.Fatal("TERM=xterm-256color not found in cmd.Env")
	}
	if !hasColorterm {
		t.Fatal("COLORTERM=truecolor not found in cmd.Env")
	}
}

// TestNewHandler_ENVCustomOverride verifies custom env is appended AFTER
// the standard env, so user-provided TERM can override the default.
func TestNewHandler_ENVCustomOverride(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"},
		WithWorkDir("/"),
		WithEnv([]string{"TERM=dumb"}),
	)
	err := h.ensureStarted(80, 24)
	if err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	defer h.Shutdown()

	// Custom TERM should appear AFTER the default one (last wins in execve)
	env := h.cmd.Env
	lastTerm := ""
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			lastTerm = e
		}
	}
	if lastTerm != "TERM=dumb" {
		t.Fatalf("last TERM = %q, want TERM=dumb (custom should override)", lastTerm)
	}
}

// TestNewHandler_WithScrollbackCapacity_Zero verifies zero capacity doesn't panic.
func TestNewHandler_WithScrollbackCapacity_Zero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with zero scrollback capacity: %v", r)
		}
	}()
	h := NewHandler([]string{"/bin/sh"}, WithScrollbackCapacity(0))
	if h.cfg.scrollbackCapacity != 0 {
		t.Fatalf("scrollbackCapacity = %d, want 0", h.cfg.scrollbackCapacity)
	}
	// Simulate what happens when screen scrolls — Append must not panic with real data.
	h.scrollback.Append([][]vt.WireRun{{{T: "hello"}}})
}

// TestNewHandler_WithScrollbackCapacity_Negative verifies negative capacity is clamped to 0.
func TestNewHandler_WithScrollbackCapacity_Negative(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with negative scrollback capacity: %v", r)
		}
	}()
	h := NewHandler([]string{"/bin/sh"}, WithScrollbackCapacity(-1))
	if h.cfg.scrollbackCapacity != 0 {
		t.Fatalf("scrollbackCapacity = %d, want 0 (clamped from -1)", h.cfg.scrollbackCapacity)
	}
	// Append must not panic on zero-cap ring.
	h.scrollback.Append([][]vt.WireRun{{{T: "hello"}}})
}

// TestNewHandler_WithScrollbackCapacity_LargeNegative verifies large negative is clamped.
func TestNewHandler_WithScrollbackCapacity_LargeNegative(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with large negative scrollback capacity: %v", r)
		}
	}()
	h := NewHandler([]string{"/bin/sh"}, WithScrollbackCapacity(-999999))
	if h.cfg.scrollbackCapacity != 0 {
		t.Fatalf("scrollbackCapacity = %d, want 0", h.cfg.scrollbackCapacity)
	}
}

// TestNewHandler_LastOptionWins verifies that duplicate options use last-wins semantics.
func TestNewHandler_LastOptionWins(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"},
		WithScrollbackCapacity(100),
		WithScrollbackCapacity(200),
		WithScrollbackCapacity(300),
	)
	if h.cfg.scrollbackCapacity != 300 {
		t.Fatalf("scrollbackCapacity = %d, want 300 (last option)", h.cfg.scrollbackCapacity)
	}
}

// TestNewHandler_NilLoggerDoesNotPanic verifies that WithLogger(nil) (documented
// as "disables logging") does not cause a nil-pointer panic when the handler
// logs internally.
func TestNewHandler_NilLoggerDoesNotPanic(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"},
		WithWorkDir("/"),
		WithLogger(nil),
	)
	err := h.ensureStarted(80, 24)
	if err != nil {
		t.Fatalf("ensureStarted: %v", err)
	}
	defer h.Shutdown()
}

// TestNewHandler_WithEnv_Nil verifies WithEnv(nil) is safe and doesn't corrupt defaults.
func TestNewHandler_WithEnv_Nil(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithEnv(nil))
	if h.cfg.env != nil {
		t.Fatalf("env = %v, want nil", h.cfg.env)
	}
}

// TestNewHandler_WithOnProcessExit_Nil verifies nil callback is safe.
func TestNewHandler_WithOnProcessExit_Nil(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithOnProcessExit(nil))
	if h.cfg.onProcessExit != nil {
		t.Fatal("onProcessExit should be nil")
	}
}

// TestNewHandler_WithAcceptOptions_Nil verifies nil accept options is safe.
func TestNewHandler_WithAcceptOptions_Nil(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithAcceptOptions(nil))
	if h.cfg.acceptOptions != nil {
		t.Fatal("acceptOptions should be nil")
	}
}
