package terminal

import (
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ClientRegistry tracks connected WebSocket clients and their session
// state. It owns its own mutex so client add/remove and session
// lookup don't contend with the screen/PTY lock in Handler.
type ClientRegistry struct {
	clients  map[*websocket.Conn]*ClientState
	sessions map[string]*sessionState
	mu       sync.Mutex
}

// NewClientRegistry returns an initialized registry.
func NewClientRegistry() *ClientRegistry {
	return &ClientRegistry{
		clients:  make(map[*websocket.Conn]*ClientState),
		sessions: make(map[string]*sessionState),
	}
}

// Add registers a new WebSocket connection and returns its state.
func (r *ClientRegistry) Add(ws *websocket.Conn) *ClientState {
	state := &ClientState{}
	r.mu.Lock()
	r.clients[ws] = state
	r.mu.Unlock()
	return state
}

// Remove unregisters a WebSocket connection.
func (r *ClientRegistry) Remove(ws *websocket.Conn) {
	r.mu.Lock()
	delete(r.clients, ws)
	r.mu.Unlock()
}

// Snapshot returns a map of connected clients to their session ack
// values. The returned map is safe to use without holding the lock.
func (r *ClientRegistry) Snapshot() map[*websocket.Conn]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[*websocket.Conn]uint64, len(r.clients))
	for ws, state := range r.clients {
		var ack uint64
		if sess := state.session.Load(); sess != nil {
			ack = sess.bytesReceived
		}
		m[ws] = ack
	}
	return m
}

// ResolveSession looks up or creates a session for the given ID,
// attaches it to the client state, and returns the session's current
// bytesReceived and whether scrollback replay is needed (true on first
// resume for this sessionId — i.e. page refresh or new tab).
// Opportunistically GCs sessions idle >60 min. The 60-minute window is
// long enough to survive iOS Safari aggressively unloading a backgrounded
// tab (which can keep the same sessionId via sessionStorage but suspends
// the WebSocket for an unbounded period). A shorter window (the previous
// 10-minute one) caused a duplicate-resend bug: tab suspended >10 min →
// session GC'd → reconnect creates new session with bytesReceived=0 →
// client's resumeAck handler trims nothing → retransmitOutbox replays
// every queued chunk → the child re-receives the same input as fresh
// keystrokes and queues duplicate messages.
func (r *ClientRegistry) ResolveSession(state *ClientState, sessionID string) (ack uint64, needsReplay bool) {
	r.mu.Lock()
	sess, ok := r.sessions[sessionID]
	if !ok {
		sess = &sessionState{lastSeen: time.Now()}
		r.sessions[sessionID] = sess
		for id, s := range r.sessions {
			if time.Since(s.lastSeen) > 60*time.Minute {
				// Log when GCing a session that received input — a
				// reconnecting client with this sessionId will see
				// bytesReceived=0 and (per its own safeguard) drop
				// any unacked bytes, surfacing as input-loss rather
				// than duplicate-resend. The log entry helps correlate
				// user-visible "my input vanished" reports with the GC.
				if s.bytesReceived > 0 {
					slog.Info("terminal: gc'd idle session with received bytes",
						"session_id", id,
						"bytes_received", s.bytesReceived,
						"idle", time.Since(s.lastSeen).Round(time.Second))
				}
				delete(r.sessions, id)
			}
		}
	}
	needsReplay = !sess.replayedScrollback
	sess.replayedScrollback = true
	state.session.Store(sess)
	ack = sess.bytesReceived
	r.mu.Unlock()
	return ack, needsReplay
}

// IncrementReceived adds n to the session's bytesReceived counter.
func (r *ClientRegistry) IncrementReceived(state *ClientState, n int) {
	if n <= 0 {
		return
	}
	if sess := state.session.Load(); sess != nil {
		r.mu.Lock()
		sess.bytesReceived += uint64(n)
		sess.lastSeen = time.Now()
		r.mu.Unlock()
	}
}
