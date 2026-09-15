export * from "./types.js";
export { LineStore } from "./store.js";
export type { WindowState, StoreChanges, StoreSnapshot } from "./store.js";
/**
 * The DOM renderer: it owns a `LineStore` of absolute-index lines and reflects
 * it to one `div.term-row` per line inside a natively-scrolled container, so
 * there is no live-zone/scrollback split to reconcile. Reach for it to paint
 * decoded frames (`handleScreen`, `handleScroll`), to measure the grid
 * (`updateFontMetrics`, `computeSize`, `getCursorPx`), and to swap stores on a
 * tab switch (`bind`).
 *
 * `init` must run once against the two elements before any frame is applied,
 * and it always installs a fresh implicit store — so a consumer that manages
 * its own stores binds AFTER init, never before. `getHighestIndex` reports how
 * far the store's content reaches and is NOT a resume claim; a transport asks
 * `getReplayBoundary` for that, and wiring the former is the defect the latter
 * exists to prevent.
 */
export * as render from "./render.js";
export type { ViewMemory } from "./render.js";
/**
 * Key encoding, and nothing else: `mapKeyboardEvent` turns a browser
 * `KeyboardEvent` into either the bytes a PTY expects or a local action, and
 * the paste helpers (`bracketTextForPaste`, `prepareTextForTerminal`) do the
 * same for text. Reach for it when your own input surface — a hidden textarea,
 * an IME composition, a paste handler — has an event and needs the wire form.
 *
 * It touches no DOM and sends nothing: the caller delivers the bytes, and the
 * `scroll-up` / `scroll-down` results carry no bytes at all because local
 * scrollback navigation is the consumer's to perform. Mode state arrives as an
 * explicit `KeyboardModes` argument rather than being read from a global, so a
 * tabbed shell encodes against the active session's modes.
 */
export * as keyboard from "./keyboard.js";
/**
 * The on-screen mobile key toolbar — the one DOM widget in the engine.
 * `bindMobileToolbar` wires an existing container's buttons (Ctrl, arrows, Tab,
 * Enter, Esc, a collapse toggle) to a send sink and returns a controller owning
 * sticky-Ctrl state and tear-down. Reach for it on a touch UI, where the keys a
 * physical keyboard supplies have to be painted.
 *
 * The consumer supplies the markup; the module only looks buttons up by id
 * within the container, so a missing button is skipped rather than fatal.
 * Sticky Ctrl only takes effect on text routed through
 * `controller.applyStickyCtrl` — the toolbar cannot see keystrokes it did not
 * emit. Its arrow and Escape bytes come from `keyboard`'s exported logical-key
 * encodings, so they cannot drift from the physical-key path.
 */
export * as toolbar from "./toolbar.js";
/**
 * The scroll controller: the single owner of the scroll container's
 * `scrollTop`, holding one piece of state — whether the viewport is following
 * the tail or holding a reading position — derived from scroll events alone.
 * Reach for it to `init` the container and for consumer-facing controls: a
 * jump-to-bottom button (`scrollToBottom`), and `isUserScrolledUp` to decide
 * whether new output deserves an affordance.
 *
 * Everything else here is the renderer's to call around its own DOM mutations
 * (`stickToBottom`, `adjustForContentShift`, `noteContentShrink`). A consumer
 * that writes `scrollTop` directly is competing with this module for the same
 * value, which is how a held reading position gets lost.
 */
export * as scroll from "./scroll.js";
/**
 * The DEC private-mode mirror for the ACTIVE session — bracketed paste,
 * application cursor and keypad, mouse tracking and its encoding, focus
 * reporting, reverse video, kitty keyboard flags. Reach for it to read what the
 * application has turned on, most often to decide whether a paste needs
 * bracketing or whether a mouse gesture belongs to the terminal at all.
 *
 * It is a module singleton with one writer: `connection` applies every inbound
 * modes frame and, in a tabbed shell, restores the target session's snapshot
 * inside `setSession`. A consumer that calls `setModes` is racing that writer.
 * Values lag the server by the one frame in flight, and an unseen session
 * starts at `POWER_ON_MODES`, where bracketed paste is already ON so the first
 * paste cannot be delivered as typed input.
 */
export * as modes from "./modes.js";
/**
 * Mouse input for the terminal: SGR 1006 encoding (`encodeSGR`) plus `init`,
 * which attaches pointer and wheel listeners to an element and returns an
 * idempotent disposer. Reach for it when the terminal element should forward
 * clicks, drags and wheel ticks to the application rather than the page.
 *
 * The listeners gate on the mode state, so they are inert until the
 * application enables tracking — which is also why selection and page
 * scrolling keep working in a normal shell. Reports are best-effort: one
 * describes a screen a resume may since have repainted, so a refused report is
 * forgotten rather than retried. Focus is not mouse input and lives in
 * `connection.setClientFocus`.
 */
export * as mouse from "./mouse.js";
/**
 * The client-to-server transport. It owns the socket, the jittered
 * exponential-backoff reconnect, and the resume/inputAck layer that makes input
 * survive a reconnect: a bounded outbox retransmitted against the server's
 * acked byte count, resume by absolute index, and server-restart detection via
 * the boot epoch. Reach for it to `init` the callbacks, `connect`, send input
 * (`sendBinary`), announce geometry (`sendResize`), and switch sessions
 * (`setSession`).
 *
 * It decodes frames and applies mode changes itself, so a consumer's
 * `onMessage` only has to route screen and scroll to the renderer. Supply as
 * few of the resume and history callbacks as possible: an omitted one gets the
 * renderer's own answer, read per resume rather than captured, and overriding
 * one means deciding a local fact on the engine's behalf — `getReplayMax`, how
 * much history the client keeps resident, is the one override that is expected.
 */
export * as connection from "./connection.js";
export { decodeWireBinary } from "./wire-binary.js";
export {
  MIN_SUPPORTED_SERVER_WIRE_VERSION,
  WIRE_COMPATIBILITY,
  WIRE_INCOMPATIBLE_CLOSE_CODE,
  WIRE_PROTOCOL_VERSION,
} from "./wire-compatibility.js";
export type { WireCompatibility, WireIncompatibility } from "./wire-compatibility.js";
export { controlFrame, CONTROL_FRAME_PREFIX } from "./wire.js";
export { wsURL } from "./wsurl.js";
export { WS_PATH, SESSIONS_PATH, SESSION_EVENTS_PATH } from "./routes.js";
export { connectStatusStream } from "./status-stream.js";
export type {
  SessionInfo,
  SessionStatus,
  StatusStream,
  StatusStreamCallbacks,
  EventSourceFactory,
  EventSourceLike,
} from "./status-stream.js";
