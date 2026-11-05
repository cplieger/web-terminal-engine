package terminal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"
)

// TestPingLoop_repeatedFailuresCancel verifies pingLoop calls cancel after
// maxConsecutiveFailures failed pings, at exactly the third tick. It runs in a
// synctest bubble on httptest.NewTestServer's in-memory network: a real socket
// parks a goroutine on an external FD and the clock never advances.
func TestPingLoop_repeatedFailuresCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			// Keep the server side reading so the handshake completes cleanly and
			// the handler returns once the client connection drops.
			for {
				if _, _, rerr := ws.Read(r.Context()); rerr != nil {
					return
				}
			}
		}))

		// The in-memory path leaves Server.URL empty and srv.Client() routes
		// every request to the handler whatever the host, so the authority here
		// is a placeholder.
		//nolint:bodyclose // coder/websocket Dial nils resp.Body on success
		ws, _, err := websocket.Dial(t.Context(), "ws://fake.invalid/", &websocket.DialOptions{
			HTTPClient: srv.Client(),
		})
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}

		// Kill the client connection so each ws.Ping fails immediately rather
		// than blocking until the pong timeout.
		_ = ws.CloseNow()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		canceled := make(chan struct{})
		var once sync.Once
		recordCancel := func() {
			once.Do(func() { close(canceled) })
			cancel()
		}

		go pingLoop(ctx, recordCancel, ws, slog.Default())

		// maxConsecutiveFailures ticks at wsPingInterval, plus a hair so the
		// last tick is inside the window; Sleep also waits for the loop to
		// settle, so the check below needs no second synchronisation.
		synctest.Sleep(maxConsecutiveFailures*wsPingInterval + time.Millisecond)
		select {
		case <-canceled:
			// pingLoop observed repeated ping failures and closed the connection.
		default:
			t.Fatalf("pingLoop did not cancel within %d failed pings", maxConsecutiveFailures)
		}
	})
}

// TestPinger_continuesBackoffBelowFailureThreshold verifies a single ping
// failure (below maxConsecutiveFailures) backs off and keeps the connection:
// handlePingFailure returns stop=false and does NOT call cancel.
func TestPinger_continuesBackoffBelowFailureThreshold(t *testing.T) {
	p := &pinger{stat: newPingStat(), logger: slog.Default()} // consecFails starts at 0
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stop := p.handlePingFailure(errors.New("ping miss"), time.Second, time.Second, cancel)

	if stop {
		t.Errorf("handlePingFailure(1st failure): stop=true; want false (1 < %d, keep backing off)", maxConsecutiveFailures)
	}
	if p.consecFails != 1 {
		t.Errorf("handlePingFailure(1st failure): consecFails=%d, want 1", p.consecFails)
	}
	select {
	case <-ctx.Done():
		t.Errorf("handlePingFailure(1st failure): cancel was called; a single miss must not close the connection")
	default:
	}
}

// TestPinger_cancelsConnectionAtFailureThreshold verifies the connection is
// declared dead exactly when consecutive failures reach maxConsecutiveFailures:
// handlePingFailure returns stop=true and calls cancel. Pins the >= boundary so
// flipping it to > (one failure too lenient) or to < (cancels immediately) is
// caught.
func TestPinger_cancelsConnectionAtFailureThreshold(t *testing.T) {
	p := &pinger{stat: newPingStat(), logger: slog.Default(), consecFails: maxConsecutiveFailures - 1}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stop := p.handlePingFailure(errors.New("ping miss"), time.Second, time.Second, cancel)

	if !stop {
		t.Errorf("handlePingFailure(at threshold): stop=false; want true (consecFails reached %d)", maxConsecutiveFailures)
	}
	if p.consecFails != maxConsecutiveFailures {
		t.Errorf("handlePingFailure(at threshold): consecFails=%d, want %d", p.consecFails, maxConsecutiveFailures)
	}
	select {
	case <-ctx.Done():
		// cancel was invoked: the connection is closed as intended.
	default:
		t.Errorf("handlePingFailure(at threshold): cancel not called; the dead connection must be closed")
	}
}
