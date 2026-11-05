package terminal

import (
	"sync"
	"time"
)

// The pong timeout is RFC 6298's TCP retransmission timeout per connection:
// SRTT and RTTVAR are EMAs (α = 1/8, β = 1/4), RTO = SRTT + 4·RTTVAR, and the
// first sample seeds SRTT = R, RTTVAR = R/2. A timeout doubles the RTO (capped)
// without sampling (RFC 6298 §5.5): the elapsed time is not an RTT, and feeding
// it in would pin the timeout at its own threshold. maxConsecutiveFailures
// backoffs in a row close the connection.
const (
	// minPongTimeout floors the RTO above RFC 6298's 1 s: one sample per 2 s
	// gives the model far less data than TCP's per-RTT sampling.
	minPongTimeout = 3 * time.Second

	// maxPongTimeout caps the RTO, backoff included, well under RFC 6298's
	// 60 s: past 15 s an interactive terminal is degraded enough to warn.
	maxPongTimeout = 15 * time.Second

	// bootstrapPongTimeout applies until the first sample, above RFC 6298's
	// 1 s: a ~1 s-RTT VPN link's first ping can hit a 2-3 s jitter spike.
	bootstrapPongTimeout = 8 * time.Second

	// alphaShift is SRTT's EMA factor, 1/8 per RFC 6298 §2.3.
	alphaShift = 3 // 1/8 = 1 / (1 << 3)

	// beta is the EMA smoothing factor for RTTVAR. RFC 6298 §2.3
	// recommends 1/4: variance reacts twice as fast as mean, so a
	// jitter spike widens the bound before the mean catches up.
	betaShift = 2 // 1/4 = 1 / (1 << 2)

	// k is RTTVAR's multiplier in the RTO, 4 per RFC 6298 §2.3.
	k = 4

	// maxConsecutiveFailures backoffs in a row declare the connection dead,
	// after at most about 45 s of silence.
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
		dev := (p.srtt - rtt).Abs()
		p.rttvar = p.rttvar - p.rttvar>>betaShift + dev>>betaShift
		p.srtt = p.srtt - p.srtt>>alphaShift + rtt>>alphaShift
	}
	p.samples++
	p.rto = clampRTO(p.srtt + time.Duration(k)*p.rttvar)
}

// Backoff doubles the RTO, capped at maxPongTimeout, after a ping timed out,
// leaving SRTT and RTTVAR alone (RFC 6298 §5.5). It returns the new RTO.
func (p *pingStat) Backoff() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rto = clampRTO(p.rto * 2)
	return p.rto
}

// Timeout returns the current pong timeout and whether it sits at
// maxPongTimeout, from the model or from backoff.
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
	return min(max(rto, minPongTimeout), maxPongTimeout)
}
