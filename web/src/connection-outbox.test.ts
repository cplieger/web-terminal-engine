// The connection's RELIABLE-INPUT half: the pending queue, the in-flight window
// and what a reconnect replays over the new socket. inFlight is the only copy of
// input the server has not confirmed and applyAck the only thing that removes
// bytes from it, so an off-by-one there is silent: keystrokes vanish on a
// reconnect (trimmed too far) or run twice (trimmed too little). The observable
// is the WIRE, the frames each socket sends, never the instance's counters.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  type Connection,
  createConnection,
  type DeferredInput,
  MAX_OUTBOX_BYTES,
} from "./connection.js";
import { createModeState, type ModeState } from "./modes.js";
import { connectionDeps } from "./test-helpers/connection-fakes.js";
import { registerForDispose } from "./test-helpers/engine-fixture.js";
import { WIRE_PROTOCOL_VERSION } from "./wire-compatibility.js";
import limitsRaw from "../../wire-golden/limits.json?raw";

const limits = JSON.parse(limitsRaw) as { maxInputMessageBytes: number };

let conn: Connection;
let modes: ModeState;

interface MockWS {
  readyState: number;
  listeners: Map<string, ((ev: unknown) => void)[]>;
  send: ReturnType<typeof vi.fn>;
  close: ReturnType<typeof vi.fn>;
  addEventListener: (
    type: string,
    handler: (ev: unknown) => void,
    opts?: { signal?: AbortSignal },
  ) => void;
  fireOpen: () => void;
  fireMessage: (data: ArrayBuffer) => void;
}

const sockets: MockWS[] = [];

function makeMockWebSocket(): typeof WebSocket {
  const ctor = function (): MockWS {
    const sock: MockWS = {
      readyState: 0,
      listeners: new Map(),
      send: vi.fn(),
      close: vi.fn(function (this: MockWS) {
        this.readyState = 3;
      }) as unknown as ReturnType<typeof vi.fn>,
      addEventListener(
        this: MockWS,
        type: string,
        handler: (ev: unknown) => void,
        opts?: { signal?: AbortSignal },
      ) {
        if (!this.listeners.has(type)) {
          this.listeners.set(type, []);
        }
        const list = this.listeners.get(type)!;
        list.push(handler);
        if (opts?.signal) {
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
      fireMessage(this: MockWS, data: ArrayBuffer) {
        for (const fn of this.listeners.get("message") ?? []) {
          fn({ data });
        }
      },
    } as unknown as MockWS;
    Object.setPrototypeOf(sock, Mock.prototype);
    sockets.push(sock);
    return sock;
  } as unknown as typeof WebSocket;
  class Mock {}
  ctor.prototype = Mock.prototype as unknown as WebSocket;
  return ctor;
}

/**
 * A resumeAck frame, mirroring encodeResumeAck (wire_binary.go): 35 bytes, so
 * it carries the version byte that arms the typed-framing upgrade. `version`
 * exists for the v3 cases — a server that stays below TYPED_FRAMING_MIN_VERSION
 * leaves the socket in sentinel mode, which is where the leading-NUL split
 * lives.
 */
function resumeAckFrame(opts: {
  received?: number;
  version?: number | undefined;
  ledgerLost?: boolean;
  serverEpoch?: number;
}): ArrayBuffer {
  const buf = new ArrayBuffer(35);
  const v = new DataView(buf);
  v.setUint8(0, 2); // MSG_RESUME_ACK
  v.setBigUint64(1, BigInt(opts.received ?? 0), true);
  v.setBigUint64(9, BigInt(opts.serverEpoch ?? 0), true); // 0: no restart detection
  v.setBigUint64(17, 0n, true); // committed
  v.setBigUint64(25, 0n, true); // oldestIndex
  v.setUint8(33, opts.version ?? WIRE_PROTOCOL_VERSION);
  v.setUint8(34, opts.ledgerLost === true ? 1 : 0); // ackFlags bit0: ledgerLost
  return buf;
}

/** A modes frame, mirroring encodeModesMsg: flags byte (bit0 DEC 2004), mouseMode, kbdFlags. */
function modesFrame(opts: { bracketedPaste?: boolean } = {}): ArrayBuffer {
  const buf = new ArrayBuffer(13);
  const v = new DataView(buf);
  v.setUint8(0, 3); // MSG_MODES
  v.setBigUint64(1, 0n, true); // inputAck
  v.setUint8(9, opts.bracketedPaste === true ? 1 : 0);
  v.setUint16(10, 0, true);
  v.setUint8(12, 0);
  return buf;
}

/** A bare ack, mirroring MSG_ACK_ONLY: [1B type=7][8B inputAck]. */
function ackOnlyFrame(inputAck: number): ArrayBuffer {
  const buf = new ArrayBuffer(9);
  const v = new DataView(buf);
  v.setUint8(0, 7);
  v.setBigUint64(1, BigInt(inputAck), true);
  return buf;
}

/**
 * The PTY-input frames the socket was asked to send, decoded as text. The
 * encoding is the exact discriminator: `controlFrame` sends a Uint8Array and
 * `textControl` a string, while every input path, live send and retransmit
 * alike, sends an ArrayBuffer. So this sees keystrokes and nothing else, a
 * solitary NUL frame included, which a "first byte is 0x00" filter would swallow.
 */
function inputSent(sock: MockWS): string[] {
  const calls = (sock.send as unknown as { mock: { calls: unknown[][] } }).mock.calls;
  const out: string[] = [];
  for (const c of calls) {
    if (c[0] instanceof ArrayBuffer) {
      out.push(new TextDecoder().decode(c[0]));
    }
  }
  return out;
}

/** Byte lengths of the input frames sent, so a test can pin FRAMING, not text. */
function inputFrameSizes(sock: MockWS): number[] {
  const calls = (sock.send as unknown as { mock: { calls: unknown[][] } }).mock.calls;
  const out: number[] = [];
  for (const c of calls) {
    if (c[0] instanceof ArrayBuffer) {
      out.push(c[0].byteLength);
    }
  }
  return out;
}

/** Every input byte the socket was asked to send, in order. */
function inputBytes(sock: MockWS): Uint8Array {
  const calls = (sock.send as unknown as { mock: { calls: unknown[][] } }).mock.calls;
  const parts = calls.flatMap((c) => (c[0] instanceof ArrayBuffer ? [new Uint8Array(c[0])] : []));
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
}

/** A paste of `n` bytes whose every byte depends on its offset, so order shows. */
function paste(n: number): Uint8Array {
  const out = new Uint8Array(n);
  for (let i = 0; i < n; i++) {
    out[i] = 0x21 + (i % 89);
  }
  return out;
}

const enc = new TextEncoder();
const MiB = 1 << 20;

let pendingReports: ReturnType<typeof vi.fn<(bytes: number, cancelable: boolean) => void>>;
let serverRestart: ReturnType<typeof vi.fn<() => void>>;
let computeSize: ReturnType<typeof vi.fn<() => { cols: number; rows: number }>>;
let session = 0;

/** Bring up a socket for a FRESH session and take it through its resumeAck. */
function openSession(opts: { version?: number | undefined } = {}): MockWS {
  session++;
  conn.setSession(`outbox-${String(session)}`);
  const sock = sockets[sockets.length - 1]!;
  sock.fireOpen();
  sock.fireMessage(resumeAckFrame({ received: 0, version: opts.version }));
  return sock;
}

/** A paste as a sender queues it: its body and closing tail, encoded when sent. */
function pasteInput(body: Uint8Array, tail = new Uint8Array(0)): DeferredInput {
  return { paste: true, size: body.length, encode: () => ({ body, tail }) };
}

/** Reconnect and answer the new socket's resume with `received`. */
function reconnectAcking(received: number, opts: { version?: number | undefined } = {}): MockWS {
  conn.reconnectNow();
  const sock = sockets[sockets.length - 1]!;
  sock.fireOpen();
  sock.fireMessage(resumeAckFrame({ received, version: opts.version }));
  return sock;
}

describe("connection: the outbox and the acked-byte window", () => {
  beforeEach(() => {
    sockets.length = 0;
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    pendingReports = vi.fn<(bytes: number, cancelable: boolean) => void>();
    serverRestart = vi.fn<() => void>();
    computeSize = vi.fn<() => { cols: number; rows: number }>(() => ({ cols: 80, rows: 24 }));
    modes = createModeState();
    conn = registerForDispose(
      createConnection({
        ...connectionDeps(),
        modes,
        callbacks: {
          onMessage: () => undefined,
          onOpen: () => undefined,
          onClose: () => undefined,
          computeSize,
          onPendingInput: pendingReports,
          onServerRestart: serverRestart,
        },
      }),
    );
  });

  afterEach(() => {
    conn.disconnect();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  describe("streaming input", () => {
    it("delivers a 5 MiB paste whole, at most 1 MiB in flight, in messages the server accepts", () => {
      const sock = openSession();
      const data = paste(5 * MiB);

      expect(conn.sendBinary(data)).toBe(true);
      expect(inputBytes(sock).length).toBe(MAX_OUTBOX_BYTES);
      expect(conn.pendingInputBytes()).toBe(5 * MiB - MAX_OUTBOX_BYTES);
      for (let acked = 0; acked < 5 * MiB;) {
        acked = inputBytes(sock).length;
        sock.fireMessage(ackOnlyFrame(acked));
      }

      expect(inputBytes(sock)).toEqual(data);
      expect(Math.max(...inputFrameSizes(sock))).toBe(limits.maxInputMessageBytes);
      expect(conn.pendingInputBytes()).toBe(0);
    });

    it("sends a key typed during a paste after the paste's last byte", () => {
      const sock = openSession();
      const data = paste(2 * MiB);
      conn.sendBinary(data);
      conn.sendBinary(enc.encode("\r"));

      sock.fireMessage(ackOnlyFrame(MiB));
      sock.fireMessage(ackOnlyFrame(2 * MiB));

      const sent = inputBytes(sock);
      expect(sent.length).toBe(2 * MiB + 1);
      expect(sent.subarray(0, 2 * MiB)).toEqual(data);
      expect(sent[2 * MiB]).toBe(0x0d);
    });

    it("cancel drops the unsent rest, still sends the tail, and keeps keys typed after", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      const data = paste(3 * MiB);
      conn.sendBinary(pasteInput(data, enc.encode("\x1b[201~")));
      conn.sendBinary(enc.encode("k"));

      expect(conn.cancelPendingInput()).toBe(true);
      sock.fireMessage(ackOnlyFrame(MAX_OUTBOX_BYTES));

      const sent = inputBytes(sock);
      expect(sent.subarray(0, MAX_OUTBOX_BYTES)).toEqual(data.subarray(0, MAX_OUTBOX_BYTES));
      expect(new TextDecoder().decode(sent.subarray(MAX_OUTBOX_BYTES))).toBe("\x1b[201~k");
      expect(conn.cancelPendingInput()).toBe(false);
    });

    it("keeps a held session's input off the wire, in order, until the last hold is released", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      const releaseA = conn.holdInput();
      const releaseB = conn.holdInput();
      conn.sendBinary(enc.encode("a"));
      conn.sendBinary({ size: 1, encode: () => ({ body: enc.encode("b") }) });

      releaseA();
      releaseA(); // a second call does nothing
      expect(inputSent(sock)).toEqual([]);
      expect(conn.pendingInputBytes()).toBe(2);

      releaseB();
      expect(inputSent(sock)).toEqual(["a", "b"]);
    });

    it("holds only the session it was taken on", () => {
      const sockA = openSession();
      sockA.fireMessage(modesFrame());
      const release = conn.holdInput();
      conn.sendBinary(enc.encode("for A"));
      const sockB = openSession();
      sockB.fireMessage(modesFrame());
      conn.sendBinary(enc.encode("for B"));
      expect(inputSent(sockB)).toEqual(["for B"]);

      release();
      expect(inputSent(sockB)).toEqual(["for B"]);
      conn.setSession(`outbox-${String(session - 1)}`);
      const back = sockets[sockets.length - 1]!;
      back.fireOpen();
      back.fireMessage(resumeAckFrame({ received: 0 }));
      expect(inputSent(back)).toEqual(["for A"]);
    });

    it("cuts a cancelable paste only between UTF-8 characters", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      // 3-byte characters: 1 MiB is not a multiple of 3, so the window ends mid-character.
      conn.sendBinary(pasteInput(enc.encode("€".repeat(MiB))));

      conn.cancelPendingInput();

      const sent = inputBytes(sock);
      expect(sent.length).toBe(MAX_OUTBOX_BYTES - (MAX_OUTBOX_BYTES % 3));
      expect(new TextDecoder("utf-8", { fatal: true }).decode(sent)).toBe(
        "€".repeat(sent.length / 3),
      );
    });

    it("delivers a cancelable entry that is not UTF-8 whole, cut at the window", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      const data = new Uint8Array(2 * MiB).fill(0x80);
      conn.sendBinary(pasteInput(data));
      expect(inputBytes(sock).length).toBe(MAX_OUTBOX_BYTES);

      sock.fireMessage(ackOnlyFrame(MAX_OUTBOX_BYTES));
      sock.fireMessage(ackOnlyFrame(2 * MiB));

      expect(inputBytes(sock)).toEqual(data);
      expect(conn.pendingInputBytes()).toBe(0);
    });

    it("resends the in-flight bytes once after a reconnect mid-paste, then continues", () => {
      const first = openSession();
      const data = paste(2 * MiB);
      conn.sendBinary(data);
      first.fireMessage(ackOnlyFrame(MiB / 2));

      const next = reconnectAcking(MiB / 2);
      next.fireMessage(ackOnlyFrame((3 * MiB) / 2));

      expect(inputBytes(next)).toEqual(data.subarray(MiB / 2));
    });

    it("sends a key typed before the resumeAck once, after the ack", () => {
      openSession();
      conn.reconnectNow();
      const sock = sockets[sockets.length - 1]!;
      sock.fireOpen();
      conn.sendBinary(enc.encode("x"));

      expect(inputSent(sock)).toEqual([]);
      sock.fireMessage(resumeAckFrame({ received: 0 }));

      expect(inputSent(sock)).toEqual(["x"]);
    });

    it("claims no sent bytes for input typed before the first socket, and delivers it", () => {
      session++;
      conn.setSession(`outbox-${String(session)}`);
      conn.sendBinary(enc.encode("early"));
      const sock = sockets[sockets.length - 1]!;
      sock.fireOpen();

      expect(controlsOfType(sock, "resume")[0]?.["sentBytes"]).toBe(0);
      sock.fireMessage(resumeAckFrame({ received: 0 }));
      expect(inputSent(sock)).toEqual(["early"]);
    });

    it("keeps unsent input across a lost ledger", () => {
      const first = openSession();
      conn.sendBinary(paste(MiB + 3));

      conn.reconnectNow();
      const next = sockets[sockets.length - 1]!;
      next.fireOpen();
      next.fireMessage(resumeAckFrame({ received: 0, ledgerLost: true }));

      expect(serverRestart).toHaveBeenCalledTimes(1);
      expect(inputBytes(next)).toEqual(paste(MiB + 3).subarray(MiB));
      expect(inputBytes(first).length).toBe(MiB);
    });

    it("reports the pending count at each zero edge and at most every 250 ms between", () => {
      const sock = openSession();
      conn.sendBinary(paste(3 * MiB));
      expect(pendingReports.mock.calls).toEqual([[2 * MiB, false]]);

      sock.fireMessage(ackOnlyFrame(MiB));
      expect(pendingReports).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(250);
      expect(pendingReports.mock.calls.at(-1)).toEqual([MiB, false]);

      sock.fireMessage(ackOnlyFrame(2 * MiB));
      expect(pendingReports.mock.calls.at(-1)).toEqual([0, false]);
    });

    it("reports another session's pending count at once on a switch", () => {
      openSession();
      const first = conn.currentSessionId();
      conn.sendBinary(paste(MiB + 5));
      openSession();
      conn.sendBinary(paste(MiB + 7));
      expect(pendingReports.mock.calls).toEqual([
        [5, false],
        [0, false],
        [7, false],
      ]);

      conn.setSession(first);

      expect(pendingReports.mock.calls.at(-1)).toEqual([5, false]);
    });

    it("reports whether a cancel would drop part of the pending input", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      conn.sendBinary(pasteInput(paste(2 * MiB), enc.encode("\x1b[201~")));
      expect(pendingReports.mock.calls).toEqual([[MiB + 6, true]]);

      conn.sendBinary(enc.encode("k"));
      conn.cancelPendingInput();
      expect(pendingReports.mock.calls.at(-1)).toEqual([7, false]);

      sock.fireMessage(ackOnlyFrame(MiB));
      expect(pendingReports.mock.calls.at(-1)).toEqual([0, false]);
    });

    it("refuses input only once disposed", () => {
      openSession();
      conn.dispose();

      expect(conn.sendBinary(enc.encode("x"))).toBe(false);
    });
  });

  describe("input whose bytes depend on the modes", () => {
    /** A key whose bytes follow DEC 2004, so the modes it was encoded under show. */
    function modeKey(): { encode: ReturnType<typeof vi.fn<DeferredInput["encode"]>> } {
      return {
        encode: vi.fn<DeferredInput["encode"]>(() => ({
          body: enc.encode(modes.isBracketedPaste() ? "B" : "p"),
        })),
      };
    }

    it("encodes it once, under the modes frame's values, and sends what follows it after", () => {
      const sock = openSession();
      const key = modeKey();
      conn.sendBinary(key);
      conn.sendBinary(enc.encode("k"));

      expect(key.encode).not.toHaveBeenCalled();
      expect(inputSent(sock)).toEqual([]);
      sock.fireMessage(modesFrame({ bracketedPaste: true }));

      expect(key.encode).toHaveBeenCalledTimes(1);
      expect(inputSent(sock)).toEqual(["B", "k"]);
    });

    it("drops input accepted while the link was down when the resumeAck reports a new process", () => {
      const first = openSession();
      first.fireMessage(resumeAckFrame({ received: 0, serverEpoch: 111 }));
      first.fireMessage(modesFrame());
      conn.reconnectNow();
      const key = modeKey();
      conn.sendBinary(key);
      conn.sendBinary(pasteInput(enc.encode("make deploy\r")));

      const next = sockets[sockets.length - 1]!;
      next.fireOpen();
      next.fireMessage(resumeAckFrame({ received: 0, serverEpoch: 999 }));
      next.fireMessage(modesFrame());

      expect(serverRestart).toHaveBeenCalledTimes(1);
      expect(key.encode).not.toHaveBeenCalled();
      expect(inputSent(next)).toEqual([]);
      expect(conn.pendingInputBytes()).toBe(0);
    });

    it("cancels a paste still waiting for its modes whole, and keeps the keys after it", () => {
      const sock = openSession();
      const encode = vi.fn(() => ({
        body: enc.encode("\x1b[200~x"),
        tail: enc.encode("\x1b[201~"),
      }));
      conn.sendBinary({ paste: true, size: 1, encode });
      conn.sendBinary(enc.encode("k"));

      expect(conn.cancelPendingInput()).toBe(true);
      sock.fireMessage(modesFrame());

      expect(encode).not.toHaveBeenCalled();
      expect(inputSent(sock)).toEqual(["k"]);
    });

    it("reports a paste waiting for its modes as cancelable, at its declared size", () => {
      openSession();
      conn.sendBinary(pasteInput(paste(5000)));

      expect(pendingReports.mock.calls).toEqual([[5000, true]]);
      expect(conn.pendingInputBytes()).toBe(5000);
    });

    it("keeps it with its session across a switch and encodes it under that session's modes", () => {
      const a = openSession();
      const aId = conn.currentSessionId();
      const key = modeKey();
      conn.sendBinary(key);
      const b = openSession();
      b.fireMessage(modesFrame({ bracketedPaste: true }));

      expect(key.encode).not.toHaveBeenCalled();
      conn.setSession(aId);
      const a2 = sockets[sockets.length - 1]!;
      a2.fireOpen();
      a2.fireMessage(resumeAckFrame({ received: 0 }));
      a2.fireMessage(modesFrame());

      expect(inputSent(a)).toEqual([]);
      expect(inputSent(b)).toEqual([]);
      expect(inputSent(a2)).toEqual(["p"]);
    });

    it("sends nothing for an empty body, its tail included", () => {
      const sock = openSession();
      sock.fireMessage(modesFrame());
      conn.sendBinary(pasteInput(new Uint8Array(0), enc.encode("\x1b[201~")));
      conn.sendBinary(enc.encode("k"));

      expect(inputSent(sock)).toEqual(["k"]);
    });

    it("drops an input whose encoder throws and sends the rest", () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
      const sock = openSession();
      sock.fireMessage(modesFrame());
      conn.sendBinary({
        encode: () => {
          throw new Error("boom");
        },
      });
      conn.sendBinary(enc.encode("k"));

      expect(inputSent(sock)).toEqual(["k"]);
      expect(warn).toHaveBeenCalledTimes(1);
      warn.mockRestore();
    });

    it("leaves input queued by an encoder's consumer to the same pass, in order", () => {
      const sock = openSession();
      conn.sendBinary({
        encode: () => {
          conn.sendBinary(enc.encode("2"));
          return { body: enc.encode("1") };
        },
      });
      sock.fireMessage(modesFrame());

      expect(inputSent(sock)).toEqual(["1", "2"]);
    });

    it("encodes once an input whose encoder cancels an encoded paste queued ahead of it", () => {
      const sock = openSession();
      conn.sendBinary(pasteInput(enc.encode("rm -rf build\r")));
      const encode = vi.fn<DeferredInput["encode"]>(() => {
        conn.cancelPendingInput();
        return { body: enc.encode("\x03") };
      });
      conn.sendBinary({ encode });
      sock.fireMessage(modesFrame());
      conn.sendBinary(enc.encode("k"));

      expect(encode).toHaveBeenCalledTimes(1);
      expect(inputSent(sock)).toEqual(["\x03", "k"]);
    });

    it("drops a paste whose own encoder cancels it", () => {
      const sock = openSession();
      conn.sendBinary({
        paste: true,
        size: 4,
        encode: () => {
          conn.cancelPendingInput();
          return { body: enc.encode("text"), tail: enc.encode("\x1b[201~") };
        },
      });
      conn.sendBinary(enc.encode("k"));
      sock.fireMessage(modesFrame());

      expect(inputSent(sock)).toEqual(["k"]);
      expect(conn.pendingInputBytes()).toBe(0);
    });

    it("calls no later encoder once an encoder forgets the shown session", () => {
      const sock = openSession();
      const id = conn.currentSessionId();
      conn.sendBinary({
        encode: () => {
          conn.forgetSession(id);
          return { body: enc.encode("1") };
        },
      });
      const later = modeKey();
      conn.sendBinary(later);
      sock.fireMessage(modesFrame());

      expect(later.encode).not.toHaveBeenCalled();
      expect(inputSent(sock)).toEqual([]);
    });

    it("keeps an encoder's result for its session when the encoder switches away, and encodes the rest there", () => {
      const a = openSession();
      const aId = conn.currentSessionId();
      conn.sendBinary({
        encode: () => {
          conn.setSession("outbox-elsewhere");
          return { body: enc.encode("1") };
        },
      });
      const later = modeKey();
      conn.sendBinary(later);
      a.fireMessage(modesFrame({ bracketedPaste: true }));

      expect(later.encode).not.toHaveBeenCalled();
      conn.setSession(aId);
      const a2 = sockets[sockets.length - 1]!;
      a2.fireOpen();
      a2.fireMessage(resumeAckFrame({ received: 0 }));
      a2.fireMessage(modesFrame());

      expect(later.encode).toHaveBeenCalledTimes(1);
      expect(inputSent(a2)).toEqual(["1", "p"]);
    });

    it("sends nothing and calls no later encoder once an encoder disposes the connection", () => {
      const sock = openSession();
      conn.sendBinary({
        encode: () => {
          conn.dispose();
          return { body: enc.encode("1") };
        },
      });
      const later = modeKey();
      conn.sendBinary(later);
      sock.fireMessage(modesFrame());

      expect(later.encode).not.toHaveBeenCalled();
      expect(inputSent(sock)).toEqual([]);
    });
  });

  describe("what a reconnect replays", () => {
    it("replays exactly the bytes the resume ack left unacked", () => {
      openSession();
      conn.sendBinary(enc.encode("hello"));

      // The server confirms 2 of the 5 bytes: "he" is applied, "llo" is not.
      const next = reconnectAcking(2);

      expect(inputSent(next)).toEqual(["llo"]);
    });

    it("trims a second ack from the already-trimmed head", () => {
      openSession();
      conn.sendBinary(enc.encode("hello"));
      const first = sockets[sockets.length - 1]!;
      first.fireMessage(ackOnlyFrame(2));
      first.fireMessage(ackOnlyFrame(4));

      // Both acks landed inside the same chunk, so the second one has to
      // measure its drop against the REMAINING head, not the original.
      const next = reconnectAcking(4);

      expect(inputSent(next)).toEqual(["o"]);
    });

    it("drops every chunk an ack spans and keeps the partial remainder", () => {
      openSession();
      conn.sendBinary(enc.encode("ab"));
      conn.sendBinary(enc.encode("cde"));

      // 4 of 5 bytes acked: the whole first chunk plus two bytes of the second.
      const next = reconnectAcking(4);

      expect(inputSent(next)).toEqual(["e"]);
    });

    it("replays nothing once every sent byte is acked", () => {
      const errors = vi.spyOn(console, "error").mockImplementation(() => {
        /* keep the log quiet; the assertion is that it stays empty */
      });
      openSession();
      conn.sendBinary(enc.encode("ab"));
      conn.sendBinary(enc.encode("cd"));
      const first = sockets[sockets.length - 1]!;
      first.fireMessage(ackOnlyFrame(4));

      const next = reconnectAcking(4);

      // A fully-acked outbox that still holds a chunk is duplicate input: the
      // server already ran those keystrokes.
      expect(inputSent(next)).toEqual([]);
      // And the walk that emptied it has to TERMINATE. A throw inside the ack
      // handling is swallowed by the frame listener's try/catch, so a walk that
      // runs off the end of the outbox leaves no trace except this log line —
      // and skips the retransmit that follows it.
      expect(errors).not.toHaveBeenCalled();
    });

    it("does not let a stale ack reopen the window a later ack closed", () => {
      openSession();
      conn.sendBinary(enc.encode("hello"));
      const first = sockets[sockets.length - 1]!;
      first.fireMessage(ackOnlyFrame(3));
      // A late duplicate from the flush sweep, below the high-water mark. It
      // must not move the window backwards: the received=0 resume below is the
      // server telling us it no longer holds this ledger, and the client can
      // only tell that from a bytesAcked the stale ack did not zero.
      first.fireMessage(ackOnlyFrame(0));

      const next = reconnectAcking(0);

      expect(inputSent(next)).toEqual([]);
      expect(serverRestart).toHaveBeenCalledTimes(1);
    });

    it("clamps an ack that claims more bytes than were sent, so later acks still trim", () => {
      openSession();
      conn.sendBinary(enc.encode("hello"));
      const first = sockets[sockets.length - 1]!;
      // A server that counts bytes we never sent (its own control frames, say)
      // would otherwise park bytesAcked above every future ack, and the outbox
      // would never trim again.
      first.fireMessage(ackOnlyFrame(99));
      conn.sendBinary(enc.encode("more"));
      first.fireMessage(ackOnlyFrame(7));

      const next = reconnectAcking(7);

      expect(inputSent(next)).toEqual(["re"]);
    });
  });

  describe("v3 framing of replayed input", () => {
    it("splits leading NULs into solitary frames with no empty frame after them", () => {
      // A server below the typed-framing floor never latches, so every frame
      // the client sends stays parseable as a v3 control unless each leading
      // 0x00 goes out alone. The trailing byte count matters as much as the
      // split: an empty frame is not a control and not input.
      const sock = openSession({ version: 3 });

      conn.sendBinary(new Uint8Array([0x00, 0x00]));

      expect(inputFrameSizes(sock)).toEqual([1, 1]);
    });

    it("splits a NUL that a message-size cut leaves at the front of the next message", () => {
      const sock = openSession({ version: 3 });
      const data = new Uint8Array(limits.maxInputMessageBytes + 2);
      data.fill(0x61);
      data[limits.maxInputMessageBytes] = 0x00;

      conn.sendBinary(data);

      expect(inputFrameSizes(sock)).toEqual([limits.maxInputMessageBytes, 1, 1]);
    });

    it("sends the remainder after a leading NUL as one frame", () => {
      const sock = openSession({ version: 3 });

      conn.sendBinary(new Uint8Array([0x00, 0x41, 0x42]));

      expect(inputFrameSizes(sock)).toEqual([1, 2]);
    });
  });

  describe("resize coalescing", () => {
    it("announces a resize that changed only the row count", () => {
      const sock = openSession();
      computeSize.mockReturnValue({ cols: 80, rows: 24 });
      conn.sendResize();
      const before = controlsOfType(sock, "resize").length;
      computeSize.mockReturnValue({ cols: 80, rows: 30 });

      conn.sendResize();

      // Both dimensions have to match for a resize to be redundant. Suppressing
      // on either one alone loses a real geometry change, and the server keeps
      // rendering at the old size.
      expect(controlsOfType(sock, "resize").length).toBe(before + 1);
      expect(controlsOfType(sock, "resize").at(-1)).toEqual({ type: "resize", cols: 80, rows: 30 });
    });

    it("announces a resize that changed only the column count", () => {
      const sock = openSession();
      computeSize.mockReturnValue({ cols: 80, rows: 24 });
      conn.sendResize();
      const before = controlsOfType(sock, "resize").length;
      computeSize.mockReturnValue({ cols: 100, rows: 24 });

      conn.sendResize();

      // The mirror of the row-only case: each dimension has to be compared on
      // its own, and a column-only change is the common one (a rotated phone).
      expect(controlsOfType(sock, "resize").length).toBe(before + 1);
      expect(controlsOfType(sock, "resize").at(-1)).toEqual({
        type: "resize",
        cols: 100,
        rows: 24,
      });
    });

    it("does not ask the consumer to measure while there is no socket", () => {
      openSession();
      conn.disconnect();
      computeSize.mockClear();

      conn.sendResize();

      // Measuring forces layout in a real consumer, and the answer could not be
      // sent anywhere: the dedup baseline is reset on every open, so a size
      // recorded now would only be a lie about what the server has been told.
      expect(computeSize).not.toHaveBeenCalled();
    });
  });
});

/** Control frames of one type, decoded from either encoding, in order. */
function controlsOfType(sock: MockWS, type: string): Record<string, unknown>[] {
  const calls = (sock.send as unknown as { mock: { calls: unknown[][] } }).mock.calls;
  const out: Record<string, unknown>[] = [];
  for (const c of calls) {
    const a = c[0];
    if (typeof a === "string") {
      const msg = JSON.parse(a) as Record<string, unknown>;
      if (msg["type"] === type) {
        out.push(msg);
      }
      continue;
    }
    if (!(a instanceof Uint8Array) || a.length === 0 || a[0] !== 0x00) {
      continue;
    }
    const msg = JSON.parse(new TextDecoder().decode(a.subarray(1))) as Record<string, unknown>;
    if (msg["type"] === type) {
      out.push(msg);
    }
  }
  return out;
}
