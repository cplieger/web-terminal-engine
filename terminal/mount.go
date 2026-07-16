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
	// SessionsSubtreePath is the session REST subtree: DELETE /{id} (close)
	// and PUT /{id}/title (set the client-fallback title). ServeMux treats the
	// trailing-slash pattern as a distinct mount, so the REST handler is
	// mounted at both SessionsPath and this subtree to receive every method.
	SessionsSubtreePath = "/api/sessions/"
	// SessionEventsPath is the session status stream (SSE) route. It is a more
	// specific pattern than SessionsSubtreePath, so ServeMux routes it to the
	// events handler rather than the REST DELETE /{id} pattern.
	SessionEventsPath = "/api/sessions/events"
)

// MountOption configures MountSessionRoutes.
type MountOption func(*mountConfig)

// mountConfig holds resolved MountSessionRoutes configuration.
type mountConfig struct {
	createGate func(http.Handler) http.Handler
}

// WithCreateGate wraps the session REST handler with mw before mounting, so a
// caller cannot spawn PTY-backed child processes without bound: Create eagerly
// forks one process per admitted POST, which is exactly the
// expensive-shared-resource shape an aggregate rate limit exists for. The gate
// is INJECTED rather than built in because throttle policy (tuning, the 429
// body, whether to gate at all) is the consumer's decision and the engine
// takes no HTTP-middleware dependency; pass a middleware that scopes itself to
// the create request — e.g. webhttp.SessionCreateRateLimit(SessionsPath),
// whose predicate gates POST SessionsPath and passes GET/DELETE/PUT through.
// The gate wraps the REST handler on BOTH its mounts (SessionsPath and
// SessionsSubtreePath) and never wraps the WebSocket or events handlers. A nil
// mw is ignored (no gate).
func WithCreateGate(mw func(http.Handler) http.Handler) MountOption {
	return func(c *mountConfig) {
		if mw != nil {
			c.createGate = mw
		}
	}
}

// MountSessionRoutes wires the session-manager HTTP surface on mux — the
// engine-owned mount contract, in code:
//
//	WSPath              -> ws     (terminal WebSocket, ?session=<id>)
//	SessionsPath        -> rest   (POST create, GET list; create-gated)
//	SessionsSubtreePath -> rest   (DELETE /{id}, PUT /{id}/title; create-gated)
//	SessionEventsPath   -> events (status SSE)
//
// Exactly these four mounts and no others: the engine's debug or future
// routes never appear implicitly, and any addition to the set is a
// release-noted API change (see the path constants). The handlers are passed
// in — normally WebSocketHandler, RESTHandler, and EventsHandler of one
// SessionManager, which MountAPI wires for you — so a consumer's tests can
// exercise routing and middleware with stubs, without a real PTY.
//
// The mount shape encodes two ServeMux subtleties consumers previously
// re-derived by hand: the REST handler needs BOTH the exact path and the
// subtree mount (its internal patterns span /api/sessions and
// /api/sessions/{id}...), and the SSE path, being more specific than the
// subtree, routes to events rather than the REST DELETE /{id} pattern.
func MountSessionRoutes(mux *http.ServeMux, ws, rest, events http.Handler, opts ...MountOption) {
	var c mountConfig
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if c.createGate != nil {
		rest = c.createGate(rest)
	}
	mux.Handle(WSPath, ws)
	mux.Handle(SessionsPath, rest)
	mux.Handle(SessionsSubtreePath, rest)
	mux.Handle(SessionEventsPath, events)
}

// MountAPI mounts this manager's WebSocket, REST, and status-stream handlers
// on mux per the MountSessionRoutes contract. It is the convenience for the
// common case of one manager serving one mux; use MountSessionRoutes directly
// to inject stub handlers in tests.
func (m *SessionManager) MountAPI(mux *http.ServeMux, opts ...MountOption) {
	MountSessionRoutes(mux, m.WebSocketHandler(), m.RESTHandler(), m.EventsHandler(), opts...)
}
