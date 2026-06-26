package terminal

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestResolveSession_GCsIdleSession verifies the opportunistic GC sweep in
// ResolveSession removes a session idle longer than the 60-minute window.
// Resolving an unknown session id triggers the sweep.
func TestResolveSession_GCsIdleSession(t *testing.T) {
	r := NewClientRegistry()
	r.sessions["idle"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 7,
	}

	// Resolving an unknown session id triggers the opportunistic GC sweep.
	r.ResolveSession(&ClientState{}, "fresh")

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
	r := NewClientRegistry()
	r.sessions["recent"] = &sessionState{
		lastSeen:      time.Now().Add(-1 * time.Minute),
		bytesReceived: 3,
	}

	r.ResolveSession(&ClientState{}, "fresh")

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
	r := NewClientRegistry()
	r.sessions["had-bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 5,
	}
	r.ResolveSession(&ClientState{}, "fresh1")
	if !strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived>0 did not emit %q", logMsg)
	}

	// bytesReceived == 0 -> the GC must NOT log.
	buf.Reset()
	r2 := NewClientRegistry()
	r2.sessions["no-bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 0,
	}
	r2.ResolveSession(&ClientState{}, "fresh2")
	if strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived==0 emitted %q; want silent", logMsg)
	}
}

// TestIncrementReceived_addsPositiveCount verifies a positive byte count is
// added to the session's running total.
func TestIncrementReceived_addsPositiveCount(t *testing.T) {
	r := NewClientRegistry()
	st := &ClientState{}
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
		t.Errorf("IncrementReceived(st, 0) modified lastSeen to %v; want unchanged %v", sess.lastSeen, sentinel)
	}
}

// TestRegistry_ConcurrentResolveIncrementSnapshot stresses the registry's own
// lock: many goroutines resolve sessions, increment received bytes, and
// snapshot concurrently. Run under -race to surface data races on the
// sessions map. Real *websocket.Conn values aren't needed — the contention
// under test is on session state, not the client map keys.
func TestRegistry_ConcurrentResolveIncrementSnapshot(t *testing.T) {
	r := NewClientRegistry()
	const goroutines = 20
	const iters = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for i := range iters {
				state := &ClientState{}
				sessionID := "session-" + string(rune('A'+id)) + "-" + string(rune('0'+i%10))
				_, _ = r.ResolveSession(state, sessionID)
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
	r := NewClientRegistry()
	const goroutines = 50
	const iters = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iters {
				state := &ClientState{}
				sid := "shared-session"
				if i%3 == 0 {
					sid = "alt-session"
				}
				ack, replay := r.ResolveSession(state, sid)
				_ = ack
				_ = replay
				r.IncrementReceived(state, 10)
			}
		}()
	}
	wg.Wait()
}
