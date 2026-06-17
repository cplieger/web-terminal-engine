package terminal

// Round-2 mutant-killing tests for package terminal. These cover the two
// surviving mutants that are killable by a direct test without adding any
// production seam (the clamp / Duration.Abs survivors were killed by
// production min/max/stdlib edits instead). Identifiers are prefixed
// gk_vterm_r2_ / Test_gk_vterm_r2_ to avoid collisions with sibling units.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// client_registry.go:87:24 CONDITIONALS_BOUNDARY + CONDITIONALS_NEGATION —
// `if s.bytesReceived > 0 {` inside the idle-session GC sweep, which gates the
// "gc'd idle session with received bytes" info log.
//   - BOUNDARY `> 0` -> `>= 0`: a GC'd session with bytesReceived==0 would log.
//   - NEGATION `> 0` -> `<= 0`: a GC'd session with bytesReceived>0 would NOT log.
//
// The stale session's lastSeen is set far in the past as plain data (no clock
// seam); resolving a fresh session id triggers the real GC sweep. Both the
// logged and non-logged cases are asserted, killing both mutants.
func Test_gk_vterm_r2_ResolveSession_GCLogsOnlyWithReceivedBytes(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	const logMsg = "gc'd idle session with received bytes"

	// bytesReceived > 0 → the GC must log.
	r := NewClientRegistry()
	r.sessions["gk_vterm_r2_with_bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 5,
	}
	r.ResolveSession(&ClientState{}, "gk_vterm_r2_new1")
	if !strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived>0 did not emit %q (negation `<= 0` would skip the log)", logMsg)
	}

	// bytesReceived == 0 → the GC must NOT log.
	buf.Reset()
	r2 := NewClientRegistry()
	r2.sessions["gk_vterm_r2_no_bytes"] = &sessionState{
		lastSeen:      time.Now().Add(-61 * time.Minute),
		bytesReceived: 0,
	}
	r2.ResolveSession(&ClientState{}, "gk_vterm_r2_new2")
	if strings.Contains(buf.String(), logMsg) {
		t.Errorf("GC of idle session with bytesReceived==0 emitted %q (boundary `>= 0` would log)", logMsg)
	}
}

// pingstat.go:145:11 INCREMENT_DECREMENT — `p.samples++` in Record. The field
// is documented as the count of successful Record calls; `++` -> `--` makes it
// run negative. (The mutation is behaviourally inert for the timeout math —
// only the `samples == 0` first-sample check is consumed, and the count is
// non-zero after the first Record under either sign — so the documented counter
// value is the discriminating observable.)
func Test_gk_vterm_r2_PingStatRecordCountsSamples(t *testing.T) {
	p := newPingStat()
	if p.samples != 0 {
		t.Fatalf("newPingStat().samples = %d, want 0", p.samples)
	}

	p.Record(10 * time.Millisecond)
	p.Record(20 * time.Millisecond)
	p.Record(30 * time.Millisecond)

	if p.samples != 3 {
		t.Errorf("samples after 3 successful Records = %d, want 3 (samples++ must increment, not decrement)", p.samples)
	}
}
