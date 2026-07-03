package terminal

// SessionManager runs N independent PTY-backed terminal sessions behind one set
// of HTTP handlers, so a browser can open several terminals (tabs) against one
// server. Each session is a *Handler (its own process, VT screen, scrollback,
// and resume state); sessions run continuously whether or not a client is
// attached. The manager owns creation, lookup, the crypto-random session id
// (which is both the routing id in /ws?session=<id> and the resume id), a cap on
// concurrent sessions, and an ownership-keyed idle reaper.
//
// Why N plain PTYs and not a terminal multiplexer: nesting under tmux would mean
// speaking tmux control mode to get per-session chrome, and a full-screen TUI
// under tmux collides on the prefix key and mouse/focus passthrough. dtach adds
// nothing here because the server process already outlives any client socket,
// which is the persistence we need.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ErrTooManySessions is returned by Create when the manager is at its
// MaxSessions cap. Handlers translate it to HTTP 429.
var ErrTooManySessions = errors.New("terminal: too many sessions")

// SessionInfo is the public description of one session, returned by List and
// carried on the status stream.
type SessionInfo struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
}

// Session status values. The manager computes working/idle/exited from process
// liveness and (in the status stream) output activity; a consumer's classifier
// may refine idle into needs-input from the OSC 9 notification.
const (
	StatusWorking = "working"
	StatusIdle    = "idle"
	StatusInput   = "input"
	StatusExited  = "exited"
)

// ManagerOption configures a SessionManager.
type ManagerOption func(*managerConfig)

type managerConfig struct {
	logger      *slog.Logger
	classifier  func(string) (string, bool)
	maxSessions int
	maxSubs     int
	idleWindow  time.Duration
}

// WithMaxSessions caps the number of concurrent sessions; Create returns
// ErrTooManySessions at the cap. Zero (the default) means unlimited, which is
// only appropriate behind an authenticated, trusted deployment.
func WithMaxSessions(n int) ManagerOption {
	return func(c *managerConfig) { c.maxSessions = n }
}

// WithIdleReaper enables the ownership-keyed idle reaper: when no client (WS or
// status stream) has been connected to the manager for d, all sessions are
// reaped (the operator's browser closed without deleting them). Zero (the
// default) disables reaping. This is keyed on client presence, not on a
// per-session socket, so a backgrounded tab with no live socket is not reaped
// while any client of the same browser is still connected.
func WithIdleReaper(d time.Duration) ManagerOption {
	return func(c *managerConfig) { c.idleWindow = d }
}

// WithManagerLogger sets the logger. A nil logger discards output.
func WithManagerLogger(l *slog.Logger) ManagerOption {
	return func(c *managerConfig) { c.logger = l }
}

// WithStatusClassifier maps an OSC 9 notification message to a session status
// (return ok=false to ignore a message). This keeps the engine generic: vibecli
// maps kiro-cli's "Permission required" to input and "Response complete" to
// idle, while a plain shell server sets no classifier and gets working/idle from
// output activity only. A classified input status latches (persists while the
// process waits) until the turn resumes or a done message clears it.
func WithStatusClassifier(fn func(notification string) (status string, ok bool)) ManagerOption {
	return func(c *managerConfig) { c.classifier = fn }
}

// WithMaxSubscribers caps concurrent status-stream (SSE) subscribers, bounding
// subscriber goroutines and buffers independently of the session cap. Zero
// selects a built-in default.
func WithMaxSubscribers(n int) ManagerOption {
	return func(c *managerConfig) { c.maxSubs = n }
}

type session struct {
	createdAt time.Time
	handler   *Handler
	id        string
}

// SessionManager maps session ids to PTY-backed handlers and serves the
// terminal WebSocket, the REST session API, and (see events.go) the status
// stream. Safe for concurrent use.
type SessionManager struct {
	factory       func(id string) *Handler
	logger        *slog.Logger
	sessions      map[string]*session
	trackers      map[string]*statusTracker
	subs          map[chan statusEvent]struct{}
	classifier    func(string) (string, bool)
	reaperCancel  context.CancelFunc
	sweepCancel   context.CancelFunc
	idleSince     time.Time
	mu            sync.Mutex
	subsMu        sync.Mutex
	idleWindow    time.Duration
	maxSessions   int
	maxSubs       int
	activeClients int
	created       uint64
	closed        uint64
	reaped        uint64
}

// NewSessionManager returns a manager that builds each session's handler with
// factory (called with the new session id, so the factory can scope the
// handler's logger and working directory). Options configure the cap, the idle
// reaper, and the logger.
func NewSessionManager(factory func(id string) *Handler, opts ...ManagerOption) *SessionManager {
	cfg := managerConfig{logger: slog.Default()}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	m := &SessionManager{
		factory:     factory,
		logger:      cfg.logger,
		sessions:    make(map[string]*session),
		trackers:    make(map[string]*statusTracker),
		subs:        make(map[chan statusEvent]struct{}),
		classifier:  cfg.classifier,
		idleSince:   time.Now(),
		idleWindow:  cfg.idleWindow,
		maxSessions: cfg.maxSessions,
		maxSubs:     cfg.maxSubs,
	}
	if m.maxSubs <= 0 {
		m.maxSubs = defaultMaxSubscribers
	}
	if m.idleWindow > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		m.reaperCancel = cancel
		go m.reapLoop(ctx)
	}
	// The status sweep computes per-session status and pushes changes to
	// subscribers. It runs regardless of subscribers (cheap, a near-no-op when
	// there are none) so status is current the instant a client subscribes.
	sctx, scancel := context.WithCancel(context.Background())
	m.sweepCancel = scancel
	go m.sweepLoop(sctx)
	return m
}

// Create starts a new session (eagerly spawning its process at a default size)
// and returns its id. Returns ErrTooManySessions at the cap.
func (m *SessionManager) Create() (string, error) {
	m.mu.Lock()
	if m.maxSessions > 0 && len(m.sessions) >= m.maxSessions {
		m.mu.Unlock()
		return "", ErrTooManySessions
	}
	id, err := newSessionID()
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	h := m.factory(id)
	m.mu.Unlock()

	// Eager start outside the lock: spawning a process should not block other
	// manager operations. A duplicate id is astronomically unlikely (128-bit
	// random) so we do not re-check under the lock after start.
	if err := h.StartEager(); err != nil {
		return "", err
	}

	m.mu.Lock()
	m.sessions[id] = &session{id: id, handler: h, createdAt: time.Now()}
	n := len(m.sessions)
	m.created++
	m.mu.Unlock()

	m.logger.Info("session: created", "session", id, "sessions", n)
	return id, nil
}

// List returns all sessions sorted by creation time.
func (m *SessionManager) List() []SessionInfo {
	m.mu.Lock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, SessionInfo{
			ID:        s.id,
			Title:     s.handler.Title(),
			Status:    statusOf(s.handler),
			CreatedAt: s.createdAt,
		})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Close terminates a session and removes it. Returns false if the id is unknown.
func (m *SessionManager) Close(id string) bool {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
		m.closed++
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	s.handler.Shutdown()
	m.logger.Info("session: closed", "session", id)
	return true
}

// Shutdown stops the reaper and terminates every session.
func (m *SessionManager) Shutdown() {
	m.mu.Lock()
	if m.reaperCancel != nil {
		m.reaperCancel()
		m.reaperCancel = nil
	}
	if m.sweepCancel != nil {
		m.sweepCancel()
		m.sweepCancel = nil
	}
	victims := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		victims = append(victims, s)
	}
	m.sessions = make(map[string]*session)
	m.mu.Unlock()
	for _, s := range victims {
		s.handler.Shutdown()
	}
}

// WebSocketHandler serves the terminal stream at /ws?session=<id>. An unknown
// or missing id is a 404. While a client is attached the manager counts it as
// present, which suppresses the idle reaper.
func (m *SessionManager) WebSocketHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("session")
		m.mu.Lock()
		s, ok := m.sessions[id]
		m.mu.Unlock()
		if !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		m.clientConnected()
		defer m.clientDisconnected()
		s.handler.ServeHTTP(w, r) // blocks for the WS lifetime
	})
}

// RESTHandler serves the session REST API: POST /api/sessions (create),
// GET /api/sessions (list), DELETE /api/sessions/{id} (close). Mount it for the
// /api/sessions and /api/sessions/ paths.
func (m *SessionManager) RESTHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", m.handleCreate)
	mux.HandleFunc("GET /api/sessions", m.handleList)
	mux.HandleFunc("DELETE /api/sessions/{id}", m.handleDelete)
	return mux
}

func (m *SessionManager) handleCreate(w http.ResponseWriter, _ *http.Request) {
	id, err := m.Create()
	if errors.Is(err, ErrTooManySessions) {
		http.Error(w, "too many sessions", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		m.logger.Error("session: create failed", "error", err)
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	// A freshly eager-started session is idle until it produces output; the
	// status stream corrects this within a tick if the process died instantly.
	writeJSON(w, http.StatusCreated, SessionInfo{ID: id, Status: StatusIdle, CreatedAt: time.Now()})
}

func (m *SessionManager) handleList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, m.List())
}

func (m *SessionManager) handleDelete(w http.ResponseWriter, r *http.Request) {
	if m.Close(r.PathValue("id")) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Error(w, "unknown session", http.StatusNotFound)
}

// clientConnected records that a client (WS or status stream) is present, so
// the idle reaper does not fire while the operator is here.
func (m *SessionManager) clientConnected() {
	m.mu.Lock()
	m.activeClients++
	m.mu.Unlock()
}

// clientDisconnected records a client leaving; when the last one leaves, the
// idle window starts counting from now.
func (m *SessionManager) clientDisconnected() {
	m.mu.Lock()
	m.activeClients--
	if m.activeClients <= 0 {
		m.activeClients = 0
		m.idleSince = time.Now()
	}
	m.mu.Unlock()
}

func (m *SessionManager) reapLoop(ctx context.Context) {
	interval := min(m.idleWindow, 30*time.Second)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.maybeReap()
		}
	}
}

// maybeReap reaps every session when no client has been connected for the idle
// window. It reaps all sessions together (not per-session): with no client the
// owning browser is gone, so every session is orphaned. A backgrounded tab with
// no socket is safe as long as any client (another tab's WS, or the status
// stream) keeps activeClients above zero.
func (m *SessionManager) maybeReap() {
	m.mu.Lock()
	if m.activeClients > 0 || len(m.sessions) == 0 || time.Since(m.idleSince) < m.idleWindow {
		m.mu.Unlock()
		return
	}
	victims := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		victims = append(victims, s)
	}
	m.sessions = make(map[string]*session)
	m.reaped += uint64(len(victims))
	m.mu.Unlock()
	for _, s := range victims {
		s.handler.Shutdown()
		m.logger.Info("session: reaped (no client for idle window)", "session", s.id)
	}
}

// statusOf computes a session's coarse status from process liveness. The status
// stream (events.go) refines a live process into working/idle/needs-input.
func statusOf(h *Handler) string {
	if h.Exited() {
		return StatusExited
	}
	return StatusIdle
}

// newSessionID returns a 128-bit crypto-random hex id, used as both the routing
// id (/ws?session=<id>) and the resume id.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // #nosec G104 -- client hangup is not actionable
}
