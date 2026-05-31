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
func pingLoop(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn) {
	stat := newPingStat()
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	consecFails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			timeout, capped := stat.Timeout()
			if capped {
				srtt, rttvar := stat.Stats()
				slog.Warn("terminal: ws ping timeout at cap",
					"timeout", timeout, "cap", maxPongTimeout,
					"srtt", srtt, "rttvar", rttvar,
					"consec_fails", consecFails)
			}
			pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
			start := time.Now()
			err := ws.Ping(pingCtx)
			pingCancel()
			rtt := time.Since(start)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				newRTO := stat.Backoff()
				consecFails++
				if consecFails >= maxConsecutiveFailures {
					srtt, rttvar := stat.Stats()
					slog.Error("terminal: ws ping failed; closing connection",
						"error", err,
						"timeout", timeout,
						"observed_rtt", rtt,
						"consec_fails", consecFails,
						"srtt", srtt, "rttvar", rttvar)
					cancel()
					return
				}
				slog.Warn("terminal: ws ping miss; backoff",
					"error", err,
					"timeout", timeout,
					"observed_rtt", rtt,
					"new_rto", newRTO,
					"consec_fails", consecFails,
					"max_fails", maxConsecutiveFailures)
				continue
			}
			consecFails = 0
			stat.Record(rtt)
		}
	}
}
