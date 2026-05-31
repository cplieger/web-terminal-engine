package terminal

import (
	"sync"
	"time"
)

// Adaptive pong timeout per WebSocket connection — Jacobson/Karels.
//
// Implements RFC 6298 (TCP RTO), the same algorithm TCP has used for
// retransmission timeouts since 1988. We re-use it here for
// WebSocket pong timeouts because the problem is the same: pick a
// timeout that adapts to the connection's actual round-trip time
// without false-disconnecting when latency spikes happen.
//
// Two state variables tracked per connection:
//   - SRTT:   smoothed (EMA-mean) RTT
//   - RTTVAR: smoothed (EMA-mean) absolute deviation |SRTT - R|
//
// Updated on every successful ping/pong:
//
//	RTTVAR ← (1 - β) · RTTVAR + β · |SRTT - R|       β = 1/4
//	SRTT   ← (1 - α) · SRTT   + α · R                α = 1/8
//	RTO    ← SRTT + K · RTTVAR                       K = 4
//
// First sample R seeds: SRTT = R, RTTVAR = R/2 (RFC 6298 §2.2).
//
// On a timeout (Karn/Jacobson rule 5.5), we double the current RTO
// (capped) and do NOT update SRTT/RTTVAR from the failed attempt.
// The elapsed-time-before-failure is not a real RTT measurement
// (the pong might have arrived in 2.001s or 60s — we don't know),
// so feeding it into the model would bias the mean toward the
// arbitrary timeout value. Backoff handles this case correctly:
// the next attempt has more time. When a real successful sample
// finally arrives, the standard update converges back.
//
// After maxConsecutiveFailures backoff steps in a row, the model is
// presumed stale (the connection is genuinely dead) and the loop
// closes the connection.
//
// Why this design:
//   - 37 years of TCP operational experience. Known-good algorithm.
//   - MAD over variance: less sensitive to single outliers (RFC 6298
//     explicitly chose this).
//   - α=1/8 (slow mean) keeps the model stable across normal
//     fluctuation; β=1/4 (faster variance) reacts to spikes.
//   - K=4 gives ~99%+ tolerance for Gaussian-ish RTT distributions.
//   - Backoff-on-timeout, not sample-from-timeout: avoids the trap
//     where repeated near-threshold misses get recorded at the same
//     threshold and the timeout never adapts upward.
//
// Concurrency: Record/Backoff/Timeout are called from the same
// pingLoop goroutine, but the type guards with a mutex anyway so
// future callers (metrics, debug pages) can read state safely.
const (
	// minPongTimeout is the hard floor on RTO. Never wait less than
	// this regardless of the model's output. Protects against
	// pathologically small SRTT/RTTVAR estimates (LAN-fast steady
	// state) and single-sample anomalies. RFC 6298 uses 1s for TCP;
	// we use 3s because our sampling frequency is much lower (1
	// per 2s vs many per RTT) so the model has less data to lean on.
	minPongTimeout = 3 * time.Second

	// maxPongTimeout is the hard cap on RTO, both for steady-state
	// and after backoff doubling. RFC 6298 allows at least 60s; we
	// cap lower because our use case is interactive (a user staring
	// at a terminal) and >15s of unresponsiveness is degraded enough
	// that operators should see warnings.
	maxPongTimeout = 15 * time.Second

	// bootstrapPongTimeout is used until the first successful sample
	// arrives. RFC 6298 §2.1 specifies 1s for TCP; we use 8s because
	// our worst-case deployment (cross-Pacific VPN) has ~1s steady-
	// state RTT, and the first ping might hit a 2-3s jitter spike.
	bootstrapPongTimeout = 8 * time.Second

	// alpha is the EMA smoothing factor for SRTT. RFC 6298 §2.3
	// recommends 1/8: each new sample shifts the smoothed mean by
	// 12.5%, giving an effective half-life of ~5.5 samples. At our
	// 2s ping interval, that's ~11 seconds of effective memory.
	alphaShift = 3 // 1/8 = 1 / (1 << 3)

	// beta is the EMA smoothing factor for RTTVAR. RFC 6298 §2.3
	// recommends 1/4: variance reacts twice as fast as mean, so a
	// jitter spike widens the bound before the mean catches up.
	betaShift = 2 // 1/4 = 1 / (1 << 2)

	// k is the multiplier on RTTVAR added to SRTT to compute RTO.
	// RFC 6298 §2.3 specifies 4. With Gaussian-distributed RTT, K=4
	// covers >99.99% of the distribution; with the heavier-tailed
	// real-world distribution it covers somewhat less.
	k = 4

	// maxConsecutiveFailures is the number of in-a-row backoffs
	// before the loop declares the connection dead. Three steps of
	// doubling (capped at maxPongTimeout each) means the loop
	// tolerates ~maxConsecutiveFailures × maxPongTimeout of
	// unresponsiveness ≈ 45s before close.
	maxConsecutiveFailures = 3
)

// pingStat tracks RTO state for one WebSocket connection per
// RFC 6298 (Jacobson/Karels). Zero value is not usable; construct
// with newPingStat.
type pingStat struct {
	mu sync.Mutex
	// srtt and rttvar are stored as time.Duration. Both are zero
	// before the first sample (which case is detected via samples).
	srtt    time.Duration
	rttvar  time.Duration
	rto     time.Duration // current adaptive timeout, derived from srtt + k*rttvar (clamped)
	samples int           // count of successful Record calls
}

// newPingStat returns a fresh stat tracker. Safe for concurrent use.
// The initial Timeout() returns bootstrapPongTimeout until the first
// Record() lands.
func newPingStat() *pingStat {
	return &pingStat{rto: bootstrapPongTimeout}
}

// Record updates SRTT and RTTVAR with one successful RTT measurement
// per RFC 6298 §2.2 (first sample) or §2.3 (subsequent). Negative
// durations are ignored (clock-skew protection).
func (p *pingStat) Record(rtt time.Duration) {
	if rtt < 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.samples == 0 {
		// First measurement: SRTT = R, RTTVAR = R/2.
		p.srtt = rtt
		p.rttvar = rtt / 2
	} else {
		// RTTVAR ← (1 - β) · RTTVAR + β · |SRTT - R|
		// SRTT   ← (1 - α) · SRTT   + α · R
		dev := p.srtt - rtt
		if dev < 0 {
			dev = -dev
		}
		p.rttvar = p.rttvar - p.rttvar>>betaShift + dev>>betaShift
		p.srtt = p.srtt - p.srtt>>alphaShift + rtt>>alphaShift
	}
	p.samples++
	p.rto = clampRTO(p.srtt + time.Duration(k)*p.rttvar)
}

// Backoff doubles the current RTO (capped at maxPongTimeout) per
// RFC 6298 §5.5. Called when a ping times out; SRTT and RTTVAR are
// intentionally NOT updated because the elapsed time before failure
// isn't a real RTT measurement. Returns the new RTO so the caller
// can log it.
func (p *pingStat) Backoff() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rto = clampRTO(p.rto * 2)
	return p.rto
}

// Timeout returns the current pong timeout and a flag indicating
// whether it sits at the cap (either from steady-state computation
// or accumulated backoff). The capped flag drives operator-visible
// warnings.
func (p *pingStat) Timeout() (timeout time.Duration, capped bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rto, p.rto >= maxPongTimeout
}

// Stats returns the current SRTT and RTTVAR for diagnostic logging.
// Returns (0, 0) before any samples are recorded.
func (p *pingStat) Stats() (srtt, rttvar time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.srtt, p.rttvar
}

// clampRTO applies the floor and cap.
func clampRTO(rto time.Duration) time.Duration {
	if rto < minPongTimeout {
		return minPongTimeout
	}
	if rto > maxPongTimeout {
		return maxPongTimeout
	}
	return rto
}
