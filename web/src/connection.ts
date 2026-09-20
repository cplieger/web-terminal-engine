// WebSocket lifecycle with reliable input delivery across reconnects: the
// client → server half of the protocol (wire-binary.ts decodes the other). The
// client keeps an `outbox` of input bytes sent but not yet acknowledged and a
// `bytesSent` counter; on open it sends {type:"resume", sessionId, sentBytes},
// the server answers {type:"resumeAck", received} and every later frame carries
// inputAck, and the client trims the outbox by the acked count and retransmits
// the rest, which covers a ws.send() that reported success before TCP failed.
// The outbox is bounded at MAX_OUTBOX_BYTES; sendBinary refuses beyond it.

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
  | { status: "connected"; sock: WebSocket; abort: AbortController; upgraded: boolean }
  | { status: "reconnecting"; timer: ReturnType<typeof setTimeout>; delayMs: number }
  | { status: "incompatible" };

// The server's close code for "the child process has exited"
// (terminal/terminal.go statusProcessExited): definitive, so no reconnect.
const PROCESS_EXITED_CLOSE_CODE = 4001;

// The server's close code for "the manager does not know this session id"
// (statusUnknownSession). The server ACCEPTS the upgrade and closes with it so
// the client can read it (a pre-upgrade 404 is an opaque 1006 in browser JS);
// like 4001 it is definitive and takes the no-reconnect path.
const SESSION_UNKNOWN_CLOSE_CODE = 4004;

// Per-session reliable-input accounting, so a tab switch reconnects to another
// session without replaying the previous tab's unacked bytes onto it and
// without a false restart reset (each epoch is compared only against its own).
interface ResumeState {
  id: string; // server session id: the routing id (?session=); resumeKey derives the ledger key
  bytesSent: number; // total bytes ever passed to sendBinary for this session
  bytesAcked: number; // confirmed by server inputAck/resumeAck
  outbox: Uint8Array[]; // unacked chunks (sum of lengths = bytesSent - bytesAcked)
  outboxBytes: number; // running sum of outbox chunk lengths; keeps applyAck O(n) not O(n²)
  lastServerEpoch: number | null; // process-start nanos last seen for this session
  // A SEEDED epoch is a claim about content restored from storage, worth
  // something only once a server confirms or contradicts it, so a resume that
  // reports no epoch leaves it unverifiable and is handled as a restart. A
  // learned epoch must not get that treatment: an ack without an epoch from a
  // server that never reports one is ordinary operation.
  epochSeeded: boolean;
  // Restored synchronously into the ModeState by setSession, so a keystroke in
  // the switch window encodes under THIS session's modes, never the previous tab's.
  modes: ModeSnapshot;
}

function newResumeState(id: string): ResumeState {
  return {
    id,
    bytesSent: 0,
    bytesAcked: 0,
    outbox: [],
    outboxBytes: 0,
    lastServerEpoch: null,
    epochSeeded: false,
    modes: { ...POWER_ON_MODES },
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

/**
 * The server's clamp on `replayMax`, mirrored here so the client sends the
 * value the server will actually honor. The equality is load-bearing: the
 * replay-jump prediction computes `committed - sentReplayMax`, so a server
 * honoring something smaller would place the real replay start above the
 * prediction and leave a genuine jump undetected (docs/paged-scrollback.md §4.5).
 */
export const MAX_REPLAY_LINES = 2000;

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
 * Maximum bytes we keep in the outbox before refusing new input. 1
 * MiB at typical typing rates is hours of held keys; fast enough to
 * accept any normal disconnect, low enough that an offline tab can't
 * silently grow memory unbounded.
 */
export const MAX_OUTBOX_BYTES = 1 << 20;

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

// The ONE encoder for PTY input bytes, shared by sendBinary and
// retransmitOutbox so the two cannot disagree on framing. On a v4-upgraded
// socket the bytes go out verbatim; on a v3-mode socket each leading 0x00 byte
// is its own 1-byte message so no client frame can be misread as a control
// frame. Splitting alters message count, never byte count or order.
function sendInputFrames(sock: WebSocket, upgraded: boolean, chunk: Uint8Array): void {
  let rest = chunk;
  if (!upgraded) {
    while (rest.length > 0 && rest[0] === 0x00) {
      sock.send(new Uint8Array([0x00]).buffer);
      rest = rest.subarray(1);
    }
  }
  if (rest.length > 0) {
    sock.send(rest.buffer.slice(rest.byteOffset, rest.byteOffset + rest.byteLength) as ArrayBuffer);
  }
}

// A control for a v4-upgraded socket: bare JSON in a TEXT frame, the message
// type being the discriminator. controlFrame is the v3-mode sentinel form.
function textControl(msg: ControlMessage): string {
  return JSON.stringify(msg);
}

// Called whenever the server no longer holds the ledger these counters were
// keyed to: its replacement counts from zero, so non-zero counters would make
// every later ack read as stale and the outbox would never trim again.
function resetLedger(st: ResumeState): void {
  st.bytesSent = 0;
  st.bytesAcked = 0;
  st.outbox.length = 0;
  st.outboxBytes = 0;
}

// Drops chunks from the front of the outbox until the unacked total matches
// (bytesSent - newAck); O(chunks dropped) through the running outboxBytes.
function applyAck(st: ResumeState, received: number): void {
  if (received <= st.bytesAcked) {
    return;
  }
  st.bytesAcked = Math.min(received, st.bytesSent);
  const targetUnacked = st.bytesSent - st.bytesAcked;
  while (st.outbox.length > 0 && st.outboxBytes > targetUnacked) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- length checked above
    const head = st.outbox[0]!;
    const dropFromHead = st.outboxBytes - targetUnacked;
    if (head.length <= dropFromHead) {
      st.outbox.shift();
      st.outboxBytes -= head.length;
    } else {
      st.outbox[0] = head.subarray(dropFromHead);
      st.outboxBytes -= dropFromHead;
      break;
    }
  }
}

// After the resumeAck adjusted bytesAcked, replay what is still unacked.
function retransmitOutbox(sock: WebSocket, upgraded: boolean, st: ResumeState): void {
  for (const chunk of st.outbox) {
    sendInputFrames(sock, upgraded, chunk);
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
  onOutboxFull?(): void;
  /**
   * Fired instead of onClose when the server closes with a DEFINITIVE code:
   * process-exited (4001) or unknown-session (4004). When wired, the connection
   * does NOT auto-reconnect that socket and does not call onClose for it; the
   * consumer may still reconnect explicitly. When absent, every close keeps
   * the transient treatment (onClose plus backoff reconnect).
   */
  onProcessExit?(): void;
  /**
   * Fired when the client's queued input can no longer be trusted: the server's
   * boot-epoch changed, or the server no longer holds this session's input
   * ledger AND the outbox still held unacked bytes. The ledger has been reset
   * by the time it fires, so subsequent input starts from zero.
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
   * The size to announce BEFORE asking to resume, or null while the client
   * cannot measure itself trustworthily (web fonts loading). Announcing first
   * makes the resume snapshot arrive at this client's own geometry, with the
   * app's SIGWINCH redraw after a coherent snapshot instead of interleaved with
   * the replay. Null is identical to omitting the callback.
   */
  initialSize?(): { cols: number; rows: number } | null;
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
    | "maybeFetchHistory"
  >;
  /** Receives every inbound modes frame and the restored snapshot on setSession. */
  modes: Pick<ModeState, "applySnapshot">;
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
   * Queue input for reliable delivery. Returns false when the outbox is full
   * (onOutboxFull has fired) or the connection is disposed. Any byte sequence is
   * deliverable, leading NULs included.
   */
  sendBinary(data: Uint8Array): boolean;
  /**
   * Deliver BEST-EFFORT input (a mouse report): sent only on a live socket, never
   * queued or retransmitted. Returns whether it was sent; on false the caller
   * FORGETS the event. Falls back to reliable input against a server that did
   * not declare `ephemeralInput`, because total silence is a worse failure.
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
   * Measure through `computeSize` and announce the size; a size equal to the last
   * one SENT is dropped, and nothing is queued while no socket is connected.
   */
  sendResize(): void;
  /** Replace whatever socket exists with a fresh one immediately, skipping the backoff. */
  reconnectNow(): void;
  /**
   * Switch the live socket to server session `id`, keeping every session's resume
   * state; marks the connection managed (the URL carries `?session=`). A no-op
   * when `id` is already the active, connected session.
   */
  setSession(id: string): void;
  /** Drop a session's resume state; tears the socket down without reconnecting if it was active. */
  forgetSession(id: string): void;
  /**
   * Seed the server boot epoch a session's PERSISTED content belongs to, before
   * connecting, so the first resumeAck can detect a restart. A zero or non-finite
   * epoch, or a session that already knows its epoch, is ignored.
   */
  adoptPersistedEpoch(sessionId: string, epoch: number): void;
  /** The current adaptive page budget; read it for both a request's length and its anchor. */
  historyBudget(): number;
  /**
   * Request at most `maxLines` lines of history from `fromAbs`. Returns whether
   * it went out; it declines when paging is undeclared, unacked, in flight or
   * paced, and a paced denial arms a coalesced retry.
   */
  requestHistory(fromAbs: number, maxLines: number): boolean;
  /** The server boot epoch last observed for a session, or 0 when none is known. */
  serverEpochOf(sessionId: string): number;
  /** The session id the socket serves, minting the unmanaged id if none is set; "" once disposed. */
  currentSessionId(): string;
  /** Tear down the live socket without reconnecting; resume state is kept. */
  disconnect(): void;
  /**
   * Open the WebSocket; readiness is reported through onOpen, and a connect that
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
  let lastSentCols = 0;
  let lastSentRows = 0;

  const sessions = new Map<string, ResumeState>();
  // The session the live socket serves; null until the first connect or
  // setSession, when the unmanaged path mints a sessionStorage-backed id.
  let activeId: string | null = null;
  // managed = a consumer selected sessions through setSession, so the URL
  // carries ?session=<id>. Unmanaged keeps the bare wsPath and a sessionStorage
  // id, which survives iOS tab-suspend and BFCache so a reload resumes rather
  // than orphaning its outbox.
  let managed = false;

  // In managed mode the routing id is SHARED by every device attached to the
  // session, but the server-side input ledger must be PER SENDER: with a shared
  // key, device B's input advances the ledger and the ack reaches device A,
  // whose applyAck trims bytes the server never received from A. So the resume
  // frame carries `<serverSessionId>#<clientInstanceId>` while the URL keeps the
  // routing id. Page-lifetime and NOT persisted: a reload is a fresh sender with
  // an empty outbox. Lazy so a cryptoless environment throws on first CONNECT.
  let clientInstanceId: string | null = null;

  function resumeKey(st: ResumeState): string {
    if (!managed) {
      return st.id;
    }
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

  // --- demand-paged scrollback: per-socket fetch state (docs/paged-scrollback.md) ---

  let history: HistoryState = newHistoryState();

  let caps: ServerCaps = { ephemeralInput: false, serverFocus: false };

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

  function sendBinary(data: Uint8Array): boolean {
    if (isDisposed()) {
      return false;
    }
    const st = activeState();
    if (st.outboxBytes + data.length > MAX_OUTBOX_BYTES) {
      cb.onOutboxFull?.();
      return false;
    }
    // Bytes leave the outbox only when the server acks them, so a network blip
    // after a successful ws.send() still retransmits.
    const copy = new Uint8Array(data); // defensive copy (caller may reuse buffer)
    st.outbox.push(copy);
    st.outboxBytes += copy.length;
    st.bytesSent += copy.length;
    if (connState.status === "connected") {
      sendInputFrames(connState.sock, connState.upgraded, copy);
    }
    return true;
  }

  // Refusing on a closing socket is what xterm.js's AttachAddon and ttyd both do.
  function sendEphemeral(text: string): boolean {
    if (isDisposed() || connState.status !== "connected" || connState.sock.readyState !== WS_OPEN) {
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
      size = cb.initialSize?.() ?? null;
    } catch (err) {
      console.warn("vterm: initialSize provider threw; announcing no size", err);
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

  // A fresh socket clears the dedup baseline as it opens, so the consumer's next
  // call after a reconnect always goes out.
  function sendResize(): void {
    if (isDisposed() || connState.status !== "connected") {
      return;
    }
    const { cols, rows } = cb.computeSize();
    if (isDisposed() || (cols === lastSentCols && rows === lastSentRows)) {
      return;
    }
    lastSentCols = cols;
    lastSentRows = rows;
    sendControl({ type: "resize", cols, rows });
  }

  // Reserved for a boot-epoch change, the one cause where "the server
  // restarted" is literally true.
  function resetSessionAfterRestart(st: ResumeState): void {
    resetLedger(st);
    cb.onServerRestart?.();
  }

  // A forgotten ledger is not a restart and not by itself a loss: only the
  // UNACKED remainder was at risk, so a session whose every byte was acked
  // resets silently. Reporting that common case was a real defect: an iPad that
  // slept for half an hour woke to a "server restarted" banner with an empty
  // outbox and a fully applied session.
  function resetForgottenLedger(st: ResumeState): void {
    const lostUnacked = st.bytesSent > st.bytesAcked;
    resetLedger(st);
    if (lostUnacked) {
      cb.onServerRestart?.();
    }
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
    connState = { status: "reconnecting", timer, delayMs: step.scheduledMs };
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
  }

  // Unconditional teardown, whatever connState says: on iOS wake the socket
  // reads OPEN for a while but is a zombie the OS froze during sleep, and
  // trusting that state left content printed during sleep missing until a
  // manual refresh. The resume protocol aligns by absolute index, so a
  // reconnect over a healthy socket costs one handshake and no duplicate output.
  function reconnectNow(): void {
    if (isDisposed() || connState.status === "incompatible") {
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
    const target = ensureState(id);
    if (
      id === activeId &&
      (connState.status === "connected" || connState.status === "connecting")
    ) {
      return; // already serving this session
    }
    activeId = id;
    // SYNCHRONOUSLY, so a keystroke in the switch window (before the new
    // session's modes frame arrives) encodes under the target's modes.
    modes.applySnapshot(target.modes);
    reconnectNow();
  }

  function forgetSession(id: string): void {
    if (isDisposed()) {
      return;
    }
    sessions.delete(id);
    if (id === activeId) {
      activeId = null;
      teardown();
    }
  }

  // Restart detection is otherwise in-memory, which is wrong for a store
  // hydrated from disk: a restarted server begins its indices at 0 again and the
  // hydrated store would refuse the new session's output. Zero or non-finite
  // means "never recorded"; a session that already knows its epoch is left
  // alone, or a genuine restart could read as agreement.
  function adoptPersistedEpoch(sessionId: string, epoch: number): void {
    if (isDisposed() || !Number.isFinite(epoch) || epoch === 0) {
      return;
    }
    const st = ensureState(sessionId);
    if (st.lastServerEpoch !== null) {
      return;
    }
    st.lastServerEpoch = epoch;
    // A CLAIM, not an observation: a server that reports no epoch never
    // contradicts it, so the resume handler treats an unconfirmed seed as a restart.
    st.epochSeeded = true;
  }

  // An anchor computed from the full page size while the length is shrunken
  // serves a range that ends far from the reader (docs/paged-scrollback.md §4.2).
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

  // 0 is deliberately the value `adoptPersistedEpoch` ignores, so a snapshot taken
  // without an epoch cannot later be mistaken for one taken under a known epoch.
  function serverEpochOf(sessionId: string): number {
    return isDisposed() ? 0 : (sessions.get(sessionId)?.lastServerEpoch ?? 0);
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
        // A new socket has no size on record with the server.
        lastSentCols = 0;
        lastSentRows = 0;
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
          lastSentCols = size.cols;
          lastSentRows = size.rows;
        }
        // The server's reply is a function of the exact haveThrough and
        // replayMax sent, so the replay-jump prediction is computed from the
        // values remembered on the socket, never from store state a frame
        // between send and ack could have moved (docs/paged-scrollback.md §4.5).
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
          }),
        );
        // Every socket starts in v3 mode; the resumeAck decides the upgrade.
        connState = { status: "connected", sock, abort: connectAbort, upgraded: false };
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
        // Server-restart detection: an epoch that differs from the recorded one
        // means the new process has no record of the previous boot's input.
        const epoch = msg.serverEpoch;
        let epochChanged = false;
        if (epoch !== undefined && epoch !== 0) {
          if (st.lastServerEpoch !== null && st.lastServerEpoch !== epoch) {
            epochChanged = true;
            resetSessionAfterRestart(st);
            if (isDisposed()) {
              return;
            }
          }
          st.lastServerEpoch = epoch;
          st.epochSeeded = false;
        } else if (st.epochSeeded) {
          // A SEEDED epoch this server will not confirm: unverifiable restored
          // content is handled as a restart, because the alternative is
          // presenting a previous run's output as live and then refusing the new
          // session's low absolute indices.
          epochChanged = true;
          resetSessionAfterRestart(st);
          if (isDisposed()) {
            return;
          }
          st.lastServerEpoch = null;
          st.epochSeeded = false;
        }
        // An absent flags tail (an older server) reads as unset bits.
        history.paging = msg.historyPaging === true;
        history.acked = true;
        // Capabilities, the focus re-report and the store's ONE ack transition
        // all run BEFORE the ledger-lost returns below: a ledger loss is not a
        // capability event, and the long-absence attach that loses its ledger
        // is exactly the one that needs its focus asserted and carries a replay
        // jump (docs/paged-scrollback.md §4.5).
        caps = {
          ephemeralInput: msg.ephemeralInput === true,
          serverFocus: msg.serverFocus === true,
        };
        focusReported = null;
        setClientFocus(clientFocused);
        renderer.applyResumeTransition({
          epochChanged,
          committed: typeof msg.committed === "number" ? msg.committed : null,
          serverOldest: typeof msg.oldestIndex === "number" ? msg.oldestIndex : null,
          paging: history.paging,
          sentHaveThrough: history.sentHaveThrough,
          sentReplayMax: history.sentReplayMax,
        });
        if (typeof msg.committed === "number" && typeof msg.oldestIndex === "number") {
          renderer.noteResumeBounds(msg.committed, msg.oldestIndex);
          cb.onResumeBounds?.(msg.committed, msg.oldestIndex);
          if (isDisposed()) {
            return;
          }
        }
        // The server cannot vouch for ANY previously sent input once it lost the
        // ledger, so replaying the outbox risks duplicate execution: drop it. The
        // explicit flag also covers bytesAcked === 0 (acks that never reached
        // us); the received=0 heuristic is the older server's form of the same
        // fact, skipped on a genuine first connect where bytesAcked is 0.
        if (msg.ledgerLost) {
          resetForgottenLedger(st);
          return;
        }
        if (msg.received === 0 && st.bytesAcked > 0) {
          resetForgottenLedger(st);
          return;
        }
        applyAck(st, msg.received);
        retransmitOutbox(
          sock,
          connState.status === "connected" && connState.sock === sock && connState.upgraded,
          st,
        );
        // The first instant a best-effort report can leave (`upgraded` and
        // `caps` are both set); a gesture recorded before the gap is cancelled
        // now or the application holds that button for the rest of the session.
        mouse.resyncGesture();
        return;
      }
      if (msg.type === "ackOnly") {
        // Input was applied but no content frame carried the count (a silent
        // app, `read -s`). Transport-internal, never forwarded to onMessage.
        applyAck(st, msg.inputAck);
        return;
      }
      if (msg.type === "modes") {
        // The cached session and the ModeState are the same session by
        // construction: a superseded socket's listeners are aborted.
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
        st.modes = snap;
        modes.applySnapshot(snap);
        if (typeof msg.inputAck === "number") {
          applyAck(st, msg.inputAck);
        }
        cb.onMessage(msg);
        return;
      }
      if (typeof msg.inputAck === "number") {
        applyAck(st, msg.inputAck);
      }
      // PRE-ACK CONTENT SUPPRESSION (docs/paged-scrollback.md §4.5): on a busy
      // session a live frame can arrive before this socket's resumeAck, and its
      // rows would mutate the store under a stale residency cap. Field-aware:
      // the inputAck was applied above, ED3 is a consumed one-shot the resume
      // batch hard-codes false and is forwarded as a rows-less clear, `bell`
      // announces a screen the batch is about to repaint and is dropped. The
      // rows are lossless by supersession: the replay re-delivers them.
      if (!history.acked && (msg.type === "screen" || msg.type === "scroll")) {
        if (msg.type === "screen" && msg.scrollbackCleared) {
          cb.onMessage({ ...msg, changed: [], rows: [], bell: false });
        }
        return;
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
        connState = { status: "disconnected" };
        // A definitive close can only be collected again by a reconnect, an
        // endless churn that reads as a flapping "Reconnecting…" banner.
        if (
          (ev.code === PROCESS_EXITED_CLOSE_CODE || ev.code === SESSION_UNKNOWN_CLOSE_CODE) &&
          cb.onProcessExit
        ) {
          cb.onProcessExit();
          return;
        }
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
    renderer.clearSolicited();
    cancelScheduledReconnect();
    connState = { status: "disconnected" };
    for (const st of sessions.values()) {
      resetLedger(st);
    }
    sessions.clear();
    activeId = null;
    cb = DISPOSED_CALLBACKS;
  }

  return {
    sendBinary,
    sendEphemeral,
    setClientFocus,
    sendResize,
    reconnectNow,
    setSession,
    forgetSession,
    adoptPersistedEpoch,
    historyBudget,
    requestHistory,
    serverEpochOf,
    currentSessionId,
    disconnect,
    connect,
    dispose,
  };
}
