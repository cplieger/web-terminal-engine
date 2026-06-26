package terminal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestPingLoop_repeatedFailuresCancel verifies pingLoop closes the connection
// (calls cancel) after maxConsecutiveFailures pings fail in a row. We dial a
// real WebSocket then CloseNow the client so every subsequent ws.Ping fails
// immediately; after the failure threshold pingLoop must invoke cancel.
func TestPingLoop_repeatedFailuresCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	//nolint:bodyclose // coder/websocket Dial nils resp.Body on success
	ws, _, err := websocket.Dial(dctx, wsURL, nil)
	dcancel()
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}

	// Kill the client connection so each ws.Ping fails immediately rather than
	// blocking until the pong timeout.
	_ = ws.CloseNow()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan struct{})
	var once sync.Once
	recordCancel := func() {
		once.Do(func() { close(canceled) })
		cancel()
	}

	go pingLoop(ctx, recordCancel, ws)

	// maxConsecutiveFailures (3) ticks at wsPingInterval (2s) is roughly 6s;
	// generous bound for slow CI.
	select {
	case <-canceled:
		// pingLoop observed repeated ping failures and closed the connection.
	case <-time.After(25 * time.Second):
		t.Fatal("pingLoop did not cancel after repeated failed pings")
	}
}
