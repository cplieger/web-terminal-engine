// WebSocket lifecycle with reliable input delivery across reconnects: the
// client → server half of the protocol (wire-binary.ts decodes the other). Each
// session's input is one ordered queue: `pending` (accepted, not yet sent) and
// `inFlight` (sent, not yet acknowledged, at most MAX_OUTBOX_BYTES). On open the
// client sends the resume control (docs/wire-protocol.md, "The resume control");
// the binary resumeAck and every later inputAck trim `inFlight`, the resume
// retransmits the rest (a ws.send() can succeed before TCP fails), and acks make
// room for `pending`.

import { wsURL } from "./wsurl.js";
import { WS_PATH } from "./routes.js";
import { controlFrame } from "./wire.js";
import { decodeWireBinary } from "./wire-binary.js";
import {
  MIN_SUPPORTED_SERVER_WIRE_VERSION,
  WIRE_INCOMPATIBLE_CLOSE_CODE,
  WIRE_PROTOCOL_VERSION,
  type WireIncompatibility,
} from "./wire-compatibility.js";
import { type ModeSnapshot, type ModeState, POWER_ON_MODES } from "./modes.js";
// ONE page size for the whole feature: the fetch trigger anchors a request from
// the STORE's value while the transport clamps the length, so two literals would
// let a shrunken page carry a full-size anchor.
import { PAGE_SIZE } from "./store.js";
import { MAX_REPLAY_LINES } from "./replay.js";
import type { Renderer } from "./render.js";
import type { MouseController } from "./mouse.js";
import type { ControlMessage, ScrollMessage, ServerMessage } from "./types.js";
import { INITIAL_DELAY_MS, nextBackoffDelay } from "./reconnect.js";

// First wire revision with typed client→server framing (text = control, binary
// = full-alphabet input); a resumeAck at or above it triggers the per-socket
// upgrade. Mirrors typedFramingMinVersion in the Go terminal package.
const TYPED_FRAMING_MIN_VERSION = 4;

type ConnState =
  | { status: "disconnected" }
  | { status: "connecting"; sock: WebSocket; abort: AbortController }
  // `upgraded` is the v4 typed-framing latch for THIS socket (the state object
  // is replaced on every transition, so it cannot leak across a reconnect).
  // false: controls are 0x00-sentinel binary frames and input is leading-NUL-
  // split. true: controls are text frames and input is raw binary.
  // `resumed`: this socket's resumeAck was handled. `size`: the last size it
  // announced, null before its first. Input goes out once both hold
  // (`inputReady`): a byte before the resume is counted and retransmitted too,
  // and a byte before the size reaches the PTY at a fresh PTY's default size.
  | {
      status: "connected";
      sock: WebSocket;
      abort: AbortController;
      upgraded: boolean;
      resumed: boolean;
      size: { cols: number; rows: number } | null;
      inputReady: boolean;
      gestureResynced: boolean;
      st: ResumeState;
    }
  | { status: "reconnecting"; timer: ReturnType<typeof setTimeout> }
  | { status: "incompatible" }
  // The server closed with a definitive code (4001, 4004) and onProcessExit is
  // wired: no wake path reconnects, only connect(), setSession() or forgetSession().
  | { status: "ended" };

// The server's close code for "the child process has exited"
// (terminal/terminal.go statusProcessExited): definitive, so no reconnect.
const PROCESS_EXITED_CLOSE_CODE = 4001;

// The server's close code for "the manager does not know this session id"
// (statusUnknownSession). The server ACCEPTS the upgrade and closes with it so
// the client can read it (a pre-upgrade 404 is an opaque 1006 in browser JS);
// like 4001 it is definitive and takes the no-reconnect path.
const SESSION_UNKNOWN_CLOSE_CODE = 4004;

/**
 * An input whose bytes depend on the mode state (a key press, a bracketed
 * paste), encoded by the connection when it is sent.
 */
export interface DeferredInput {
  /**
   * Called at most once, when every input queued before it is encoded and the
   * shown session's modes are known (at once when they already are). When
   * `paste` is set, cancelPendingInput may drop the body's unsent rest; a `tail`
   * is always sent after what was. Null or an empty body sends nothing; a throw
   * drops the input.
   */
  encode(): { body: Uint8Array; tail?: Uint8Array } | null;
  /** A paste: cancelPendingInput drops it whole while it waits to be encoded. */
  readonly paste?: boolean;
  /** What it counts in pendingInputBytes until it is encoded (default 0). */
  readonly size?: number;
}

// One queued input: bytes, or an input still waiting for its modes. Only a
// cancelable entry may be dropped.
interface PendingInput {
  data: Uint8Array;
  deferred: DeferredInput | null;
  cancelable: boolean;
}

// Per-session reliable-input accounting, so a tab switch reconnects to another
// session without replaying the previous tab's unacked bytes onto it and
// without a false restart reset (each epoch is compared only against its own).
interface ResumeState {
  id: string; // server session id: the routing id (?session=); resumeKey derives the ledger key
  bytesSent: number; // bytes put on the wire for this session's server ledger
  bytesAcked: number; // confirmed by server inputAck/resumeAck
  inFlight: Uint8Array[]; // sent, unacked chunks (sum of lengths = bytesSent - bytesAcked)
  inFlightBytes: number; // running sum of inFlight lengths; keeps applyAck O(n) not O(n²)
  pending: PendingInput[]; // accepted, not yet sent, in acceptance order
  pendingBytes: number;
  holds: number; // live holdInput() calls; while above 0 nothing pending is encoded or sent
  // The server process this session's queued input was meant for; null before
  // the first ack, while the bound LineStore's content stands in for it. The
  // content's epoch is the store's: a store moves between connections, this does not.
  lastServerEpoch: number | null;
}

function newResumeState(id: string): ResumeState {
  return {
    id,
    bytesSent: 0,
    bytesAcked: 0,
    inFlight: [],
    inFlightBytes: 0,
    pending: [],
    pendingBytes: 0,
    holds: 0,
    lastServerEpoch: null,
  };
}

/** How often liveness is evaluated. */
const HEARTBEAT_INTERVAL_MS = 5_000;
/** Inbound silence that must elapse before we actively probe with a ping. */
const IDLE_BEFORE_PROBE_MS = 10_000;
/** How long an unanswered probe is tolerated before declaring the socket stale. */
const PONG_TIMEOUT_MS = 7_000;

/** Requests served back-to-back before the client's own bucket throttles. */
const HISTORY_BURST = 4;

/**
 * Client-side refill interval. Deliberately SLOWER than the server's floor
 * (1.5 s): independent clocks and latency jitter compress arrival spacing, so
 * identical constants would let a healthy client trip the server's silent drop.
 * The slack absorbs that by construction rather than by coincidence.
 */
const HISTORY_REFILL_MS = 2_000;

/**
 * How long to wait for a page before releasing single-flight and retrying. It
 * fires FIRST by design: the server's write context is 10 s, and in
 * coder/websocket a write-deadline expiry CLOSES the socket, so a server that
 * gave up first would turn every slow reply into a reconnect and make this
 * retry path unreachable.
 */
const HISTORY_DATA_TIMEOUT_MS = 8_000;

/** The smallest request the adaptive budget will shrink to. */
const HISTORY_MIN_PAGE = 125;

// Everything the fetch controller owns for ONE socket, replaced wholesale on
// reconnect: a surviving empty bucket would stall against a fresh server
// bucket, a carried-over capability would page against a server that never
// declared it, and a carried-over 125-line budget would punish a new link.
interface HistoryState {
  /** The server declared paging on this socket's resume ack. */
  paging: boolean;
  /** An ack has been processed; before that, content frames are suppressed. */
  acked: boolean;
  /** The in-flight request's window, or null when nothing is outstanding. */
  inFlight: { fromAbs: number; end: number } | null;
  /** Token bucket fill and its last refill instant. */
  tokens: number;
  lastRefill: number;
  /** The adaptive request budget, and the recovery ceiling above it. */
  effMax: number;
  budgetCeiling: number;
  /** Timers: the in-flight data timeout, and the coalesced pending demand. */
  dataTimer: ReturnType<typeof setTimeout> | null;
  demandTimer: ReturnType<typeof setTimeout> | null;
  /** The values this socket SENT on its resume, which its reply answers. */
  sentHaveThrough: number;
  sentReplayMax: number | null;
}

function newHistoryState(): HistoryState {
  return {
    paging: false,
    acked: false,
    inFlight: null,
    // The bucket starts FULL so a fresh socket can burst; the server's bucket
    // does the same, so the two agree on the first few requests.
    tokens: HISTORY_BURST,
    lastRefill: Date.now(),
    effMax: PAGE_SIZE,
    budgetCeiling: PAGE_SIZE,
    dataTimer: null,
    demandTimer: null,
    sentHaveThrough: -1,
    sentReplayMax: null,
  };
}

// The capabilities the server DECLARED on this socket's resume ack, per socket
// because post-latch an unrecognized text control is DROPPED silently.
interface ServerCaps {
  /** ackFlags bit3: the server serves the `ephemeralInput` control. */
  ephemeralInput: boolean;
  /** ackFlags bit2: the server derives the DEC 1004 answer from `focus`. */
  serverFocus: boolean;
}

/** WebSocket.OPEN. Spelled out because a test's fake constructor has no statics. */
const WS_OPEN = 1;

const ephemeralEncoder = new TextEncoder();

/**
 * The most input bytes in flight (sent, not yet acknowledged) per session, so
 * the most a reconnect retransmits. Input beyond it waits, in order, unbounded.
 */
export const MAX_OUTBOX_BYTES = 1 << 20;

// The server's read limit for one message (wire-golden/limits.json); a larger
// message closes the socket 1009.
const MAX_INPUT_MESSAGE_BYTES = 64 * 1024;

// How often onPendingInput may report a count between two non-zero values.
const PENDING_REPORT_INTERVAL_MS = 250;

// The session id is a resume token the server trusts to re-attach a client, so
// it must not be predictable: `crypto.randomUUID` where it exists (it needs a
// secure context), otherwise 16 CSPRNG bytes as hex, and a throw rather than
// Math.random() when neither is available.
function generateSessionId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }
  throw new Error("vterm: no cryptographically secure RNG available for session id");
}

// The ONE encoder for PTY input bytes, shared by the pump and the retransmit so
// the two cannot disagree on framing. Messages are at most
// MAX_INPUT_MESSAGE_BYTES; on a v3-mode socket each message's leading 0x00 is
// its own 1-byte message so none can be misread as a control frame. Splitting
// alters message count, never byte count or order.
function sendInputFrames(sock: WebSocket, upgraded: boolean, chunk: Uint8Array): void {
  let rest = chunk;
  while (rest.length > 0) {
    if (!upgraded && rest[0] === 0x00) {
      sock.send(new Uint8Array([0x00]).buffer);
      rest = rest.subarray(1);
      continue;
    }
    const part = rest.subarray(0, MAX_INPUT_MESSAGE_BYTES);
    sock.send(part.buffer.slice(part.byteOffset, part.byteOffset + part.byteLength) as ArrayBuffer);
    rest = rest.subarray(part.length);
  }
}

// The largest n <= limit such that data[0..n) ends on a UTF-8 character
// boundary, so a cancel after a partial send never leaves half a character.
// The caller passes limit < data.length. A boundary is at most 3 bytes back;
// bytes that are not UTF-8 are cut at limit, or a full window of them would
// return 0 with nothing in flight and no ack to wait for.
function utf8Cut(data: Uint8Array, limit: number): number {
  for (let n = limit; n >= Math.max(0, limit - 3); n--) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- n <= limit < data.length
    if (n === 0 || (data[n]! & 0xc0) !== 0x80) {
      return n;
    }
  }
  return limit;
}

// A control for a v4-upgraded socket: bare JSON in a TEXT frame, the message
// type being the discriminator. controlFrame is the v3-mode sentinel form.
function textControl(msg: ControlMessage): string {
  return JSON.stringify(msg);
}

// Called whenever the server no longer holds the ledger these counters were
// keyed to: its replacement counts from zero, so non-zero counters would make
// every later ack read as stale and inFlight would never trim again. Pending
// input was never sent, so no ledger covers it and it stays.
function resetLedger(st: ResumeState): void {
  st.bytesSent = 0;
  st.bytesAcked = 0;
  st.inFlight.length = 0;
  st.inFlightBytes = 0;
}

// Copies the bytes: the caller may reuse its buffer.
function enqueue(st: ResumeState, data: Uint8Array): void {
  if (data.length > 0) {
    st.pending.push({ data: new Uint8Array(data), deferred: null, cancelable: false });
    st.pendingBytes += data.length;
  }
}

function deferredSize(input: DeferredInput): number {
  const size = input.size ?? 0;
  return Number.isSafeInteger(size) && size > 0 ? size : 0;
}

function encodedParts(
  input: DeferredInput,
  encoded: ReturnType<DeferredInput["encode"]>,
): PendingInput[] {
  if (encoded === null || encoded.body.length === 0) {
    return [];
  }
  const parts: PendingInput[] = [
    { data: new Uint8Array(encoded.body), deferred: null, cancelable: input.paste === true },
  ];
  if (encoded.tail !== undefined && encoded.tail.length > 0) {
    parts.push({ data: new Uint8Array(encoded.tail), deferred: null, cancelable: false });
  }
  return parts;
}

function dropPending(st: ResumeState): void {
  st.pending.length = 0;
  st.pendingBytes = 0;
}

// Drops chunks from the front of inFlight until the unacked total matches
// (bytesSent - newAck); O(chunks dropped) through the running inFlightBytes.
function applyAck(st: ResumeState, received: number): void {
  if (received <= st.bytesAcked) {
    return;
  }
  st.bytesAcked = Math.min(received, st.bytesSent);
  const targetUnacked = st.bytesSent - st.bytesAcked;
  while (st.inFlight.length > 0 && st.inFlightBytes > targetUnacked) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- length checked above
    const head = st.inFlight[0]!;
    const dropFromHead = st.inFlightBytes - targetUnacked;
    if (head.length <= dropFromHead) {
      st.inFlight.shift();
      st.inFlightBytes -= head.length;
    } else {
      st.inFlight[0] = head.subarray(dropFromHead);
      st.inFlightBytes -= dropFromHead;
      break;
    }
  }
}

/** The `sessionStorage` key of the unmanaged session id when `ConnectionOptions.sessionIdKey` is absent. */
const DEFAULT_SESSION_ID_KEY = "vterm-session-id";

/**
 * What the connection needs from its consumer. Every member is called, never
 * captured: the connection reads the current one at each event. A member may
 * call `dispose()` on the connection that invoked it; the connection stops
 * after the member returns. A member that throws propagates out of the
 * connection's handler, except `initialSize`, which is isolated (readInitialSize).
 */
export interface ConnectionCallbacks {
  onMessage(msg: ServerMessage): void;
  onOpen(): void;
  onClose(): void;
  onConnecting?(): void;
  /**
   * The active session's count of accepted input bytes not yet sent, and
   * whether cancelPendingInput would drop some of them: fired when the count
   * moves between zero and non-zero or `cancelable` changes, at most every
   * 250 ms in between, and at once when a session switch changes either.
   */
  onPendingInput?(bytes: number, cancelable: boolean): void;
  /**
   * Fired instead of onClose when the server closes with a DEFINITIVE code:
   * process-exited (4001) or unknown-session (4004). When wired, the session
   * has ended: no onClose, no backoff reconnect, and `reconnectNow()` is
   * refused until `connect()`, `setSession()` or `forgetSession()`. When absent,
   * every close keeps the transient treatment (onClose plus backoff reconnect).
   */
  onProcessExit?(): void;
  /**
   * Fired at most once per resumeAck when the server restarted under something
   * this client held: the bound store discarded its content (another process's,
   * or restored and not confirmed, see `LineStore.applyResumeAck`), input queued
   * for the old process was dropped, or the server lost this session's ledger
   * with bytes in flight.
   */
  onServerRestart?(): void;
  /**
   * Fired when a resumeAck carries an explicit server revision outside
   * [MIN_SUPPORTED_SERVER_WIRE_VERSION, WIRE_PROTOCOL_VERSION]. A newer server
   * warns but continues; a below-floor server also fires onWireIncompatible and
   * stops the socket. Version-silent servers never fire either callback.
   */
  onWireVersionMismatch?(server: number, client: number): void;
  /**
   * Fired when the connection definitively stops a socket for an incompatible
   * declared wire revision (resumeAck metadata or close code 4002). Reconnects
   * stay blocked until an explicit disconnect, whether or not this is wired.
   */
  onWireIncompatible?(details: WireIncompatibility): void;
  /** The terminal's current geometry, integer cols/rows > 0: the server FLOORS an out-of-range resize. */
  computeSize(): { cols: number; rows: number };
  /**
   * How many lines of resume replay this client wants at most, a REFINEMENT of
   * the protocol ceiling: clamped to MAX_REPLAY_LINES before it is sent; absent
   * or null means the renderer's own residency answers.
   */
  getReplayMax?(): number | null;
  /**
   * Fired on resume with the server's retained-history bounds (`committed` is
   * one past the newest retained line, `oldest` the oldest retained index),
   * AFTER the renderer has been given them.
   */
  onResumeBounds?(committed: number, oldest: number): void;
  /**
   * The size each socket announces BEFORE asking to resume, so the snapshot
   * arrives at this client's own geometry; absent, `computeSize` answers. Null
   * (the client cannot measure itself yet, web fonts loading) announces
   * nothing: that socket's input then waits for its first `sendResize`.
   */
  initialSize?(): { cols: number; rows: number } | null;
  /**
   * Fired once per socket, with the size it announced, when its input starts:
   * after its first size and its resumeAck, so a restart's input drop and
   * `onServerRestart` come first. Input sent from here reaches the process this
   * socket attached to; none goes on the socket before it.
   */
  onInputReady?(size: { cols: number; rows: number }): void;
}

/** What `createConnection` drives and reads. */
export interface ConnectionOptions {
  renderer: Pick<
    Renderer,
    | "getReplayBoundary"
    | "replayMaxForResume"
    | "applyResumeTransition"
    | "noteResumeBounds"
    | "handleHistoryReply"
    | "noteSolicited"
    | "clearSolicited"
    | "clearGrid"
    | "maybeFetchHistory"
  >;
  /**
   * Known only from the current socket's modes frame (or its first screen frame,
   * from a server that sends none): unknown from creation, from every connect,
   * close and teardown, and reset to `POWER_ON_MODES` on a session switch.
   */
  modes: Pick<ModeState, "applySnapshot" | "setKnown" | "isKnown">;
  mouse: Pick<MouseController, "resyncGesture">;
  callbacks: ConnectionCallbacks;
  /** WebSocket endpoint path (default "/ws"). */
  wsPath?: string;
  /**
   * The `sessionStorage` key of the unmanaged session id (default
   * "vterm-session-id"). Two unmanaged connections on one page need distinct
   * keys or they attach to the same session.
   */
  sessionIdKey?: string;
}

/**
 * The client-to-server transport: it owns the socket, the jittered
 * exponential-backoff reconnect, and the resume/inputAck layer that makes input
 * survive a reconnect. It decodes frames and applies mode changes itself, so a
 * consumer's `onMessage` only has to route screen and scroll to the renderer.
 */
export interface Connection {
  /**
   * Queue input for the active session's reliable, in-order delivery, of any
   * size and any bytes. Returns false only once disposed. Input stays with its
   * session across a switch; a server restart drops it, encoded or not. It
   * goes on a socket only after that socket's resume and first size.
   */
  sendBinary(input: Uint8Array | DeferredInput): boolean;
  /**
   * Drop every paste of the active session not yet encoded and the unsent body
   * of every one that is, keeping their tails and every other entry. Returns
   * whether anything was dropped.
   */
  cancelPendingInput(): boolean;
  /** The active session's accepted input bytes not yet sent (DeferredInput.size before encoding). */
  pendingInputBytes(): number;
  /**
   * Keep the active session's queued input off the wire, encoded or not, until
   * the returned release runs (once; a second call does nothing). Holds nest,
   * input still queues in order with its session, and a server restart still
   * drops it.
   */
  holdInput(): () => void;
  /**
   * Deliver BEST-EFFORT input (a mouse report): sent only on a live socket
   * that announced its size, never queued or retransmitted. Returns whether
   * it was sent; on false the caller FORGETS the event. Falls back to reliable
   * input against a server that did not declare `ephemeralInput`, because
   * total silence is a worse failure.
   */
  sendEphemeral(text: string): boolean;
  /**
   * Report whether the terminal widget is focused. Reconciled, not replayed: a
   * repeat sends nothing, a report with no live socket is dropped, a reconnect
   * re-asserts the current value, and a server that did not declare
   * `serverFocus` gets no report.
   */
  setClientFocus(focused: boolean): void;
  /**
   * Announce the size `computeSize` answers. Dropped when this socket already
   * announced that size, and nothing is queued while no socket is connected.
   * A socket's first announce lets its waiting input go.
   */
  sendResize(): void;
  /**
   * The last size announced on any socket of this connection (the open
   * handler's `initialSize` included), kept across reconnects; null before the
   * first announce and once disposed.
   */
  lastAnnouncedSize(): { cols: number; rows: number } | null;
  /**
   * Replace whatever socket exists with a fresh one immediately, skipping the
   * backoff: the wake path. A no-op once the session has ended (onProcessExit)
   * or the server is incompatible.
   */
  reconnectNow(): void;
  /**
   * Switch the live socket to server session `id`, keeping every session's resume
   * state; marks the connection managed (the URL carries `?session=`). A no-op
   * when `id` is already the active, connected session; an ended one reconnects.
   */
  setSession(id: string): void;
  /** Drop a session's resume state; tears the socket down without reconnecting if it was active. */
  forgetSession(id: string): void;
  /** The current adaptive page budget; read it for both a request's length and its anchor. */
  historyBudget(): number;
  /**
   * Request at most `maxLines` lines of history from `fromAbs`. Returns whether
   * it went out; it declines when paging is undeclared, unacked, in flight or
   * paced, and a paced denial arms a coalesced retry.
   */
  requestHistory(fromAbs: number, maxLines: number): boolean;
  /** The session id the socket serves, minting the unmanaged id if none is set; "" once disposed. */
  currentSessionId(): string;
  /** Tear down the live socket without reconnecting; resume state is kept. */
  disconnect(): void;
  /**
   * Open the WebSocket, an ended session's included; readiness is reported through onOpen, and a connect that
   * never opens fails after 10 s with onClose and a scheduled retry. At most one
   * socket exists per connection whatever the call pattern.
   */
  connect(): void;
  /**
   * Close the socket with code 1000 (no onClose fires), stop every timer, drop
   * every session's queued bytes and detach the callbacks. Idempotent; every
   * method is a no-op or answers its empty value afterwards.
   */
  dispose(): void;
}

const noop = (): void => undefined;
const DISPOSED_CALLBACKS: ConnectionCallbacks = {
  onMessage: noop,
  onOpen: noop,
  onClose: noop,
  computeSize: () => ({ cols: 0, rows: 0 }),
};

/** Creates a connection; it does not connect until `connect()` or `setSession()`. */
export function createConnection(opts: ConnectionOptions): Connection {
  // Invariant: `renderer`, `modes` and `mouse` are read by member call at each call site, never as captured methods.
  const { renderer, modes, mouse } = opts;
  modes.setKnown(false);
  const wsPath = opts.wsPath ?? WS_PATH;
  const sessionIdKey = opts.sessionIdKey ?? DEFAULT_SESSION_ID_KEY;
  let cb: ConnectionCallbacks = opts.callbacks;
  // The lifetime latch (`connState` is overwritten by the reconnect paths, so it
  // cannot hold it). A callback may dispose the connection that invoked it, so every
  // callback followed by work re-checks it, through a call the compiler cannot narrow.
  let disposed = false;
  const isDisposed = (): boolean => disposed;
  let connState: ConnState = { status: "disconnected" };
  let reconnectDelay = INITIAL_DELAY_MS;
  let announcedSize: { cols: number; rows: number } | null = null;

  const sessions = new Map<string, ResumeState>();
  // The session the live socket serves; null until the first connect or
  // setSession, when the unmanaged path mints a sessionStorage-backed id.
  let activeId: string | null = null;
  // managed = a consumer selected sessions through setSession, so the URL
  // carries ?session=<id>. Unmanaged keeps the bare wsPath and a sessionStorage
  // id, which survives iOS tab-suspend, BFCache and reload.
  let managed = false;

  // Several live pages can share one routing id (every device on a managed
  // session; a duplicated tab copies sessionStorage), but the server's input
  // ledger is per sender, so the resume key carries this page-lifetime suffix.
  // Never persisted: a reloaded page has no input in flight and nothing to resume.
  // Lazy so a cryptoless environment throws on first connect, not at init.
  let clientInstanceId: string | null = null;

  function resumeKey(st: ResumeState): string {
    clientInstanceId ??= generateSessionId();
    return `${st.id}#${clientInstanceId}`;
  }

  function ensureState(id: string): ResumeState {
    let s = sessions.get(id);
    if (s === undefined) {
      s = newResumeState(id);
      sessions.set(id, s);
    }
    return s;
  }

  function activeState(): ResumeState {
    activeId ??= loadOrCreateSessionId();
    return ensureState(activeId);
  }

  // --- Client-side liveness ---
  // A socket can go silently half-open with no wake event (a NAT idle timeout
  // on a backgrounded tab) and then read OPEN forever while delivering nothing;
  // the server's WS-protocol pings are answered by the browser without reaching
  // JS. So after a stretch of silence the client sends an app-level ping; any
  // inbound frame clears the probe, and an unanswered one reconnects. The probe
  // is what distinguishes "idle but alive" from "dead".
  let lastActivityAt = 0; // Date.now() of the last inbound frame (any kind)
  let probeSentAt = 0; // Date.now() the outstanding probe ping was sent; 0 = none
  let heartbeatTimer: ReturnType<typeof setInterval> | null = null;

  // --- demand-paged scrollback: per-socket fetch state ---

  let history: HistoryState = newHistoryState();

  let caps: ServerCaps = { ephemeralInput: false, serverFocus: false };

  // onPendingInput's throttle: a zero edge or another session's count reports at
  // once, one session's count between two non-zero values at most every
  // PENDING_REPORT_INTERVAL_MS.
  let reportedPending = 0;
  let reportedCancelable = false;
  let reportedPendingFor: string | null = null;
  let pendingReportAt = 0;
  let pendingReportTimer: ReturnType<typeof setTimeout> | null = null;

  // Per PAGE, not per socket: the widget's own state, which a new socket must be told.
  let clientFocused = false;
  // The focus value THIS socket has been told, or null; per socket, so a
  // reconnect re-asserts focus instead of suppressing it as a repeat.
  let focusReported: boolean | null = null;

  /** Cancel both timers and release single-flight. */
  function clearHistoryTimers(): void {
    if (history.dataTimer !== null) {
      clearTimeout(history.dataTimer);
      history.dataTimer = null;
    }
    if (history.demandTimer !== null) {
      clearTimeout(history.demandTimer);
      history.demandTimer = null;
    }
  }

  // Everything a new socket must not inherit, reset together: splitting them is
  // how a client pages against a server that never declared it or holds a focus
  // report back from the new socket as a repeat of what the old one was told.
  function resetForNewSocket(): void {
    clearHistoryTimers();
    history = newHistoryState();
    caps = { ephemeralInput: false, serverFocus: false };
    focusReported = null;
  }

  // Spend a pacing token: 0 when granted, else the wait in ms so the caller can
  // arm the coalesced pending demand for exactly that instant.
  function takeHistoryToken(): number {
    const now = Date.now();
    history.tokens = Math.min(
      HISTORY_BURST,
      history.tokens + (now - history.lastRefill) / HISTORY_REFILL_MS,
    );
    history.lastRefill = now;
    if (history.tokens < 1) {
      return Math.ceil((1 - history.tokens) * HISTORY_REFILL_MS);
    }
    history.tokens -= 1;
    return 0;
  }

  // sessionStorage is per tab and survives iOS suspend, BFCache and reload but
  // not a true tab close, which is the wanted semantic: a fresh tab is a fresh
  // session. Private mode or disabled storage falls back to in-memory only.
  function loadOrCreateSessionId(): string {
    try {
      const existing = sessionStorage.getItem(sessionIdKey);
      if (existing) {
        return existing;
      }
      const fresh = generateSessionId();
      sessionStorage.setItem(sessionIdKey, fresh);
      return fresh;
    } catch {
      return generateSessionId();
    }
  }

  function sendBinary(input: Uint8Array | DeferredInput): boolean {
    if (isDisposed()) {
      return false;
    }
    const st = activeState();
    if (input instanceof Uint8Array) {
      enqueue(st, input);
    } else {
      st.pending.push({
        data: new Uint8Array(0),
        deferred: input,
        cancelable: input.paste === true,
      });
      st.pendingBytes += deferredSize(input);
    }
    pumpInput();
    reportPending();
    return true;
  }

  // An encoder calls back into its consumer, which may queue more input: the
  // nested call leaves the queue to the outer pump.
  let pumping = false;

  // The one writer of input to the wire. Bytes leave `inFlight` only when the
  // server acks them.
  function pumpInput(): void {
    if (pumping) {
      return;
    }
    pumping = true;
    try {
      const active = activeId === null ? undefined : sessions.get(activeId);
      if (activeId !== null && active?.holds === 0 && modes.isKnown()) {
        encodeDeferred(activeId, active);
      }
      if (!isDisposed()) {
        sendPending();
      }
    } finally {
      pumping = false;
    }
    reportPending();
  }

  // Each entry is taken before its encoder runs, so a cancel inside it drops the
  // result and no later pass encodes it again; a switch, teardown, forget, hold
  // or dispose inside an encoder ends the pass.
  function encodeDeferred(id: string, st: ResumeState): void {
    for (let i = 0; i < st.pending.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- i < length
      const entry = st.pending[i]!;
      const input = entry.deferred;
      if (input === null) {
        continue;
      }
      entry.deferred = null;
      st.pendingBytes -= deferredSize(input);
      let encoded: ReturnType<DeferredInput["encode"]> = null;
      try {
        encoded = input.encode();
      } catch (err) {
        console.warn("vterm: an input encoder threw; the input is dropped", err);
      }
      if (isDisposed() || sessions.get(id) !== st) {
        return;
      }
      const at = st.pending.indexOf(entry);
      if (at === -1) {
        i = -1; // a cancel rebuilt the queue: rescan it, encoded entries skip
      } else {
        const parts = encodedParts(input, encoded);
        st.pending.splice(at, 1, ...parts);
        for (const p of parts) {
          st.pendingBytes += p.data.length;
        }
        i = at + parts.length - 1;
      }
      if (activeId !== id || !modes.isKnown() || st.holds > 0) {
        return;
      }
    }
  }

  function sendPending(): void {
    if (connState.status !== "connected" || !connState.inputReady) {
      return;
    }
    const { sock, upgraded, st } = connState;
    while (st.holds === 0 && st.pending.length > 0 && st.inFlightBytes < MAX_OUTBOX_BYTES) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- length checked above
      const head = st.pending[0]!;
      if (head.deferred !== null) {
        break; // its modes are not known yet
      }
      let n = Math.min(head.data.length, MAX_OUTBOX_BYTES - st.inFlightBytes);
      if (head.cancelable && n < head.data.length) {
        n = utf8Cut(head.data, n);
        if (n === 0) {
          break; // less than one character of room: the next ack makes more
        }
      }
      const chunk = head.data.subarray(0, n);
      if (n === head.data.length) {
        st.pending.shift();
      } else {
        head.data = head.data.subarray(n);
      }
      st.pendingBytes -= n;
      st.inFlight.push(chunk);
      st.inFlightBytes += n;
      st.bytesSent += n;
      sendInputFrames(sock, upgraded, chunk);
    }
  }

  function cancelPendingInput(): boolean {
    if (isDisposed() || activeId === null) {
      return false;
    }
    const st = sessions.get(activeId);
    if (st === undefined) {
      return false;
    }
    const kept = st.pending.filter((e) => !e.cancelable);
    if (kept.length === st.pending.length) {
      return false;
    }
    st.pending = kept;
    st.pendingBytes = kept.reduce(
      (sum, e) => sum + (e.deferred === null ? e.data.length : deferredSize(e.deferred)),
      0,
    );
    reportPending();
    return true;
  }

  function holdInput(): () => void {
    if (isDisposed()) {
      return noop;
    }
    const st = activeState();
    st.holds++;
    let held = true;
    return () => {
      if (!held) {
        return;
      }
      held = false;
      st.holds--;
      if (!isDisposed() && st.holds === 0) {
        pumpInput();
      }
    };
  }

  function pendingInputBytes(): number {
    if (isDisposed() || activeId === null) {
      return 0;
    }
    return sessions.get(activeId)?.pendingBytes ?? 0;
  }

  function reportPending(): void {
    if (isDisposed()) {
      return;
    }
    const bytes = pendingInputBytes();
    const cancelable =
      activeId !== null && (sessions.get(activeId)?.pending.some((e) => e.cancelable) ?? false);
    if (bytes === reportedPending && cancelable === reportedCancelable) {
      return;
    }
    const now = Date.now();
    const sinceLast = now - pendingReportAt;
    if (
      bytes !== 0 &&
      reportedPending !== 0 &&
      cancelable === reportedCancelable &&
      activeId === reportedPendingFor &&
      sinceLast < PENDING_REPORT_INTERVAL_MS
    ) {
      pendingReportTimer ??= setTimeout(() => {
        pendingReportTimer = null;
        reportPending();
      }, PENDING_REPORT_INTERVAL_MS - sinceLast);
      return;
    }
    if (pendingReportTimer !== null) {
      clearTimeout(pendingReportTimer);
      pendingReportTimer = null;
    }
    reportedPending = bytes;
    reportedCancelable = cancelable;
    reportedPendingFor = activeId;
    pendingReportAt = now;
    cb.onPendingInput?.(bytes, cancelable);
  }

  // Refusing on a closing socket is what xterm.js's AttachAddon and ttyd both do.
  function sendEphemeral(text: string): boolean {
    // Before the socket's first size the server's grid is not this client's,
    // so a report's coordinates mean nothing there.
    if (
      isDisposed() ||
      connState.status !== "connected" ||
      connState.size === null ||
      connState.sock.readyState !== WS_OPEN
    ) {
      return false;
    }
    if (!connState.upgraded || !caps.ephemeralInput) {
      // RELIABLE input rather than nothing: an unrecognized text control is
      // dropped post-latch, and total silence is a worse failure than a stale
      // report (a click before a disconnect is retransmitted after the resume).
      return sendBinary(ephemeralEncoder.encode(text));
    }
    connState.sock.send(textControl({ type: "ephemeralInput", data: text }));
    return true;
  }

  function setClientFocus(focused: boolean): void {
    if (isDisposed()) {
      return;
    }
    clientFocused = focused;
    // Focus is STATE, so a report with no socket, or for a server that does not
    // derive the DEC 1004 answer, is DROPPED rather than held: the resumeAck
    // re-reports the current value on the new socket.
    if (connState.status !== "connected") {
      return;
    }
    if (!caps.serverFocus || !connState.upgraded) {
      return;
    }
    if (focused === focusReported) {
      return;
    }
    focusReported = focused;
    sendControl({ type: "focus", focused });
  }

  function sendControl(msg: ControlMessage): void {
    if (connState.status !== "connected") {
      return;
    }
    if (connState.upgraded) {
      connState.sock.send(textControl(msg));
      return;
    }
    connState.sock.send(controlFrame(msg));
  }

  // Validated because the server FLOORS an out-of-range resize rather than
  // dropping it. Isolated in a try/catch because this runs inside the open
  // handler after the connect timeout was cleared and BEFORE the resume is sent:
  // a throwing provider would otherwise leave the resume unsent and the state
  // stuck at "connecting" with no timeout left to rescue it.
  function readInitialSize(): { cols: number; rows: number } | null {
    let size: { cols: number; rows: number } | null;
    try {
      size = cb.initialSize === undefined ? cb.computeSize() : cb.initialSize();
    } catch (err) {
      console.warn("vterm: measuring the size at open threw; announcing no size", err);
      return null;
    }
    if (
      size === null ||
      !Number.isInteger(size.cols) ||
      !Number.isInteger(size.rows) ||
      size.cols <= 0 ||
      size.rows <= 0
    ) {
      return null;
    }
    return { cols: size.cols, rows: size.rows };
  }

  function sendResize(): void {
    if (isDisposed() || connState.status !== "connected") {
      return;
    }
    const live = connState;
    const { cols, rows } = cb.computeSize();
    if (
      isDisposed() ||
      connState !== live ||
      (live.size?.cols === cols && live.size.rows === rows)
    ) {
      return;
    }
    announcedSize = { cols, rows };
    sendControl({ type: "resize", cols, rows });
    live.size = { cols, rows };
    releaseInput(live);
  }

  // The one place a socket's input starts: the unacked rest is retransmitted
  // ahead of everything queued since. The host hears it last, so input it
  // sends from onInputReady queues behind the retransmit.
  function releaseInput(live: Extract<ConnState, { status: "connected" }>): void {
    if (live.inputReady || !live.resumed || live.size === null) {
      return;
    }
    for (const chunk of live.st.inFlight) {
      sendInputFrames(live.sock, live.upgraded, chunk);
    }
    live.inputReady = true;
    pumpInput();
    resyncGestureWhenReady();
    if (!isDisposed() && connState === live) {
      cb.onInputReady?.({ ...live.size });
    }
  }

  // A gesture recorded before the gap is cancelled once per socket, or the
  // application holds that button for the rest of the session. It waits for
  // BOTH the modes (before them the program may not be tracking) and the
  // socket's input start (before the resumeAck `upgraded` and `caps` are
  // unset, so the release would fall back to reliable, retransmittable input).
  function resyncGestureWhenReady(): void {
    if (
      isDisposed() ||
      !modes.isKnown() ||
      connState.status !== "connected" ||
      !connState.inputReady ||
      connState.gestureResynced
    ) {
      return;
    }
    connState.gestureResynced = true;
    mouse.resyncGesture();
  }

  function lastAnnouncedSize(): { cols: number; rows: number } | null {
    return isDisposed() || announcedSize === null ? null : { ...announcedSize };
  }

  // A boot-epoch change: every queued byte, sent or not, was meant for the old
  // process. Returns whether any input was dropped.
  function dropInputForDeadProcess(st: ResumeState): boolean {
    const lost = st.bytesSent > st.bytesAcked || st.pendingBytes > 0;
    resetLedger(st);
    dropPending(st);
    reportPending();
    return lost;
  }

  // A forgotten ledger is not a restart and not by itself a loss: only the
  // UNACKED remainder was at risk, so a session whose every byte was acked
  // resets silently (an iPad waking from a long sleep has nothing in flight).
  // Returns whether unacked input was dropped.
  function resetForgottenLedger(st: ResumeState): boolean {
    const lostUnacked = st.bytesSent > st.bytesAcked;
    resetLedger(st);
    return lostUnacked;
  }

  function scheduleReconnect(): void {
    // Both callers set "disconnected" before cb.onClose(), so a consumer that
    // reconnects from its own close hook lands here at "connecting"; scheduling
    // over it would OVERWRITE connState, hide that socket from connect()'s
    // double-call guard, and let the timer's connect() add a second live socket.
    if (
      isDisposed() ||
      connState.status === "reconnecting" ||
      connState.status === "connecting" ||
      connState.status === "connected"
    ) {
      return;
    }
    const step = nextBackoffDelay(reconnectDelay);
    reconnectDelay = step.nextBaseMs;
    const timer = setTimeout(() => {
      connState = { status: "disconnected" };
      connect();
    }, step.scheduledMs);
    connState = { status: "reconnecting", timer };
  }

  function cancelScheduledReconnect(): void {
    if (connState.status === "reconnecting") {
      clearTimeout(connState.timer);
      connState = { status: "disconnected" };
    }
  }

  function markActivity(): void {
    lastActivityAt = Date.now();
    probeSentAt = 0;
  }

  function startHeartbeat(): void {
    stopHeartbeat();
    markActivity();
    heartbeatTimer = setInterval(heartbeatTick, HEARTBEAT_INTERVAL_MS);
  }

  function stopHeartbeat(): void {
    if (heartbeatTimer !== null) {
      clearInterval(heartbeatTimer);
      heartbeatTimer = null;
    }
    probeSentAt = 0;
  }

  // The one place that decides a connected socket is stale: a probe after
  // enough silence, a reconnect after a probe goes unanswered.
  function heartbeatTick(): void {
    if (isDisposed() || connState.status !== "connected") {
      return;
    }
    // A hidden tab's timers are throttled or frozen, so a probe could fire
    // stale; the wake path handles foregrounding.
    if (typeof document !== "undefined" && document.visibilityState === "hidden") {
      return;
    }
    const now = Date.now();
    if (probeSentAt > 0) {
      if (now - probeSentAt >= PONG_TIMEOUT_MS) {
        probeSentAt = 0;
        reconnectNow();
      }
      return;
    }
    if (now - lastActivityAt >= IDLE_BEFORE_PROBE_MS) {
      probeSentAt = now;
      sendControl({ type: "ping" });
    }
  }

  // Leaves the connection disconnected with every session's resume state
  // intact, so a later connect() resumes cleanly.
  function teardown(): void {
    if (connState.status === "connecting" || connState.status === "connected") {
      // Abort BEFORE close: aborting detaches the listeners, so frames arriving
      // between close() and the close handshake are not processed twice.
      connState.abort.abort();
      try {
        connState.sock.close();
      } catch {
        /* ignore */
      }
    }
    stopHeartbeat();
    resetForNewSocket();
    renderer.clearSolicited();
    cancelScheduledReconnect();
    connState = { status: "disconnected" };
    modes.setKnown(false);
  }

  // Unconditional teardown, whatever connState says: on iOS wake the socket
  // reads OPEN for a while but is a zombie the OS froze during sleep, and
  // trusting that state left content printed during sleep missing until a
  // manual refresh. The resume protocol aligns by absolute index, so a
  // reconnect over a healthy socket costs one handshake and no duplicate output.
  function reconnectNow(): void {
    if (isDisposed() || connState.status === "incompatible" || connState.status === "ended") {
      return;
    }
    teardown();
    connect();
  }

  function setSession(id: string): void {
    if (isDisposed()) {
      return;
    }
    managed = true;
    ensureState(id);
    if (
      id === activeId &&
      (connState.status === "connected" || connState.status === "connecting")
    ) {
      return; // already serving this session
    }
    activeId = id;
    // Another session's modes never carry over, and neither does an old
    // snapshot of this one: the server may have changed them since.
    modes.setKnown(false);
    modes.applySnapshot(POWER_ON_MODES);
    reportPending();
    if (isDisposed() || connState.status === "incompatible") {
      return;
    }
    // A deliberate attach, so an ended session is shown again.
    teardown();
    connect();
  }

  function forgetSession(id: string): void {
    if (isDisposed()) {
      return;
    }
    sessions.delete(id);
    if (id === activeId) {
      activeId = null;
      teardown();
      reportPending();
    }
  }

  // An anchor computed from the full page size while the length is shrunken
  // serves a range that ends far from the reader.
  function historyBudget(): number {
    return isDisposed() ? 0 : history.effMax;
  }

  // A paced denial ARMS the coalesced pending demand rather than dropping: on an
  // idle session no future scroll or flush event would re-fire it, and a
  // byte-short continuation would stall forever.
  function requestHistory(fromAbs: number, maxLines: number): boolean {
    if (isDisposed() || !history.paging || !history.acked) {
      return false;
    }
    if (history.inFlight !== null) {
      return false;
    }
    if (
      !Number.isSafeInteger(fromAbs) ||
      !Number.isSafeInteger(maxLines) ||
      fromAbs < 0 ||
      maxLines < 1 ||
      // The subtraction form: the addition form is itself the overflow it
      // would exist to reject.
      fromAbs > Number.MAX_SAFE_INTEGER - maxLines
    ) {
      return false;
    }
    if (connState.status !== "connected" || !connState.upgraded) {
      return false;
    }
    const wait = takeHistoryToken();
    if (wait > 0) {
      armPendingDemand(wait);
      return false;
    }
    const bounded = Math.min(maxLines, history.effMax);
    const end = fromAbs + bounded;
    history.inFlight = { fromAbs, end };
    renderer.noteSolicited(fromAbs, end);
    // Through sendControl, which owns the ENCODING: this runs only on an
    // `upgraded` socket, where a v3 sentinel frame is written straight to the
    // PTY, typing the control's JSON into the user's shell.
    sendControl({ type: "history", fromAbs, maxLines: bounded });
    history.dataTimer = setTimeout(onHistoryDataTimeout, HISTORY_DATA_TIMEOUT_MS);
    return true;
  }

  // ONE timer that re-runs the FULL trigger when the bucket refills, rather than
  // replaying the denied request: the gap may have healed or the session
  // entered alt in the meantime.
  function armPendingDemand(waitMs: number): void {
    if (history.demandTimer !== null) {
      return;
    }
    history.demandTimer = setTimeout(() => {
      history.demandTimer = null;
      if (isDisposed()) {
        return;
      }
      renderer.maybeFetchHistory();
    }, waitMs);
  }

  // Release single-flight, drop to the floor and remember half the failed size
  // as the recovery ceiling (RFC 5681's `ssthresh` role): halving alone would
  // oscillate on a link that carries 500 but not 1000, while climbing back
  // toward the ceiling converges on the largest size the link carries.
  function onHistoryDataTimeout(): void {
    if (isDisposed()) {
      return;
    }
    const failed = history.inFlight;
    history.dataTimer = null;
    history.inFlight = null;
    renderer.clearSolicited();
    if (failed !== null) {
      const size = failed.end - failed.fromAbs;
      history.budgetCeiling = Math.max(HISTORY_MIN_PAGE, Math.floor(size / 2));
      history.effMax = HISTORY_MIN_PAGE;
    }
    armPendingDemand(0);
  }

  // A frame is the reply iff its `firstIndex` lies inside the request window.
  // CONTAINMENT decides the CONTROL effects separately: only a reply that fits
  // entirely inside the window releases single-flight and grows the budget, so a
  // timed-out larger reply sharing a retry's `fromAbs` has its intersection
  // applied and leaves the attempt's own reply to complete it. `raiseFloorTo` is
  // the CLAMP signal (`firstIndex > fromAbs`, or an empty reply condemning the
  // whole window).
  function correlateHistoryReply(msg: ScrollMessage): {
    correlated: boolean;
    raiseFloorTo: number | null;
    contained: boolean;
  } {
    const req = history.inFlight;
    if (req === null) {
      return { correlated: false, raiseFloorTo: null, contained: false };
    }
    if (msg.firstIndex < req.fromAbs || msg.firstIndex >= req.end) {
      return { correlated: false, raiseFloorTo: null, contained: false };
    }
    const count = msg.lines.length;
    let raiseFloorTo: number | null = null;
    if (count === 0) {
      raiseFloorTo = req.end;
    } else if (msg.firstIndex > req.fromAbs) {
      raiseFloorTo = msg.firstIndex;
    }
    const contained = msg.firstIndex + count <= req.end;
    if (contained) {
      if (history.dataTimer !== null) {
        clearTimeout(history.dataTimer);
        history.dataTimer = null;
      }
      history.inFlight = null;
      // Recovery: climb toward the remembered ceiling, never past it.
      history.effMax = Math.min(history.effMax * 2, history.budgetCeiling);
    }
    return { correlated: true, raiseFloorTo, contained };
  }

  function currentSessionId(): string {
    if (isDisposed()) {
      return "";
    }
    activeId ??= loadOrCreateSessionId();
    return activeId;
  }

  function disconnect(): void {
    if (isDisposed()) {
      return;
    }
    teardown();
  }

  function connect(): void {
    if (isDisposed() || connState.status === "incompatible") {
      return;
    }
    // A previous socket still CONNECTING/OPEN would be orphaned with its
    // handlers bound; aborting its controller detaches them so it cannot
    // deliver frames after the page has moved on.
    if (connState.status === "connecting" || connState.status === "connected") {
      connState.abort.abort();
      try {
        connState.sock.close();
      } catch {
        /* ignore */
      }
    }
    // A pending backoff timer would otherwise fire later and spawn a SECOND
    // socket beside the one created below, with the first one's listeners bound.
    cancelScheduledReconnect();
    modes.setKnown(false);
    renderer.clearGrid();

    cb.onConnecting?.();
    if (isDisposed()) {
      return;
    }

    // Captured for the socket's lifetime: a switch aborts this socket's
    // listeners, so a late frame is handled against the session it was opened for.
    const st = activeState();
    let url = wsURL(location.protocol, location.host, wsPath);
    if (managed) {
      url += (url.includes("?") ? "&" : "?") + "session=" + encodeURIComponent(st.id);
    }
    const sock = new WebSocket(url);
    sock.binaryType = "arraybuffer";

    // One AbortController governs THIS sock's listeners: every addEventListener
    // below passes its signal, so an abort removes them atomically.
    const connectAbort = new AbortController();
    const timeoutId = setTimeout(() => {
      // Abort algorithms run BEFORE the abort event, so aborting detaches the
      // "close" listener that would schedule the reconnect; a connect that never
      // opens would otherwise pin connState at "connecting" with no retry.
      connectAbort.abort();
      if (!isDisposed() && connState.status === "connecting" && connState.sock === sock) {
        stopHeartbeat();
        connState = { status: "disconnected" };
        modes.setKnown(false);
        cb.onClose();
        if (isDisposed()) {
          return;
        }
        scheduleReconnect();
      }
    }, 10_000);
    connectAbort.signal.addEventListener("abort", () => {
      clearTimeout(timeoutId);
      // Force-close so the OS-level socket goes away promptly, not only when
      // the browser completes its close handshake.
      try {
        sock.close();
      } catch {
        /* ignore */
      }
    });

    connState = { status: "connecting", sock, abort: connectAbort };

    sock.addEventListener(
      "open",
      () => {
        clearTimeout(timeoutId);
        // The size goes BEFORE the resume: the server applies a resize the
        // moment it decodes it, so the snapshot and replay come back at THIS
        // client's geometry. The resume still precedes every TEXT frame (the
        // server must see protocolVersion first) and every PTY input (input
        // before the resume is skipped by the received-byte ledger), which is
        // why cb.onOpen() runs after it.
        const size = readInitialSize();
        if (isDisposed()) {
          return;
        }
        if (size !== null) {
          sock.send(controlFrame({ type: "resize", cols: size.cols, rows: size.rows }));
          announcedSize = size;
        }
        // The server's reply is a function of the exact haveThrough and
        // replayMax sent, so the replay-jump prediction is computed from the
        // values remembered on the socket, never from store state a frame
        // between send and ack could have moved.
        resetForNewSocket();
        const sentHaveThrough = renderer.getReplayBoundary();
        // ALWAYS a number: the server clamps to the same ceiling
        // unconditionally, and a client that sent nothing would predict no
        // replay jump where one happened.
        const rawReplayMax = cb.getReplayMax?.() ?? renderer.replayMaxForResume();
        if (isDisposed()) {
          return;
        }
        const sentReplayMax =
          Number.isSafeInteger(rawReplayMax) && rawReplayMax >= 1
            ? Math.min(rawReplayMax, MAX_REPLAY_LINES)
            : MAX_REPLAY_LINES;
        history.sentHaveThrough = sentHaveThrough;
        history.sentReplayMax = sentReplayMax;
        // Binary-sentinel encoded, understood by every server revision; the
        // server replays everything after haveThrough, idempotent by index.
        sock.send(
          controlFrame({
            type: "resume",
            sessionId: resumeKey(st),
            sentBytes: st.bytesSent,
            haveThrough: sentHaveThrough,
            replayMax: sentReplayMax,
            protocolVersion: WIRE_PROTOCOL_VERSION,
            gridFrames: true,
          }),
        );
        // Every socket starts in v3 mode; the resumeAck decides the upgrade.
        const live: Extract<ConnState, { status: "connected" }> = {
          status: "connected",
          sock,
          abort: connectAbort,
          upgraded: false,
          resumed: false,
          size,
          inputReady: false,
          gestureResynced: false,
          st,
        };
        connState = live;
        reconnectDelay = INITIAL_DELAY_MS;
        cb.onOpen();
        if (isDisposed()) {
          return;
        }
        startHeartbeat();
      },
      { signal: connectAbort.signal },
    );

    // iOS Safari can deliver binary frames as Blob, whose conversion is async;
    // chaining keeps arrival order, which unordered resolution would corrupt.
    let blobChain: Promise<void> = Promise.resolve();

    sock.addEventListener(
      "message",
      (ev: MessageEvent) => {
        markActivity();
        if (ev.data instanceof ArrayBuffer) {
          try {
            handleDecoded(decodeWireBinary(ev.data));
          } catch (err) {
            // Logged with engine context, as the Blob branch does, so field
            // observability is the same for both frame types.
            console.error("vterm: dropped binary frame", err);
          }
          return;
        }
        if (ev.data instanceof Blob) {
          const blob = ev.data;
          blobChain = blobChain
            .then(() => blob.arrayBuffer())
            .then((ab) => {
              // A conversion already queued outlives the abort, and a frame from
              // a superseded socket must not reach handleDecoded, where its
              // resumeAck could reset or retransmit against the REPLACEMENT.
              if (
                isDisposed() ||
                connectAbort.signal.aborted ||
                connState.status !== "connected" ||
                connState.sock !== sock
              ) {
                return;
              }
              handleDecoded(decodeWireBinary(ab));
            })
            .catch((err: unknown) => {
              // A throw must NOT poison the chain: a rejected blobChain skips
              // every later frame's .then, and since markActivity() already ran
              // the liveness probe never fires, so the tab looks connected and
              // renders nothing.
              console.error("vterm: dropped binary (blob) frame", err);
            });
          return;
        }
        // A server text frame is undefined in the protocol and is ignored.
      },
      { signal: connectAbort.signal },
    );

    function markModesKnown(): void {
      if (modes.isKnown()) {
        return;
      }
      modes.setKnown(true);
      if (isDisposed()) {
        return;
      }
      pumpInput();
      resyncGestureWhenReady();
    }

    // An ack frees window room, so the pump runs after it.
    function ack(received: number): void {
      applyAck(st, received);
      pumpInput();
    }

    function handleDecoded(msg: ServerMessage | null): void {
      if (msg === null) {
        return;
      }
      if (msg.type === "resumeAck") {
        // A below-floor revision is definitive: stop the socket BEFORE the
        // consumer callbacks and latch the no-reconnect state. A missing tail
        // remains the version-silent compatibility path.
        if (
          msg.serverWireVersion !== undefined &&
          msg.serverWireVersion < MIN_SUPPORTED_SERVER_WIRE_VERSION
        ) {
          const reason = `server wire protocol ${msg.serverWireVersion} is below client minimum ${MIN_SUPPORTED_SERVER_WIRE_VERSION}; upgrade the server`;
          console.warn("vterm: refusing incompatible server wire protocol", reason);
          stopHeartbeat();
          connState = { status: "incompatible" };
          modes.setKnown(false);
          try {
            sock.close(WIRE_INCOMPATIBLE_CLOSE_CODE, reason);
          } finally {
            connectAbort.abort();
          }
          cb.onWireVersionMismatch?.(msg.serverWireVersion, WIRE_PROTOCOL_VERSION);
          cb.onWireIncompatible?.({
            source: "server-version",
            serverVersion: msg.serverWireVersion,
            clientVersion: WIRE_PROTOCOL_VERSION,
            minimumServerVersion: MIN_SUPPORTED_SERVER_WIRE_VERSION,
            reason,
          });
          return;
        }
        // A newer server may retain this client's baseline: warn and continue.
        if (msg.serverWireVersion !== undefined && msg.serverWireVersion > WIRE_PROTOCOL_VERSION) {
          console.warn(
            "vterm: server wire-protocol version is newer than client",
            "server",
            msg.serverWireVersion,
            "client",
            WIRE_PROTOCOL_VERSION,
            "- upgrade the client if terminal behavior is incorrect",
          );
          cb.onWireVersionMismatch?.(msg.serverWireVersion, WIRE_PROTOCOL_VERSION);
          if (isDisposed()) {
            return;
          }
        }
        // Typed-framing upgrade: the text transition goes FIRST, so WebSocket
        // ordering guarantees the server latches before any unsplit binary
        // input, and BEFORE the retransmit below so it already uses the
        // upgraded framing.
        if (
          msg.serverWireVersion !== undefined &&
          msg.serverWireVersion >= TYPED_FRAMING_MIN_VERSION &&
          connState.status === "connected" &&
          connState.sock === sock &&
          !connState.upgraded
        ) {
          sock.send(textControl({ type: "upgrade" }));
          connState.upgraded = true;
        }
        // 0 is the wire's "no epoch" (a server that does not report one).
        const epoch =
          msg.serverEpoch !== undefined && msg.serverEpoch !== 0 ? msg.serverEpoch : null;
        // Queued input was typed against the process this session last acked
        // or, before its first ack, against the bound store's content.
        const ackedEpochGone =
          epoch !== null && st.lastServerEpoch !== null && st.lastServerEpoch !== epoch;
        const typedAgainstStore = st.lastServerEpoch === null;
        if (epoch !== null) {
          st.lastServerEpoch = epoch;
        }
        // An absent flags tail (an older server) reads as unset bits.
        history.paging = msg.historyPaging === true;
        history.acked = true;
        const contentLost = renderer.applyResumeTransition({
          serverEpoch: epoch,
          committed: typeof msg.committed === "number" ? msg.committed : null,
          serverOldest: typeof msg.oldestIndex === "number" ? msg.oldestIndex : null,
          paging: history.paging,
          sentHaveThrough: history.sentHaveThrough,
          sentReplayMax: history.sentReplayMax,
        });
        let inputLost = false;
        if (ackedEpochGone || (typedAgainstStore && contentLost)) {
          inputLost = dropInputForDeadProcess(st);
          if (isDisposed()) {
            return;
          }
        }
        const restartReported = inputLost || contentLost;
        if (restartReported) {
          cb.onServerRestart?.();
          if (isDisposed()) {
            return;
          }
        }
        // Capabilities and the focus re-report run whether or not the ledger
        // was lost: a ledger loss is not a capability event, and the
        // long-absence attach that loses its ledger is exactly the one that
        // needs its focus asserted.
        caps = {
          ephemeralInput: msg.ephemeralInput === true,
          serverFocus: msg.serverFocus === true,
        };
        focusReported = null;
        setClientFocus(clientFocused);
        if (typeof msg.committed === "number" && typeof msg.oldestIndex === "number") {
          renderer.noteResumeBounds(msg.oldestIndex);
          cb.onResumeBounds?.(msg.committed, msg.oldestIndex);
          if (isDisposed()) {
            return;
          }
        }
        // The server cannot vouch for ANY previously sent input once it lost the
        // ledger, so replaying inFlight risks duplicate execution: drop it. The
        // explicit flag also covers bytesAcked === 0 (acks that never reached
        // us); the received=0 heuristic is the older server's form of the same
        // fact, skipped on a genuine first connect where bytesAcked is 0.
        if (msg.ledgerLost || (msg.received === 0 && st.bytesAcked > 0)) {
          if (resetForgottenLedger(st) && !restartReported) {
            cb.onServerRestart?.();
            if (isDisposed()) {
              return;
            }
          }
        } else {
          applyAck(st, msg.received);
        }
        if (connState.status !== "connected" || connState.sock !== sock) {
          return;
        }
        connState.resumed = true;
        releaseInput(connState);
        return;
      }
      if (msg.type === "ackOnly") {
        // Input was applied but no content frame carried the count (a silent
        // app, `read -s`). Transport-internal, never forwarded to onMessage.
        ack(msg.inputAck);
        return;
      }
      if (msg.type === "modes") {
        const snap: ModeSnapshot = {
          bracketedPaste: msg.bracketedPaste,
          applicationCursor: msg.applicationCursor,
          mouseSGR: msg.mouseSGR,
          focusReporting: msg.focusReporting,
          mouseMode: msg.mouseMode,
          applicationKeypad: msg.applicationKeypad,
          reverseVideo: msg.reverseVideo,
          mousePixels: msg.mousePixels,
          keyboardFlags: msg.keyboardFlags,
        };
        modes.applySnapshot(snap);
        markModesKnown();
        if (isDisposed()) {
          return;
        }
        if (typeof msg.inputAck === "number") {
          ack(msg.inputAck);
        }
        cb.onMessage(msg);
        return;
      }
      if (typeof msg.inputAck === "number") {
        ack(msg.inputAck);
      }
      // PRE-ACK CONTENT SUPPRESSION: on a busy session a live frame can arrive
      // before this socket's resumeAck, and its rows would mutate the store under
      // a stale residency cap. Field-aware: the inputAck was applied above, ED3
      // is a consumed one-shot the resume batch hard-codes false and is forwarded
      // as a rows-less clear, `bell` announces a screen the batch is about to
      // repaint and is dropped. The replay re-delivers the rows.
      if (!history.acked && (msg.type === "screen" || msg.type === "scroll")) {
        if (msg.type === "screen" && msg.scrollbackCleared) {
          cb.onMessage({ ...msg, changed: [], rows: [], bell: false });
        }
        return;
      }
      if (msg.type === "screen") {
        // A server that sends no modes frame must not hold input forever.
        markModesKnown();
        if (isDisposed()) {
          return;
        }
      }
      if (msg.type === "scroll") {
        const { correlated, raiseFloorTo, contained } = correlateHistoryReply(msg);
        if (correlated) {
          renderer.handleHistoryReply(msg, raiseFloorTo);
          // The solicited window is the store's permission to admit lines below
          // its stale-re-send watermark, so it outlives the apply; left open with
          // no request in flight, a later duplicate frame in that range could
          // resurrect an evicted row. An OVERSPILLING reply keeps it: its
          // attempt is still open.
          if (contained) {
            renderer.clearSolicited();
          }
          return;
        }
      }
      cb.onMessage(msg);
    }

    sock.addEventListener(
      "close",
      (ev: CloseEvent) => {
        // A superseded sock's listener is removed by its abort; the check
        // guards the window before the abort has propagated.
        if (connState.status !== "connecting" && connState.status !== "connected") {
          return;
        }
        if (connState.sock !== sock) {
          return;
        }
        stopHeartbeat();
        resetForNewSocket();
        renderer.clearSolicited();
        modes.setKnown(false);
        if (ev.code === WIRE_INCOMPATIBLE_CLOSE_CODE) {
          const reason =
            ev.reason ||
            "server rejected this client wire protocol; reload or upgrade the client/server";
          connState = { status: "incompatible" };
          cb.onWireIncompatible?.({
            source: "server-close",
            clientVersion: WIRE_PROTOCOL_VERSION,
            minimumServerVersion: MIN_SUPPORTED_SERVER_WIRE_VERSION,
            reason,
          });
          return;
        }
        // A definitive close can only be collected again by a reconnect, an
        // endless churn that reads as a flapping "Reconnecting…" banner.
        if (
          (ev.code === PROCESS_EXITED_CLOSE_CODE || ev.code === SESSION_UNKNOWN_CLOSE_CODE) &&
          cb.onProcessExit
        ) {
          connState = { status: "ended" };
          cb.onProcessExit();
          return;
        }
        connState = { status: "disconnected" };
        cb.onClose();
        if (isDisposed()) {
          return;
        }
        scheduleReconnect();
      },
      { signal: connectAbort.signal },
    );

    sock.addEventListener(
      "error",
      () => {
        /* no-op: prevents unhandled error */
      },
      { signal: connectAbort.signal },
    );
  }

  // Close BEFORE abort: the abort listener calls a bare `close()`, which would
  // emit a code-less close and make the later `close(1000)` a no-op; close-first
  // emits 1000 and the bare close that follows is the no-op instead.
  function dispose(): void {
    if (isDisposed()) {
      return;
    }
    disposed = true;
    if (connState.status === "connecting" || connState.status === "connected") {
      const { sock, abort } = connState;
      try {
        sock.close(1000);
      } catch {
        /* ignore */
      }
      abort.abort();
    }
    stopHeartbeat();
    clearHistoryTimers();
    if (pendingReportTimer !== null) {
      clearTimeout(pendingReportTimer);
      pendingReportTimer = null;
    }
    renderer.clearSolicited();
    cancelScheduledReconnect();
    connState = { status: "disconnected" };
    for (const st of sessions.values()) {
      resetLedger(st);
      dropPending(st);
    }
    sessions.clear();
    activeId = null;
    cb = DISPOSED_CALLBACKS;
  }

  return {
    sendBinary,
    cancelPendingInput,
    pendingInputBytes,
    holdInput,
    sendEphemeral,
    setClientFocus,
    sendResize,
    lastAnnouncedSize,
    reconnectNow,
    setSession,
    forgetSession,
    historyBudget,
    requestHistory,
    currentSessionId,
    disconnect,
    connect,
    dispose,
  };
}
