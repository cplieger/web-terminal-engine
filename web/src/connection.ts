// WebSocket lifecycle with reliable input delivery across reconnects.
//
// This is the client → server half of the terminal protocol (the
// server → client half is the binary screen/scroll/modes wire format
// decoded by wire-binary.ts). It owns the socket, the reconnect
// backoff, and the resume/inputAck reliability layer.
//
// Protocol (resume / inputAck):
//   - Client maintains a `sessionId` (UUID) for the page lifetime, an
//     `outbox` of input bytes sent but not yet acknowledged, and a
//     `bytesSent` counter.
//   - On WS open, client sends control: {type:"resume", sessionId, sentBytes}.
//   - Server replies with {type:"resumeAck", received:M}; subsequent
//     screen/scroll messages also carry inputAck = bytesReceived. Client
//     trims the outbox by acked count and retransmits the remainder.
//   - This handles the network-blip failure mode where ws.send() reports
//     success but TCP couldn't deliver before the connection broke.
//
// Outbox is bounded at MAX_OUTBOX_BYTES; once full, sendBinary refuses
// new input and surfaces the failure via onOutboxFull. This prevents
// holding-down a key during a long disconnect from growing the outbox
// without bound.

import { wsURL } from "./wsurl.js";
import { WS_PATH } from "./routes.js";
import { controlFrame } from "./wire.js";
import { decodeWireBinary, WIRE_PROTOCOL_VERSION } from "./wire-binary.js";
import * as modes from "./modes.js";
import type { ControlMessage, ServerMessage } from "./types.js";
import { INITIAL_DELAY_MS, nextBackoffDelay } from "./reconnect.js";

type ConnState =
  | { status: "disconnected" }
  | { status: "connecting"; sock: WebSocket; abort: AbortController }
  | { status: "connected"; sock: WebSocket; abort: AbortController }
  | { status: "reconnecting"; timer: ReturnType<typeof setTimeout>; delayMs: number };

// The server's application close code for "the session's child process has
// exited" (terminal/terminal.go statusProcessExited). It marks a close as
// definitive — the session cannot produce output again — as opposed to a
// transient network drop that the backoff reconnect should heal. Private
// application range (4000-4999) per RFC 6455.
const PROCESS_EXITED_CLOSE_CODE = 4001;

let connState: ConnState = { status: "disconnected" };
let reconnectDelay = INITIAL_DELAY_MS;
let lastSentCols = 0;
let lastSentRows = 0;
let wsPath: string = WS_PATH;

// --- Per-session resume state (the switching cache's connection half) ---
//
// Each server session carries its own reliable-input accounting: an outbox of
// unacked bytes, byte counters, and the last server boot-epoch it saw. Scoping
// this per session (rather than one module-global set) is what lets a tab switch
// reconnect to a different session without replaying the previous tab's unacked
// bytes onto it and without firing a false server-restart reset, because each
// session's epoch is compared only against its own bootEpoch (design section 8).
interface ResumeState {
  id: string; // server session id: both the routing id (?session=) and the resume id
  bytesSent: number; // total bytes ever passed to sendBinary for this session
  bytesAcked: number; // confirmed by server inputAck/resumeAck
  outbox: Uint8Array[]; // unacked chunks (sum of lengths = bytesSent - bytesAcked)
  outboxBytes: number; // running sum of outbox chunk lengths; keeps applyAck O(n) not O(n²)
  lastServerEpoch: number | null; // process-start nanos last seen for this session
}

const sessions = new Map<string, ResumeState>();
// The session the live socket currently serves. null until the first connect or
// setSession; the unmanaged single-terminal path lazily creates a default
// session with a sessionStorage-backed id.
let activeId: string | null = null;
// managed = a consumer selected sessions explicitly via setSession, so the WS URL
// carries ?session=<id>. Unmanaged keeps the bare wsPath and a sessionStorage id,
// preserving the original single-terminal behavior and its iOS-resume semantics
// (sessionStorage survives iOS tab-suspend/BFCache, so an unmanaged reload
// resumes rather than orphaning its outbox). A tabbed shell is managed; it
// rebuilds tabs from GET /api/sessions on reload (section 17), so it needs no
// client-side id persistence.
let managed = false;

const SESSION_ID_KEY = "vterm-session-id";

function newResumeState(id: string): ResumeState {
  return { id, bytesSent: 0, bytesAcked: 0, outbox: [], outboxBytes: 0, lastServerEpoch: null };
}

function ensureState(id: string): ResumeState {
  let s = sessions.get(id);
  if (s === undefined) {
    s = newResumeState(id);
    sessions.set(id, s);
  }
  return s;
}

// activeState returns the ResumeState the live socket serves, lazily creating
// the default (unmanaged) session from a sessionStorage-backed id on first use.
function activeState(): ResumeState {
  activeId ??= loadOrCreateSessionId();
  return ensureState(activeId);
}

// --- Client-side liveness (bug 2 defense-in-depth) ---
//
// On iOS wake, visibilitychange + pageshow fire and call reconnectNow(),
// which is the primary fix. But a socket can also go silently half-open
// without any wake event (a NAT/idle timeout on a backgrounded-then-
// foregrounded tab, a flaky network that drops the path without a close
// frame). The socket then reads OPEN forever and delivers nothing. The
// server's ping loop notices the dead client, but those are WS-protocol
// pings the browser answers without surfacing to JS, so the client can't
// see them. So the client runs its own probe: after a stretch of silence
// it sends an app-level ping; the server echoes a pong. Any inbound frame
// (the pong, or normal output) proves the socket is alive and clears the
// probe. If the probe goes unanswered, the socket is stale and we
// reconnectNow() — which resumes by absolute index, so nothing is lost or
// duplicated. The probe is what distinguishes "idle but alive" from
// "dead": without it, a quiet-but-healthy terminal would reconnect-flap.
let lastActivityAt = 0; // Date.now() of the last inbound frame (any kind)
let probeSentAt = 0; // Date.now() the outstanding probe ping was sent; 0 = none
let heartbeatTimer: ReturnType<typeof setInterval> | null = null;

/** How often liveness is evaluated. */
const HEARTBEAT_INTERVAL_MS = 5_000;
/** Inbound silence that must elapse before we actively probe with a ping. */
const IDLE_BEFORE_PROBE_MS = 10_000;
/** How long an unanswered probe is tolerated before declaring the socket stale. */
const PONG_TIMEOUT_MS = 7_000;

/**
 * Maximum bytes we keep in the outbox before refusing new input. 1
 * MiB at typical typing rates is hours of held keys; fast enough to
 * accept any normal disconnect, low enough that an offline tab can't
 * silently grow memory unbounded.
 */
export const MAX_OUTBOX_BYTES = 1 << 20;

function loadOrCreateSessionId(): string {
  // sessionStorage is per-tab and survives most iOS lifecycle events
  // (suspend/resume, BFCache restore, page reload). It does NOT survive
  // a true tab close + reopen, which is the desired semantic: a fresh
  // tab should be a fresh terminal session, not a resume of an older one.
  try {
    const existing = sessionStorage.getItem(SESSION_ID_KEY);
    if (existing) {
      return existing;
    }
    const fresh = generateSessionId();
    sessionStorage.setItem(SESSION_ID_KEY, fresh);
    return fresh;
  } catch {
    // Private mode or storage disabled — fall back to in-memory only.
    // Reload-as-new-session semantics in this fallback path are
    // unavoidable; the outbox-clear safeguard in handleResumeAck below
    // protects against duplicate retransmission when the server returns
    // bytesReceived=0 for a session it doesn't recognize.
    return generateSessionId();
  }
}

// Exported for unit testing of the RNG fallback. Not part of the
// stable client API surface; callers use loadOrCreateSessionId.
export function generateSessionId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  // Fallback when crypto.randomUUID is unavailable. randomUUID requires a
  // secure context (HTTPS/localhost); getRandomValues does not, so it
  // covers plain-HTTP origins while still being a CSPRNG. sessionId is a
  // resume token the server trusts to re-attach a client to its prior
  // session, so it must not be predictable — Math.random() (a non-crypto
  // PRNG whose state is recoverable from output) would allow guessing
  // another client's session. Emit 16 random bytes as hex (128 bits).
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }
  // No Web Crypto at all: refuse rather than mint a guessable token.
  throw new Error("vterm: no cryptographically secure RNG available for session id");
}

export interface Callbacks {
  onMessage(msg: ServerMessage): void;
  onOpen(): void;
  onClose(): void;
  onConnecting?(): void;
  onOutboxFull?(): void;
  /** Fired instead of onClose when the server closes with the
   *  process-exited application close code (4001): the session's child
   *  process has ended, so the close is DEFINITIVE, not a transient drop.
   *  When this callback is wired, the module does NOT auto-reconnect that
   *  socket — reconnecting a dead session just replays its final screen and
   *  earns another 4001 (the endless "Reconnecting…" flash) — and does not
   *  call onClose for it. The consumer decides what "ended" looks like
   *  (banner, tab state) and may still reconnect explicitly (setSession /
   *  reconnectNow) to re-view the final screen. When absent, every close
   *  keeps the legacy transient treatment (onClose + backoff reconnect), so
   *  existing consumers are unaffected until they opt in. */
  onProcessExit?(): void;
  /** Fired when the server's boot-epoch in resumeAck differs from the
   *  one observed on a previous connection — i.e. the server has
   *  restarted. By the time this fires, the connection module has
   *  already reset bytesSent/bytesAcked/outbox so subsequent input
   *  starts from zero. UI should clear scrollback and surface a
   *  banner so the user knows old input may have been lost. */
  onServerRestart?(): void;
  computeSize(): { cols: number; rows: number };
  /** Returns the highest absolute line index the client currently holds, or
   *  -1 if it holds nothing. Sent as `haveThrough` on resume so the server
   *  replays only the lines missed (e.g. printed while the device slept).
   *  When absent, the client requests a full retained replay (-1). */
  getHaveThrough?(): number;
  /** Fired on resume with the server's retained-history bounds: `committed`
   *  is one past the newest retained line, `oldest` the oldest retained
   *  absolute index. The consumer forwards these to the renderer/store so it
   *  can tell a genuine history trim (the server evicted lines the client was
   *  missing) from a still-loading state. Resync guard 8.2.2. */
  onResumeBounds?(committed: number, oldest: number): void;
  /** Optional WebSocket endpoint path (default "/ws"). vibekit serves
   *  the shell at "/api/shell/ws"; web-terminal-kiro at "/ws". */
  wsPath?: string;
}

let cb: Callbacks | null = null;

export function init(callbacks: Callbacks): void {
  cb = callbacks;
  if (callbacks.wsPath !== undefined) {
    wsPath = callbacks.wsPath;
  }
}

/**
 * sendBinary queues data for delivery. Returns true if accepted, false
 * if the outbox is full (caller should surface a UI signal that input
 * was dropped). Always copies the input to defend against caller-side
 * buffer reuse.
 */
export function sendBinary(data: Uint8Array): boolean {
  const st = activeState();
  if (st.outboxBytes + data.length > MAX_OUTBOX_BYTES) {
    cb?.onOutboxFull?.();
    return false;
  }
  // Always go through the active session's outbox. Bytes leave it only when the
  // server explicitly acks them — guarantees correct retransmission after a
  // network blip even if ws.send() reported success.
  const copy = new Uint8Array(data); // defensive copy (caller may reuse buffer)
  st.outbox.push(copy);
  st.outboxBytes += copy.length;
  st.bytesSent += copy.length;
  if (connState.status === "connected") {
    connState.sock.send(copy.buffer.slice(copy.byteOffset, copy.byteOffset + copy.byteLength));
  }
  return true;
}

function sendControl(msg: ControlMessage): void {
  if (connState.status !== "connected") {
    return;
  }
  connState.sock.send(controlFrame(msg));
}

export function sendResize(): void {
  if (connState.status !== "connected" || !cb) {
    return;
  }
  const { cols, rows } = cb.computeSize();
  if (cols === lastSentCols && rows === lastSentRows) {
    return;
  }
  lastSentCols = cols;
  lastSentRows = rows;
  sendControl({ type: "resize", cols, rows });
}

// resetSessionAfterRestart clears a session's reliable-input accounting and
// notifies the UI. Called when the server's boot epoch changes (restart) or when
// the server no longer recognizes the session (received=0 with prior acks) -- both
// invalidate the local bytesSent/bytesAcked/outbox state.
function resetSessionAfterRestart(st: ResumeState): void {
  st.bytesSent = 0;
  st.bytesAcked = 0;
  st.outbox.length = 0;
  st.outboxBytes = 0;
  cb?.onServerRestart?.();
}

// applyAck drops chunks from the front of the session's outbox until the
// running total of unacked bytes matches (bytesSent - newAck). Runs in
// O(chunks_dropped) by tracking outboxBytes incrementally rather than
// re-summing on every loop iteration.
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

// On reconnect, after sending the resume control message and getting resumeAck,
// replay anything still in the session's outbox over its (now open) socket. The
// server has adjusted bytesAcked already — only unacked bytes remain.
function retransmitOutbox(sock: WebSocket, st: ResumeState): void {
  for (const chunk of st.outbox) {
    sock.send(
      chunk.buffer.slice(chunk.byteOffset, chunk.byteOffset + chunk.byteLength) as ArrayBuffer,
    );
  }
}

function scheduleReconnect(): void {
  if (connState.status === "reconnecting") {
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

// markActivity records that the socket just delivered a frame. Any frame —
// the pong, a screen update, anything — proves the socket is alive, so it
// refreshes the liveness clock and clears any outstanding probe.
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

// heartbeatTick is the one place that decides a connected socket is stale.
// It never touches scrollTop, never reconnects a healthy socket, and never
// probes a backgrounded tab (timer throttling makes hidden-tab timing
// meaningless, and the wake path handles foregrounding). Its only actions
// are: send a probe after enough silence, or reconnect after a probe goes
// unanswered.
function heartbeatTick(): void {
  if (connState.status !== "connected") {
    return;
  }
  // A hidden tab is handled by visibilitychange/pageshow on wake; probing it
  // is pointless (its timers are throttled or frozen) and could fire stale.
  if (typeof document !== "undefined" && document.visibilityState === "hidden") {
    return;
  }
  const now = Date.now();
  if (probeSentAt > 0) {
    if (now - probeSentAt >= PONG_TIMEOUT_MS) {
      // The probe drew no reply (nor any other frame) in the grace window:
      // the socket is stale. Tear it down and resume by absolute index.
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

// teardown aborts and closes the live socket (if any) and stops the heartbeat
// and any scheduled reconnect, leaving the module disconnected. It never touches
// per-session resume state, so a later connect() resumes cleanly. Shared by
// reconnectNow (reconnects after) and disconnect/forgetSession (do not).
function teardown(): void {
  if (connState.status === "connecting" || connState.status === "connected") {
    // Abort BEFORE close: aborting detaches all listeners on the existing
    // sock, so frames arriving between close() and the close handshake aren't
    // processed twice (the iPad-wake duplicate-output race).
    connState.abort.abort();
    try {
      connState.sock.close();
    } catch {
      /* ignore */
    }
  }
  stopHeartbeat();
  cancelScheduledReconnect();
  connState = { status: "disconnected" };
}

export function reconnectNow(): void {
  // Unconditional teardown. On iOS wake (visibilitychange + pageshow), the
  // socket frequently reads OPEN/"connected" for a while but is actually a
  // zombie — the OS froze it during sleep and frames printed meanwhile never
  // arrive. The old early-return on "connected" trusted that stale state and
  // skipped the reconnect, which is exactly why content printed during sleep
  // stayed missing until a manual refresh (bug 2). So we never trust the
  // current state on a wake: abort + close whatever socket exists and
  // reconnect. The resume protocol (by absolute index) then backfills exactly
  // the missed lines, so a reconnect over a still-healthy socket is a cheap,
  // duplicate-free no-op rather than a risk.
  teardown();
  connect();
}

/**
 * setSession switches the live socket to a different server session, keeping
 * every session's resume state intact (design section 5, the switch). The
 * current socket is torn down (its outbox and byte counters preserved for a
 * later switch back) and a fresh socket connects to `id` with its own resume
 * state, sending `?session=id`. Calling this marks the module "managed": the WS
 * URL then carries the session id. A no-op when `id` is already the active,
 * connected session.
 */
export function setSession(id: string): void {
  managed = true;
  ensureState(id);
  if (id === activeId && (connState.status === "connected" || connState.status === "connecting")) {
    return; // already serving this session
  }
  activeId = id;
  reconnectNow();
}

/**
 * forgetSession drops a session's resume state (on tab close, design section 17).
 * If it was the active session, the live socket is torn down without
 * reconnecting; the shell then selects another tab via setSession.
 */
export function forgetSession(id: string): void {
  sessions.delete(id);
  if (id === activeId) {
    activeId = null;
    teardown();
  }
}

/**
 * disconnect tears down the live socket without reconnecting. Per-session resume
 * state is kept, so a later setSession/connect resumes cleanly. Used when the
 * shell has no active tab to show (e.g. the last tab closed).
 */
export function disconnect(): void {
  teardown();
}

export function connect(): void {
  // Guard against double-call: a stray invocation while a previous
  // socket is still CONNECTING/OPEN would orphan it (its handlers
  // remain bound but the new sock assignment makes it unreachable).
  // Aborting the previous controller detaches all listeners on the
  // old sock so it can't deliver frames to the page after we've moved
  // on (the iPad-wake duplicate-output race).
  if (connState.status === "connecting" || connState.status === "connected") {
    connState.abort.abort();
    try {
      connState.sock.close();
    } catch {
      /* ignore */
    }
  }
  // Re-entry while a backoff reconnect is pending (e.g. a consumer calling
  // connect() to restore a panel during the 500ms-8s backoff window): clear the
  // scheduled timer so it cannot fire later and spawn a SECOND socket alongside
  // the one created below. The orphaned timer resets connState to disconnected
  // and calls connect() again, while the existing socket's listeners stay bound
  // (its abort never fired) -> a duplicate server connection + double delivery.
  // cancelScheduledReconnect is a no-op in any non-reconnecting state.
  cancelScheduledReconnect();

  cb?.onConnecting?.();

  // The resume state this socket serves, captured for the socket's lifetime.
  // A switch aborts this socket's listeners, so even a late frame is handled
  // against the session it was opened for, never whoever is active now.
  const st = activeState();
  let url = wsURL(location.protocol, location.host, wsPath);
  if (managed) {
    // Route the socket to this session (SessionManager's WebSocketHandler
    // dispatches on ?session=). The unmanaged single-terminal path keeps the
    // bare wsPath, matching resume purely by the resume frame's sessionId.
    url += (url.includes("?") ? "&" : "?") + "session=" + encodeURIComponent(st.id);
  }
  const sock = new WebSocket(url);
  sock.binaryType = "arraybuffer";

  // One AbortController governs the lifetime of THIS sock's listeners.
  // - Connect-timeout fallback: aborts after 10s if open never fires.
  // - Listener auto-detach: every addEventListener below uses
  //   { signal: connectAbort.signal }, so when the controller is
  //   aborted (by reconnectNow / connect / close) the listeners are
  //   removed atomically and can't fire again.
  const connectAbort = new AbortController();
  const timeoutId = setTimeout(() => {
    // Aborting detaches every listener registered with connectAbort.signal
    // (abort algorithms run BEFORE the abort event fires), INCLUDING the
    // "close" listener that normally schedules the reconnect. A connect that
    // never opens (SYN dropped by a firewall / an overloaded server) would
    // otherwise leave connState pinned at "connecting" with no auto-retry.
    // Drive the reconnect explicitly, mirroring the close handler.
    connectAbort.abort();
    if (connState.status === "connecting" && connState.sock === sock) {
      stopHeartbeat();
      connState = { status: "disconnected" };
      cb?.onClose();
      scheduleReconnect();
    }
  }, 10_000);
  connectAbort.signal.addEventListener("abort", () => {
    clearTimeout(timeoutId);
    // Force-close on abort so the OS-level socket goes away promptly,
    // not only when the browser eventually completes its close
    // handshake. Belt-and-braces with the .close() in our callers.
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
      connState = { status: "connected", sock, abort: connectAbort };
      reconnectDelay = INITIAL_DELAY_MS;
      lastSentCols = 0;
      lastSentRows = 0;
      cb?.onOpen();

      // Send resume immediately so server can respond with its current
      // bytesReceived for this session — we trim/retransmit the outbox
      // when that resumeAck arrives (handled in the message listener).
      sock.send(
        controlFrame({
          type: "resume",
          sessionId: st.id,
          sentBytes: st.bytesSent,
          // Highest absolute line index the client holds (-1 if none). The
          // server replays everything after it, so lines printed while the
          // device slept are backfilled exactly on wake (bug 2), with no
          // duplication because applying a line by absolute index is
          // idempotent. Falls back to -1 (full retained replay) if the
          // consumer wired no getHaveThrough.
          haveThrough: cb?.getHaveThrough?.() ?? -1,
          // Lets the server detect a client built against a different wire
          // revision (e.g. a stale cached bundle) and warn rather than
          // silently mis-decode.
          protocolVersion: WIRE_PROTOCOL_VERSION,
        }),
      );

      // Begin client-side liveness probing for this socket. Idempotent
      // (clears any prior timer) and resets the activity clock to now.
      startHeartbeat();
    },
    { signal: connectAbort.signal },
  );

  // Queue for serializing Blob→ArrayBuffer conversion. iOS Safari can
  // deliver binary WS frames as Blob; the conversion is async via
  // .arrayBuffer() and unordered resolution would corrupt screen state.
  // We chain promises so each frame is processed in arrival order.
  let blobChain: Promise<void> = Promise.resolve();

  sock.addEventListener(
    "message",
    (ev: MessageEvent) => {
      // Any inbound frame — pong, screen update, anything — proves the
      // socket is delivering, so it refreshes the liveness clock before we
      // even decode it. A malformed frame that decodes to null still counts.
      markActivity();
      if (ev.data instanceof ArrayBuffer) {
        try {
          handleDecoded(decodeWireBinary(ev.data));
        } catch (err) {
          // Mirror the Blob branch below: a throw here (a consumer onMessage
          // callback, or the documented re-throw of a non-RangeError from
          // decodeWireBinary) is logged with engine context instead of
          // surfacing as a bare uncaught exception, so field observability is
          // the same across ArrayBuffer (non-iOS) and Blob (iOS Safari) frames.
          console.error("vterm: dropped binary frame", err);
        }
        return;
      }
      if (ev.data instanceof Blob) {
        const blob = ev.data;
        blobChain = blobChain
          .then(() => blob.arrayBuffer())
          .then((ab) => {
            handleDecoded(decodeWireBinary(ab));
          })
          .catch((err: unknown) => {
            // A throw here (typically a consumer onMessage callback) must NOT
            // poison the chain: without this catch blobChain stays rejected and
            // every later Blob frame's .then is skipped, silently dropping all
            // binary frames until reconnect. iOS Safari delivers binary WS
            // frames as Blob, and markActivity() already ran on arrival, so the
            // liveness probe never fires -> the tab looks connected but renders
            // nothing. Log and continue; arrival order is preserved.
            console.error("vterm: dropped binary (blob) frame", err);
          });
        return;
      }
      if (typeof ev.data === "string") {
        try {
          handleDecoded(JSON.parse(ev.data) as ServerMessage);
        } catch {
          // ignore malformed text frames
        }
      }
    },
    { signal: connectAbort.signal },
  );

  function handleDecoded(msg: ServerMessage | null): void {
    if (msg === null) {
      return;
    }
    if (msg.type === "resumeAck") {
      // Server-restart detection. The first resumeAck we see records
      // the epoch; subsequent ones compare to it. A mismatch means the
      // server's process has restarted, which invalidates our local
      // bytesSent/bytesAcked accounting (the new server has no record
      // of the previous boot's input). Reset state and notify the UI.
      const epoch = msg.serverEpoch;
      if (epoch !== undefined && epoch !== 0) {
        if (st.lastServerEpoch !== null && st.lastServerEpoch !== epoch) {
          resetSessionAfterRestart(st);
        }
        st.lastServerEpoch = epoch;
      }
      // Resync guard 8.2.2: hand the server's retained-history bounds to the
      // consumer so it can surface a trim marker when history the client was
      // missing is gone for good. (If the session-forgotten path below resets
      // state, a fresh server's oldest=0 simply reads as "no trim".)
      if (typeof msg.committed === "number" && typeof msg.oldestIndex === "number") {
        cb?.onResumeBounds?.(msg.committed, msg.oldestIndex);
      }
      // Server-doesn't-recognize-this-session safeguard: if the server
      // returns received=0 but the client already had bytesAcked > 0,
      // the server has forgotten our session (idle GC kicked in, or
      // sessionId persistence failed and a reload created a new one).
      // Replaying the outbox would deliver every queued chunk again,
      // causing the iOS tab-suspend duplicate-resend bug. Drop the
      // outbox and notify the UI as if the server restarted — input
      // since the last successful ack is irrecoverable but at least
      // not duplicated. Skip this branch when bytesSent = 0 (genuine
      // first-connect; received=0 is correct).
      if (msg.received === 0 && st.bytesAcked > 0) {
        resetSessionAfterRestart(st);
        return;
      }
      applyAck(st, msg.received);
      retransmitOutbox(sock, st);
      return;
    }
    if (msg.type === "modes") {
      modes.setModes(
        msg.bracketedPaste,
        msg.applicationCursor,
        msg.mouseSGR,
        msg.focusReporting,
        msg.mouseMode,
        msg.applicationKeypad,
        msg.reverseVideo,
        msg.mousePixels,
        msg.keyboardFlags,
      );
      if (typeof msg.inputAck === "number") {
        applyAck(st, msg.inputAck);
      }
      // Notify the UI so it can react to mode changes (e.g. clear
      // scrollback on alt-screen entry — handled by the caller).
      cb?.onMessage(msg);
      return;
    }
    if (typeof msg.inputAck === "number") {
      applyAck(st, msg.inputAck);
    }
    cb?.onMessage(msg);
  }

  sock.addEventListener(
    "close",
    (ev: CloseEvent) => {
      // Only the active sock's close should drive reconnect logic; an
      // already-superseded sock has been aborted and this listener
      // wouldn't fire (signal removes it). The check stays as a belt-
      // and-braces guard in case the abort hasn't propagated yet.
      if (connState.status !== "connecting" && connState.status !== "connected") {
        return;
      }
      if (connState.sock !== sock) {
        return;
      }
      stopHeartbeat();
      connState = { status: "disconnected" };
      // A process-exited close (4001) is definitive: the child is gone, so a
      // backoff reconnect can only replay the final screen and collect another
      // 4001 — an endless, pointless churn that reads as a flapping
      // "Reconnecting…" banner. Route it to onProcessExit (no reconnect, no
      // onClose) when the consumer wired it; without the callback, keep the
      // legacy transient treatment so existing consumers see no change.
      if (ev.code === PROCESS_EXITED_CLOSE_CODE && cb?.onProcessExit) {
        cb.onProcessExit();
        return;
      }
      cb?.onClose();
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
