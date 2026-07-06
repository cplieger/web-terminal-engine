package terminal

import (
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// maxResumeSessions caps the number of retained resume sessions. The session
// map is keyed by client-supplied sessionID, so without a cap a client
// sending many distinct sessionIDs could grow it unbounded (CWE-770)
// until the 60-minute idle GC reclaims them. The cap is generous enough
// that legitimate multi-tab reconnects never hit it; only abusive clients
// do, so there is no behavior change for normal use.
const maxResumeSessions = 1024

// clientRegistry tracks connected WebSocket clients and their session
// state. It owns its own mutex so client add/remove and session
// lookup don't contend with the screen/PTY lock in Handler.
type clientRegistry struct {
	clients  map[*websocket.Conn]*clientState
	sessions map[string]*sessionState
	logger   *slog.Logger
	mu       sync.Mutex
}

// newClientRegistry returns an initialized registry. The logger receives
// session-GC notices; pass the handler's configured logger so WithLogger(nil)
// silences them and a custom logger receives them.
func newClientRegistry(logger *slog.Logger) *clientRegistry {
	return &clientRegistry{
		clients:  make(map[*websocket.Conn]*clientState),
		sessions: make(map[string]*sessionState),
		logger:   logger,
	}
}

// Add registers a new WebSocket connection and returns its state.
func (r *clientRegistry) Add(ws *websocket.Conn) *clientState {
	state := &clientState{}
	r.mu.Lock()
	r.clients[ws] = state
	r.mu.Unlock()
	return state
}

// Remove unregisters a WebSocket connection.
func (r *clientRegistry) Remove(ws *websocket.Conn) {
	r.mu.Lock()
	delete(r.clients, ws)
	r.mu.Unlock()
}

// Snapshot returns a map of connected clients to their session ack
// values. The returned map is safe to use without holding the lock.
func (r *clientRegistry) Snapshot() map[*websocket.Conn]uint64 {
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
// bytesReceived.
// Opportunistically GCs sessions idle >60 min. The 60-minute window is
// long enough to survive iOS Safari aggressively unloading a backgrounded
// tab (which can keep the same sessionId via sessionStorage but suspends
// the WebSocket for an unbounded period). A shorter window (the previous
// 10-minute one) caused a duplicate-resend bug: tab suspended >10 min →
// session GC'd → reconnect creates new session with bytesReceived=0 →
// client's resumeAck handler trims nothing → retransmitOutbox replays
// every queued chunk → the child re-receives the same input as fresh
// keystrokes and queues duplicate messages.
func (r *clientRegistry) ResolveSession(state *clientState, sessionID string) (ack uint64) {
	r.mu.Lock()
	sess, ok := r.sessions[sessionID]
	if !ok {
		sess = &sessionState{lastSeen: time.Now()}
		r.sessions[sessionID] = sess
		r.gcIdleSessions()
		r.evictOldestSession()
	}
	state.session.Store(sess)
	ack = sess.bytesReceived
	r.mu.Unlock()
	return ack
}

// gcIdleSessions deletes sessions idle longer than the 60-minute
// retention window, logging any GC'd session that had received bytes (a
// reconnect with that sessionId will see bytesReceived=0 and, per its
// own safeguard, drop unacked bytes — surfacing as input-loss rather
// than duplicate-resend; the log helps correlate those reports).
// The caller MUST hold r.mu.
func (r *clientRegistry) gcIdleSessions() {
	for id, s := range r.sessions {
		if time.Since(s.lastSeen) > 60*time.Minute {
			if s.bytesReceived > 0 {
				r.logger.Info("terminal: gc'd idle session with received bytes",
					"session_id", logID(id),
					"bytes_received", s.bytesReceived,
					"idle", time.Since(s.lastSeen).Round(time.Second))
			}
			delete(r.sessions, id)
		}
	}
}

// evictOldestSession caps the retained session count at maxResumeSessions by
// evicting the oldest-lastSeen entry (backstops the idle GC against a
// client minting many sessionIDs inside the 60-minute window). The
// caller MUST hold r.mu.
func (r *clientRegistry) evictOldestSession() {
	if len(r.sessions) <= maxResumeSessions {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, sx := range r.sessions {
		if oldestID == "" || sx.lastSeen.Before(oldest) {
			oldestID, oldest = id, sx.lastSeen
		}
	}
	if oldestID != "" {
		delete(r.sessions, oldestID)
	}
}

// IncrementReceived adds n to the session's bytesReceived counter.
func (r *clientRegistry) IncrementReceived(state *clientState, n int) {
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
