package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestResolveSession_GCsIdleSession verifies the opportunistic GC sweep in
// ResolveSession removes a session idle longer than the 60-minute window.
// Resolving an unknown session id triggers the sweep.
func TestResolveSession_GCsIdleSession(t *testing.T) {
	r := newClientRegistry(slog.Default())
	r.sessions["idle"] = ledger(time.Now().Add(-61*time.Minute), 7)

	// Resolving an unknown session id triggers the opportunistic GC sweep.
	r.ResolveSession(&clientState{}, "fresh")

	r.mu.Lock()
	_, present := r.sessions["idle"]
	r.mu.Unlock()
	if present {
		t.Errorf("ResolveSession: 61-minute-idle session still present; want it GC'd (idle > 60 min)")
	}
}

// TestResolveSession_retainsRecentSession verifies the GC sweep keeps a
// session whose last activity is well within the 60-minute window.
func TestResolveSession_retainsRecentSession(t *testing.T) {
	r := newClientRegistry(slog.Default())
	r.sessions["recent"] = ledger(time.Now().Add(-1*time.Minute), 3)

	r.ResolveSession(&clientState{}, "fresh")

	r.mu.Lock()
	_, present := r.sessions["recent"]
	r.mu.Unlock()
	if !present {
		t.Errorf("ResolveSession: 1-minute-idle session was removed; want it retained (threshold is 60 min)")
	}
}

// captureLogs makes a Debug-level text handler over a fresh buffer slog's
// default for the test's duration and returns the buffer. Callers must be
// serial (no t.Parallel): the default logger is a process global.
//
// slog.SetDefault also points the standard log package at the installed
// handler, and it skips that redirect when the logger being installed carries
// slog's own default handler. Reinstalling the previous logger therefore does
// not undo the redirect, so the writer and flags are saved and restored
// explicitly. slog goes back first: reinstalling a previous handler that is not
// slog's default re-runs the redirect and would overwrite a log restore done
// before it. The cleanup is t.Cleanup rather than defer so it also runs when a
// subtest of the caller fails.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

// TestCaptureLogsRestoresLogGlobals pins the restore in captureLogs: the swap
// redirects the standard log package's writer and zeroes its flags, and the
// cleanup must put both back. Without it, one test silences slog for the rest of
// the package, because slog's own default handler writes through log.Output.
func TestCaptureLogsRestoresLogGlobals(t *testing.T) {
	wantWriter, wantFlags := log.Writer(), log.Flags()

	t.Run("swap", func(t *testing.T) {
		captureLogs(t)
		if log.Writer() == wantWriter {
			t.Fatal("captureLogs did not redirect log.Writer(); the restore under test would guard nothing")
		}
	})

	if got := log.Writer(); got != wantWriter {
		t.Errorf("log.Writer() after captureLogs cleanup = %T(%p), want the original %T(%p)", got, got, wantWriter, wantWriter)
	}
	if got := log.Flags(); got != wantFlags {
		t.Errorf("log.Flags() after captureLogs cleanup = %d, want %d", got, wantFlags)
	}
}

// TestResolveSession_GCLogsOnlyWhenSessionHadBytes verifies the GC sweep emits
// the "gc'd idle session with received bytes" info log only when the evicted
// session actually received input; a zero-byte session is GC'd silently. The
// log lets operators correlate user-visible "my input vanished" reports with
// the eviction.
func TestResolveSession_GCLogsOnlyWhenSessionHadBytes(t *testing.T) {
	buf := captureLogs(t)

	const logMsg = "gc'd idle session with received bytes"

	// received > 0 -> the GC must log.
	r := newClientRegistry(slog.Default())
	r.sessions["had-bytes"] = ledger(time.Now().Add(-61*time.Minute), 5)
	r.ResolveSession(&clientState{}, "fresh1")
	if !strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with received>0 did not emit %q", logMsg)
	}

	// received == 0 -> the GC must NOT log.
	buf.Reset()
	r2 := newClientRegistry(slog.Default())
	r2.sessions["no-bytes"] = ledger(time.Now().Add(-61*time.Minute), 0)
	r2.ResolveSession(&clientState{}, "fresh2")
	if strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with received==0 emitted %q; want silent", logMsg)
	}
}

// ledger builds a sessionState fixture; received is atomic, so a composite
// literal cannot set it.
func ledger(lastSeen time.Time, received uint64) *sessionState {
	s := &sessionState{lastSeen: lastSeen}
	s.received.Store(received)
	return s
}

// applyN sends n bytes of input from state through the production input path
// and fails the test unless they were applied.
func applyN(t *testing.T, r *clientRegistry, state *clientState, n int) {
	t.Helper()
	applied, err := r.ApplyInput(state, io.Discard, make([]byte, n))
	if err != nil || !applied {
		t.Fatalf("ApplyInput(%d bytes) = (applied %v, err %v), want (true, nil)", n, applied, err)
	}
}

// TestApplyInput_writesAndCountsForTheOwner verifies the owner's frame reaches
// the PTY whole, advances the ledger by its length, and refreshes lastSeen.
func TestApplyInput_writesAndCountsForTheOwner(t *testing.T) {
	r := newClientRegistry(slog.Default())
	st := &clientState{}
	r.ResolveSession(st, "sid")
	sess := st.session.Load()
	r.mu.Lock()
	sess.lastSeen = time.Unix(1_000_000, 0)
	r.mu.Unlock()

	var pty bytes.Buffer
	applied, err := r.ApplyInput(st, &pty, []byte("hello"))
	if err != nil || !applied {
		t.Fatalf("ApplyInput(owner, %q) = (applied %v, err %v), want (true, nil)", "hello", applied, err)
	}
	if got := pty.String(); got != "hello" {
		t.Errorf("PTY received %q, want %q", got, "hello")
	}
	if got := sess.received.Load(); got != 5 {
		t.Errorf("received after a 5-byte frame = %d, want 5", got)
	}
	r.mu.Lock()
	age := time.Since(sess.lastSeen)
	r.mu.Unlock()
	if age > time.Minute {
		t.Errorf("ApplyInput left lastSeen %v old; want refreshed to ~now", age.Round(time.Second))
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("pty closed") }

// TestApplyInput_failedWriteIsNotCounted verifies a frame the PTY refused does
// not advance the ledger: an ack covering it would trim bytes the child never
// received out of the client's outbox.
func TestApplyInput_failedWriteIsNotCounted(t *testing.T) {
	r := newClientRegistry(slog.Default())
	st := &clientState{}
	r.ResolveSession(st, "sid")

	applied, err := r.ApplyInput(st, errWriter{}, []byte("hello"))
	if err == nil || applied {
		t.Errorf("ApplyInput(failing PTY) = (applied %v, err %v), want (false, non-nil)", applied, err)
	}
	if got := st.session.Load().received.Load(); got != 0 {
		t.Errorf("received after a failed write = %d, want 0", got)
	}
}

// TestApplyInput_beforeResumeWritesWithoutALedger verifies a socket that has
// not resumed still reaches the PTY (a client that never speaks the protocol)
// and that its input is counted on no ledger.
func TestApplyInput_beforeResumeWritesWithoutALedger(t *testing.T) {
	r := newClientRegistry(slog.Default())
	st := &clientState{}

	var pty bytes.Buffer
	applied, err := r.ApplyInput(st, &pty, []byte("ls\n"))
	if err != nil || !applied {
		t.Fatalf("ApplyInput(no ledger) = (applied %v, err %v), want (true, nil)", applied, err)
	}
	if got := pty.String(); got != "ls\n" {
		t.Errorf("PTY received %q, want %q", got, "ls\n")
	}
	if len(r.sessions) != 0 {
		t.Errorf("pre-resume input created %d ledgers, want 0", len(r.sessions))
	}
}

// TestResolveSession_transfersTheLedgerAndFencesThePriorOwner pins ownership:
// a second socket resuming the same key takes the ledger over, the first is
// returned fenced and its input is refused unwritten and uncounted, and the
// new owner's ack covers exactly what the first one applied.
func TestResolveSession_transfersTheLedgerAndFencesThePriorOwner(t *testing.T) {
	r := newClientRegistry(slog.Default())
	a, b := &clientState{}, &clientState{}

	if _, _, displaced := r.ResolveSession(a, "sid#page"); displaced != nil {
		t.Fatalf("first resume displaced %p, want nil (the ledger had no owner)", displaced)
	}
	applyN(t, r, a, 7)

	ack, created, displaced := r.ResolveSession(b, "sid#page")
	if created || ack != 7 {
		t.Errorf("B's resume = (ack %d, created %v), want (7, false)", ack, created)
	}
	if displaced != a {
		t.Errorf("B's resume displaced %p, want A (%p)", displaced, a)
	}
	if !a.fenced.Load() {
		t.Errorf("A.fenced = false after B took its ledger, want true")
	}
	if b.fenced.Load() {
		t.Errorf("B.fenced = true, want false (B is the owner)")
	}

	var pty bytes.Buffer
	applied, err := r.ApplyInput(a, &pty, []byte("x\n"))
	if err != nil || applied {
		t.Errorf("ApplyInput(fenced A) = (applied %v, err %v), want (false, nil)", applied, err)
	}
	if pty.Len() != 0 {
		t.Errorf("fenced A's frame reached the PTY: %q", pty.String())
	}
	if got := b.session.Load().received.Load(); got != 7 {
		t.Errorf("received after fenced A's frame = %d, want 7 (unchanged)", got)
	}
	applyN(t, r, b, 2)
	if got := b.session.Load().received.Load(); got != 9 {
		t.Errorf("received after B's 2-byte frame = %d, want 9", got)
	}
}

// TestResolveSession_doesNotFenceASocketThatMovedLedgers verifies a socket
// re-resuming its own key displaces nobody, and that a socket which moved to
// another key gave the first one up: a later resume there fences no one.
func TestResolveSession_doesNotFenceASocketThatMovedLedgers(t *testing.T) {
	r := newClientRegistry(slog.Default())
	a, b := &clientState{}, &clientState{}

	r.ResolveSession(a, "k1")
	if _, _, displaced := r.ResolveSession(a, "k1"); displaced != nil || a.fenced.Load() {
		t.Errorf("A re-resuming k1 displaced %p (A fenced %v), want nil and unfenced", displaced, a.fenced.Load())
	}
	r.ResolveSession(a, "k2")
	if _, _, displaced := r.ResolveSession(b, "k1"); displaced != nil {
		t.Errorf("B resuming k1 after A moved to k2 displaced %p, want nil", displaced)
	}
	if a.fenced.Load() {
		t.Errorf("A was fenced by a resume on a ledger it had left")
	}
	applyN(t, r, a, 3)
}

// TestResolveSession_aFencedSocketDoesNotTakeTheLedgerBack verifies a resume
// from a socket that was already superseded leaves the newer owner in place:
// it displaces nobody, fences nobody, and its input stays refused.
func TestResolveSession_aFencedSocketDoesNotTakeTheLedgerBack(t *testing.T) {
	r := newClientRegistry(slog.Default())
	a, b := &clientState{}, &clientState{}
	r.ResolveSession(a, "sid#page")
	applyN(t, r, a, 4)
	if _, _, displaced := r.ResolveSession(b, "sid#page"); displaced != a {
		t.Fatalf("B's resume displaced %p, want A (%p)", displaced, a)
	}

	if _, _, displaced := r.ResolveSession(a, "sid#page"); displaced != nil {
		t.Errorf("fenced A's resume displaced %p, want nil", displaced)
	}
	if b.fenced.Load() {
		t.Errorf("fenced A's resume fenced B, the ledger's owner")
	}
	if applied, err := r.ApplyInput(a, io.Discard, []byte("x")); err != nil || applied {
		t.Errorf("ApplyInput(fenced A) after its resume = (applied %v, err %v), want (false, nil)", applied, err)
	}
	applyN(t, r, b, 2)
	if got := b.session.Load().received.Load(); got != 6 {
		t.Errorf("received after B's 2-byte frame = %d, want 6", got)
	}
}

// TestResolveSession_aTakeoverDuringAMoveEndsTheMovingSocket holds a socket
// between attaching to its new ledger and leaving its old one, and lets another
// socket take the old ledger over in that gap. The moving socket is fenced by
// that takeover, so it must not become the new ledger's owner either.
func TestResolveSession_aTakeoverDuringAMoveEndsTheMovingSocket(t *testing.T) {
	r := newClientRegistry(slog.Default())
	a, b := &clientState{}, &clientState{}
	r.ResolveSession(a, "k1")

	var takeoverDisplaced *clientState
	hold := func() {
		testLedgerMoveHold.Store(nil)
		_, _, takeoverDisplaced = r.ResolveSession(b, "k1")
	}
	testLedgerMoveHold.Store(&hold)
	t.Cleanup(func() { testLedgerMoveHold.Store(nil) })

	if _, _, displaced := r.ResolveSession(a, "k2"); displaced != nil {
		t.Errorf("A's move to k2 displaced %p, want nil (k2 had no owner)", displaced)
	}
	if takeoverDisplaced != a {
		t.Fatalf("B's takeover of k1 mid-move displaced %p, want A (%p)", takeoverDisplaced, a)
	}
	if applied, err := r.ApplyInput(a, io.Discard, []byte("x")); err != nil || applied {
		t.Errorf("ApplyInput(A) after the takeover = (applied %v, err %v), want (false, nil)", applied, err)
	}
	if _, _, displaced := r.ResolveSession(&clientState{}, "k2"); displaced != nil {
		t.Errorf("a fresh resume on k2 displaced %p, want nil (fenced A must not own k2)", displaced)
	}
	if b.fenced.Load() {
		t.Errorf("B was fenced, want it to keep k1")
	}
	applyN(t, r, b, 1)
}

// TestRemove_releasesTheLedger verifies a departed owner leaves its ledger
// unowned, so the next resume on it supersedes nobody.
func TestRemove_releasesTheLedger(t *testing.T) {
	r := newClientRegistry(slog.Default())
	ws := &websocket.Conn{}
	a := r.Add(ws)
	r.ResolveSession(a, "sid")
	r.Remove(ws)

	if _, _, displaced := r.ResolveSession(&clientState{}, "sid"); displaced != nil {
		t.Errorf("resume after the owner departed displaced %p, want nil", displaced)
	}
}

// TestRegistry_ConcurrentResolveApplySnapshot stresses the registry's own
// lock: many goroutines resolve sessions, apply input, and
// snapshot concurrently. Run under -race to surface data races on the
// sessions map. Real *websocket.Conn values aren't needed — the contention
// under test is on session state, not the client map keys.
func TestRegistry_ConcurrentResolveApplySnapshot(t *testing.T) {
	r := newClientRegistry(slog.Default())
	payload := make([]byte, 42)
	const goroutines = 20
	const iters = 200

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range iters {
				state := &clientState{}
				sessionID := SessionID("session-" + string(rune('A'+g)) + "-" + string(rune('0'+i%10)))
				_, _, _ = r.ResolveSession(state, sessionID)
				_, _ = r.ApplyInput(state, io.Discard, payload)
				_, _, _ = r.Snapshot()
			}
		})
	}
	wg.Wait()
}

// TestRegistry_ConcurrentResolveSharedSession stresses contention on the same
// sessionState: many goroutines resolve a small set of shared session ids and
// apply input concurrently, each resolve taking the ledger over from the last.
// Run under -race.
func TestRegistry_ConcurrentResolveSharedSession(t *testing.T) {
	r := newClientRegistry(slog.Default())
	payload := make([]byte, 10)
	const goroutines = 50
	const iters = 100

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for i := range iters {
				state := &clientState{}
				sid := SessionID("shared-session")
				if i%3 == 0 {
					sid = "alt-session"
				}
				_, _, _ = r.ResolveSession(state, sid)
				_, _ = r.ApplyInput(state, io.Discard, payload)
			}
		})
	}
	wg.Wait()
}

// TestResolveSession_evictsOldestWhenOverCap pins the maxResumeSessions cap backstop
// (CWE-770) that ResolveSession enforces via evictOldestSession: when a new
// session pushes the retained count past maxResumeSessions, the single oldest-lastSeen
// entry is evicted (not the newcomer) and the count returns to maxResumeSessions.
func TestResolveSession_evictsOldestWhenOverCap(t *testing.T) {
	r := newClientRegistry(slog.Default())
	now := time.Now()

	// One session is distinctly the oldest, but still inside the 60-min
	// retention window so the idle GC does NOT remove it -- forcing the cap
	// eviction (not the GC) to be the remover we assert on.
	const oldestID = "oldest"
	r.sessions[oldestID] = &sessionState{lastSeen: now.Add(-30 * time.Minute)}
	// Fill the rest to exactly maxResumeSessions with recent entries. Distinct 2-byte
	// keys avoid an fmt/strconv import (none collide with the longer ASCII ids).
	for i := 1; i < maxResumeSessions; i++ {
		r.sessions[SessionID([]byte{byte(i), byte(i >> 8)})] = &sessionState{lastSeen: now.Add(-time.Minute)}
	}
	if len(r.sessions) != maxResumeSessions {
		t.Fatalf("setup: %d sessions, want exactly maxResumeSessions=%d", len(r.sessions), maxResumeSessions)
	}

	// Resolving a new, unknown session id pushes the count to maxResumeSessions+1 and
	// triggers the cap eviction.
	r.ResolveSession(&clientState{}, "newcomer")

	r.mu.Lock()
	defer r.mu.Unlock()
	if got := len(r.sessions); got != maxResumeSessions {
		t.Errorf("after over-cap resolve: %d sessions retained, want %d (cap eviction must drop exactly one)", got, maxResumeSessions)
	}
	if _, ok := r.sessions[oldestID]; ok {
		t.Errorf("cap eviction kept the oldest session %q; want the oldest-lastSeen entry evicted", oldestID)
	}
	if _, ok := r.sessions["newcomer"]; !ok {
		t.Error("cap eviction removed the just-added newcomer; want the OLDEST entry removed, not the newest")
	}
}

// TestMinLiveSize_minsEachDimensionAndSkipsSizeless verifies MinLiveSize
// returns the per-dimension minimum across connected clients that reported a
// size, skipping any that never sent one, so the result fits inside every
// connected client's viewport.
func TestMinLiveSize_minsEachDimensionAndSkipsSizeless(t *testing.T) {
	r := newClientRegistry(slog.Default())
	r.clients[new(websocket.Conn)] = &clientState{cols: 120, rows: 40}
	r.clients[new(websocket.Conn)] = &clientState{cols: 80, rows: 24}
	r.clients[new(websocket.Conn)] = &clientState{} // never reported a size -> skipped

	cols, rows, ok := r.MinLiveSize()
	if !ok || cols != 80 || rows != 24 {
		t.Errorf("MinLiveSize() = (%d, %d, %v), want (80, 24, true) [min per dimension]", cols, rows, ok)
	}
}

// TestMinLiveSize_falseWhenNoSizedClient verifies MinLiveSize reports ok=false
// when no connected client has a known size, so the heal becomes a no-op.
func TestMinLiveSize_falseWhenNoSizedClient(t *testing.T) {
	r := newClientRegistry(slog.Default())
	r.clients[new(websocket.Conn)] = &clientState{} // connected but no size yet
	if _, _, ok := r.MinLiveSize(); ok {
		t.Errorf("MinLiveSize() ok=true with no sized client; want false")
	}
}

// TestRemove_returnsDepartedSize verifies Remove returns the size the removed
// socket had reported (so the caller can decide whether the departure should
// heal the shared screen) and drops it from the registry.
func TestRemove_returnsDepartedSize(t *testing.T) {
	r := newClientRegistry(slog.Default())
	ws := new(websocket.Conn)
	st := r.Add(ws)
	r.RecordSize(st, 100, 30)

	cols, rows := r.Remove(ws)
	if cols != 100 || rows != 30 {
		t.Errorf("Remove() = (%d, %d), want (100, 30) [the departed socket's recorded size]", cols, rows)
	}
	r.mu.Lock()
	_, present := r.clients[ws]
	r.mu.Unlock()
	if present {
		t.Errorf("Remove: connection still registered after removal")
	}
}

// TestRemove_stampsLastSeenOnDetach pins the retention semantics the iOS-sleep
// resume depends on: the 60-minute window measures time since a client last
// HELD the ledger, not time since the last keystroke. A session whose last
// input was hours ago (a tab reading agent output) must survive a detach plus a
// sub-window sleep — before the detach stamp it was reclaimed on the very next
// key miss, and the returning client got a false ledger-loss / "server
// restarted" banner.
func TestRemove_stampsLastSeenOnDetach(t *testing.T) {
	r := newClientRegistry(slog.Default())
	ws := &websocket.Conn{}
	state := r.Add(ws)
	r.ResolveSession(state, "sid")
	applyN(t, r, state, 10)

	// Two hours of reading output with no typing: nothing refreshes lastSeen
	// while the socket stays up, so the ledger ages past the GC window.
	r.mu.Lock()
	r.sessions["sid"].lastSeen = time.Now().Add(-2 * time.Hour)
	r.mu.Unlock()

	r.Remove(ws) // the screen goes to sleep and the socket drops

	// The detach stamp is the whole fix: the clock restarts here, so the ledger
	// now reads as freshly idle rather than two hours stale.
	r.mu.Lock()
	aged := time.Since(r.sessions["sid"].lastSeen)
	r.mu.Unlock()
	if aged > time.Minute {
		t.Fatalf("detach left lastSeen %v old; want stamped to ~now", aged.Round(time.Second))
	}

	// A key miss elsewhere (another tab reconnecting) triggers the sweep 30
	// minutes later — inside the window, measured from the detach.
	r.mu.Lock()
	r.sessions["sid"].lastSeen = time.Now().Add(-30 * time.Minute)
	r.mu.Unlock()

	r.ResolveSession(&clientState{}, "other") // opportunistic GC sweep

	r.mu.Lock()
	_, present := r.sessions["sid"]
	r.mu.Unlock()
	if !present {
		t.Errorf("GC reclaimed a ledger detached only 30m ago; want it retained until 60m past the detach")
	}

	// And it still expires: past the window, measured from the detach.
	r.mu.Lock()
	r.sessions["sid"].lastSeen = time.Now().Add(-61 * time.Minute)
	r.mu.Unlock()
	r.ResolveSession(&clientState{}, "other2")
	r.mu.Lock()
	_, present = r.sessions["sid"]
	r.mu.Unlock()
	if present {
		t.Errorf("GC retained a ledger detached 61m ago; want it reclaimed (the window must still bind)")
	}
}

// TestResolveSession_createdFlagAndLastSeenRefresh pins the two ResolveSession
// behaviors the ledger-loss protocol depends on: a key miss reports
// created=true (handleResume turns that plus claimed sentBytes into the
// resumeAck ledgerLost flag), and a key HIT refreshes lastSeen so an attached
// but input-idle client (a pure viewer reconnecting) never ages into the GC
// window merely because it never sent input.
func TestResolveSession_createdFlagAndLastSeenRefresh(t *testing.T) {
	r := newClientRegistry(slog.Default())

	_, created, _ := r.ResolveSession(&clientState{}, "sid")
	if !created {
		t.Errorf("first ResolveSession(sid): created=false, want true (key miss)")
	}

	// Age the session, then hit it again: created must be false and lastSeen
	// must be refreshed to ~now.
	r.mu.Lock()
	r.sessions["sid"].lastSeen = time.Now().Add(-59 * time.Minute)
	r.mu.Unlock()

	_, created, _ = r.ResolveSession(&clientState{}, "sid")
	if created {
		t.Errorf("second ResolveSession(sid): created=true, want false (key hit)")
	}
	r.mu.Lock()
	age := time.Since(r.sessions["sid"].lastSeen)
	r.mu.Unlock()
	if age > time.Minute {
		t.Errorf("key hit left lastSeen %v old; want refreshed to ~now", age.Round(time.Second))
	}
}

// TestGCSkipsAttachedSessions verifies gcIdleSessions never reclaims a ledger
// attached to a live client: two sessions idle past the 60-minute window, one
// attached to a registered client — the attached one survives the sweep, the
// orphan is deleted.
func TestGCSkipsAttachedSessions(t *testing.T) {
	r := newClientRegistry(slog.Default())
	attachedSess := ledger(time.Now().Add(-61*time.Minute), 3)
	orphanSess := ledger(time.Now().Add(-61*time.Minute), 5)
	r.sessions["attached"] = attachedSess
	r.sessions["orphan"] = orphanSess
	ws := &websocket.Conn{}
	state := r.Add(ws)
	state.session.Store(attachedSess)

	// Resolving an unknown session id triggers the opportunistic GC sweep.
	r.ResolveSession(&clientState{}, "fresh")

	r.mu.Lock()
	_, attachedPresent := r.sessions["attached"]
	_, orphanPresent := r.sessions["orphan"]
	r.mu.Unlock()
	if !attachedPresent {
		t.Errorf("GC reclaimed a session attached to a live client; want it retained")
	}
	if orphanPresent {
		t.Errorf("GC retained an unattached idle session; want it reclaimed")
	}
}

// TestEvictOldestSession_prefersUnattached verifies the cap eviction picks the
// oldest UNATTACHED victim when the globally-oldest session is attached to a
// live client: an abuser minting ids can then only evict abandoned ledgers,
// never a connected client's resume state.
func TestEvictOldestSession_prefersUnattached(t *testing.T) {
	r := newClientRegistry(slog.Default())

	// Oldest overall: attached to a live client.
	attachedSess := &sessionState{lastSeen: time.Now().Add(-50 * time.Minute)}
	r.sessions["attached-oldest"] = attachedSess
	ws := &websocket.Conn{}
	state := r.Add(ws)
	state.session.Store(attachedSess)

	// Second-oldest: unattached — the expected victim.
	r.sessions["unattached-victim"] = &sessionState{lastSeen: time.Now().Add(-40 * time.Minute)}

	// Fill to the cap so the next create must evict.
	for i := 0; len(r.sessions) < maxResumeSessions; i++ {
		r.sessions[SessionID(fmt.Sprintf("filler-%d", i))] = &sessionState{lastSeen: time.Now()}
	}

	r.ResolveSession(&clientState{}, "overflow") // cap+1 → eviction

	r.mu.Lock()
	_, attachedPresent := r.sessions["attached-oldest"]
	_, victimPresent := r.sessions["unattached-victim"]
	total := len(r.sessions)
	r.mu.Unlock()
	if !attachedPresent {
		t.Errorf("cap eviction removed the attached session; want the oldest unattached victim instead")
	}
	if victimPresent {
		t.Errorf("cap eviction kept the oldest unattached session; want it evicted")
	}
	if total > maxResumeSessions {
		t.Errorf("session count %d exceeds cap %d after eviction", total, maxResumeSessions)
	}
}

// TestAckSweepTargets_recordsOptimisticallyAndHonorsNoteAcksSent pins the ack
// sweep's bookkeeping: a session whose received count advanced past lastAckSent
// is a target exactly once (optimistic record), a session already covered by a
// dispatched content frame (NoteAcksSent) is skipped, and a session-less
// client is never a target.
func TestAckSweepTargets_recordsOptimisticallyAndHonorsNoteAcksSent(t *testing.T) {
	r := newClientRegistry(slog.Default())
	ws := &websocket.Conn{}
	state := r.Add(ws)
	r.ResolveSession(state, "sid")
	r.Add(&websocket.Conn{}) // session-less client: never a target

	applyN(t, r, state, 5)
	targets := r.AckSweepTargets()
	if got := targets[ws]; got != 5 || len(targets) != 1 {
		t.Fatalf("AckSweepTargets after +5 input = %v, want map[%p:5] with exactly one entry", targets, ws)
	}
	if again := r.AckSweepTargets(); len(again) != 0 {
		t.Errorf("second AckSweepTargets = %v, want empty (optimistic record must stick)", again)
	}

	// A content frame carried the next value: NoteAcksSent must suppress the sweep.
	applyN(t, r, state, 4) // received now 9
	r.NoteAcksSent(map[*websocket.Conn]uint64{ws: 9})
	if after := r.AckSweepTargets(); len(after) != 0 {
		t.Errorf("AckSweepTargets after NoteAcksSent(9) = %v, want empty", after)
	}

	// NoteAcksSent is MONOTONIC: a durable-stripped write finishing after a
	// resume batch reports an ack at or below the batch's resumeAck, and
	// recording it must not regress lastAckSent ("the highest ack this
	// socket was told") — a regression would make the next sweep re-send a
	// value the client already has AND mask the dedupe the field exists
	// for. Backward resyncs are handleResume's unconditional Store alone.
	r.NoteAcksSent(map[*websocket.Conn]uint64{ws: 4})
	if got := state.lastAckSent.Load(); got != 9 {
		t.Errorf("lastAckSent after NoteAcksSent(4) = %d, want 9 (monotonic: an older delivered ack must not regress it)", got)
	}
	if after := r.AckSweepTargets(); len(after) != 0 {
		t.Errorf("AckSweepTargets after the stale NoteAcksSent(4) = %v, want empty (received is still 9)", after)
	}
}

// TestPerSenderResumeKeysKeepIndependentLedgers pins per-sender resume keys:
// two clients on ONE managed session resume with distinct keys (`<sid>#<A>`,
// `<sid>#<B>`), and the registry, which keys ledgers by the resume string
// verbatim, gives each its own received count. A shared key would ack the
// combined total to both, so A's applyAck would trim bytes only B had sent.
// With A at 120 sent and 100 received, A's ack stays 100 however much B sends.
func TestPerSenderResumeKeysKeepIndependentLedgers(t *testing.T) {
	r := newClientRegistry(slog.Default())
	wsA, wsB := &websocket.Conn{}, &websocket.Conn{}
	stateA := r.Add(wsA)
	stateB := r.Add(wsB)
	defer r.Remove(wsA)
	defer r.Remove(wsB)

	ackA, createdA, _ := r.ResolveSession(stateA, "sess-1#instance-A")
	ackB, createdB, _ := r.ResolveSession(stateB, "sess-1#instance-B")
	if !createdA || !createdB {
		t.Fatalf("fresh per-sender keys must create distinct ledgers (createdA=%v createdB=%v)", createdA, createdB)
	}
	if ackA != 0 || ackB != 0 {
		t.Fatalf("fresh ledgers must start at zero (ackA=%d ackB=%d)", ackA, ackB)
	}

	// A sends 120 bytes but the server receives only 100 before the drop;
	// B sends 50. Each increments ITS OWN ledger.
	applyN(t, r, stateA, 100)
	applyN(t, r, stateB, 50)

	// A's resume acks A's ledger (100), not the combined 150: its 20 unacked
	// bytes stay in its outbox and retransmit. B's resume acks 50.
	if ack, created, _ := r.ResolveSession(stateA, "sess-1#instance-A"); created || ack != 100 {
		t.Errorf("A's resume = (ack %d, created %v), want (100, false): B's input must not advance A's ledger", ack, created)
	}
	if ack, created, _ := r.ResolveSession(stateB, "sess-1#instance-B"); created || ack != 50 {
		t.Errorf("B's resume = (ack %d, created %v), want (50, false)", ack, created)
	}
}

// TestMinLiveSize_skipsAClientMissingEitherDimension verifies each dimension is
// screened on its own: a client whose cols or rows is still unknown is not a
// sized client, so it must not enter the minimum. A single unscreened zero would
// pull the shared screen to a zero width or height — the whole screen, for every
// attached client, on one half-initialised socket.
func TestMinLiveSize_skipsAClientMissingEitherDimension(t *testing.T) {
	t.Run("rows reported without cols", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		r.clients[new(websocket.Conn)] = &clientState{cols: 80, rows: 24}
		r.clients[new(websocket.Conn)] = &clientState{rows: 24} // cols never reported

		cols, rows, ok := r.MinLiveSize()
		if !ok || cols != 80 || rows != 24 {
			t.Errorf("MinLiveSize() = (%d, %d, %v), want (80, 24, true): a client with no cols is not sized", cols, rows, ok)
		}
	})

	t.Run("cols reported without rows", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		r.clients[new(websocket.Conn)] = &clientState{cols: 80, rows: 24}
		r.clients[new(websocket.Conn)] = &clientState{cols: 80} // rows never reported

		cols, rows, ok := r.MinLiveSize()
		if !ok || cols != 80 || rows != 24 {
			t.Errorf("MinLiveSize() = (%d, %d, %v), want (80, 24, true): a client with no rows is not sized", cols, rows, ok)
		}
	})
}

// TestResolveSession_retainsExactlyTheCap pins the other side of the cap
// eviction: at maxResumeSessions the map is FULL, not over, so nothing is
// evicted. The distinction is a retained ledger: evicting at the cap would drop
// the oldest legitimate resume state one session before the cap is actually
// reached, and the client that owned it reconnects to a ledger-lost banner.
func TestResolveSession_retainsExactlyTheCap(t *testing.T) {
	r := newClientRegistry(slog.Default())
	now := time.Now()

	// Distinctly the oldest, but well inside the 60-minute retention window so
	// the idle GC is not the remover under test.
	const oldestID = "oldest"
	r.sessions[oldestID] = &sessionState{lastSeen: now.Add(-30 * time.Minute)}
	// One short of the cap, so the newcomer below lands exactly ON it.
	for i := 1; i < maxResumeSessions-1; i++ {
		r.sessions[SessionID([]byte{byte(i), byte(i >> 8)})] = &sessionState{lastSeen: now.Add(-time.Minute)}
	}
	if len(r.sessions) != maxResumeSessions-1 {
		t.Fatalf("setup: %d sessions, want maxResumeSessions-1 = %d", len(r.sessions), maxResumeSessions-1)
	}

	r.ResolveSession(&clientState{}, "newcomer")

	r.mu.Lock()
	defer r.mu.Unlock()
	if got := len(r.sessions); got != maxResumeSessions {
		t.Errorf("at the cap: %d sessions retained, want %d (the cap is full, not exceeded)", got, maxResumeSessions)
	}
	if _, ok := r.sessions[oldestID]; !ok {
		t.Errorf("the oldest session was evicted at the cap; want it retained until the cap is exceeded")
	}
}
