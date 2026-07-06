package terminal

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

// wsPingInterval bounds how fast a dead client is noticed.
const wsPingInterval = 2 * time.Second

// pingLoop periodically pings the WS to detect dead clients. The pong
// RTT is fed into a Jacobson/Karels RTO tracker (see pingstat.go) so
// the timeout adapts to the connection's actual round-trip time. Calls
// cancel() when the model decides the connection is genuinely dead.
//
// Adaptive timeouts are essential on flaky high-latency links (e.g. a
// VPN-relayed mobile connection) where a fixed deadline either
// false-disconnects on transient spikes or waits too long when the
// peer truly drops.
func pingLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, logger *slog.Logger) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	p := &pinger{ws: ws, stat: newPingStat(), logger: logger}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p.tick(ctx, cancel) {
				return
			}
		}
	}
}

// pinger carries the per-connection accounting the ping select-loop
// mutates across ticks (the adaptive RTO model and the consecutive-
// failure counter). Extracting it lets each tick's work live in a
// method, keeping pingLoop's select body small.
type pinger struct {
	ws          *websocket.Conn
	stat        *pingStat
	logger      *slog.Logger
	consecFails int
}

// tick performs one ping/pong attempt: it sends a ping with the model's
// current adaptive timeout and feeds the result back into the model.
// Returns true when the loop should stop (the connection was declared
// dead — cancel() already called — or the context was canceled during
// the ping).
func (p *pinger) tick(ctx context.Context, cancel context.CancelFunc) (stop bool) {
	timeout, capped := p.stat.Timeout()
	if capped {
		srtt, rttvar := p.stat.Stats()
		p.logger.Warn("terminal: ws ping timeout at cap",
			"timeout", timeout, "cap", maxPongTimeout,
			"srtt", srtt, "rttvar", rttvar,
			"consec_fails", p.consecFails)
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
	start := time.Now()
	err := p.ws.Ping(pingCtx)
	pingCancel()
	rtt := time.Since(start)
	if err != nil {
		return p.handlePingFailure(err, timeout, rtt, cancel)
	}
	p.consecFails = 0
	p.stat.Record(rtt)
	return false
}

// handlePingFailure applies the Karn/Jacobson backoff rule to a failed
// ping and decides whether the connection is dead. A context-canceled
// error means the loop is shutting down (stop without backoff). After
// maxConsecutiveFailures backoffs in a row it calls cancel() and stops;
// otherwise it backs off and continues.
func (p *pinger) handlePingFailure(err error, timeout, rtt time.Duration, cancel context.CancelFunc) (stop bool) {
	if errors.Is(err, context.Canceled) {
		return true
	}
	newRTO := p.stat.Backoff()
	p.consecFails++
	if p.consecFails >= maxConsecutiveFailures {
		srtt, rttvar := p.stat.Stats()
		p.logger.Warn("terminal: ws ping failed; closing connection",
			"error", err,
			"timeout", timeout,
			"observed_rtt", rtt,
			"consec_fails", p.consecFails,
			"srtt", srtt, "rttvar", rttvar)
		cancel()
		return true
	}
	p.logger.Warn("terminal: ws ping miss; backoff",
		"error", err,
		"timeout", timeout,
		"observed_rtt", rtt,
		"new_rto", newRTO,
		"consec_fails", p.consecFails,
		"max_fails", maxConsecutiveFailures)
	return false
}
