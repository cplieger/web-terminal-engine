package terminal

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startReplyLoop runs h's replyLoop for the test, as ensureStarted does for a
// started session, and stops it at cleanup.
func startReplyLoop(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { h.replyLoop(ctx) })
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

// TestReplies_fullInputQueueNeverStallsTheReadLoop fills the PTY input pipe, as
// a child busy with a large paste leaves it, and then makes the session owe the
// child a reply. The read loop must keep handing output to the screen: the child
// only reads its input again once its own output drains, so a read loop waiting
// on that reply's write deadlocks the session.
func TestReplies_fullInputQueueNeverStallsTheReadLoop(t *testing.T) {
	tests := []struct {
		name string
		// owe makes the session owe the child a reply; it may block in the old
		// shape, so it runs on its own goroutine.
		owe func(h *Handler, a *clientState)
	}{
		{"a focus report", func(h *Handler, a *clientState) {
			h.registry.SetClientFocus(a, true)
			h.reportFocus()
		}},
		{"a device-query answer", func(h *Handler, _ *clientState) {
			h.handlePTYData([]byte("\x1b[6n"))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, pr := focusPipeHandler(t)
			h.screen.FocusReporting = true
			a := h.registry.Add(&websocket.Conn{})
			fillPipe(t, h.ptmx)

			owed := make(chan struct{})
			go func() {
				defer close(owed)
				tc.owe(h, a)
			}()
			select {
			case <-owed:
			case <-time.After(100 * time.Millisecond):
			}
			read := make(chan struct{})
			go func() {
				defer close(read)
				h.handlePTYData([]byte("still printing"))
			}()
			select {
			case <-read:
			case <-time.After(2 * time.Second):
				t.Errorf("the read loop's next chunk waited on a reply blocked by the full input queue")
			}

			if err := pr.SetReadDeadline(time.Time{}); err != nil {
				t.Fatalf("clear read deadline: %v", err)
			}
			go io.Copy(io.Discard, pr)
			<-owed
			<-read
			h.mu.Lock()
			rows, _ := h.screen.MainScreenText()
			h.mu.Unlock()
			if !strings.Contains(strings.Join(rows, "\n"), "still printing") {
				t.Errorf("screen = %q, want the output the read loop handed on", rows)
			}
		})
	}
}

// TestReplies_queueIsBounded pins that replies owed to a child that never reads
// stop queueing at maxPendingReplies.
func TestReplies_queueIsBounded(t *testing.T) {
	h := NewHandler([]string{"true"}, WithLogger(nil))
	answer := []byte("\x1b[1;1R")
	h.mu.Lock()
	defer h.mu.Unlock()
	for range maxPendingReplies/len(answer) + 10 {
		h.queueReplyLocked(answer)
	}
	if got := len(h.pendingReplies); got > maxPendingReplies {
		t.Errorf("pending replies = %d bytes, want at most %d", got, maxPendingReplies)
	}
	if got := len(h.pendingReplies) % len(answer); got != 0 {
		t.Errorf("pending replies end mid-answer (%d stray bytes), want whole answers only", got)
	}
}
