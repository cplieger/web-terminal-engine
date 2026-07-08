package terminal

import (
	"bytes"
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
	r.sessions["idle"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 7,
	}

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
	r.sessions["recent"] = &sessionState{
		lastSeen:      time.Now().Add(-1 * time.Minute),
		bytesReceived: 3,
	}

	r.ResolveSession(&clientState{}, "fresh")

	r.mu.Lock()
	_, present := r.sessions["recent"]
	r.mu.Unlock()
	if !present {
		t.Errorf("ResolveSession: 1-minute-idle session was removed; want it retained (threshold is 60 min)")
	}
}

// TestResolveSession_GCLogsOnlyWhenSessionHadBytes verifies the GC sweep emits
// the "gc'd idle session with received bytes" info log only when the evicted
// session actually received input; a zero-byte session is GC'd silently. The
// log lets operators correlate user-visible "my input vanished" reports with
// the eviction.
func TestResolveSession_GCLogsOnlyWhenSessionHadBytes(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	const logMsg = "gc'd idle session with received bytes"

	// bytesReceived > 0 -> the GC must log.
	r := newClientRegistry(slog.Default())
	r.sessions["had-bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 5,
	}
	r.ResolveSession(&clientState{}, "fresh1")
	if !strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived>0 did not emit %q", logMsg)
	}

	// bytesReceived == 0 -> the GC must NOT log.
	buf.Reset()
	r2 := newClientRegistry(slog.Default())
	r2.sessions["no-bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 0,
	}
	r2.ResolveSession(&clientState{}, "fresh2")
	if strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived==0 emitted %q; want silent", logMsg)
	}
}

// TestIncrementReceived_addsPositiveCount verifies a positive byte count is
// added to the session's running total.
func TestIncrementReceived_addsPositiveCount(t *testing.T) {
	r := newClientRegistry(slog.Default())
	st := &clientState{}
	sess := &sessionState{}
	st.session.Store(sess)

	r.IncrementReceived(st, 5)

	if sess.bytesReceived != 5 {
		t.Errorf("IncrementReceived(st, 5) = %d, want 5", sess.bytesReceived)
	}
}

// TestIncrementReceived_zeroIsNoOp verifies a non-positive count returns early
// without touching the session: neither bytesReceived nor lastSeen changes.
// lastSeen is the discriminating observable since += 0 is a no-op on the
// counter regardless.
func TestIncrementReceived_zeroIsNoOp(t *testing.T) {
	r := newClientRegistry(slog.Default())
	st := &clientState{}
	sentinel := time.Unix(1_000_000, 0)
	sess := &sessionState{lastSeen: sentinel}
	st.session.Store(sess)

	r.IncrementReceived(st, 0)

	if sess.bytesReceived != 0 {
		t.Errorf("IncrementReceived(st, 0): bytesReceived = %d, want 0", sess.bytesReceived)
	}
	if !sess.lastSeen.Equal(sentinel) {
		t.Errorf("IncrementReceived(st, 0) modified lastSeen to %v; want unchanged %v", sess.lastSeen, sentinel)
	}
}

// TestRegistry_ConcurrentResolveIncrementSnapshot stresses the registry's own
// lock: many goroutines resolve sessions, increment received bytes, and
// snapshot concurrently. Run under -race to surface data races on the
// sessions map. Real *websocket.Conn values aren't needed — the contention
// under test is on session state, not the client map keys.
func TestRegistry_ConcurrentResolveIncrementSnapshot(t *testing.T) {
	r := newClientRegistry(slog.Default())
	const goroutines = 20
	const iters = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range iters {
				state := &clientState{}
				sessionID := "session-" + string(rune('A'+id)) + "-" + string(rune('0'+i%10))
				_ = r.ResolveSession(state, sessionID)
				r.IncrementReceived(state, 42)
				_ = r.Snapshot()
			}
		}(g)
	}
	wg.Wait()
}

// TestRegistry_ConcurrentResolveSharedSession stresses contention on the same
// sessionState: many goroutines resolve a small set of shared session ids and
// increment their counters concurrently. Run under -race.
func TestRegistry_ConcurrentResolveSharedSession(t *testing.T) {
	r := newClientRegistry(slog.Default())
	const goroutines = 50
	const iters = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iters {
				state := &clientState{}
				sid := "shared-session"
				if i%3 == 0 {
					sid = "alt-session"
				}
				_ = r.ResolveSession(state, sid)
				r.IncrementReceived(state, 10)
			}
		}()
	}
	wg.Wait()
}

// TestResolveSession_evictsOldestWhenOverCap pins the maxResumeSessions cap backstop
// (CWE-770) that ResolveSession enforces via evictOldestSession: when a new
// session pushes the retained count past maxResumeSessions, the single oldest-lastSeen
// entry is evicted (not the newcomer) and the count returns to maxResumeSessions. The
// eviction body (find-oldest loop + delete) was unexercised after extraction
// into evictOldestSession (only the early-return ran, 22.2% coverage), so a
// mutant in the `sx.lastSeen.Before(oldest)` comparison or the delete survived.
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
		r.sessions[string([]byte{byte(i), byte(i >> 8)})] = &sessionState{lastSeen: now.Add(-time.Minute)}
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
