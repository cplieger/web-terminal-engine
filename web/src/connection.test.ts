// @vitest-environment happy-dom
//
// Regression test for the duplicate-output-on-reconnect bug. When the
// browser fired both `visibilitychange` and `pageshow` on iPad wake,
// each event triggered connection.reconnectNow(); the old WebSocket
// was .close()'d but its addEventListener('message', ...) handler was
// NOT removed. Frames the server delivered between the close request
// and the close-handshake completion were processed by both the
// orphaned old sock AND the freshly-created new sock — every WS frame
// produced two handleScreen / handleScroll calls, leading to
// duplicated DOM rows. The fix uses an AbortController per sock and
// passes its signal to every addEventListener so the listeners
// auto-detach when the controller is aborted (which the new connect()
// does before creating the replacement sock).

import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";
import { connect, generateSessionId, init, reconnectNow } from "./connection.js";
import type { ServerMessage } from "./types.js";

interface MockWS {
  url: string;
  binaryType: string;
  readyState: number;
  closed: boolean;
  listeners: Map<string, ((ev: unknown) => void)[]>;
  signals: Map<string, AbortSignal | undefined>;
  send: (...args: unknown[]) => unknown;
  close: (...args: unknown[]) => unknown;
  addEventListener: (
    type: string,
    handler: (ev: unknown) => void,
    opts?: { signal?: AbortSignal },
  ) => void;
  // Test helpers:
  fireOpen: () => void;
  fireMessage: (data: ArrayBuffer | Blob | string) => void;
  fireClose: () => void;
}

const allMockWebSockets: MockWS[] = [];

function makeMockWebSocket(): typeof WebSocket {
  // Constructor returning MockWS; we type it as `unknown as typeof WebSocket`
  // so it can stand in for the global.
  const ctor = function (url: string): MockWS {
    const sock: MockWS = {
      url,
      binaryType: "blob",
      readyState: 0, // CONNECTING
      closed: false,
      listeners: new Map(),
      signals: new Map(),
      send: vi.fn(),
      close: vi.fn(function (this: MockWS) {
        this.closed = true;
        this.readyState = 3; // CLOSED
      }) as unknown as ReturnType<typeof vi.fn>,
      addEventListener(
        this: MockWS,
        type: string,
        handler: (ev: unknown) => void,
        opts?: { signal?: AbortSignal },
      ): void {
        if (!this.listeners.has(type)) {
          this.listeners.set(type, []);
        }
        this.listeners.get(type)!.push(handler);
        // Honor signal: when aborted, remove this listener.
        if (opts?.signal) {
          const list = this.listeners.get(type)!;
          opts.signal.addEventListener("abort", () => {
            const idx = list.indexOf(handler);
            if (idx >= 0) {
              list.splice(idx, 1);
            }
          });
        }
      },
      fireOpen(this: MockWS) {
        this.readyState = 1;
        for (const fn of this.listeners.get("open") ?? []) {
          fn({});
        }
      },
      fireMessage(this: MockWS, data: ArrayBuffer | Blob | string) {
        for (const fn of this.listeners.get("message") ?? []) {
          fn({ data });
        }
      },
      fireClose(this: MockWS) {
        this.readyState = 3;
        for (const fn of this.listeners.get("close") ?? []) {
          fn({});
        }
      },
    } as unknown as MockWS;
    Object.setPrototypeOf(sock, MockWebSocket.prototype);
    allMockWebSockets.push(sock);
    return sock;
  } as unknown as typeof WebSocket;

  // Stamp a prototype so `instanceof` checks work if any. Not used here
  // but keeps TS happy.
  class MockWebSocket {}
  ctor.prototype = MockWebSocket.prototype as unknown as WebSocket;
  return ctor;
}

// Decode a 0x00-prefixed control frame (the JSON the client sends for
// resume/resize) back to an object. Returns null for raw-input frames or
// non-frame sends, so callers can filter the send log down to control frames.
interface DecodedControlFrame {
  type?: string;
  haveThrough?: number;
  sentBytes?: number;
  sessionId?: string;
}

function decodeControlFrame(arg: unknown): DecodedControlFrame | null {
  let bytes: Uint8Array | null = null;
  if (arg instanceof Uint8Array) {
    bytes = arg;
  } else if (arg instanceof ArrayBuffer) {
    bytes = new Uint8Array(arg);
  }
  if (!bytes || bytes.length === 0 || bytes[0] !== 0x00) {
    return null;
  }
  return JSON.parse(new TextDecoder().decode(bytes.subarray(1))) as DecodedControlFrame;
}

// Every control frame the mock socket was asked to send, decoded, in order.
function controlFramesSent(sock: MockWS): DecodedControlFrame[] {
  const calls = (sock.send as unknown as { mock: { calls: unknown[][] } }).mock.calls;
  return calls
    .map((c) => decodeControlFrame(c[0]))
    .filter((m): m is DecodedControlFrame => m !== null);
}

// A binary pong frame as the server sends it: [1B type=5 pong][8B ack=0].
// Mirrors encodePongMsg (Go) / MSG_PONG (wire-binary.ts). Decodes to null —
// the client treats its mere arrival as proof of liveness.
function pongFrame(): ArrayBuffer {
  return new Uint8Array([5, 0, 0, 0, 0, 0, 0, 0, 0]).buffer;
}

describe("connection: a socket superseded by a reconnect delivers no duplicate messages", () => {
  // Drives the REAL connection module (init + connect + reconnectNow) with
  // a fake global WebSocket, asserting the observable contract: once a
  // socket is superseded, frames still arriving on it never reach the
  // onMessage callback. This is the duplicate-output-on-iPad-wake bug —
  // the fix is the AbortController-per-socket whose signal detaches every
  // listener when the replacement socket is created.
  let onMessage: ReturnType<typeof vi.fn<(msg: ServerMessage) => void>>;

  beforeEach(() => {
    allMockWebSockets.length = 0;
    vi.useFakeTimers(); // neutralize the 10s connect-timeout / reconnect backoff
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    onMessage = vi.fn<(msg: ServerMessage) => void>();
    init({
      onMessage,
      onOpen: () => {
        /* no-op */
      },
      onClose: () => {
        /* no-op */
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function titleFrame(title: string): string {
    return JSON.stringify({ type: "title", title });
  }

  it("reconnecting while still connecting orphans the first socket so its late frames are ignored", () => {
    connect();
    const first = allMockWebSockets[0]!;

    // iPad wake fires visibilitychange + pageshow almost together; the
    // second reconnect supersedes `first` before it ever opened.
    reconnectNow();
    const second = allMockWebSockets[1]!;
    expect(second).toBeDefined();
    expect(second).not.toBe(first);

    // A frame the server already had in flight is delivered to the
    // orphaned socket after it was superseded — it must NOT propagate.
    first.fireOpen();
    first.fireMessage(titleFrame("ghost"));
    expect(onMessage).not.toHaveBeenCalled();

    // The live socket delivers normally.
    second.fireOpen();
    second.fireMessage(titleFrame("live"));
    expect(onMessage).toHaveBeenCalledTimes(1);
  });

  it("a frame in flight on the previous socket is not double-delivered after a fresh connect", () => {
    connect();
    const first = allMockWebSockets[0]!;
    first.fireOpen();
    first.fireMessage(titleFrame("one"));
    expect(onMessage).toHaveBeenCalledTimes(1);

    // A new connect() while connected supersedes `first` (its double-call
    // guard aborts the existing socket before creating the replacement).
    connect();
    const second = allMockWebSockets[1]!;
    expect(second).not.toBe(first);

    first.fireMessage(titleFrame("dup"));
    expect(onMessage).toHaveBeenCalledTimes(1); // orphan can't re-deliver

    second.fireOpen();
    second.fireMessage(titleFrame("two"));
    expect(onMessage).toHaveBeenCalledTimes(2);
  });
});

describe("connection: wake reconnect resumes from the held index (bug 2)", () => {
  // Bug 2: on iOS wake the socket is frequently a zombie that still reads
  // OPEN, so the old reconnectNow() early-returned on "connected" and never
  // resynced — anything printed during sleep stayed missing until a manual
  // refresh. These pin the two halves of the fix: (a) reconnectNow()
  // unconditionally tears down a healthy socket and opens a fresh one, and
  // (b) the resume frame carries the highest absolute line index the client
  // holds (getHaveThrough), so the server backfills exactly the missed lines.
  let onMessage: ReturnType<typeof vi.fn<(msg: ServerMessage) => void>>;

  beforeEach(() => {
    allMockWebSockets.length = 0;
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    onMessage = vi.fn<(msg: ServerMessage) => void>();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function baseCallbacks() {
    return {
      onMessage,
      onOpen: () => {
        /* no-op */
      },
      onClose: () => {
        /* no-op */
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    };
  }

  it("tears down a healthy connected socket and opens a fresh one", () => {
    init(baseCallbacks());
    connect();
    const first = allMockWebSockets[0]!;
    first.fireOpen(); // status === "connected": the zombie-looking-but-healthy state
    expect(first.closed).toBe(false);

    // Simulate iOS wake. The old code returned early here (status was
    // "connected") and never resynced — the exact bug-2 smoking gun.
    reconnectNow();

    expect(first.closed).toBe(true); // the (possibly zombie) socket is torn down ...
    expect(allMockWebSockets.length).toBe(2); // ... and a fresh socket is opened
    expect(allMockWebSockets[1]!).not.toBe(first);
  });

  it("sends a resume frame carrying the haveThrough the consumer reports", () => {
    init({ ...baseCallbacks(), getHaveThrough: () => 42 });
    connect();
    const sock = allMockWebSockets[0]!;
    sock.fireOpen();

    const resume = controlFramesSent(sock).find((m) => m.type === "resume");
    expect(resume).toBeDefined();
    expect(resume!.haveThrough).toBe(42); // server replays only lines after 42
  });

  it("falls back to haveThrough = -1 (full retained replay) when no getHaveThrough is wired", () => {
    init(baseCallbacks());
    connect();
    const sock = allMockWebSockets[0]!;
    sock.fireOpen();

    const resume = controlFramesSent(sock).find((m) => m.type === "resume");
    expect(resume).toBeDefined();
    expect(resume!.haveThrough).toBe(-1);
  });

  it("probes a silent socket with a ping and reconnects when the probe goes unanswered", () => {
    init(baseCallbacks());
    connect();
    const first = allMockWebSockets[0]!;
    first.fireOpen();

    // No inbound frames at all. After IDLE_BEFORE_PROBE_MS of silence the
    // heartbeat actively probes with a ping (10s; one tick lands at 10s).
    vi.advanceTimersByTime(10_000);
    expect(controlFramesSent(first).some((m) => m.type === "ping")).toBe(true);

    // The probe draws no pong (nor any other frame): the socket is stale.
    // Past the grace window the heartbeat tears it down and opens a fresh one.
    vi.advanceTimersByTime(11_000);
    expect(first.closed).toBe(true);
    expect(allMockWebSockets.length).toBe(2);
    expect(allMockWebSockets[1]!).not.toBe(first);
  });

  it("keeps a socket whose probe is answered by a pong (and the pong stays out of the UI)", () => {
    init(baseCallbacks());
    connect();
    const first = allMockWebSockets[0]!;
    first.fireOpen();

    vi.advanceTimersByTime(10_000); // triggers a probe ping
    expect(controlFramesSent(first).some((m) => m.type === "ping")).toBe(true);

    // The pong arrives. It refreshes the liveness clock (so the socket is not
    // declared stale) and decodes to null (so it never reaches onMessage).
    first.fireMessage(pongFrame());
    expect(onMessage).not.toHaveBeenCalled();

    // Advance past the point where an unanswered probe WOULD have reconnected:
    // because the pong reset the clock, the socket is kept.
    vi.advanceTimersByTime(11_000);
    expect(first.closed).toBe(false);
    expect(allMockWebSockets.length).toBe(1);
  });
});

describe("generateSessionId: session token is cryptographically random", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("delegates to crypto.randomUUID when available", () => {
    vi.stubGlobal("crypto", {
      randomUUID: () => "11111111-2222-3333-4444-555555555555",
      getRandomValues: () => {
        throw new Error("should not be called when randomUUID exists");
      },
    });
    expect(generateSessionId()).toBe("11111111-2222-3333-4444-555555555555");
  });

  it("falls back to crypto.getRandomValues (a CSPRNG) when randomUUID is absent", () => {
    // randomUUID requires a secure context; getRandomValues does not, so
    // this is the path taken on a plain-HTTP origin.
    const getRandomValues = vi.fn((arr: Uint8Array) => {
      for (let i = 0; i < arr.length; i++) {
        arr[i] = i;
      }
      return arr;
    });
    vi.stubGlobal("crypto", { getRandomValues });

    const id = generateSessionId();

    expect(getRandomValues).toHaveBeenCalledTimes(1);
    // 16 bytes -> 32 lowercase hex chars; deterministic given the stub.
    expect(id).toBe("000102030405060708090a0b0c0d0e0f");
  });

  it("produces a 128-bit lowercase-hex token via the getRandomValues fallback", () => {
    // Use the environment's real CSPRNG, asserting only on shape so the
    // test stays deterministic without pinning the bytes.
    const realGet = globalThis.crypto.getRandomValues.bind(globalThis.crypto);
    vi.stubGlobal("crypto", { getRandomValues: realGet });

    const a = generateSessionId();
    const b = generateSessionId();

    expect(a).toMatch(/^[0-9a-f]{32}$/);
    expect(b).toMatch(/^[0-9a-f]{32}$/);
    expect(a).not.toBe(b);
  });

  it("never falls back to Math.random", () => {
    const mathRandom = vi.spyOn(Math, "random");
    vi.stubGlobal("crypto", {
      getRandomValues: (arr: Uint8Array) => arr,
    });

    generateSessionId();

    expect(mathRandom).not.toHaveBeenCalled();
  });

  it("fails closed when no Web Crypto RNG is available (no guessable token)", () => {
    // crypto present but without either method, e.g. a stripped global.
    vi.stubGlobal("crypto", {});
    expect(() => generateSessionId()).toThrow(/secure RNG/);
  });
});
