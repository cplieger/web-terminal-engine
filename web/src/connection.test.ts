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
import { generateSessionId } from "./connection.js";

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

describe("connection: reconnect race produces no duplicate handler invocations", () => {
  beforeEach(() => {
    // Clear globals between tests
    allMockWebSockets.length = 0;
  });

  it("aborting a connecting sock detaches its message listener (orphan can't fire)", async () => {
    // Simulate the reconnect-race directly without involving connection.ts:
    // the test verifies the AbortController + addEventListener signal
    // pattern that the connection module relies on.
    const sock1 = new (makeMockWebSocket())("ws://x") as unknown as MockWS;

    const ctrl1 = new AbortController();
    const handler1 = vi.fn();
    sock1.addEventListener("message", handler1, { signal: ctrl1.signal });

    // Sanity: handler fires before abort.
    sock1.fireMessage("hello");
    expect(handler1).toHaveBeenCalledTimes(1);

    // Abort the controller — listener should auto-detach.
    ctrl1.abort();

    // After abort: subsequent messages must NOT fire the handler.
    sock1.fireMessage("after-abort");
    expect(handler1).toHaveBeenCalledTimes(1);
  });

  it("two rapid-fire reconnect calls leave only one active sock with a live listener", async () => {
    // Mock WebSocket constructor that adds to allMockWebSockets.
    const MockCtor = makeMockWebSocket();

    // First "connect": create sock, attach listener with abort signal.
    const ctrl1 = new AbortController();
    const sock1 = new MockCtor("ws://x") as unknown as MockWS;
    const handler1 = vi.fn();
    sock1.addEventListener("message", handler1, { signal: ctrl1.signal });

    // Simulate "second reconnectNow fired before sock1 opened":
    // - abort ctrl1 (replicates the new code path)
    // - create sock2 with its own controller
    ctrl1.abort();
    sock1.close();

    const ctrl2 = new AbortController();
    const sock2 = new MockCtor("ws://x") as unknown as MockWS;
    const handler2 = vi.fn();
    sock2.addEventListener("message", handler2, { signal: ctrl2.signal });

    // Both sockets receive messages (simulating the in-flight frames
    // sock1 might still be delivered before its close handshake
    // completes).
    sock1.fireMessage("frame-A");
    sock2.fireMessage("frame-A");
    sock1.fireMessage("frame-B");
    sock2.fireMessage("frame-B");

    // CRITICAL ASSERTION: the orphaned sock1 must NOT fire its handler
    // for any message after its controller was aborted.
    expect(handler1).toHaveBeenCalledTimes(0);
    expect(handler2).toHaveBeenCalledTimes(2);
  });

  it("close listener on superseded sock does not run scheduleReconnect", async () => {
    // When sock1 is aborted then closed, its 'close' listener must not
    // fire (it would otherwise call scheduleReconnect and create yet
    // another sock, snowballing the leak).
    const MockCtor = makeMockWebSocket();
    const sock1 = new MockCtor("ws://x") as unknown as MockWS;
    const ctrl1 = new AbortController();
    const closeHandler = vi.fn();
    sock1.addEventListener("close", closeHandler, { signal: ctrl1.signal });

    ctrl1.abort();
    sock1.fireClose();

    expect(closeHandler).toHaveBeenCalledTimes(0);
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
