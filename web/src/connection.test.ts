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
