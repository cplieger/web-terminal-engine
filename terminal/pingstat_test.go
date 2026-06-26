package terminal

import (
	"sync"
	"testing"
	"time"
)

func TestPingStat_initial_returns_bootstrap(t *testing.T) {
	p := newPingStat()
	got, capped := p.Timeout()
	if got != bootstrapPongTimeout {
		t.Errorf("zero samples: timeout = %v, want %v", got, bootstrapPongTimeout)
	}
	if capped {
		t.Errorf("zero samples: capped = true, want false")
	}
}

func TestPingStat_first_sample_seeds_per_rfc6298(t *testing.T) {
	p := newPingStat()

	// First sample 800ms → SRTT = 800ms, RTTVAR = 400ms,
	// RTO = 800ms + 4*400ms = 2400ms (above 3s floor? no, 2.4s < 3s
	// so floor wins).
	p.Record(800 * time.Millisecond)
	srtt, rttvar := p.Stats()
	if srtt != 800*time.Millisecond {
		t.Errorf("first SRTT: got %v, want 800ms", srtt)
	}
	if rttvar != 400*time.Millisecond {
		t.Errorf("first RTTVAR: got %v, want 400ms", rttvar)
	}
	got, _ := p.Timeout()
	if got != minPongTimeout {
		t.Errorf("first timeout (formula 2.4s < floor 3s): got %v, want floor %v", got, minPongTimeout)
	}
}

func TestPingStat_first_sample_high_rtt_clears_floor(t *testing.T) {
	p := newPingStat()

	// First sample 1.5s → SRTT=1.5s, RTTVAR=750ms, RTO=1.5+3=4.5s.
	p.Record(1500 * time.Millisecond)
	got, capped := p.Timeout()
	if got != 4500*time.Millisecond {
		t.Errorf("first timeout: got %v, want 4500ms", got)
	}
	if capped {
		t.Errorf("4.5s under 15s cap; capped should be false")
	}
}

func TestPingStat_first_sample_extreme_rtt_clamps_at_cap(t *testing.T) {
	p := newPingStat()

	// First sample 5s → SRTT=5s, RTTVAR=2.5s, RTO=5+10=15s. Hits cap.
	p.Record(5 * time.Second)
	got, capped := p.Timeout()
	if got != maxPongTimeout {
		t.Errorf("extreme first timeout: got %v, want maxPongTimeout %v", got, maxPongTimeout)
	}
	if !capped {
		t.Errorf("at cap; capped should be true")
	}
}

func TestPingStat_subsequent_sample_jacobson_update(t *testing.T) {
	p := newPingStat()

	// First sample 800ms → SRTT=800, RTTVAR=400.
	p.Record(800 * time.Millisecond)

	// Second sample 1000ms.
	// dev = |800 - 1000| = 200ms
	// RTTVAR = (1 - 1/4)*400 + (1/4)*200 = 300 + 50 = 350ms
	// SRTT   = (1 - 1/8)*800 + (1/8)*1000 = 700 + 125 = 825ms
	// RTO = 825 + 4*350 = 2225ms → clamped to floor 3s.
	p.Record(1000 * time.Millisecond)
	srtt, rttvar := p.Stats()

	// Allow ±1ms tolerance for integer arithmetic.
	wantSRTT := 825 * time.Millisecond
	wantRTTVAR := 350 * time.Millisecond
	if absDiff(srtt, wantSRTT) > time.Millisecond {
		t.Errorf("second SRTT: got %v, want %v", srtt, wantSRTT)
	}
	if absDiff(rttvar, wantRTTVAR) > time.Millisecond {
		t.Errorf("second RTTVAR: got %v, want %v", rttvar, wantRTTVAR)
	}
}

func TestPingStat_backoff_doubles_rto(t *testing.T) {
	p := newPingStat()

	// Bootstrap RTO = 8s.
	got, _ := p.Timeout()
	if got != bootstrapPongTimeout {
		t.Fatalf("pre-backoff: got %v, want %v", got, bootstrapPongTimeout)
	}

	// Backoff: 8s * 2 = 16s, clamped to 15s.
	newRTO := p.Backoff()
	if newRTO != maxPongTimeout {
		t.Errorf("backoff from 8s: got %v, want %v (clamped)", newRTO, maxPongTimeout)
	}

	// Already at cap; subsequent backoff stays at cap.
	newRTO = p.Backoff()
	if newRTO != maxPongTimeout {
		t.Errorf("backoff at cap: got %v, want %v", newRTO, maxPongTimeout)
	}

	got, capped := p.Timeout()
	if got != maxPongTimeout || !capped {
		t.Errorf("post-backoff timeout: got (%v, %v), want (%v, true)",
			got, capped, maxPongTimeout)
	}
}

func TestPingStat_backoff_does_not_update_srtt_or_rttvar(t *testing.T) {
	p := newPingStat()
	p.Record(800 * time.Millisecond)
	srttBefore, rttvarBefore := p.Stats()

	p.Backoff()
	srttAfter, rttvarAfter := p.Stats()

	if srttBefore != srttAfter {
		t.Errorf("backoff modified SRTT: before=%v after=%v", srttBefore, srttAfter)
	}
	if rttvarBefore != rttvarAfter {
		t.Errorf("backoff modified RTTVAR: before=%v after=%v", rttvarBefore, rttvarAfter)
	}
}

func TestPingStat_record_after_backoff_recomputes_rto(t *testing.T) {
	p := newPingStat()
	p.Record(800 * time.Millisecond)
	p.Backoff() // RTO doubles to maxPongTimeout

	// A successful sample should snap RTO back to the formula value.
	p.Record(900 * time.Millisecond)
	got, capped := p.Timeout()

	// After the second sample (third Record total), the formula
	// produces something well below the cap, so capped should be
	// false and the timeout should be the recomputed clamped value.
	if capped {
		t.Errorf("after recovery sample, capped = true (should be false)")
	}
	if got >= maxPongTimeout {
		t.Errorf("recovery sample didn't reduce RTO from cap: got %v", got)
	}
}

func TestPingStat_negative_rtt_ignored(t *testing.T) {
	p := newPingStat()
	p.Record(-5 * time.Millisecond)
	if got, _ := p.Timeout(); got != bootstrapPongTimeout {
		t.Errorf("negative RTT shouldn't be recorded: got %v, want bootstrap", got)
	}
}

func TestPingStat_concurrent_no_race(t *testing.T) {
	p := newPingStat()
	var wg sync.WaitGroup

	for range 10 {
		wg.Go(func() {
			for i := range 1000 {
				p.Record(time.Duration(i) * time.Millisecond)
				_, _ = p.Timeout()
				if i%50 == 0 {
					p.Backoff()
				}
				_, _ = p.Stats()
			}
		})
	}
	wg.Wait()

	got, _ := p.Timeout()
	if got < minPongTimeout || got > maxPongTimeout {
		t.Errorf("post-storm timeout out of bounds: got %v", got)
	}
}

func TestPingStat_record_counts_samples(t *testing.T) {
	p := newPingStat()
	if p.samples != 0 {
		t.Fatalf("newPingStat().samples = %d, want 0", p.samples)
	}

	p.Record(10 * time.Millisecond)
	p.Record(20 * time.Millisecond)
	p.Record(30 * time.Millisecond)

	if p.samples != 3 {
		t.Errorf("samples after 3 successful Records = %d, want 3", p.samples)
	}
}

func TestPingStat_record_zero_rtt_is_valid_sample(t *testing.T) {
	// rtt == 0 is a valid sample (only rtt < 0 is ignored): srtt=rttvar=0
	// makes rto clamp up to the floor, replacing the bootstrap timeout.
	p := newPingStat()
	p.Record(0)

	got, _ := p.Timeout()
	if got != minPongTimeout {
		t.Errorf("Timeout after Record(0) = %v, want %v", got, minPongTimeout)
	}
}

func absDiff(a, b time.Duration) time.Duration {
	if a > b {
		return a - b
	}
	return b - a
}
