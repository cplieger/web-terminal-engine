package terminal

// gremlins_kill_vterm_u8_test.go — added by mutant-killing unit vterm-u8.
// Tests ONLY; no production code is modified. Each test documents the exact
// surviving gremlins mutant(s) it kills and why the asserted value depends on
// the precise operator at that source location.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// --- client_registry.go ----------------------------------------------------

// Kills client_registry.go:80:30 CONDITIONALS_NEGATION (true branch).
// Line: `if time.Since(s.lastSeen) > 60*time.Minute {` — the idle-session
// GC sweep inside ResolveSession. Original `>` GCs a session idle MORE than
// 60 minutes; the negation `<=` would keep it. A 61-minute-idle session must
// be removed when a brand-new session is resolved (which triggers the sweep).
func TestGkVtermU8_ResolveSession_idleSessionGCd(t *testing.T) {
	r := NewClientRegistry()
	// Insert an idle session directly (internal package access).
	r.sessions["gk_vterm_u8_old"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 7,
	}

	// Resolving an unknown session id triggers the opportunistic GC sweep.
	r.ResolveSession(&ClientState{}, "gk_vterm_u8_new")

	r.mu.Lock()
	_, present := r.sessions["gk_vterm_u8_old"]
	r.mu.Unlock()
	if present {
		t.Errorf("ResolveSession GC: 61-minute-idle session still present; want GC'd (idle > 60*time.Minute)")
	}
}

// Kills client_registry.go:80:30 CONDITIONALS_NEGATION (false branch) AND
// client_registry.go:80:34 ARITHMETIC_BASE.
// A session idle only 1 minute must be RETAINED by the sweep.
//   - NEGATION `<=`: 1min <= 60min is true → it would be GC'd → assertion fails.
//   - ARITHMETIC `60/time.Minute` (== 0): 1min > 0 is true → it would be GC'd →
//     assertion fails. (`60*time.Minute` == 1h; `60/time.Minute` is integer
//     division → 0, so the threshold collapses to "any past timestamp".)
func TestGkVtermU8_ResolveSession_recentSessionRetained(t *testing.T) {
	r := NewClientRegistry()
	r.sessions["gk_vterm_u8_recent"] = &sessionState{
		lastSeen:      time.Now().Add(-1 * time.Minute),
		bytesReceived: 3,
	}

	r.ResolveSession(&ClientState{}, "gk_vterm_u8_new")

	r.mu.Lock()
	_, present := r.sessions["gk_vterm_u8_recent"]
	r.mu.Unlock()
	if !present {
		t.Errorf("ResolveSession GC: 1-minute-idle session was removed; want retained (threshold is 60*time.Minute, not 0 and not <=)")
	}
}

// Kills client_registry.go:107:7 CONDITIONALS_NEGATION.
// Line: `if n <= 0 { return }` in IncrementReceived. With n=5 the original
// falls through and adds 5; the negation `n > 0` returns early → 0.
func TestGkVtermU8_IncrementReceived_positiveIncrements(t *testing.T) {
	r := NewClientRegistry()
	st := &ClientState{}
	sess := &sessionState{}
	st.session.Store(sess)

	r.IncrementReceived(st, 5)

	if sess.bytesReceived != 5 {
		t.Errorf("IncrementReceived(st, 5) = %d, want 5", sess.bytesReceived)
	}
}

// Kills client_registry.go:107:7 CONDITIONALS_BOUNDARY (`<=`→`<`) AND
// CONDITIONALS_NEGATION (`<=`→`>`).
// With n=0 the original early-returns (`0 <= 0` true) WITHOUT touching the
// session, so lastSeen stays at the sentinel. Both mutants make the guard
// false for n=0 (`0 < 0` and `0 > 0` are both false), so control falls through
// to the increment block which sets `sess.lastSeen = time.Now()` — detectably
// different from the sentinel. (bytesReceived stays 0 either way because
// `+= uint64(0)` is a no-op, so lastSeen is the discriminating observable.)
func TestGkVtermU8_IncrementReceived_zeroIsNoop(t *testing.T) {
	r := NewClientRegistry()
	st := &ClientState{}
	sentinel := time.Unix(1_000_000, 0)
	sess := &sessionState{lastSeen: sentinel}
	st.session.Store(sess)

	r.IncrementReceived(st, 0)

	if sess.bytesReceived != 0 {
		t.Errorf("IncrementReceived(st, 0): bytesReceived = %d, want 0", sess.bytesReceived)
	}
	if !sess.lastSeen.Equal(sentinel) {
		t.Errorf("IncrementReceived(st, 0) modified lastSeen to %v; want unchanged %v (n<=0 must early-return before touching the session)", sess.lastSeen, sentinel)
	}
}

// --- flush_builder.go ------------------------------------------------------

// Kills flush_builder.go:53:41 CONDITIONALS_NEGATION.
// Line: `if !screen.InAltScreen && len(drained) > 0 {` — gates whether drained
// scrollback becomes the frame's scrollLines. With real scrollback present and
// not in alt-screen, the original `> 0` is true → scrollLines non-empty; the
// negation `<= 0` is false → scrollLines empty.
func TestGkVtermU8_Build_scrollbackProducesScrollLines(t *testing.T) {
	screen := vt.New(3, 20) // tiny screen so writing many lines scrolls
	for range 15 {
		if _, err := screen.Write([]byte("gk_vterm_u8 scrollback line\r\n")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
	}

	b := &FlushFrameBuilder{}
	frame := b.Build(screen, true, map[*websocket.Conn]uint64{})
	if frame == nil {
		t.Fatalf("Build returned nil; expected a frame (full repaint baseline)")
	}
	if len(frame.scrollLines) == 0 {
		t.Errorf("Build: scrollLines empty; want non-empty (scrollback drained, not alt-screen → len(drained) > 0 must hold)")
	}
}

// Kills flush_builder.go:151:34 CONDITIONALS_NEGATION.
// Line: `if b.titleAnnounced && curTitle == b.prevTitle {` in buildTitlePayload.
// When the title is announced and UNCHANGED, the original `==` returns nil
// (suppresses the frame). The negation `!=` would instead emit a frame.
// When the title CHANGES, the original emits a frame; the negation suppresses it.
func TestGkVtermU8_BuildTitlePayload_changeDetection(t *testing.T) {
	screen := vt.New(5, 20)
	b := &FlushFrameBuilder{}

	screen.Title = "gk_vterm_u8_foo"
	// First call: not yet announced → must emit a payload.
	if got := b.buildTitlePayload(screen); len(got) == 0 {
		t.Fatalf("first buildTitlePayload = empty, want a title frame")
	}
	// Same title, now announced → original suppresses (nil). Negation `!=` emits.
	if got := b.buildTitlePayload(screen); len(got) != 0 {
		t.Errorf("unchanged-title buildTitlePayload = %d bytes, want 0 (curTitle == prevTitle must suppress)", len(got))
	}
	// Changed title → original emits. Negation `!=` would suppress.
	screen.Title = "gk_vterm_u8_bar"
	if got := b.buildTitlePayload(screen); len(got) == 0 {
		t.Errorf("changed-title buildTitlePayload = empty, want a title frame (curTitle != prevTitle must emit)")
	}
}

// Kills flush_builder.go:180:16 CONDITIONALS_BOUNDARY.
// Line: `if y < 0 || y >= rowCount {` in appendRowIfMissing — out-of-range
// guard. At y == rowCount the original `>=` rejects (out of range, not
// appended); the boundary mutant `>` accepts it (appended). The in-range
// neighbour y == rowCount-1 must always be appended.
func TestGkVtermU8_AppendRowIfMissing_rowCountBoundary(t *testing.T) {
	// y == rowCount is out of [0, rowCount): must NOT be appended.
	if got := appendRowIfMissing(nil, 5, 5); len(got) != 0 {
		t.Errorf("appendRowIfMissing(nil, 5, 5) = %v, want empty (y==rowCount is out of range)", got)
	}
	// y == rowCount-1 is in range: must be appended.
	got := appendRowIfMissing(nil, 4, 5)
	if len(got) != 1 || got[0] != 4 {
		t.Errorf("appendRowIfMissing(nil, 4, 5) = %v, want [4]", got)
	}
}

// --- ping.go ---------------------------------------------------------------

// Kills ping.go:47:11 CONDITIONALS_NEGATION.
// Line: `if err != nil {` in pingLoop — the failed-ping branch. We dial a real
// WebSocket, then CloseNow the client so every subsequent ws.Ping fails fast
// (the write-lock acquire returns net.ErrClosed immediately). The original
// `err != nil` enters the failure branch on each tick; after
// maxConsecutiveFailures it calls cancel(). The negation `err == nil` would
// skip the failure branch on every (failed) ping, so cancel() is never called.
func TestGkVtermU8_PingLoop_repeatedFailuresCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow() //nolint:errcheck // best-effort cleanup
		// Keep the server side reading so the handshake completes cleanly and
		// the handler returns once the client connection drops.
		for {
			if _, _, rerr := ws.Read(r.Context()); rerr != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	//nolint:bodyclose // coder/websocket Dial nils resp.Body on success
	ws, _, err := websocket.Dial(dctx, wsURL, nil)
	dcancel()
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}

	// Kill the client connection so each ws.Ping fails immediately rather than
	// blocking until the pong timeout.
	_ = ws.CloseNow()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan struct{})
	var once sync.Once
	recordCancel := func() {
		once.Do(func() { close(canceled) })
		cancel()
	}

	go pingLoop(ctx, recordCancel, ws)

	// maxConsecutiveFailures (3) ticks at wsPingInterval (2s) ≈ 6s under the
	// original; the negation never cancels. Generous bound for slow CI.
	select {
	case <-canceled:
		// pingLoop observed repeated ping failures and closed the connection.
	case <-time.After(25 * time.Second):
		t.Fatal("pingLoop did not cancel after repeated failed pings; failure branch (err != nil) not taken")
	}
}

// --- pingstat.go -----------------------------------------------------------

// Kills pingstat.go:126:9 CONDITIONALS_BOUNDARY.
// Line: `if rtt < 0 { return }` in Record — only NEGATIVE rtt is ignored. A
// zero-duration sample is valid: the original records it (samples becomes 1,
// srtt=0, rttvar=0 → rto=clampRTO(0)=minPongTimeout). The boundary mutant
// `rtt <= 0` ignores rtt==0, leaving rto at the bootstrap timeout.
func TestGkVtermU8_Record_zeroRTTRecorded(t *testing.T) {
	p := newPingStat()
	p.Record(0)

	got, _ := p.Timeout()
	if got != minPongTimeout {
		t.Errorf("Timeout after Record(0) = %v, want %v (rtt==0 is a valid sample; only rtt<0 is ignored)", got, minPongTimeout)
	}
}

// --- terminal.go -----------------------------------------------------------

// Kills terminal.go:216:14 CONDITIONALS_NEGATION.
// Line: `if h.cancel != nil { h.cancel() }` in Shutdown. With a non-nil cancel
// the original invokes it; the negation `== nil` would skip it.
func TestGkVtermU8_Shutdown_callsCancel(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"})
	called := false
	h.cancel = func() { called = true }
	h.started.Store(true) // Shutdown returns early unless started

	h.Shutdown()

	if !called {
		t.Errorf("Shutdown did not invoke h.cancel; want it called (h.cancel != nil branch)")
	}
}

// Kills terminal.go:220:12 CONDITIONALS_NEGATION.
// Line: `if h.ptmx != nil { h.ptmx.Close() }` in Shutdown. With a real
// (non-nil) file the original closes it, so a second Close returns an
// already-closed error. The negation `== nil` would skip the close, so the
// first real Close here succeeds (nil error).
func TestGkVtermU8_Shutdown_closesPtmx(t *testing.T) {
	h := NewHandler([]string{"/bin/sh"})
	f, err := os.CreateTemp(t.TempDir(), "gk_vterm_u8_ptmx")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	h.ptmx = f
	h.cancel = func() {} // non-nil no-op so the cancel branch never panics
	h.started.Store(true)

	h.Shutdown()

	// Original: Shutdown already closed it → re-Close errors.
	// Mutant (== nil): Shutdown skipped the close → this first close succeeds.
	if err := h.ptmx.Close(); err == nil {
		t.Errorf("re-Close after Shutdown returned nil; Shutdown did not close ptmx (h.ptmx != nil branch)")
	}
}

// Kills terminal.go:272:10 CONDITIONALS_BOUNDARY (`<`→`<=`) AND
// CONDITIONALS_NEGATION (`<`→`>=`).
// Line: `if cols < 1 { cols = defaultCols }` in ensureStarted. cols==1 is NOT
// < 1, so the original keeps it (screen.Width becomes 1). Both mutants make the
// guard true at cols==1 (`1 <= 1` and `1 >= 1`), defaulting cols to defaultCols
// (120) → screen.Width 120.
func TestGkVtermU8_EnsureStarted_colsOneNotDefaulted(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"}, WithWorkDir("/"))
	if err := h.ensureStarted(1, 24); err != nil {
		t.Fatalf("ensureStarted(1, 24): %v", err)
	}
	defer h.Shutdown()

	h.mu.Lock()
	w := h.screen.Width
	h.mu.Unlock()
	if w != 1 {
		t.Errorf("ensureStarted(cols=1): screen.Width = %d, want 1 (cols==1 is not < 1, must not default to %d)", w, defaultCols)
	}
}

// Kills terminal.go:275:10 CONDITIONALS_BOUNDARY (`<`→`<=`) AND
// CONDITIONALS_NEGATION (`<`→`>=`).
// Line: `if rows < 1 { rows = defaultRows }` in ensureStarted. rows==1 is NOT
// < 1, so the original keeps it (screen.Height becomes 1). Both mutants default
// rows to defaultRows (30) at rows==1 → screen.Height 30.
func TestGkVtermU8_EnsureStarted_rowsOneNotDefaulted(t *testing.T) {
	h := NewHandler([]string{"/bin/sh", "-c", "true"}, WithWorkDir("/"))
	if err := h.ensureStarted(24, 1); err != nil {
		t.Fatalf("ensureStarted(24, 1): %v", err)
	}
	defer h.Shutdown()

	h.mu.Lock()
	ht := h.screen.Height
	h.mu.Unlock()
	if ht != 1 {
		t.Errorf("ensureStarted(rows=1): screen.Height = %d, want 1 (rows==1 is not < 1, must not default to %d)", ht, defaultRows)
	}
}
