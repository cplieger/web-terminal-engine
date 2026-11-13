package terminal

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// heldExitHandler returns a handler whose onProcessExit callback parks until the
// returned release channel is closed, plus a channel that closes when the
// callback is entered.
//
// The callback is the monitor's last statement, so parking there holds a
// finished teardown open for as long as the test wants.
func heldExitHandler(t *testing.T) (h *Handler, entered <-chan struct{}, release chan struct{}) {
	t.Helper()
	enteredCh := make(chan struct{})
	release = make(chan struct{})
	h = NewHandler([]string{"/bin/sh", "-c", "exit 0"},
		WithWorkDir("/"), WithLogger(nil),
		WithOnProcessExit(func(error) {
			close(enteredCh)
			<-release
		}),
	)
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	return h, enteredCh, release
}

// TestShutdownWaitsForTeardown is the contract this whole pair exists for: when
// Shutdown returns nil, the handler's teardown is over. Close is the opposite
// promise and is asserted in the same test, because the two are only meaningful
// against each other.
func TestShutdownWaitsForTeardown(t *testing.T) {
	h, entered, release := heldExitHandler(t)

	// Close must NOT wait: it is what an HTTP DELETE handler and the idle reaper
	// call, and neither can block on a teardown of unbounded duration.
	closeReturned := make(chan struct{})
	go func() {
		h.Close()
		close(closeReturned)
	}()
	select {
	case <-closeReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s; it must never wait for teardown")
	}

	<-entered // the monitor is now parked inside teardown

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownReturned := make(chan error, 1)
	go func() { shutdownReturned <- h.Shutdown(ctx) }()

	// Negative half: while teardown is parked, Shutdown must still be blocked.
	select {
	case err := <-shutdownReturned:
		t.Fatalf("Shutdown(ctx) = %v while teardown was still running; want it to block", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	if err := <-shutdownReturned; err != nil {
		t.Errorf("Shutdown(ctx) = %v, want nil once teardown finished", err)
	}
}

// TestShutdownReportsAnExpiredBudget pins the diagnostic half. A teardown that
// outruns its grace is the one thing the old fire-and-forget shape could never
// report, and reporting it is most of the value: the caller is stopping either
// way, so the return exists to be logged rather than branched on.
func TestShutdownReportsAnExpiredBudget(t *testing.T) {
	h, entered, release := heldExitHandler(t)
	h.Close()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := h.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown(expired ctx) = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	close(release)
}

// TestShutdownOnAnUnstartedHandlerReturnsNil covers the documented "safe even if
// the process was never started" case. There is no goroutine to wait for, so the
// wait must be a no-op rather than a block on a nil channel, which would hang
// forever.
func TestShutdownOnAnUnstartedHandlerReturnsNil(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"}, WithLogger(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Shutdown(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Shutdown(ctx) on an unstarted handler = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown(ctx) on an unstarted handler blocked; want an immediate nil")
	}
}

// TestManagerShutdownWaitsForEverySession pins the fan-in. One slow session must
// hold the manager's Shutdown, or a caller that waits learns nothing about the
// sessions it was waiting for.
func TestManagerShutdownWaitsForEverySession(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var opened bool
	m := NewSessionManager(func(SessionID) *Handler {
		// Only the first session parks; the rest tear down normally, so the test
		// also covers Shutdown waiting on a MIX rather than on one uniform case.
		if opened {
			return NewHandler([]string{"/bin/sh", "-c", "exit 0"}, WithWorkDir("/"), WithLogger(nil))
		}
		opened = true
		return NewHandler([]string{"/bin/sh", "-c", "exit 0"}, WithWorkDir("/"), WithLogger(nil),
			WithOnProcessExit(func(error) {
				close(entered)
				<-release
			}))
	})
	for range 3 {
		if _, err := m.Create(); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- m.Shutdown(ctx) }()
	select {
	case err := <-returned:
		t.Fatalf("Shutdown(ctx) = %v with a session still tearing down; want it to block", err)
	case <-time.After(250 * time.Millisecond):
	}

	close(release)
	if err := <-returned; err != nil {
		t.Errorf("Shutdown(ctx) = %v, want nil once every session finished", err)
	}
}

// TestManagerShutdownNamesTheOutstandingCount checks the error carries the count,
// not just the cause. "2 of 3 unfinished" tells an operator whether one session
// wedged or the whole teardown never ran, and those have different answers.
func TestManagerShutdownNamesTheOutstandingCount(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{}, 2)
	m := NewSessionManager(func(SessionID) *Handler {
		return NewHandler([]string{"/bin/sh", "-c", "exit 0"}, WithWorkDir("/"), WithLogger(nil),
			WithOnProcessExit(func(error) {
				entered <- struct{}{}
				<-release
			}))
	})
	for range 2 {
		if _, err := m.Create(); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	<-entered
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := m.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown(expired ctx) = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if want := "2 of 2 session teardowns unfinished"; !strings.Contains(err.Error(), want) {
		t.Errorf("Shutdown(expired ctx) error = %q, want it to contain %q", err.Error(), want)
	}
}

// TestShutdownReportsSuccessAfterCompletion pins the answer a finished teardown
// must give when the caller's budget has ALSO expired: success, every time.
//
// This is the fatal-path shape (httpserver's teardown hook runs mgr.Shutdown, then
// the error branch above it runs it again, often with the same spent context). A
// bare two-case select would satisfy it about half the time, so the loop is the
// assertion: 30 iterations put the odds of a broken implementation passing at
// under one in a billion.
func TestShutdownReportsSuccessAfterCompletion(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "exit 0"}, WithWorkDir("/"), WithLogger(nil))
	if err := h.StartEager(); err != nil {
		t.Fatalf("StartEager: %v", err)
	}
	settle, cancelSettle := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSettle()
	if err := h.Shutdown(settle); err != nil {
		t.Fatalf("Shutdown(ctx) = %v, want nil", err)
	}

	spent, cancelSpent := context.WithCancel(context.Background())
	cancelSpent() // already expired before the call
	for i := range 30 {
		if err := h.Shutdown(spent); err != nil {
			t.Fatalf("Shutdown(expired ctx) after teardown finished = %v on iteration %d, want nil", err, i)
		}
	}
}

// TestManagerShutdownIsIdempotent covers the double-call an app hits on its fatal
// path: httpserver's teardown hook runs mgr.Shutdown, and the error branch above it
// runs it again. The second call has no sessions and no live loops, so it must
// report success rather than an expiry against an already-closed wait.
func TestManagerShutdownIsIdempotent(t *testing.T) {
	m := NewSessionManager(catFactory)
	if _, err := m.Create(); err != nil {
		t.Fatalf("Create: %v", err)
	}
	shutdownManager(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown(ctx) = %v, want nil", err)
	}
}

// TestStartAfterShutdown_spawnsNothing covers a handler shut down before its
// first child: a later start path must spawn nothing, since Shutdown has
// already reported every goroutine returned.
func TestStartAfterShutdown_spawnsNothing(t *testing.T) {
	// Unregistered client states: no flush loop writes to them.
	starts := map[string]func(h *Handler) error{
		"StartEager": (*Handler).StartEager,
		"resize": func(h *Handler) error {
			h.handleResize(&clientState{}, 80, 24)
			return nil
		},
		"binary input": func(h *Handler) error {
			h.handleBinaryFrame(nil, &clientState{}, []byte("x"), false, true, nil)
			return nil
		},
	}
	for name, start := range starts {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			h := NewHandler([]string{"/bin/sleep", "30"}, WithWorkDir("/"),
				WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
			ctx, cancel := context.WithTimeout(t.Context(), waitPatience)
			defer cancel()
			if err := h.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown on an unstarted handler = %v, want nil", err)
			}
			// Kills the child a failing run spawned.
			shutdownOnCleanup(t, h)

			err := start(h)
			if name == "StartEager" && err == nil {
				t.Errorf("StartEager after Shutdown = nil, want an error")
			}
			if h.started.Load() {
				t.Errorf("a child started after Shutdown returned")
			}
			if out := logs.String(); strings.Contains(out, "process start failed") {
				t.Errorf("start after Shutdown logged an error:\n%s", out)
			}
		})
	}
}

// TestResizeAfterShutdown_endsTheSocket covers a client attached to a handler
// shut down before its first child: no process exit will ever close its socket,
// so the refused start must.
func TestResizeAfterShutdown_endsTheSocket(t *testing.T) {
	h, wsURL := fenceHandler(t, []string{"/bin/sleep", "30"})
	ctx, cancel := context.WithTimeout(t.Context(), waitPatience)
	defer cancel()
	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown on an unstarted handler = %v, want nil", err)
	}
	ws := dialWS(t, wsURL)
	resize := append([]byte{0x00}, `{"type":"resize","cols":80,"rows":24}`...)
	if err := ws.Write(ctx, websocket.MessageBinary, resize); err != nil {
		t.Fatalf("writing the resize: %v", err)
	}
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			t.Fatalf("socket still open after %v; a resize refused after Shutdown must end it", waitPatience)
		}
		if got := websocket.CloseStatus(err); got != websocket.StatusNormalClosure {
			t.Errorf("Read after a refused resize: close status %v (err %v), want %v", got, err, websocket.StatusNormalClosure)
		}
		return
	}
}
