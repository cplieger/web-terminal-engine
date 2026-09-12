// The connection layer's EPHEMERAL half: best-effort input that never enters the
// reliable ledger, and the focus state the server derives from.
//
// The two input classes have to be pinned APART, which is why one file holds
// both: the reliable outbox exists so a keystroke survives a blip, and the whole
// point of the ephemeral channel is that a mouse report must not. An SGR-1006
// report carries no sequence number and no timestamp, so a receiver cannot tell
// that it describes a screen which has since been repainted
// (https://invisible-island.net/xterm/ctlseqs/ctlseqs.html, "Mouse Tracking") —
// replaying one after a resume delivers a click against different content.
//
// The observable is deliberately the WIRE, and for the ledger it is the resume
// control's own `sentBytes`: that is the only exported view of the byte counter,
// and asserting the absence of a frame would not pin the invariant — a counted
// byte that never left still corrupts the ack arithmetic.
//
// Refusing to send on a dead socket is what xterm.js's AttachAddon does (returns
// false rather than sending) and what ttyd does (returns early unless the socket
// is open). Reporting the current focus when an application ENABLES DEC 1004 is
// what xterm.js's _reportFocus does.
//
// Drives the REAL connection module with a fake global WebSocket, the same shape
// connection-outbox.test.ts uses.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  disconnect,
  init,
  reconnectNow,
  sendBinary,
  sendEphemeral,
  setClientFocus,
  setSession,
} from "./connection.js";
import * as modes from "./modes.js";
import * as mouse from "./mouse.js";
import { WIRE_PROTOCOL_VERSION } from "./wire-compatibility.js";

/** ackFlags bit2 / bit3, as the Go encoder packs them (resumeAckFlags.bits). */
const ACK_FLAG_SERVER_FOCUS = 4;
const ACK_FLAG_EPHEMERAL_INPUT = 8;

/** A press of button 0 at column 10, row 5 in the SGR-1006 encoding. */
const REPORT = "\x1b[<0;10;5M";

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

/** A 35-byte resumeAck, mirroring encodeResumeAck (wire_binary.go). */
function resumeAckFrame(opts: { received?: number; flags?: number }): ArrayBuffer {
  const buf = new ArrayBuffer(35);
  const v = new DataView(buf);
  v.setUint8(0, 2); // MSG_RESUME_ACK
  v.setBigUint64(1, BigInt(opts.received ?? 0), true);
  v.setBigUint64(9, 0n, true); // serverEpoch 0: no restart detection
  v.setBigUint64(17, 0n, true); // committed
  v.setBigUint64(25, 0n, true); // oldestIndex
  v.setUint8(33, WIRE_PROTOCOL_VERSION);
  v.setUint8(34, opts.flags ?? 0);
  return buf;
}

/** A modes frame, mirroring encodeModesMsg: flags byte, mouseMode, kbdFlags. */
function modesFrame(opts: { focusReporting: boolean }): ArrayBuffer {
  const buf = new ArrayBuffer(13);
  const v = new DataView(buf);
  v.setUint8(0, 3); // MSG_MODES
  v.setBigUint64(1, 0n, true); // inputAck
  v.setUint8(9, opts.focusReporting ? 8 : 0); // bit3 = DEC 1004
  v.setUint16(10, 1002, true); // mouseMode
  v.setUint8(12, 0); // kbdFlags
  return buf;
}

/** The PTY-input frames this socket was asked to send, decoded as text. */
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

/** The `sentBytes` the socket's resume control claimed: the reliable ledger. */
function resumeSentBytes(sock: MockWS): number {
  const resumes = controlsOfType(sock, "resume");
  const last = resumes.at(-1);
  if (last === undefined) {
    throw new Error("socket sent no resume control");
  }
  return last["sentBytes"] as number;
}

const enc = new TextEncoder();
let session = 0;

/** Bring up a socket for a FRESH session and take it through its resumeAck. */
function openSession(flags: number): MockWS {
  session++;
  setSession(`ephemeral-${String(session)}`);
  const sock = sockets[sockets.length - 1]!;
  sock.fireOpen();
  sock.fireMessage(resumeAckFrame({ flags }));
  return sock;
}

/** Reconnect and answer the new socket's resume with `received`. */
function reconnectAcking(received: number, flags: number): MockWS {
  reconnectNow();
  const sock = sockets[sockets.length - 1]!;
  sock.fireOpen();
  sock.fireMessage(resumeAckFrame({ received, flags }));
  return sock;
}

describe("connection: ephemeral input is best-effort and uncounted", () => {
  beforeEach(() => {
    sockets.length = 0;
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    // The modes singleton and the page-level focus latch outlive one test in
    // this file, so both start from their power-on state.
    modes.setModes(true, false, true, false, 1002, false, false, false);
    init({
      onMessage: () => {
        /* no-op */
      },
      onOpen: () => {
        /* no-op */
      },
      onClose: () => {
        /* no-op */
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    });
    setClientFocus(false);
  });

  afterEach(() => {
    disconnect();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("sends nothing, queues nothing and counts nothing while the socket is down", () => {
    openSession(ACK_FLAG_EPHEMERAL_INPUT);
    sendBinary(enc.encode("abc")); // a real keystroke, so the counter is observable
    disconnect();

    expect(sendEphemeral(REPORT)).toBe(false);

    // The COUNTER, not just the absent frame: a counted byte would push the
    // server's received count past bytesSent, whose clamp then pins bytesAcked
    // and empties the outbox — silently acking keystrokes never delivered.
    const next = reconnectAcking(0, ACK_FLAG_EPHEMERAL_INPUT);
    expect(resumeSentBytes(next)).toBe(3);
    // And nothing was queued for the new socket to replay but the keystroke.
    expect(inputSent(next)).toEqual(["abc"]);
  });

  it("does not retransmit a mouse report where it DOES retransmit a keystroke", () => {
    const sock = openSession(ACK_FLAG_EPHEMERAL_INPUT);
    sendBinary(enc.encode("k"));
    expect(sendEphemeral(REPORT)).toBe(true);
    expect(controlsOfType(sock, "ephemeralInput").length).toBe(1);

    const next = reconnectAcking(0, ACK_FLAG_EPHEMERAL_INPUT);

    // The two classes, pinned apart on one wire: the unacked keystroke comes
    // back, the mouse report does not.
    expect(inputSent(next)).toEqual(["k"]);
    expect(controlsOfType(next, "ephemeralInput")).toEqual([]);
  });

  it("puts the report in an ephemeralInput control when the server declared it", () => {
    const sock = openSession(ACK_FLAG_EPHEMERAL_INPUT);

    expect(sendEphemeral(REPORT)).toBe(true);

    expect(controlsOfType(sock, "ephemeralInput")).toEqual([
      { type: "ephemeralInput", data: REPORT },
    ]);
    // Not through the reliable path: no input frame, and nothing to replay.
    expect(inputSent(sock)).toEqual([]);
  });

  it("falls back to reliable input when the server declared no capability", () => {
    // The documented degradation, pinned rather than left as a comment: total
    // silence would be worse than a stale report, so a server that does not
    // serve the control keeps a working mouse — and pays the retransmit for it.
    const sock = openSession(0);

    expect(sendEphemeral(REPORT)).toBe(true);

    expect(controlsOfType(sock, "ephemeralInput")).toEqual([]);
    expect(inputSent(sock)).toEqual([REPORT]);
    const next = reconnectAcking(0, 0);
    expect(inputSent(next)).toEqual([REPORT]);
  });
});

describe("connection: who reports focus", () => {
  beforeEach(() => {
    sockets.length = 0;
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    modes.setModes(true, false, true, false, 1002, false, false, false);
    init({
      onMessage: () => {
        /* no-op */
      },
      onOpen: () => {
        /* no-op */
      },
      onClose: () => {
        /* no-op */
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    });
    setClientFocus(false);
  });

  afterEach(() => {
    disconnect();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("sends a focus control when the server declared it owns focus", () => {
    const sock = openSession(ACK_FLAG_SERVER_FOCUS);

    setClientFocus(true);

    // The resume re-asserts the current state first, then the change.
    expect(controlsOfType(sock, "focus")).toEqual([
      { type: "focus", focused: false },
      { type: "focus", focused: true },
    ]);
    // No DEC 1004 bytes: the server derives the answer and writes them itself.
    expect(inputSent(sock)).toEqual([]);
  });

  it("repeats nothing for the state already reported", () => {
    const sock = openSession(ACK_FLAG_SERVER_FOCUS);
    setClientFocus(true);
    const before = controlsOfType(sock, "focus").length;

    setClientFocus(true);

    expect(controlsOfType(sock, "focus").length).toBe(before);
  });

  it("re-reports the current focus on the new socket after a resume", () => {
    openSession(ACK_FLAG_SERVER_FOCUS);
    setClientFocus(true);

    const next = reconnectAcking(0, ACK_FLAG_SERVER_FOCUS);

    // Focus is STATE, so the latest wins and a reconnect re-asserts it: without
    // this the new server derives its answer from a client it never heard from.
    expect(controlsOfType(next, "focus")).toEqual([{ type: "focus", focused: true }]);
  });

  it("writes the legacy DEC 1004 bytes when the server declared nothing", () => {
    const sock = openSession(0);
    sock.fireMessage(modesFrame({ focusReporting: true }));
    const beforeFocusIn = inputSent(sock).length;

    setClientFocus(true);

    expect(controlsOfType(sock, "focus")).toEqual([]);
    expect(inputSent(sock).slice(beforeFocusIn)).toEqual(["\x1b[I"]);
    setClientFocus(false);
    expect(inputSent(sock).slice(beforeFocusIn)).toEqual(["\x1b[I", "\x1b[O"]);
  });

  it("writes nothing on the legacy path while the application has 1004 off", () => {
    const sock = openSession(0);
    sock.fireMessage(modesFrame({ focusReporting: false }));

    setClientFocus(true);

    expect(inputSent(sock)).toEqual([]);
  });

  it("reports the current focus on the 1004 enable edge (the legacy path)", () => {
    // An application that enables focus reporting while the tab is ALREADY
    // focused otherwise learns nothing until the user clicks away.
    const sock = openSession(0);
    setClientFocus(true);
    expect(inputSent(sock)).toEqual([]); // 1004 still off: nothing to say

    sock.fireMessage(modesFrame({ focusReporting: true }));

    expect(inputSent(sock)).toEqual(["\x1b[I"]);
  });

  it("does not re-report when a modes frame repeats an already-enabled 1004", () => {
    const sock = openSession(0);
    sock.fireMessage(modesFrame({ focusReporting: true }));
    const afterEnable = inputSent(sock).length;

    sock.fireMessage(modesFrame({ focusReporting: true }));

    expect(inputSent(sock).length).toBe(afterEnable);
  });

  it("queues no focus report while the socket is down", () => {
    // Focus is STATE. The legacy write goes through the RELIABLE outbox, so a
    // report made while the socket is down would be retransmitted on reconnect
    // and assert a focus the widget may since have lost. Assert the COUNTER: the
    // dropped report must add no outbox entry, and the new socket is told the
    // current value by the resume instead.
    const sock = openSession(0);
    sock.fireMessage(modesFrame({ focusReporting: true }));
    setClientFocus(true);
    disconnect();

    setClientFocus(false); // the user clicks away with no socket to say it on

    const next = reconnectAcking(0, 0);
    // Two 3-byte reports were written while connected (the resume's own, then
    // the focus-in); a queued third would make this 9.
    expect(resumeSentBytes(next)).toBe(6);
  });
});

describe("connection: the transport fires the gesture resync", () => {
  // The trigger belongs here rather than in a consumer's callback, and this is the
  // test that says so: `resumeAck` is never forwarded to `onMessage`, so a kernel
  // that hooked it would hold a dead branch and the application would keep a
  // button held for the rest of the session.
  beforeEach(() => {
    sockets.length = 0;
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", makeMockWebSocket());
    modes.setModes(true, false, true, false, 1002, false, false, false);
    init({
      onMessage: () => {
        /* no-op */
      },
      onOpen: () => {
        /* no-op */
      },
      onClose: () => {
        /* no-op */
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    });
    setClientFocus(false);
  });

  afterEach(() => {
    disconnect();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("cancels a gesture recorded before the gap, on the reconnect's resumeAck", () => {
    const term = document.createElement("div");
    // A known box at the viewport origin, so the press below resolves to a cell
    // rather than being refused for landing above the element (body's default
    // margin puts an unstyled div's top edge at y=8).
    Object.assign(term.style, {
      position: "fixed",
      left: "0px",
      top: "0px",
      width: "400px",
      height: "320px",
    });
    document.body.appendChild(term);
    const dispose = mouse.init({
      sendReport: (data) => sendEphemeral(data),
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    });
    try {
      const first = openSession(ACK_FLAG_EPHEMERAL_INPUT);
      // setSession applies the incoming session's mode mirror, so tracking is armed
      // after the attach, as the server's own modes frame would.
      modes.setModes(true, false, true, false, 1002, false, false, false);
      term.dispatchEvent(
        new MouseEvent("mousedown", {
          bubbles: true,
          cancelable: true,
          button: 0,
          buttons: 1,
          clientX: 20,
          clientY: 40,
        }),
      );
      // The release happened while the socket was down, so the browser's next
      // reading is all that says the button is up.
      term.dispatchEvent(
        new MouseEvent("mousemove", { bubbles: true, buttons: 0, clientX: 20, clientY: 40 }),
      );

      // The press must have been DELIVERED, or the pairing rule would correctly
      // decline to release a gesture the application never learned about.
      // (20,40) over an 8x16 cell at the viewport origin is column 3, row 3.
      expect(controlsOfType(first, "ephemeralInput").map((c) => c["data"])).toEqual([
        "\x1b[<0;3;3M",
      ]);

      const sock = reconnectAcking(0, ACK_FLAG_EPHEMERAL_INPUT);

      // The release carries the PRESS's coordinates, per xterm's report grammar.
      const released = controlsOfType(sock, "ephemeralInput").map((c) => c["data"]);
      expect(released).toEqual(["\x1b[<0;3;3m"]);
    } finally {
      dispose();
      term.remove();
    }
  });
});
