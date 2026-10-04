package terminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fenceHandler serves a real Handler running cmd and returns it with its /ws
// URL. Server and handler are torn down by t.Cleanup.
func fenceHandler(t *testing.T, cmd []string, opts ...Option) (h *Handler, wsURL string) {
	t.Helper()
	h = NewHandler(cmd, append([]Option{WithWorkDir("/"), WithLogger(nil)}, opts...)...)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		h.Close()
	})
	return h, "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// dialWS opens a client socket against wsURL, closed by t.Cleanup.
func dialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	//nolint:bodyclose // library contract: Body is nil on success
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// awaitResumeAck reads ws until a resumeAck arrives and returns its inputAck.
// The read context ends only after a successful read, so the socket stays
// usable for later reads.
func awaitResumeAck(t *testing.T, ws *websocket.Conn) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitPatience)
	defer cancel()
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for a resumeAck: %v", err)
		}
		if len(msg) >= 9 && msg[0] == wireMsgResumeAck {
			return binary.LittleEndian.Uint64(msg[1:9])
		}
	}
}

// writeInput sends one binary input frame, returning the write error so a
// caller writing on a socket that may already be closed can ignore it.
func writeInput(t *testing.T, ws *websocket.Conn, data string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	return ws.Write(ctx, websocket.MessageBinary, []byte(data))
}

// drainUntilClose reads ws in the background, as a live client does, and
// delivers the close status the server sent (-1 when none arrived within
// waitPatience). Reading is what answers the server's close handshake.
func drainUntilClose(t *testing.T, ws *websocket.Conn) <-chan websocket.StatusCode {
	t.Helper()
	status := make(chan websocket.StatusCode, 1)
	ctx, cancel := context.WithTimeout(context.Background(), waitPatience)
	t.Cleanup(cancel)
	go func() {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				status <- websocket.CloseStatus(err)
				return
			}
		}
	}()
	return status
}

// awaitAttached polls until exactly n sockets are registered, reporting
// whether that happened within waitPatience. A socket leaves the registry only
// after its read loop has returned, so once a departure is observed nothing it
// sent can still reach the PTY.
func awaitAttached(h *Handler, n int) bool {
	deadline := time.Now().Add(waitPatience)
	for {
		h.registry.mu.Lock()
		got := len(h.registry.clients)
		h.registry.mu.Unlock()
		if got == n {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ledgerReceived reads the received count of the ledger keyed id (0 when absent).
func ledgerReceived(h *Handler, id SessionID) uint64 {
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if sess := h.registry.sessions[id]; sess != nil {
		return sess.received.Load()
	}
	return 0
}

// TestResumeOnALiveLedgerFencesThePriorSocket pins the fence end to end: once
// B resumes A's ledger key, A is closed and a frame A sends afterwards never
// reaches the PTY, while B's input does.
func TestResumeOnALiveLedgerFencesThePriorSocket(t *testing.T) {
	h, url := fenceHandler(t, []string{"/bin/cat"})
	a := dialWS(t, url)
	sendControl(t, a, map[string]any{"type": "resize", "cols": 80, "rows": 24})
	bootstrapResume(t, a, "sid#page")
	awaitResumeAck(t, a)

	b := dialWS(t, url)
	bootstrapResume(t, b, "sid#page")
	awaitResumeAck(t, b)

	// Written before A starts reading, so the frame is on the wire ahead of
	// A's reply to the server's close.
	_ = writeInput(t, a, "fenced-x\n")
	closed := drainUntilClose(t, a)
	departed := awaitAttached(h, 1)

	if err := writeInput(t, b, "owner-y\n"); err != nil {
		t.Fatalf("B's input write: %v", err)
	}
	if got := readUntil(t, b, []byte("owner-y"), waitPatience); bytes.Contains(got, []byte("fenced-x")) {
		t.Errorf("A's frame sent after B took the ledger reached the PTY: screen carried %q", "fenced-x")
	}
	if !departed {
		t.Fatalf("A is still attached %v after B resumed its ledger, want it closed", waitPatience)
	}
	if status := <-closed; status != websocket.StatusNormalClosure {
		t.Errorf("A's close status = %v, want %v", status, websocket.StatusNormalClosure)
	}
}

// TestFencedSocketDoesNotAdvanceTheLedger pins the ack half: input the
// superseded socket sends after the transfer is not counted, so the new
// owner's ack stays at what the ledger had applied when it resumed.
func TestFencedSocketDoesNotAdvanceTheLedger(t *testing.T) {
	h, url := fenceHandler(t, []string{"/bin/cat"})
	a := dialWS(t, url)
	sendControl(t, a, map[string]any{"type": "resize", "cols": 80, "rows": 24})
	bootstrapResume(t, a, "sid#page")
	awaitResumeAck(t, a)
	if err := writeInput(t, a, "abc\n"); err != nil {
		t.Fatalf("A's input write: %v", err)
	}
	deadline := time.Now().Add(waitPatience)
	for ledgerReceived(h, "sid#page") != 4 {
		if time.Now().After(deadline) {
			t.Fatalf("ledger received = %d, want 4 after A's 4-byte frame", ledgerReceived(h, "sid#page"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	b := dialWS(t, url)
	bootstrapResume(t, b, "sid#page")
	if ack := awaitResumeAck(t, b); ack != 4 {
		t.Fatalf("B's resumeAck = %d, want 4 (what A had applied)", ack)
	}

	_ = writeInput(t, a, "zz\n")
	drainUntilClose(t, a)
	departed := awaitAttached(h, 1)

	if got := ledgerReceived(h, "sid#page"); got != 4 {
		t.Errorf("ledger received = %d after the fenced socket's 3-byte frame, want 4", got)
	}
	sendControl(t, b, map[string]any{
		"type": "resume", "sessionId": "sid#page", "sentBytes": 4,
		"haveThrough": -1, "protocolVersion": wireProtocolVersion,
	})
	if ack := awaitResumeAck(t, b); ack != 4 {
		t.Errorf("B's next resumeAck = %d with B having sent nothing, want 4", ack)
	}
	if !departed {
		t.Errorf("A is still attached %v after B resumed its ledger, want it closed", waitPatience)
	}
}

// TestLateInputRacingAResumeIsAppliedOnce drives the duplicate the fence
// exists to prevent: A streams numbered lines and keeps going after B resumes
// the same ledger (bytes still in flight on the old path), then B retransmits
// everything from its ack, as the client's outbox does. The child writes what
// it reads to a file, which must hold every line exactly once, in order. Run
// under -race.
func TestLateInputRacingAResumeIsAppliedOnce(t *testing.T) {
	const (
		lineLen = 6 // "%05d\n"
		late    = 300
		key     = SessionID("sid#page")
	)
	frame := func(i int) string { return fmt.Sprintf("%05d\n", i) }

	dir := t.TempDir()
	h, url := fenceHandler(t, []string{"/bin/sh", "-c", "exec cat > out"}, WithWorkDir(dir))
	a := dialWS(t, url)
	sendControl(t, a, map[string]any{"type": "resize", "cols": 80, "rows": 24})
	bootstrapResume(t, a, key)
	awaitResumeAck(t, a)
	b := dialWS(t, url)

	// A's sender stands in for the client's outbox: every line it attempts
	// counts as sent, whether or not the old socket still delivers it.
	resumed := make(chan struct{})
	sent := make(chan int, 1)
	go func() {
		stopAt := -1
		for i := 0; ; i++ {
			if stopAt < 0 {
				select {
				case <-resumed:
					stopAt = i + late
				default:
				}
			}
			if i == stopAt {
				sent <- i
				return
			}
			_ = writeInput(t, a, frame(i))
		}
	}()

	deadline := time.Now().Add(waitPatience)
	for ledgerReceived(h, key) < 100*lineLen {
		if time.Now().After(deadline) {
			t.Fatalf("A's stream stalled at %d bytes before the resume", ledgerReceived(h, key))
		}
		time.Sleep(time.Millisecond)
	}
	sendControl(t, b, map[string]any{
		"type": "resume", "sessionId": key, "sentBytes": ledgerReceived(h, key),
		"haveThrough": -1, "protocolVersion": wireProtocolVersion,
	})
	close(resumed)
	ack := awaitResumeAck(t, b)
	if ack%lineLen != 0 {
		t.Fatalf("B's resumeAck = %d, not on a frame boundary", ack)
	}
	lines := <-sent
	for i := int(ack / lineLen); i < lines; i++ {
		if err := writeInput(t, b, frame(i)); err != nil {
			t.Fatalf("B's retransmit of line %d: %v", i, err)
		}
	}

	var want strings.Builder
	for i := range lines {
		want.WriteString(frame(i))
	}
	total := uint64(lines * lineLen) // #nosec G115 -- a small positive count
	out := filepath.Join(dir, "out")
	deadline = time.Now().Add(waitPatience)
	for {
		info, err := os.Stat(out)
		received := ledgerReceived(h, key)
		if err == nil && received >= total && uint64(info.Size()) == received { // #nosec G115 -- a file size is non-negative
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child file never caught up with the ledger (stat err %v, ledger %d, want %d)", err, received, total)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read child output: %v", err)
	}
	if string(got) != want.String() {
		t.Errorf("child read %d bytes, want %d: each of %d lines exactly once, in order (B resumed at ack %d)",
			len(got), want.Len(), lines, ack)
	}
	if received := ledgerReceived(h, key); received != total {
		t.Errorf("ledger received = %d, want %d", received, total)
	}
}

// TestDistinctLedgersKeepWritingConcurrently guards the other side: two
// senders on one terminal hold two ledgers, and neither fences the other.
// Fencing per terminal rather than per ledger would close A here.
func TestDistinctLedgersKeepWritingConcurrently(t *testing.T) {
	h, url := fenceHandler(t, []string{"/bin/cat"})
	a := dialWS(t, url)
	sendControl(t, a, map[string]any{"type": "resize", "cols": 80, "rows": 24})
	bootstrapResume(t, a, "sid#page-a")
	awaitResumeAck(t, a)
	b := dialWS(t, url)
	bootstrapResume(t, b, "sid#page-b")
	awaitResumeAck(t, b)

	if err := writeInput(t, a, "from-a\n"); err != nil {
		t.Fatalf("A's input write: %v", err)
	}
	if err := writeInput(t, b, "from-b\n"); err != nil {
		t.Fatalf("B's input write: %v", err)
	}
	readUntil(t, b, []byte("from-b"), waitPatience)
	readUntil(t, a, []byte("from-a"), waitPatience)

	if err := writeInput(t, a, "again-a\n"); err != nil {
		t.Fatalf("A's second write: %v", err)
	}
	readUntil(t, b, []byte("again-a"), waitPatience)

	if got := ledgerReceived(h, "sid#page-a"); got != 15 {
		t.Errorf("A's ledger received = %d, want 15", got)
	}
	if got := ledgerReceived(h, "sid#page-b"); got != 7 {
		t.Errorf("B's ledger received = %d, want 7", got)
	}
}

// TestASupersededResumeEndsItsSocket pins the half of the fence the read loop's
// check cannot: a resume frame already past that check when another socket took
// the ledger over. It must hand nothing back, end its socket, and leave the
// newer socket owning the ledger.
func TestASupersededResumeEndsItsSocket(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithWorkDir("/"), WithLogger(nil))
	t.Cleanup(h.Close)
	sws, _, cleanup := wsPair(t)
	t.Cleanup(cleanup)
	bws, _, bCleanup := wsPair(t)
	t.Cleanup(bCleanup)
	a, b := h.registry.Add(sws), h.registry.Add(bws)
	h.registry.ResolveSession(a, "sid#page")
	if _, _, displaced := h.registry.ResolveSession(b, "sid#page"); displaced != a {
		t.Fatalf("B's resume displaced %p, want A (%p)", displaced, a)
	}

	served := false
	resume := []byte(`{"type":"resume","sessionId":"sid#page","sentBytes":0,"protocolVersion":4}`)
	d := h.handleControl(sws, a, resume, func() { served = true })
	if !d.closed {
		t.Errorf("handleControl(A's late resume) = %+v, want closed", d)
	}
	if served {
		t.Errorf("A's late resume was reported served")
	}
	if b.fenced.Load() {
		t.Errorf("A's late resume fenced B, the ledger's owner")
	}
	if applied, err := h.registry.ApplyInput(b, io.Discard, []byte("x")); err != nil || !applied {
		t.Errorf("ApplyInput(B) after A's late resume = (applied %v, err %v), want (true, nil)", applied, err)
	}
}

// TestFencedSocketControlsAreDropped pins the read-loop fence for controls: a
// resize that reaches a socket's read loop after a resume fenced it does not
// change the shared screen, and the loop ends. It drives the loop directly
// because over a real handler the superseded socket's close usually wins the
// race and the frame never surfaces from the websocket at all.
func TestFencedSocketControlsAreDropped(t *testing.T) {
	h := NewHandler([]string{"/bin/cat"}, WithWorkDir("/"), WithLogger(nil))
	t.Cleanup(h.Close)
	owner, _, ownerCleanup := wsPair(t)
	t.Cleanup(ownerCleanup)
	h.handleResize(h.registry.Add(owner), 100, 40)

	sws, cws, cleanup := wsPair(t)
	t.Cleanup(cleanup)
	fenced := h.registry.Add(sws)
	fenced.fenced.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.clientReadLoop(t.Context(), sws, fenced, nil)
	}()

	sendControl(t, cws, map[string]any{"type": "resize", "cols": 60, "rows": 20})
	select {
	case <-done:
	case <-time.After(waitPatience):
		t.Fatalf("a fenced socket's read loop kept running %v after a frame arrived", waitPatience)
	}

	h.mu.Lock()
	cols, rows := h.screen.Width, h.screen.Height
	h.mu.Unlock()
	if cols != 100 || rows != 40 {
		t.Errorf("screen = %dx%d after a fenced socket's 60x20 resize, want 100x40", cols, rows)
	}
}
