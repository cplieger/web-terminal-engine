package terminal

import "net/http"

// The session-manager HTTP surface's route topology. These paths are a
// CONTRACT shared with the engine's TypeScript client (the UI's tabs feature
// defaults to SessionsPath and its activity monitor to SessionEventsPath) and
// hardcoded inside RESTHandler's method patterns, so a consumer cannot mount
// the handlers anywhere else and keep a working client. MountSessionRoutes
// wires exactly this set; adding a path to it is a release-noted,
// changelog-visible API change, never a silent addition.
const (
	// WSPath is the terminal WebSocket route. The client connects per session
	// with ?session=<id> (see WebSocketHandler).
	WSPath = "/ws"
	// SessionsPath is the exact-match session REST route: POST (create) and
	// GET (list). It is also the path a create gate throttles (see
	// WithCreateGate).
	SessionsPath = "/api/sessions"
	// SessionsSubtreePath is the session REST subtree: DELETE /{id}, PUT
	// /{id}/title, PUT + DELETE /{id}/pinned-title, PUT /{id}/seen, PUT /order
	// and GET + PUT /layout. ServeMux treats the trailing-slash pattern as a
	// distinct mount, so the REST handler is mounted here and at SessionsPath.
	// /order and /layout are literal segments, which ServeMux prefers over the
	// {id} wildcard, and no hex session id can collide with them.
	SessionsSubtreePath = "/api/sessions/"
	// SessionEventsPath is the session status stream (SSE) route. It is a more
	// specific pattern than SessionsSubtreePath, so ServeMux routes it to the
	// events handler rather than the REST DELETE /{id} pattern.
	SessionEventsPath = "/api/sessions/events"
)

// The cache policy the session REST surface states, and the header it states it
// on. One spelling for the whole package so the wrapper below and writeJSON
// cannot drift apart into two different answers.
const (
	cacheControlHeader = "Cache-Control"
	noStorePolicy      = "no-store"
)

// withNoStore sets Cache-Control: no-store before h runs, so even the mux's
// own 404, 405 and redirects carry it. Session paths hold the session id, a
// capability, and RFC 9110 §15.1 lets a cache reuse a 404 or 405 keyed by that
// URL. A value already set by the consumer's middleware is kept;
// writeJSON alone overwrites, because its bodies contain the id.
func withNoStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if w.Header().Get(cacheControlHeader) == "" {
			w.Header().Set(cacheControlHeader, noStorePolicy)
		}
		h.ServeHTTP(w, r)
	})
}

// MountOption configures MountSessionRoutes.
type MountOption func(*mountConfig)

// mountConfig holds resolved MountSessionRoutes configuration.
type mountConfig struct {
	createGate func(http.Handler) http.Handler
}

// WithCreateGate wraps the session REST handler with mw on both its mounts,
// never the WebSocket or events handlers, so a consumer can bound process
// creation: each admitted POST forks one process. Pass a middleware that gates
// only POST SessionsPath, such as a rate limiter. A nil mw is ignored.
func WithCreateGate(mw func(http.Handler) http.Handler) MountOption {
	return func(c *mountConfig) {
		if mw != nil {
			c.createGate = mw
		}
	}
}

// SessionHandlers is the trio MountSessionRoutes wires: the terminal WebSocket,
// the session REST API, and the status stream (SSE), normally one
// SessionManager's handlers, which MountAPI wires for you. Every field is
// required: a nil handler panics in net/http's registration at mount time.
type SessionHandlers struct {
	// WS serves the terminal WebSocket (mounted at WSPath).
	WS http.Handler
	// REST serves the session REST API (mounted at SessionsPath and
	// SessionsSubtreePath; create-gated when WithCreateGate is passed).
	REST http.Handler
	// Events serves the session status stream, SSE (mounted at
	// SessionEventsPath).
	Events http.Handler
}

// MountSessionRoutes wires exactly four mounts on mux, the engine-owned route
// set: WSPath to h.WS, SessionsPath and SessionsSubtreePath to h.REST (create
// gated, and wrapped in withNoStore outside the gate), and SessionEventsPath to
// h.Events. Each REST route is listed at SessionsSubtreePath; any addition is a
// release-noted API change. The SSE path is more specific than the subtree, so
// it routes to h.Events. The handlers are passed in, normally one manager's
// (MountAPI wires them), so a consumer's tests can stub them.
func MountSessionRoutes(mux *http.ServeMux, h SessionHandlers, opts ...MountOption) {
	var c mountConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	rest := h.REST
	if c.createGate != nil {
		rest = c.createGate(rest)
	}
	// Outside the gate, so a gate refusal on these token-bearing paths is covered
	// too. Events sets its own headers (writeSSEHeaders).
	rest = withNoStore(rest)
	mux.Handle(WSPath, h.WS)
	mux.Handle(SessionsPath, rest)
	mux.Handle(SessionsSubtreePath, rest)
	mux.Handle(SessionEventsPath, h.Events)
}

// MountAPI mounts this manager's WebSocket, REST, and status-stream handlers
// on mux per the MountSessionRoutes contract. It is the convenience for the
// common case of one manager serving one mux; use MountSessionRoutes directly
// to inject stub handlers in tests.
func (m *SessionManager) MountAPI(mux *http.ServeMux, opts ...MountOption) {
	MountSessionRoutes(mux, SessionHandlers{
		WS:     m.WebSocketHandler(),
		REST:   m.RESTHandler(),
		Events: m.EventsHandler(),
	}, opts...)
}
