// Client for the server's session status stream (Server-Sent Events at
// /api/sessions/events). A thin EventSource wrapper: it parses each status event
// and fans it out to a callback. The reconnect-resync policy (re-fetching the
// session list after a gap) is the consumer's job; onOpen fires on every
// (re)open so the consumer can trigger it. Pairs with the Go
// terminal.SessionManager EventsHandler.

/** SessionStatus is one session's current status as carried on the stream. It
 *  mirrors the server's status event. */
export interface SessionStatus {
  readonly id: string;
  readonly status: "working" | "idle" | "input" | "exited";
  readonly title: string;
  readonly createdAt: string;
  /** true when the session is gone (closed or reaped); the consumer drops it. */
  readonly removed?: boolean;
}

/** StatusStreamCallbacks are the consumer's hooks. Declared as function-typed
 *  properties (not method shorthand) so destructuring them is safe. */
export interface StatusStreamCallbacks {
  /** Called for each status event. */
  onStatus: (status: SessionStatus) => void;
  /** Called on every (re)open, including auto-reconnects, so the consumer can
   *  resync after a gap (the stream only carries future changes). */
  onOpen?: () => void;
  /** Called when the stream errors; the EventSource then auto-reconnects. */
  onError?: () => void;
}

/** EventSourceLike is the minimal surface used here, so the stream can be tested
 *  without a DOM EventSource (the default factory adapts a real one). */
export interface EventSourceLike {
  addEventListener: (type: string, listener: (event: MessageEvent) => void) => void;
  close: () => void;
}

/** EventSourceFactory builds an EventSourceLike for a URL. */
export type EventSourceFactory = (url: string) => EventSourceLike;

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
  };
};

/** connectStatusStream opens the status stream at path and fans events out to
 *  cb. make defaults to a real EventSource; tests inject a fake. */
export function connectStatusStream(
  path: string,
  cb: StatusStreamCallbacks,
  make: EventSourceFactory = defaultFactory,
): StatusStream {
  const es = make(path);
  const { onOpen, onError } = cb;
  if (onOpen) {
    es.addEventListener("open", () => {
      onOpen();
    });
  }
  es.addEventListener("message", (event) => {
    const raw: unknown = event.data;
    if (typeof raw !== "string") {
      return; // non-text frame, ignore
    }
    let status: SessionStatus;
    try {
      status = JSON.parse(raw) as SessionStatus;
    } catch {
      return; // skip a malformed frame, keep the stream
    }
    cb.onStatus(status);
  });
  if (onError) {
    es.addEventListener("error", () => {
      onError();
    });
  }
  return {
    close: () => {
      es.close();
    },
  };
}
