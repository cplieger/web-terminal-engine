package vt

import (
	"testing"
	"time"
)

// TestFlushHoldAPI verifies the exported flush-hold gate: HoldFlush arms it,
// ReleaseFlush clears it, and IsFlushHeld reports the current state.
func TestFlushHoldAPI(t *testing.T) {
	s := New(5, 10)
	if s.IsFlushHeld() {
		t.Fatal("IsFlushHeld() = true on a fresh screen, want false (zero hold time)")
	}
	s.HoldFlush(time.Now().Add(time.Hour))
	if !s.IsFlushHeld() {
		t.Error("IsFlushHeld() = false after HoldFlush(now+1h), want true")
	}
	s.ReleaseFlush()
	if s.IsFlushHeld() {
		t.Error("IsFlushHeld() = true after ReleaseFlush(), want false")
	}
	if !s.FlushHoldUntil.IsZero() {
		t.Errorf("FlushHoldUntil = %v after ReleaseFlush(), want zero", s.FlushHoldUntil)
	}
}

// TestFlushHoldNeverShortens verifies HoldFlush extends the hold deadline but
// never pulls it earlier: a later call with an earlier deadline is a no-op.
func TestFlushHoldNeverShortens(t *testing.T) {
	s := New(5, 10)
	far := time.Now().Add(time.Hour)
	s.HoldFlush(far)
	s.HoldFlush(far.Add(-30 * time.Minute)) // earlier deadline must not shorten the hold
	if !s.FlushHoldUntil.Equal(far) {
		t.Errorf("FlushHoldUntil = %v after a shorter HoldFlush, want unchanged %v", s.FlushHoldUntil, far)
	}
}

// TestSynchronizedOutputMode2026 verifies DEC private mode 2026 (synchronized
// output) arms the flush hold on set (CSI ?2026h) and clears it on reset (CSI ?2026l).
func TestSynchronizedOutputMode2026(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("\x1b[?2026h"))
	if !s.IsFlushHeld() {
		t.Error("after CSI ?2026h, IsFlushHeld() = false, want true")
	}
	s.Write([]byte("\x1b[?2026l"))
	if s.IsFlushHeld() {
		t.Error("after CSI ?2026l, IsFlushHeld() = true, want false")
	}
}

// TestDECRQM_SynchronizedOutput verifies DECRQM reports mode 2026 status: set
// (1) while the hold is armed, reset (2) once released.
func TestDECRQM_SynchronizedOutput(t *testing.T) {
	s := New(5, 10)
	s.HoldFlush(time.Now().Add(time.Hour))
	s.Write([]byte("\x1b[?2026$p"))
	if got, want := string(s.response), "\x1b[?2026;1$y"; got != want {
		t.Errorf("DECRQM ?2026 while held = %q, want %q", got, want)
	}
	s.response = nil
	s.ReleaseFlush()
	s.Write([]byte("\x1b[?2026$p"))
	if got, want := string(s.response), "\x1b[?2026;2$y"; got != want {
		t.Errorf("DECRQM ?2026 after release = %q, want %q", got, want)
	}
}
