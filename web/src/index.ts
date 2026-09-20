export * from "./types.js";
export { LineStore } from "./store.js";
export type { WindowState, StoreChanges, StoreSnapshot } from "./store.js";
export { createTerminalEngine } from "./terminal.js";
export type { TerminalEngine, TerminalEngineOptions } from "./terminal.js";
export { createRenderer } from "./render.js";
export type { Renderer, RendererOptions, ViewMemory } from "./render.js";
export { createScrollController } from "./scroll.js";
export type { ScrollController, ScrollControllerOptions } from "./scroll.js";
export { createConnection, MAX_REPLAY_LINES, MAX_OUTBOX_BYTES } from "./connection.js";
export type { Connection, ConnectionOptions, ConnectionCallbacks } from "./connection.js";
export { createMouseController, encodeSGR } from "./mouse.js";
export type { MouseController, MouseControllerOptions, MouseInputHandler } from "./mouse.js";
export { createModeState, POWER_ON_MODES } from "./modes.js";
export type { ModeState, ModeSnapshot } from "./modes.js";
/**
 * Key encoding, and nothing else: `mapKeyboardEvent` turns a `KeyboardEvent`
 * into the bytes a PTY expects or a local action, and the paste helpers do the
 * same for text. It touches no DOM and sends nothing; mode state arrives as an
 * explicit argument (an engine's `modes` satisfies it).
 */
export * as keyboard from "./keyboard.js";
/**
 * The on-screen mobile key toolbar, the one DOM widget in the engine:
 * `bindMobileToolbar` wires an existing container's buttons to a send sink and
 * a mode state and returns a controller owning sticky-Ctrl state and tear-down.
 * A missing button is skipped rather than fatal.
 */
export * as toolbar from "./toolbar.js";
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
