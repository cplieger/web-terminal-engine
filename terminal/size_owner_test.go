package terminal

import (
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// sizedSender registers one socket with a recorded size and, when key is not
// empty, links it to that sender's ledger.
func sizedSender(r *clientRegistry, key SessionID, cols, rows int) (*websocket.Conn, *clientState) {
	ws := new(websocket.Conn)
	st := r.Add(ws)
	r.RecordSize(st, cols, rows)
	if key != "" {
		r.ResolveSession(st, key)
	}
	return ws, st
}

func wantOwnerSize(t *testing.T, r *clientRegistry, cols, rows int) {
	t.Helper()
	gotCols, gotRows, ok := r.OwnerSize()
	if !ok || gotCols != cols || gotRows != rows {
		t.Errorf("OwnerSize() = (%d, %d, %v), want (%d, %d, true)", gotCols, gotRows, ok, cols, rows)
	}
}

func TestOwnerSize(t *testing.T) {
	t.Run("before anyone types, the earliest-attached sender owns it", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		sizedSender(r, "desk", 200, 50)
		sizedSender(r, "phone", 80, 24)
		wantOwnerSize(t, r, 200, 50)
	})
	t.Run("the sender that typed last owns it", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		_, desk := sizedSender(r, "desk", 200, 50)
		_, phone := sizedSender(r, "phone", 80, 24)
		r.Claim(phone)
		wantOwnerSize(t, r, 80, 24)
		r.Claim(desk)
		wantOwnerSize(t, r, 200, 50)
	})
	t.Run("a repeated claim by the owner changes nothing", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		_, phone := sizedSender(r, "phone", 80, 24)
		if !r.Claim(phone) {
			t.Error("first Claim = false, want true")
		}
		if r.Claim(phone) {
			t.Error("second Claim by the same sender = true, want false")
		}
	})
	t.Run("a socket with no ledger cannot claim", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		sizedSender(r, "desk", 200, 50)
		_, bare := sizedSender(r, "", 80, 24)
		if r.Claim(bare) {
			t.Error("Claim(no ledger) = true, want false")
		}
		wantOwnerSize(t, r, 200, 50)
	})
	t.Run("a sizeless or fenced socket is no candidate", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		r.Add(new(websocket.Conn))
		r.clients[new(websocket.Conn)] = &clientState{cols: 80}
		_, fenced := sizedSender(r, "", 60, 20)
		fenced.fenced.Store(true)
		if _, _, ok := r.OwnerSize(); ok {
			t.Error("OwnerSize() ok = true with no sized, unfenced socket, want false")
		}
	})
	t.Run("a non-owner coming and going leaves the size alone", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		sizedSender(r, "desk", 200, 50)
		sizedSender(r, "phone", 80, 24)
		tablet, _ := sizedSender(r, "tablet", 120, 40)
		if hold := r.Remove(tablet); hold != 0 {
			t.Errorf("Remove(non-owner) hold = %d, want 0", hold)
		}
		wantOwnerSize(t, r, 200, 50)
	})
	t.Run("a departed owner holds the size until EndHold", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		_, desk := sizedSender(r, "desk", 200, 50)
		phoneWS, phone := sizedSender(r, "phone", 80, 24)
		r.Claim(phone)
		hold := r.Remove(phoneWS)
		if hold == 0 {
			t.Fatal("Remove(owner's last socket) hold = 0, want a hold")
		}
		wantOwnerSize(t, r, 80, 24)
		r.RecordSize(desk, 180, 45)
		wantOwnerSize(t, r, 80, 24)
		r.EndHold(hold)
		wantOwnerSize(t, r, 180, 45)
	})
	t.Run("the held sender's next socket ends the hold at its new size", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		sizedSender(r, "desk", 200, 50)
		phoneWS, phone := sizedSender(r, "phone", 80, 24)
		r.Claim(phone)
		hold := r.Remove(phoneWS)
		sizedSender(r, "phone", 24, 80)
		wantOwnerSize(t, r, 24, 80)
		r.EndHold(hold)
		wantOwnerSize(t, r, 24, 80)
	})
	t.Run("another sender typing during the hold takes the size", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		_, desk := sizedSender(r, "desk", 200, 50)
		phoneWS, phone := sizedSender(r, "phone", 80, 24)
		r.Claim(phone)
		r.Remove(phoneWS)
		r.Claim(desk)
		wantOwnerSize(t, r, 200, 50)
	})
	t.Run("the owner's second socket keeps the size with no hold", func(t *testing.T) {
		r := newClientRegistry(slog.Default())
		sizedSender(r, "desk", 200, 50)
		first, phone := sizedSender(r, "phone", 80, 24)
		r.Claim(phone)
		sizedSender(r, "phone", 70, 24)
		wantOwnerSize(t, r, 70, 24)
		if hold := r.Remove(first); hold != 0 {
			t.Errorf("Remove(an older socket of the owner) hold = %d, want 0", hold)
		}
		wantOwnerSize(t, r, 70, 24)
	})
}

// sizeClient is one end-to-end socket of a sender.
type sizeClient struct {
	ws  *websocket.Conn
	key SessionID
}

// attachSized dials a socket, sends its size and, when key is set, resumes
// that sender's ledger, then drains the socket's frames.
func attachSized(t *testing.T, h *Handler, url string, key SessionID, cols, rows int) sizeClient {
	t.Helper()
	ws := dialWS(t, url)
	sendControl(t, ws, map[string]any{"type": "resize", "cols": cols, "rows": rows})
	if key != "" {
		bootstrapResume(t, ws, key)
		awaitResumeAck(t, ws)
	}
	awaitRecordedSize(t, h, cols, rows)
	drainUntilClose(t, ws)
	return sizeClient{ws: ws, key: key}
}

// typeInto sends one byte of input and waits until the PTY took it.
func typeInto(t *testing.T, h *Handler, c sizeClient) {
	t.Helper()
	before := ledgerReceived(h, c.key)
	if err := writeInput(t, c.ws, "x"); err != nil {
		t.Fatalf("input write: %v", err)
	}
	deadline := time.Now().Add(waitPatience)
	for ledgerReceived(h, c.key) == before {
		if time.Now().After(deadline) {
			t.Fatalf("input on %s never reached the ledger", c.key)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitRecordedSize(t *testing.T, h *Handler, cols, rows int) {
	t.Helper()
	deadline := time.Now().Add(waitPatience)
	for {
		h.registry.mu.Lock()
		found := false
		for _, st := range h.registry.clients {
			found = found || (st.cols == cols && st.rows == rows)
		}
		h.registry.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no socket recorded %dx%d", cols, rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func screenSize(h *Handler) (cols, rows int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.Width, h.screen.Height
}

func closeAndAwait(t *testing.T, h *Handler, c sizeClient, remaining int) {
	t.Helper()
	if err := c.ws.Close(websocket.StatusNormalClosure, ""); err != nil && websocket.CloseStatus(err) == -1 {
		t.Fatalf("close: %v", err)
	}
	if !awaitAttached(h, remaining) {
		t.Fatalf("the server did not drop the closed socket")
	}
}

func wantScreen(t *testing.T, h *Handler, cols, rows int) {
	t.Helper()
	if gotCols, gotRows := screenSize(h); gotCols != cols || gotRows != rows {
		t.Errorf("screen = %dx%d, want %dx%d", gotCols, gotRows, cols, rows)
	}
}

// TestSharedSize_followsClientsNotHistory drives the shared PTY size through
// the wire: the same attached senders in the same input order give one size,
// whichever socket sent a resize last or came and went.
func TestSharedSize_followsClientsNotHistory(t *testing.T) {
	t.Run("a third device coming and going restores nothing it never owned", func(t *testing.T) {
		h, url := fenceHandler(t, []string{"/bin/cat"})
		attachSized(t, h, url, "desk", 200, 50)
		attachSized(t, h, url, "phone", 80, 24)
		wantScreen(t, h, 200, 50)
		tablet := attachSized(t, h, url, "tablet", 120, 40)
		wantScreen(t, h, 200, 50)
		closeAndAwait(t, h, tablet, 2)
		time.Sleep(sizeHoldGrace + 500*time.Millisecond)
		wantScreen(t, h, 200, 50)
	})
	t.Run("a reconnect's resize does not take the size from the device in use", func(t *testing.T) {
		h, url := fenceHandler(t, []string{"/bin/cat"})
		desk := attachSized(t, h, url, "desk", 120, 50)
		phone := attachSized(t, h, url, "phone", 40, 20)
		typeInto(t, h, desk)
		typeInto(t, h, phone)
		wantScreen(t, h, 40, 20)
		closeAndAwait(t, h, desk, 1)
		attachSized(t, h, url, "desk", 120, 50)
		wantScreen(t, h, 40, 20)
	})
	t.Run("after the owner leaves, the device typed on next sets the size", func(t *testing.T) {
		h, url := fenceHandler(t, []string{"/bin/cat"})
		desk := attachSized(t, h, url, "desk", 100, 40)
		attachSized(t, h, url, "tablet", 50, 50)
		phone := attachSized(t, h, url, "phone", 40, 20)
		typeInto(t, h, phone)
		wantScreen(t, h, 40, 20)
		closeAndAwait(t, h, phone, 2)
		sendControl(t, desk.ws, map[string]any{"type": "resize", "cols": 120, "rows": 40})
		awaitRecordedSize(t, h, 120, 40)
		typeInto(t, h, desk)
		time.Sleep(sizeHoldGrace + 500*time.Millisecond)
		wantScreen(t, h, 120, 40)
	})
	t.Run("the owner reconnecting within the grace keeps its size", func(t *testing.T) {
		h, url := fenceHandler(t, []string{"/bin/cat"})
		attachSized(t, h, url, "desk", 120, 50)
		phone := attachSized(t, h, url, "phone", 40, 20)
		typeInto(t, h, phone)
		closeAndAwait(t, h, phone, 1)
		wantScreen(t, h, 40, 20)
		attachSized(t, h, url, "phone", 40, 20)
		time.Sleep(sizeHoldGrace + 500*time.Millisecond)
		wantScreen(t, h, 40, 20)
	})
}

// soleClientOf returns the handler's one registered socket, registering a
// live one first when there is none, for tests that resize as one client.
func soleClientOf(t *testing.T, h *Handler) *clientState {
	t.Helper()
	h.registry.mu.Lock()
	for _, st := range h.registry.clients {
		h.registry.mu.Unlock()
		return st
	}
	h.registry.mu.Unlock()
	conn, _, cleanup := dualConn(t)
	t.Cleanup(cleanup)
	return h.registry.Add(conn)
}
