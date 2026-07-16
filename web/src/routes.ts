// Session-route path contract, TS half. These mirror the Go terminal
// package's exported constants (WSPath, SessionsPath, SessionEventsPath),
// whose MountSessionRoutes wires exactly this topology on the server — one
// name per path on each side of the wire, so a consumer composing its own
// URLs (or overriding a default) references the contract instead of
// restating a literal that could drift. Any addition here is an addition to
// the server mount contract and is release-noted on both halves.

/** WS_PATH is the terminal WebSocket route; the client connects per session
 *  with `?session=<id>`. Mirrors Go `terminal.WSPath`. */
export const WS_PATH = "/ws";

/** SESSIONS_PATH is the session REST route (POST create, GET list; DELETE and
 *  PUT title live under its subtree). Mirrors Go `terminal.SessionsPath`. */
export const SESSIONS_PATH = "/api/sessions";

/** SESSION_EVENTS_PATH is the session status stream (SSE). Mirrors Go
 *  `terminal.SessionEventsPath`. */
export const SESSION_EVENTS_PATH = "/api/sessions/events";
