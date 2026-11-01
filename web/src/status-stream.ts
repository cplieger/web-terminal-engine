// Client for the server's session status stream (Server-Sent Events at
// /api/sessions/events). A thin EventSource wrapper: it parses each status event
// and fans it out to a callback. The reconnect-resync policy (re-fetching the
// session list after a gap) is the consumer's job; onOpen fires on every
// (re)open so the consumer can trigger it. Pairs with the Go
// terminal.SessionManager EventsHandler.

import { nextBackoffDelay } from "./reconnect.js";

/** KnownStatus is a status the server derives itself, one per Go terminal.Status*
 *  constant. */
export type KnownStatus =
  "working" | "idle" | "input" | "done" | "exited" | "crashed" | "failed" | "warning";

/** SessionInfo is one session's wire shape: the JSON object the session REST
 *  API (GET/POST /api/sessions) returns per session, kept in lockstep with the Go
 *  terminal.SessionInfo by hand.
 *
 *  `title` is the RESOLVED display title, `pinnedTitle` the USER's name (set and
 *  cleared only by a human action, outranking everything else), and `clientTitle`
 *  a CLIENT-DERIVED automatic title a client asked the server to remember. */
export interface SessionInfo {
  readonly id: string;
  /** the session's current status. The server derives a KnownStatus from the
   *  program's OSC 7501 report, or from OSC 9;4 progress while it has sent none
   *  since a hard reset (RIS); "exited" and "crashed" mark the process's end
   *  and nothing clears them. A classified OSC 9 notification latches whatever
   *  string the server's classifier returns, and a newer server may add values,
   *  so a consumer must handle any other string: the stream forwards it
   *  unchanged. The engine's docs/sessions.md gives every mapping. */
  readonly status: KnownStatus | (string & {});
  readonly title: string;
  /** the session's short public name, safe to put in a URL: 1 to 64 characters
   *  of `[A-Za-z0-9_-]`, unique among live sessions, and NOT a capability
   *  (nothing attaches with it; only `id` does). The server mints a random one
   *  and a host may replace it (for example with its program's own session id).
   *  Absent from a server before 6.2 and from a status event carrying
   *  `removed`. Untrusted wire text: validate it before use. */
  readonly alias?: string;
  /** the raw client-derived automatic title (before the precedence baked into
   *  `title`); a consumer that treats the program's OSC window title as
   *  unreliable reads this instead of `title`. */
  readonly clientTitle?: string;
  /** the user's pinned name, set through PUT /api/sessions/{id}/pinned-title and
   *  cleared through DELETE on the same path. It outranks every automatic source
   *  in `title`; it is carried raw so a UI can tell that a pin EXISTS (to offer
   *  the automatic name again) rather than only seeing its effect. Absent or
   *  empty means the session has no user-set name. */
  readonly pinnedTitle?: string;
  readonly createdAt: string;
  /** the session's position in the display order every viewer of this server
   *  shares: 0-based, dense, and unique across the live set. Order is a property
   *  of the SESSION SET rather than of one browser, so two devices agree on the
   *  arrangement and a reorder made on one appears on the other. Set it with
   *  `PUT /api/sessions/order`, which takes every live id in the wanted order.
   *
   *  Sort by this FIELD, not by the sequence sessions arrived in. The REST list
   *  and the status stream are both served in this order, but a consumer that
   *  merges them (subscribing before its bootstrap list resolves, which is how to
   *  avoid double-adopting a session) sees neither sequence intact.
   *
   *  Absent from a server before 3.10.0, and from a status event carrying
   *  `removed` (the session has left the order). Treat absent as "this server has
   *  no shared order" and fall back to `createdAt`; do not read it as position 0,
   *  which belongs to a real session.
   *
   *  One reorder arrives as one status event PER MOVED SESSION, so until you have
   *  applied a whole tick two sessions can hold the same position. Apply the tick,
   *  then sort. And never derive an order you WRITE BACK from a partly applied
   *  view: a list built from that hybrid still names the live set exactly, so the
   *  server accepts it and it becomes the arrangement every device sees. */
  readonly order?: number;
  /** true once the session has emitted a genuine activity signal — an OSC 7501
   *  report, OSC 9;4 progress or a classified OSC 9 notification.
   *  Sticky for the session's life, a hard reset (RIS) included. Consumers
   *  reveal the per-tab activity dot only when this is set; a program that
   *  emits none of the three (a plain shell) keeps its tab dot hidden. */
  readonly reportsActivity?: boolean;
  /** the session's SECONDARY activity: a host-reported background activity that
   *  OUTLIVES the turn, carried beside `status` and never merged into it. The
   *  closed set is `"working"` (a background task is running), `"waiting"`
   *  (stopped and resumable, nobody is being asked) and `"input"` (a background
   *  task is blocked on the user); absent or an empty string means no secondary
   *  activity, and a consumer must tolerate an UNKNOWN value from a newer server
   *  by rendering nothing for it. Absent from a server that reports none. */
  readonly activity?: string;
  /** how many sources produced `activity`: >= 1 whenever `activity` is non-empty,
   *  0 when it is empty or the host does not count them. */
  readonly activityCount?: number;
  /** numbers the occurrence of the session's status. A sweep raises it by one
   *  when it emits a changed status, a status a classified notification latched
   *  since the last sweep, or an input, done or failed status after an OSC 7501
   *  report since then created a record, changed one's state or kind, or
   *  cleared one, and so changed which record the status came from, or its
   *  state or kind, at that report. A report keeping a record's state and kind
   *  raises it only by changing the status. It never falls for one id; 0,
   *  absent on the wire, means nothing numbered yet. */
  readonly statusSeq?: number;
  /** the highest `statusSeq` any reader acknowledged through
   *  `PUT /api/sessions/{id}/seen`. Never falls for one id and never exceeds
   *  `statusSeq`; a status that calls for attention is unread while
   *  `statusSeq > seenSeq`. */
  readonly seenSeq?: number;
}

/** SessionStatus is one session's current status as carried on the status
 *  stream (SSE): the REST wire shape plus the stream-only removal marker. It
 *  mirrors the server's status event. */
export interface SessionStatus extends SessionInfo {
  /** true when the session is gone (closed or reaped); the consumer drops it. */
  readonly removed?: boolean;
  /** the session's percentage: the OSC 7501 report's `progress` when it
   *  carries one, else the OSC 9;4 percentage. -1 when absent or unknown (no
   *  sequence seen, or a state that carries none — clear/indeterminate), else
   *  0-100. Paired with `status` to render a determinate bar. */
  readonly progressValue?: number;
  /** the OSC 9 notification message, delivered to EVERY subscriber whether or
   *  not the server has a status classifier installed. A notification is an
   *  EVENT, not a state: it does not latch a status, so a consumer that wants a
   *  status out of it decides that itself. */
  readonly notification?: string;
  /** the notification's sequence number (monotonic per session), so a repeated
   *  message is still recognised as a new event. */
  readonly notificationSeq?: number;
}

/** StatusStreamCallbacks are the consumer's hooks. Declared as function-typed
 *  properties (not method shorthand) so destructuring them is safe. */
export interface StatusStreamCallbacks {
  /** Called for each status event. */
  onStatus: (status: SessionStatus) => void;
  /** Called on every (re)open, including auto-reconnects, so the consumer can
   *  resync after a gap (the stream only carries future changes). */
  onOpen?: () => void;
  /** Called when the stream errors. A transient drop is auto-reconnected by
   *  EventSource; a permanent close (non-2xx / wrong content-type) is
   *  re-established by this module with capped backoff. */
  onError?: () => void;
}

/** EventSourceLike is the minimal surface used here, so the stream can be tested
 *  without a DOM EventSource (the default factory adapts a real one). */
export interface EventSourceLike {
  addEventListener: (type: string, listener: (event: MessageEvent) => void) => void;
  close: () => void;
  /** EventSource.readyState: 0 CONNECTING, 1 OPEN, 2 CLOSED. A permanent close
   *  (non-2xx response / wrong content-type / auth failure) sets CLOSED and
   *  native auto-reconnect never fires; the module re-establishes on CLOSED. */
  readonly readyState: number;
}

/** EventSourceFactory builds an EventSourceLike for a URL. */
export type EventSourceFactory = (url: string) => EventSourceLike;

/** StatusStream is the handle connectStatusStream returns: the consumer's only
 *  hold on a live stream, and close is the whole of it — everything else the
 *  stream does is reported through the callbacks. Closing is final and there is
 *  no reopen; it cancels a scheduled reconnect as well as the current stream, so
 *  a consumer that wants status back calls connectStatusStream again. */
export interface StatusStream {
  /** Closes the stream and stops reconnection. */
  close: () => void;
}

const defaultFactory: EventSourceFactory = (url) => {
  const es = new EventSource(url);
  return {
    addEventListener: (type, listener) => {
      es.addEventListener(type, listener as unknown as EventListener);
    },
    close: () => {
      es.close();
    },
    get readyState() {
      return es.readyState;
    },
  };
};

/** connectStatusStream opens the status stream at path and fans events out to
 *  cb. make defaults to a real EventSource; tests inject a fake. */
export function connectStatusStream(
  path: string,
  cb: StatusStreamCallbacks,
  make: EventSourceFactory = defaultFactory,
): StatusStream {
  let es: EventSourceLike;
  let closed = false;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let backoffMs = 500;
  const { onOpen, onError } = cb;
  const open = (): void => {
    es = make(path);
    es.addEventListener("open", () => {
      backoffMs = 500; // reset backoff on a successful (re)open
      if (onOpen) {
        onOpen();
      }
    });
    es.addEventListener("message", (event) => {
      const raw: unknown = event.data;
      if (typeof raw !== "string") {
        return; // non-text frame, ignore
      }
      let status: SessionStatus;
      try {
        status = JSON.parse(raw) as SessionStatus;
      } catch {
        console.warn("vterm: dropped malformed status-stream frame");
        return; // skip a malformed frame, keep the stream
      }
      cb.onStatus(status);
    });
    es.addEventListener("error", () => {
      if (onError) {
        onError();
      }
      // Native EventSource auto-reconnect covers a transient drop but NOT a
      // permanent close (server restart -> proxy 502/non-2xx, auth 401/403,
      // wrong content-type), which sets readyState CLOSED and never retries.
      // Re-establish so per-tab status doesn't freeze until a page reload while
      // the terminal WS recovers on its own backoff. onOpen fires on the reopen,
      // so the consumer's resync still runs.
      if (es.readyState === 2 && !closed && reconnectTimer === undefined) {
        const step = nextBackoffDelay(backoffMs);
        reconnectTimer = setTimeout(() => {
          reconnectTimer = undefined;
          if (!closed) {
            open();
          }
        }, step.scheduledMs);
        backoffMs = step.nextBaseMs;
      }
    });
  };
  open();
  return {
    close: () => {
      closed = true;
      if (reconnectTimer !== undefined) {
        clearTimeout(reconnectTimer);
      }
      es.close();
    },
  };
}
