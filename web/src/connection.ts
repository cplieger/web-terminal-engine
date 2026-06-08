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
import { controlFrame } from "./wire.js";
import { decodeWireBinary } from "./wire-binary.js";
import * as render from "./render.js";
import * as modes from "./modes.js";
import type { ControlMessage, ServerMessage } from "./types.js";
import { INITIAL_DELAY_MS, nextBackoffDelay } from "./reconnect.js";

type ConnState =
  | { status: "disconnected" }
  | { status: "connecting"; sock: WebSocket; abort: AbortController }
  | { status: "connected"; sock: WebSocket; abort: AbortController }
  | { status: "reconnecting"; timer: ReturnType<typeof setTimeout>; delayMs: number };

let connState: ConnState = { status: "disconnected" };
let reconnectDelay = INITIAL_DELAY_MS;
let lastSentCols = 0;
let lastSentRows = 0;
let wsPath = "/ws";

// Resume-protocol state. sessionId persists across iOS tab-suspend/reload
// via sessionStorage. Without this persistence, an iOS Safari tab unload
// (which Safari does aggressively when the user backgrounds the tab) and
// subsequent reconstruction from history triggers a fresh JS module load
// → new sessionId → server treats it as a new session → resumeAck.received
// returns 0 → applyAck(0) doesn't trim the outbox → retransmitOutbox sends
// every queued chunk again, causing the duplicate-message-resend bug.
const SESSION_ID_KEY = "vterm-session-id";
const sessionId = loadOrCreateSessionId();
let bytesSent = 0; // total bytes ever passed to sendBinary
let bytesAcked = 0; // confirmed by server inputAck/resumeAck
const outbox: Uint8Array[] = []; // chunks of unacked bytes (sum = bytesSent - bytesAcked)
let outboxBytes = 0; // running sum of outbox chunk lengths; keeps applyAck O(n) instead of O(n²)
let lastServerEpoch: number | null = null; // process-start nanos of the last connected server

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

function generateSessionId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  // Fallback for older browsers: pseudo-random hex string.
  let s = "";
  for (let i = 0; i < 8; i++) {
    s += Math.random().toString(16).slice(2, 6);
  }
  return s;
}

export interface Callbacks {
  onMessage(msg: ServerMessage): void;
  onOpen(): void;
  onClose(): void;
  onConnecting?(): void;
  onOutboxFull?(): void;
  /** Fired when the server's boot-epoch in resumeAck differs from the
   *  one observed on a previous connection — i.e. the server has
   *  restarted. By the time this fires, the connection module has
   *  already reset bytesSent/bytesAcked/outbox so subsequent input
   *  starts from zero. UI should clear scrollback and surface a
   *  banner so the user knows old input may have been lost. */
  onServerRestart?(): void;
  computeSize(): { cols: number; rows: number };
  /** Optional WebSocket endpoint path (default "/ws"). vibekit serves
   *  the shell at "/api/shell/ws"; vibecli at "/ws". */
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
  if (outboxBytes + data.length > MAX_OUTBOX_BYTES) {
    cb?.onOutboxFull?.();
    return false;
  }
  // Always go through the outbox. Bytes leave it only when the server
  // explicitly acks them — guarantees correct retransmission after a
  // network blip even if ws.send() reported success.
  const copy = new Uint8Array(data); // defensive copy (caller may reuse buffer)
  outbox.push(copy);
  outboxBytes += copy.length;
  bytesSent += copy.length;
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

// applyAck drops chunks from the front of the outbox until the
// running total of unacked bytes matches (bytesSent - newAck). Runs
// in O(chunks_dropped) by tracking outboxBytes incrementally rather
// than re-summing on every loop iteration.
function applyAck(received: number): void {
  if (received <= bytesAcked) {
    return;
  }
  bytesAcked = Math.min(received, bytesSent);
  const targetUnacked = bytesSent - bytesAcked;
  while (outbox.length > 0 && outboxBytes > targetUnacked) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- length checked above
    const head = outbox[0]!;
    const dropFromHead = outboxBytes - targetUnacked;
    if (head.length <= dropFromHead) {
      outbox.shift();
      outboxBytes -= head.length;
    } else {
      outbox[0] = head.subarray(dropFromHead);
      outboxBytes -= dropFromHead;
      break;
    }
  }
}

// On reconnect, after sending the resume control message and getting
// resumeAck, replay anything still in the outbox. The server has
// adjusted bytesAcked already — only unacked bytes remain.
function retransmitOutbox(): void {
  if (connState.status !== "connected") {
    return;
  }
  for (const chunk of outbox) {
    connState.sock.send(
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

export function reconnectNow(): void {
  if (connState.status === "connected") {
    return;
  }
  if (connState.status === "connecting") {
    // Abort BEFORE close: aborting detaches all listeners on the
    // existing sock, so any frames that arrive between close() and
    // the close-handshake completion (or browser shutdown of the
    // socket) won't be processed twice. Without this, on iPad wake
    // the visibilitychange + pageshow events both trigger
    // reconnectNow() within milliseconds; the first sock's listeners
    // are otherwise still alive and re-process every frame the new
    // sock receives, leading to duplicated DOM rows.
    connState.abort.abort();
    try {
      connState.sock.close();
    } catch {
      /* ignore */
    }
  }
  cancelScheduledReconnect();
  connState = { status: "disconnected" };
  connect();
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

  cb?.onConnecting?.();

  const sock = new WebSocket(wsURL(location.protocol, location.host, wsPath));
  sock.binaryType = "arraybuffer";

  // One AbortController governs the lifetime of THIS sock's listeners.
  // - Connect-timeout fallback: aborts after 10s if open never fires.
  // - Listener auto-detach: every addEventListener below uses
  //   { signal: connectAbort.signal }, so when the controller is
  //   aborted (by reconnectNow / connect / close) the listeners are
  //   removed atomically and can't fire again.
  const connectAbort = new AbortController();
  const timeoutId = setTimeout(() => {
    connectAbort.abort();
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
          sessionId,
          sentBytes: bytesSent,
          // iOS Safari can preserve sessionStorage (so sessionId
          // survives) while evicting the page entirely; on reload
          // the DOM has zero scrollback rows and we want a full
          // replay. A WS drop that doesn't lose the page keeps the
          // count and avoids duplicating scrollback rows the client
          // still has.
          scrollbackHave: render.getScrollbackRowCount(),
        }),
      );
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
      if (ev.data instanceof ArrayBuffer) {
        handleDecoded(decodeWireBinary(ev.data));
        return;
      }
      if (ev.data instanceof Blob) {
        const blob = ev.data;
        blobChain = blobChain.then(() =>
          blob.arrayBuffer().then((ab) => {
            handleDecoded(decodeWireBinary(ab));
          }),
        );
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
        if (lastServerEpoch !== null && lastServerEpoch !== epoch) {
          bytesSent = 0;
          bytesAcked = 0;
          outbox.length = 0;
          outboxBytes = 0;
          cb?.onServerRestart?.();
        }
        lastServerEpoch = epoch;
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
      if (msg.received === 0 && bytesAcked > 0) {
        bytesSent = 0;
        bytesAcked = 0;
        outbox.length = 0;
        outboxBytes = 0;
        cb?.onServerRestart?.();
        return;
      }
      applyAck(msg.received);
      retransmitOutbox();
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
      );
      if (typeof msg.inputAck === "number") {
        applyAck(msg.inputAck);
      }
      // Notify the UI so it can react to mode changes (e.g. clear
      // scrollback on alt-screen entry — handled by the caller).
      cb?.onMessage(msg);
      return;
    }
    if (typeof msg.inputAck === "number") {
      applyAck(msg.inputAck);
    }
    cb?.onMessage(msg);
  }

  sock.addEventListener(
    "close",
    () => {
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
      connState = { status: "disconnected" };
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
