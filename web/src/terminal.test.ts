import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import { createConnection } from "./connection.js";
import { bracketTextForPaste } from "./keyboard.js";
import { createModeState, POWER_ON_MODES } from "./modes.js";
import { createTerminalEngine, type TerminalEngineOptions } from "./terminal.js";
import {
  appendTerminalElements,
  createEngineFixture,
  type EngineFixture,
  noopCallbacks,
} from "./test-helpers/engine-fixture.js";
import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";
import { WIRE_PROTOCOL_VERSION } from "./wire-compatibility.js";

// Spies that call through: the rollback cases make `createConnection` throw and
// reach the mode state a failed build made through `createModeState`'s results.
vi.mock("./connection.js", { spy: true });
vi.mock("./modes.js", { spy: true });

interface MockWS {
  url: string;
  binaryType: string;
  readyState: number;
  closeArgs: unknown[][];
  sent: unknown[];
  listeners: Map<string, ((ev: unknown) => void)[]>;
  send: (data: unknown) => void;
  close: (...args: unknown[]) => void;
  addEventListener: (
    type: string,
    handler: (ev: unknown) => void,
    opts?: { signal?: AbortSignal },
  ) => void;
  fireOpen: () => void;
  fireMessage: (data: ArrayBuffer | Blob) => void;
  fireClose: (code: number) => void;
}

const sockets: MockWS[] = [];

function makeMockWebSocket(): typeof WebSocket {
  const ctor = function (url: string): MockWS {
    const sock: MockWS = {
      url,
      binaryType: "blob",
      readyState: 0,
      closeArgs: [],
      sent: [],
      listeners: new Map(),
      send(this: MockWS, data: unknown) {
        this.sent.push(data);
      },
      close(this: MockWS, ...args: unknown[]) {
        this.readyState = 3;
        this.closeArgs.push(args);
      },
      addEventListener(
        this: MockWS,
        type: string,
        handler: (ev: unknown) => void,
        opts?: { signal?: AbortSignal },
      ) {
        const list = this.listeners.get(type) ?? [];
        this.listeners.set(type, list);
        list.push(handler);
        opts?.signal?.addEventListener("abort", () => {
          const idx = list.indexOf(handler);
          if (idx >= 0) {
            list.splice(idx, 1);
          }
        });
      },
      fireOpen(this: MockWS) {
        this.readyState = 1;
        for (const fn of [...(this.listeners.get("open") ?? [])]) {
          fn({});
        }
      },
      fireMessage(this: MockWS, data: ArrayBuffer | Blob) {
        for (const fn of [...(this.listeners.get("message") ?? [])]) {
          fn({ data });
        }
      },
      fireClose(this: MockWS, code: number) {
        this.readyState = 3;
        for (const fn of [...(this.listeners.get("close") ?? [])]) {
          fn({ code, reason: "" });
        }
      },
    };
    Object.setPrototypeOf(sock, Mock.prototype);
    sockets.push(sock);
    return sock;
  } as unknown as typeof WebSocket;
  class Mock {}
  ctor.prototype = Mock.prototype as unknown as WebSocket;
  return ctor;
}

function latest(): MockWS {
  const sock = sockets[sockets.length - 1];
  if (sock === undefined) {
    throw new Error("no socket was created");
  }
  return sock;
}

/** Control messages the socket was asked to send, from either encoding. */
function controls(sock: MockWS): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  for (const a of sock.sent) {
    if (typeof a === "string") {
      out.push(JSON.parse(a) as Record<string, unknown>);
    } else if (a instanceof Uint8Array && a.length > 0 && a[0] === 0x00) {
      out.push(JSON.parse(new TextDecoder().decode(a.subarray(1))) as Record<string, unknown>);
    }
  }
  return out;
}

function controlsOfType(sock: MockWS, type: string): Record<string, unknown>[] {
  return controls(sock).filter((m) => m["type"] === type);
}

/** PTY input frames; input is always an ArrayBuffer. */
function inputFrames(sock: MockWS): ArrayBuffer[] {
  return sock.sent.filter((a): a is ArrayBuffer => a instanceof ArrayBuffer);
}

/** A 35-byte resumeAck as `wire_binary.go` writes it: the form with the version byte and the flags. */
function resumeAckFrame(
  opts: {
    received?: number;
    serverEpoch?: number;
    committed?: number;
    oldestIndex?: number;
    paging?: boolean;
  } = {},
): ArrayBuffer {
  const buf = new ArrayBuffer(35);
  const v = new DataView(buf);
  v.setUint8(0, 2);
  v.setBigUint64(1, BigInt(opts.received ?? 0), true);
  v.setBigUint64(9, BigInt(opts.serverEpoch ?? 0), true);
  v.setBigUint64(17, BigInt(opts.committed ?? 0), true);
  v.setBigUint64(25, BigInt(opts.oldestIndex ?? 0), true);
  v.setUint8(33, WIRE_PROTOCOL_VERSION);
  v.setUint8(34, opts.paging === true ? 2 : 0);
  return buf;
}

/** MSG_SCREEN with `rowCount` changed rows, each one run holding one ASCII `glyph` (default "x"). */
function screenFrame(rowCount: number, glyph = 0x78): ArrayBuffer {
  const rowBytes = 2 + 2 + (2 + 1 + 4 + 4 + 2 + 4 + 2);
  const buf = new ArrayBuffer(27 + rowCount * rowBytes);
  const v = new DataView(buf);
  v.setUint8(0, 0);
  v.setBigUint64(1, 0n, true);
  v.setBigUint64(9, 0n, true);
  v.setUint16(17, 0, true);
  v.setUint16(19, 0, true);
  v.setUint16(21, rowCount, true);
  v.setUint16(23, rowCount, true);
  v.setUint8(25, 0);
  v.setUint8(26, 1);
  let off = 27;
  for (let i = 0; i < rowCount; i++) {
    v.setUint16(off, i, true);
    v.setUint16(off + 2, 1, true);
    v.setUint16(off + 4, 1, true);
    v.setUint8(off + 6, glyph);
    v.setInt32(off + 7, -1, true);
    v.setInt32(off + 11, -1, true);
    v.setUint16(off + 15, 0, true);
    v.setInt32(off + 17, -1, true);
    v.setUint16(off + 21, 0, true);
    off += rowBytes;
  }
  return buf;
}

/** MSG_SCROLL of `count` history lines from `firstIndex`, each one run holding "h". */
function scrollFrame(firstIndex: number, count: number): ArrayBuffer {
  const lineBytes = 2 + (2 + 1 + 4 + 4 + 2 + 4 + 2);
  const buf = new ArrayBuffer(19 + count * lineBytes);
  const v = new DataView(buf);
  v.setUint8(0, 1);
  v.setBigUint64(1, 0n, true);
  v.setBigUint64(9, BigInt(firstIndex), true);
  v.setUint16(17, count, true);
  let off = 19;
  for (let i = 0; i < count; i++) {
    v.setUint16(off, 1, true);
    v.setUint16(off + 2, 1, true);
    v.setUint8(off + 4, 0x68);
    v.setInt32(off + 5, -1, true);
    v.setInt32(off + 9, -1, true);
    v.setUint16(off + 13, 0, true);
    v.setInt32(off + 15, -1, true);
    v.setUint16(off + 19, 0, true);
    off += lineBytes;
  }
  return buf;
}

/** MSG_MODES with the given flags byte; mouseMode 0 and no kitty flags. */
function modesFrame(flags: number): ArrayBuffer {
  const buf = new ArrayBuffer(13);
  const v = new DataView(buf);
  v.setUint8(0, 3);
  v.setBigUint64(1, 0n, true);
  v.setUint8(9, flags);
  v.setUint16(10, 0, true);
  v.setUint8(12, 0);
  return buf;
}

/** A decoded screen message of `rowCount` rows, for the renderer's own entry point. */
function screenMessage(rowCount: number): ScreenMessage {
  const row: WireRun[] = [{ t: "x", f: -1, b: -1, a: 0, uc: -1 }];
  return {
    type: "screen",
    base: 0,
    rows: Array.from({ length: rowCount }, () => row),
    cursor: [0, 0],
    changed: Array.from({ length: rowCount }, (_, i) => i),
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: true,
  };
}

/** A decoded scroll message of `count` history lines from index 0, each one "h" run. */
function scrollMessage(count: number): ScrollMessage {
  const line: WireRun[] = [{ t: "h", f: -1, b: -1, a: 0, uc: -1 }];
  return { type: "scroll", firstIndex: 0, lines: Array.from({ length: count }, () => line) };
}

/** Two frames deep: the first callback can run in the frame the flush was queued in. */
async function nextFrames(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

function rows(output: HTMLElement): number {
  return output.querySelectorAll(".term-row").length;
}

function rowTexts(output: HTMLElement): string[] {
  return [...output.querySelectorAll(".term-row")].map((el) => el.textContent ?? "");
}

/** A managed session on `fx`, open and acked, so content frames are admitted. */
function openSession(
  fx: EngineFixture,
  id: string,
  ack: Parameters<typeof resumeAckFrame>[0] = {},
): MockWS {
  fx.engine.connection.setSession(id);
  const sock = latest();
  sock.fireOpen();
  sock.fireMessage(resumeAckFrame(ack));
  return sock;
}

function mouseEvent(type: string, el: HTMLElement, init: MouseEventInit = {}): MouseEvent {
  const r = el.getBoundingClientRect();
  return new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: r.left + 4,
    clientY: r.bottom - 4,
    button: 0,
    buttons: 1,
    ...init,
  });
}

beforeEach(() => {
  sockets.length = 0;
  vi.stubGlobal("WebSocket", makeMockWebSocket());
});

describe("two engines on one page", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("a screen frame on each engine paints its own output and touches nothing of the other's", async () => {
    // Both engines paint the SAME absolute rows: a row map or a render queue
    // shared at module scope would hand B's frame A's elements, so A's rows
    // would change glyph and B's output would stay empty.
    vi.useRealTimers();
    const a = createEngineFixture();
    const b = createEngineFixture();
    const sockA = openSession(a, "a");
    const sockB = openSession(b, "b");

    sockA.fireMessage(screenFrame(3, 0x61)); // "a"
    await nextFrames();
    expect(rowTexts(a.output)).toEqual(["a", "a", "a"]);
    expect(rows(b.output)).toBe(0);
    expect(b.engine.renderer.getHighestIndex()).toBe(-1);

    sockB.fireMessage(screenFrame(2, 0x62)); // "b"
    await nextFrames();
    expect(rowTexts(b.output)).toEqual(["b", "b"]);
    expect(rowTexts(a.output)).toEqual(["a", "a", "a"]);
    expect(a.engine.renderer.getHighestIndex()).toBe(2);
    expect(b.engine.renderer.getHighestIndex()).toBe(1);
  });

  it("a frame on A leaves B's row backlog for B to build", async () => {
    // A backlog past the per-frame budget is the one state a flush drains from
    // its QUEUE rather than its store, so a queue shared at module scope would
    // let A's flush consume B's owed rows: B's count drops to zero and the rows
    // are never built.
    vi.useRealTimers();
    const a = createEngineFixture();
    const b = createEngineFixture();

    b.engine.renderer.handleScroll(scrollMessage(400));
    a.engine.renderer.handleScreen(screenMessage(1));
    await nextFrames();
    await nextFrames();

    expect(rows(b.output)).toBe(400);
    expect(b.engine.renderer.pendingRowCount()).toBe(0);
    expect(rows(a.output)).toBe(1);
  });

  it("a modes frame decoded on A leaves B's mode state at power-on", () => {
    const a = createEngineFixture();
    const b = createEngineFixture();
    const sock = openSession(a, "a");

    sock.fireMessage(modesFrame(0));

    expect(a.engine.modes.isBracketedPaste()).toBe(false);
    expect(b.engine.modes.isBracketedPaste()).toBe(true);
    expect(bracketTextForPaste("x", b.engine.modes)).toBe("\x1b[200~x\x1b[201~");
    expect(bracketTextForPaste("x", a.engine.modes)).toBe("x");
  });

  it("a mousedown on A's terminal reports through A's connection only", async () => {
    vi.useRealTimers();
    const a = createEngineFixture();
    const b = createEngineFixture();
    for (const fx of [a, b]) {
      fx.termWrap.style.width = "400px";
      fx.termWrap.style.height = "100px";
      fx.engine.renderer.handleScreen(screenMessage(3));
      fx.engine.modes.applySnapshot({ ...POWER_ON_MODES, mouseMode: 1000, mouseSGR: true });
    }
    await nextFrames();
    const sentA = vi.spyOn(a.engine.connection, "sendEphemeral");
    const sentB = vi.spyOn(b.engine.connection, "sendEphemeral");

    a.termWrap.dispatchEvent(mouseEvent("mousedown", a.termWrap));

    expect(sentA).toHaveBeenCalledTimes(1);
    expect(sentA.mock.calls[0]![0]).toBe("\x1b[<0;1;3M"); // left press at cell (1,3), the grid's bottom-left
    expect(sentB).not.toHaveBeenCalled();
  });

  it("managed sessions open one socket each, and closing A's reaches A's onClose only", () => {
    const closedA = vi.fn();
    const closedB = vi.fn();
    const a = createEngineFixture({ callbacks: { onClose: closedA } });
    const b = createEngineFixture({ callbacks: { onClose: closedB } });

    a.engine.connection.setSession("a");
    b.engine.connection.setSession("b");

    expect(sockets).toHaveLength(2);
    expect(new URL(sockets[0]!.url).searchParams.get("session")).toBe("a");
    expect(new URL(sockets[1]!.url).searchParams.get("session")).toBe("b");

    sockets[0]!.fireOpen();
    sockets[1]!.fireOpen();
    sockets[0]!.fireClose(1006);

    expect(closedA).toHaveBeenCalledTimes(1);
    expect(closedB).not.toHaveBeenCalled();
  });

  it("unmanaged engines under distinct sessionIdKeys mint distinct ids on the same bare URL", () => {
    sessionStorage.clear();
    const a = createEngineFixture({ sessionIdKey: "k1" });
    const b = createEngineFixture({ sessionIdKey: "k2" });

    a.engine.connection.connect();
    b.engine.connection.connect();
    const [sockA, sockB] = [sockets[0]!, sockets[1]!];
    sockA.fireOpen();
    sockB.fireOpen();

    expect(sockA.url).toBe(sockB.url);
    expect(sockA.url).not.toContain("session=");
    const resumeA = controls(sockA)[0]!;
    const resumeB = controls(sockB)[0]!;
    expect(resumeA["type"]).toBe("resume");
    expect(resumeB["type"]).toBe("resume");
    expect(resumeA["sessionId"]).not.toBe(resumeB["sessionId"]);
    expect(sessionStorage.getItem("k1")).toBe(resumeA["sessionId"]);
    expect(sessionStorage.getItem("k2")).toBe(resumeB["sessionId"]);
  });
});

describe("--char-w", () => {
  it("is written on the terminal element, not on the document root", () => {
    const fx = createEngineFixture();
    fx.engine.renderer.updateFontMetrics();

    expect(fx.termWrap.style.getPropertyValue("--char-w")).toMatch(/^\d+(\.\d+)?px$/);
    expect(document.documentElement.style.getPropertyValue("--char-w")).toBe("");
  });
});

describe("injected collaborators are read at call time", () => {
  it("a spy installed on the scroll controller after construction sees the next paint", async () => {
    const fx = createEngineFixture();
    const read = vi.spyOn(fx.engine.scroll, "currentScrollTop");

    fx.engine.renderer.handleScreen(screenMessage(2));
    await nextFrames();

    expect(read).toHaveBeenCalled();
  });
});

describe("what the engine wires between its parts", () => {
  /** A 100px viewport over 1000px of content, so a scrollTop write is a real scroll. */
  function makeScrollable(fx: EngineFixture): void {
    fx.termWrap.style.height = "100px";
    fx.termWrap.style.overflowY = "auto";
    fx.output.style.height = "1000px";
  }

  it("a screen frame reaches the consumer's onMessage after the renderer has taken it", () => {
    const highestWhenSeen: number[] = [];
    const fx = createEngineFixture({
      callbacks: {
        onMessage: (msg) => {
          if (msg.type === "screen") {
            highestWhenSeen.push(fx.engine.renderer.getHighestIndex());
          }
        },
      },
    });
    const sock = openSession(fx, "s");

    sock.fireMessage(screenFrame(3));

    expect(highestWhenSeen).toEqual([2]);
  });

  it("a scroll frame from the socket paints history rows", async () => {
    const fx = createEngineFixture();
    const sock = openSession(fx, "s");

    sock.fireMessage(scrollFrame(0, 2));
    await nextFrames();

    expect(rowTexts(fx.output)).toEqual(["h", "h"]);
  });

  it("wsPath names the socket endpoint", () => {
    const fx = createEngineFixture({ wsPath: "/pane/ws" });

    fx.engine.connection.connect();

    expect(new URL(latest().url).pathname).toBe("/pane/ws");
  });

  it("a user scroll away from the bottom reaches onUserScrollChange", () => {
    const changes: boolean[] = [];
    const fx = createEngineFixture({
      onUserScrollChange: (scrolledUp) => {
        changes.push(scrolledUp);
      },
    });
    makeScrollable(fx);

    fx.termWrap.scrollTop = 500;
    fx.termWrap.dispatchEvent(new Event("scroll"));
    fx.termWrap.scrollTop = 900;
    fx.termWrap.dispatchEvent(new Event("scroll"));

    expect(changes).toEqual([true, false]);
    expect(fx.engine.scroll.isUserScrolledUp()).toBe(false);
  });

  it("a scroll event on the terminal element reaches the renderer's scroll handler", () => {
    const fx = createEngineFixture();
    makeScrollable(fx);
    const handled = vi.spyOn(fx.engine.renderer, "handleScrollPosition");

    fx.termWrap.scrollTop = 500;
    fx.termWrap.dispatchEvent(new Event("scroll"));

    expect(handled).toHaveBeenCalledTimes(1);
  });
});

describe("dispose()", () => {
  it("releases every listener, timer, frame and the socket it holds", () => {
    let frameId = 0;
    const raf = vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation(() => ++frameId);
    const caf = vi.spyOn(globalThis, "cancelAnimationFrame").mockImplementation(() => undefined);
    const setIv = vi.spyOn(globalThis, "setInterval");
    const clearIv = vi.spyOn(globalThis, "clearInterval");
    const removeDoc = vi.spyOn(document, "removeEventListener");
    const fx = createEngineFixture();
    const sock = openSession(fx, "s");
    sock.fireMessage(screenFrame(3));
    fx.engine.modes.applySnapshot({ ...POWER_ON_MODES, mouseMode: 1003, mouseSGR: true });
    // Mode 1003 reports buttonless motion, so this arms the coalescing frame.
    fx.termWrap.dispatchEvent(mouseEvent("mousemove", fx.termWrap, { buttons: 0 }));
    expect(raf).toHaveBeenCalledTimes(2);
    expect(setIv).toHaveBeenCalledTimes(2); // the blink interval and the heartbeat

    fx.engine.dispose();

    const requested = raf.mock.results.map((r) => r.value as number);
    const cancelled = caf.mock.calls.map((c) => c[0]);
    expect(cancelled).toEqual(expect.arrayContaining(requested));
    const started = setIv.mock.results.map((r) => r.value as unknown);
    const cleared = clearIv.mock.calls.map((c) => c[0]);
    expect(cleared).toEqual(expect.arrayContaining(started));
    expect(removeDoc.mock.calls.map((c) => c[0])).toContain("visibilitychange");
    expect(sock.closeArgs[0]).toEqual([1000]);
  });

  it("clears the DOM it wrote and leaves the elements inert", async () => {
    const fx = createEngineFixture();
    const sock = openSession(fx, "s");
    fx.engine.renderer.updateFontMetrics();
    sock.fireMessage(screenFrame(3));
    sock.fireMessage(modesFrame(32)); // DECSCNM on
    await nextFrames();
    expect(rows(fx.output)).toBe(3);
    expect(fx.termWrap.classList.contains("term-reverse-video")).toBe(true);
    fx.engine.modes.applySnapshot({ ...POWER_ON_MODES, mouseMode: 1000, mouseSGR: true });
    const sentBefore = sock.sent.length;

    fx.engine.dispose();

    expect(fx.output.childElementCount).toBe(0);
    expect(fx.termWrap.style.getPropertyValue("--char-w")).toBe("");
    expect(fx.termWrap.classList.contains("cursor-blink-off")).toBe(false);
    expect(fx.termWrap.classList.contains("term-reverse-video")).toBe(false);
    fx.termWrap.scrollTop = 40;
    fx.termWrap.dispatchEvent(new Event("scroll"));
    fx.termWrap.dispatchEvent(mouseEvent("mousedown", fx.termWrap));
    await nextFrames();
    expect(fx.engine.scroll.isUserScrolledUp()).toBe(false);
    expect(sock.sent).toHaveLength(sentBefore);
    expect(fx.output.childElementCount).toBe(0);
  });

  it("is idempotent, and the post-dispose mutators return without effect", () => {
    const fx = createEngineFixture();
    const sock = openSession(fx, "s");
    const sent = vi.spyOn(fx.engine.connection, "sendEphemeral");
    fx.engine.dispose();
    const closes = sock.closeArgs.length;

    fx.engine.dispose();
    fx.engine.scroll.restoreView({ top: 40, following: false });
    fx.engine.mouse.resyncGesture();
    fx.engine.mouse.disarmGesture();
    fx.engine.modes.applySnapshot({ ...POWER_ON_MODES, bracketedPaste: false });
    fx.engine.connection.connect();
    fx.engine.connection.setSession("t");

    expect(sock.closeArgs).toHaveLength(closes);
    expect(fx.termWrap.scrollTop).toBe(0);
    expect(fx.engine.scroll.isUserScrolledUp()).toBe(false);
    expect(sent).not.toHaveBeenCalled();
    expect(fx.engine.modes.isBracketedPaste()).toBe(true);
    expect(sockets).toHaveLength(1);
  });

  it("answers the typed empty value from every non-void method", () => {
    const fx = createEngineFixture({ maxLines: 500 });
    const sock = openSession(fx, "s", { serverEpoch: 7, committed: 10, oldestIndex: 2 });
    sock.fireMessage(screenFrame(3));
    sock.fireMessage(modesFrame(0));
    expect(fx.engine.modes.isBracketedPaste()).toBe(false);
    fx.engine.dispose();
    const { renderer, scroll, connection, modes } = fx.engine;

    expect(renderer.getHighestIndex()).toBe(-1);
    expect(renderer.getReplayBoundary()).toBe(-1);
    expect(renderer.lastBrowseActivityMs()).toBe(0);
    expect(renderer.browseCacheSize()).toBe(0);
    expect(renderer.pendingRowCount()).toBe(0);
    expect(renderer.replayMaxForResume()).toBe(5000);
    expect(renderer.captureViewMemory()).toBeNull();
    expect(renderer.pendingRestoreAbs()).toBeNull();
    expect(renderer.computeSize()).toEqual({ cols: 0, rows: 0 });
    expect(renderer.gridSize()).toEqual({ cols: 0, rows: 0 });
    expect(renderer.cellSize()).toEqual({ width: 0, height: 0 });
    expect(renderer.getCursorPx()).toEqual({ left: 0, top: 0, cellH: 0 });
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(scroll.currentScrollTop()).toBe(0);
    expect(connection.sendBinary(new Uint8Array([65]))).toBe(false);
    expect(connection.sendEphemeral("x")).toBe(false);
    expect(connection.requestHistory(0, 10)).toBe(false);
    expect(connection.historyBudget()).toBe(0);
    expect(connection.serverEpochOf("s")).toBe(0);
    expect(connection.currentSessionId()).toBe("");
    expect(modes.snapshot()).toEqual(POWER_ON_MODES);
  });

  it("mints a detached store on every boundStore() call", () => {
    const fx = createEngineFixture();
    fx.engine.renderer.handleScreen(screenMessage(3));
    fx.engine.dispose();

    const first = fx.engine.renderer.boundStore();
    first.applyScreen(screenMessage(2));
    const second = fx.engine.renderer.boundStore();

    expect(second).not.toBe(first);
    expect(first.highestIndex()).toBe(1);
    expect(second.highestIndex()).toBe(-1);
    expect(fx.engine.renderer.getHighestIndex()).toBe(-1);
  });
});

describe("construction rollback", () => {
  const injected = new Error("injected");
  let termWrap: HTMLDivElement;
  let output: HTMLDivElement;
  let addWrap: MockInstance<HTMLElement["addEventListener"]>;
  let removeWrap: MockInstance<HTMLElement["removeEventListener"]>;
  let addDoc: MockInstance<Document["addEventListener"]>;
  let removeDoc: MockInstance<Document["removeEventListener"]>;
  let setIv: MockInstance<typeof setInterval>;
  let clearIv: MockInstance<typeof clearInterval>;
  let raf: MockInstance<typeof requestAnimationFrame>;
  let caf: MockInstance<typeof cancelAnimationFrame>;

  beforeEach(() => {
    ({ termWrap, output } = appendTerminalElements());
    addWrap = vi.spyOn(termWrap, "addEventListener");
    removeWrap = vi.spyOn(termWrap, "removeEventListener");
    addDoc = vi.spyOn(document, "addEventListener");
    removeDoc = vi.spyOn(document, "removeEventListener");
    setIv = vi.spyOn(globalThis, "setInterval");
    clearIv = vi.spyOn(globalThis, "clearInterval");
    raf = vi.spyOn(globalThis, "requestAnimationFrame");
    caf = vi.spyOn(globalThis, "cancelAnimationFrame");
  });

  afterEach(() => {
    termWrap.remove();
  });

  function build(
    opts: TerminalEngineOptions = { output, termWrap, callbacks: noopCallbacks() },
  ): unknown {
    try {
      createTerminalEngine(opts);
    } catch (err) {
      return err;
    }
    throw new Error("createTerminalEngine did not throw");
  }

  /** Every acquisition the spies saw succeed has a matching release. */
  function expectEverythingReleased(): void {
    const added = addWrap.mock.calls.map((c) => c[0]);
    const removed = removeWrap.mock.calls.map((c) => c[0]);
    expect(removed).toEqual(expect.arrayContaining(added));
    const addedDoc = addDoc.mock.calls.map((c) => c[0]).filter((t) => t === "visibilitychange");
    const removedDoc = removeDoc.mock.calls.map((c) => c[0]);
    expect(removedDoc).toEqual(expect.arrayContaining(addedDoc));
    const started = setIv.mock.results.map((r) => r.value as unknown);
    const cleared = clearIv.mock.calls.map((c) => c[0]);
    expect(cleared).toEqual(expect.arrayContaining(started));
    const requested = raf.mock.results.map((r) => r.value as unknown);
    const cancelled = caf.mock.calls.map((c) => c[0]);
    expect(cancelled).toEqual(expect.arrayContaining(requested));
    expect(output.childElementCount).toBe(0);
    expect(termWrap.style.getPropertyValue("--char-w")).toBe("");
  }

  it("a renderer that cannot register its visibility listener leaves no scroll listener behind and disposes the mode state", () => {
    addDoc.mockImplementation((...args: unknown[]) => {
      if (args[0] === "visibilitychange") {
        throw injected;
      }
      return (EventTarget.prototype.addEventListener as (...a: unknown[]) => void).apply(
        document,
        args,
      );
    });

    const caught = build({
      output,
      termWrap,
      callbacks: noopCallbacks(),
      initialModes: { ...POWER_ON_MODES, bracketedPaste: false },
    });

    expect(caught).toBe(injected);
    expect(addWrap.mock.calls.map((c) => c[0])).toEqual(["scroll"]);
    expect(removeWrap.mock.calls.map((c) => c[0])).toEqual(["scroll"]);
    expectEverythingReleased();
    const modes = vi.mocked(createModeState).mock.results[0]!.value;
    modes.applySnapshot({ ...POWER_ON_MODES, bracketedPaste: false });
    expect(modes.isBracketedPaste()).toBe(true);
  });

  it("a mouse controller that cannot register its wheel listener unwinds the three before it and the renderer", () => {
    addWrap.mockImplementation((...args: unknown[]) => {
      if (args[0] === "wheel") {
        throw injected;
      }
      return (EventTarget.prototype.addEventListener as (...a: unknown[]) => void).apply(
        termWrap,
        args,
      );
    });

    const caught = build();

    expect(caught).toBe(injected);
    expect(addWrap.mock.calls.map((c) => c[0])).toEqual([
      "scroll",
      "mousedown",
      "mouseup",
      "mousemove",
      "wheel",
    ]);
    expect(setIv).toHaveBeenCalledTimes(1); // the blink interval was live
    expect(addDoc.mock.calls.map((c) => c[0])).toContain("visibilitychange");
    expectEverythingReleased();
  });

  it("a connection factory that throws unwinds all four earlier instances and opens no socket", () => {
    vi.mocked(createConnection).mockImplementationOnce(() => {
      throw injected;
    });

    const caught = build();

    expect(caught).toBe(injected);
    expect(createConnection).toHaveBeenCalledTimes(1);
    expect(addWrap.mock.calls.map((c) => c[0])).toEqual([
      "scroll",
      "mousedown",
      "mouseup",
      "mousemove",
      "wheel",
    ]);
    // Reverse build order: the mouse controller unwinds before the scroll controller.
    expect(removeWrap.mock.calls.map((c) => c[0])).toEqual([
      "mousedown",
      "mouseup",
      "mousemove",
      "wheel",
      "scroll",
    ]);
    expectEverythingReleased();
    expect(sockets).toHaveLength(0);
  });
});

describe("POWER_ON_MODES", () => {
  it("is frozen, so a stray assignment throws and changes nothing", () => {
    expect(Object.isFrozen(POWER_ON_MODES)).toBe(true);
    expect(() => {
      (POWER_ON_MODES as { bracketedPaste: boolean }).bracketedPaste = false;
    }).toThrow(TypeError);
    expect(POWER_ON_MODES.bracketedPaste).toBe(true);
    expect(createModeState().isBracketedPaste()).toBe(true);
  });
});

describe("maxLines", () => {
  it.each([
    [0, 5000, true],
    [-1, 5000, true],
    [1.5, 5000, true],
    [NaN, 5000, true],
    [Infinity, 5000, true],
    [500, 500, false],
  ])("%s gives a tail cap of %s (warns: %s)", (maxLines, cap, warns) => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const fx = createEngineFixture({ maxLines });

    expect(fx.engine.renderer.boundStore().tailCap()).toBe(cap);
    if (warns) {
      expect(warn).toHaveBeenCalledWith(`vterm: ignoring invalid maxLines ${String(maxLines)}`);
    } else {
      expect(warn).not.toHaveBeenCalled();
    }
  });

  it("absent means the compatibility cap with no warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const fx = createEngineFixture();

    expect(fx.engine.renderer.boundStore().tailCap()).toBe(5000);
    expect(warn).not.toHaveBeenCalled();
  });
});

describe("dispose during a reconnect wait", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("control: the schedule is live, so a closed socket is replaced within 9 seconds", () => {
    const onClose = vi.fn();
    const fx = createEngineFixture({ callbacks: { onClose } });
    fx.engine.connection.connect();
    latest().fireOpen();
    latest().fireClose(1006);
    expect(onClose).toHaveBeenCalledTimes(1);

    vi.advanceTimersByTime(9_000);

    expect(sockets).toHaveLength(2);
  });

  it("disposing during the wait cancels the timer and no socket is ever opened", () => {
    const onClose = vi.fn();
    const fx = createEngineFixture({ callbacks: { onClose } });
    fx.engine.connection.connect();
    latest().fireOpen();
    latest().fireClose(1006);
    expect(onClose).toHaveBeenCalledTimes(1);

    fx.engine.dispose();
    vi.advanceTimersByTime(9_000);

    expect(sockets).toHaveLength(1);
  });
});

describe("dispose with a history request in flight", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("closes the solicited window and lets the data timer fire nothing", () => {
    const fx = createEngineFixture();
    const sock = openSession(fx, "s", { paging: true });
    expect(fx.engine.connection.requestHistory(500, 10)).toBe(true);
    const cleared = vi.spyOn(fx.engine.renderer, "clearSolicited");
    const retried = vi.spyOn(fx.engine.renderer, "maybeFetchHistory");

    fx.engine.dispose();
    vi.advanceTimersByTime(9_000);

    expect(cleared).toHaveBeenCalled();
    expect(retried).not.toHaveBeenCalled();
    expect(controlsOfType(sock, "history")).toHaveLength(1);
  });
});

describe("dispose from inside a callback", () => {
  let fx: EngineFixture;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("onConnecting: connect() opens no socket", () => {
    fx = createEngineFixture({ callbacks: { onConnecting: () => fx.engine.dispose() } });

    fx.engine.connection.connect();

    expect(sockets).toHaveLength(0);
  });

  it("onOpen: no heartbeat starts, the socket closes with 1000 and no ping ever leaves", () => {
    fx = createEngineFixture({ callbacks: { onOpen: () => fx.engine.dispose() } });
    fx.engine.connection.connect();
    const sock = latest();

    sock.fireOpen();

    expect(vi.getTimerCount()).toBe(0); // no heartbeat, no blink, no connect timeout
    expect(sock.closeArgs[0]).toEqual([1000]);
    vi.advanceTimersByTime(60_000);
    expect(controlsOfType(sock, "ping")).toEqual([]);
  });

  it("onClose from the socket's close: no reconnect is scheduled", () => {
    fx = createEngineFixture({ callbacks: { onClose: () => fx.engine.dispose() } });
    fx.engine.connection.connect();
    latest().fireOpen();

    latest().fireClose(1006);
    vi.advanceTimersByTime(9_000);

    expect(sockets).toHaveLength(1);
  });

  it("onClose from the connect timeout: no reconnect is scheduled", () => {
    fx = createEngineFixture({ callbacks: { onClose: () => fx.engine.dispose() } });
    fx.engine.connection.connect();

    vi.advanceTimersByTime(10_000);
    vi.advanceTimersByTime(9_000);

    expect(sockets).toHaveLength(1);
  });

  it("getReplayMax: the open handler sends no resume frame", () => {
    fx = createEngineFixture({
      callbacks: {
        getReplayMax: () => {
          fx.engine.dispose();
          return 500;
        },
      },
    });
    fx.engine.connection.connect();
    const sock = latest();

    sock.fireOpen();

    expect(controlsOfType(sock, "resume")).toEqual([]);
  });

  it("computeSize: sendResize() sends no resize frame and the socket closes with 1000", () => {
    fx = createEngineFixture({
      callbacks: {
        computeSize: () => {
          fx.engine.dispose();
          return { cols: 100, rows: 40 };
        },
      },
    });
    const sock = openSession(fx, "s");

    fx.engine.connection.sendResize();

    expect(controlsOfType(sock, "resize")).toEqual([]);
    expect(sock.closeArgs[0]).toEqual([1000]);
  });

  it("onServerRestart: the ack handler does none of the work that follows it", () => {
    const onResumeBounds = vi.fn();
    fx = createEngineFixture({
      callbacks: { onResumeBounds, onServerRestart: () => fx.engine.dispose() },
    });
    const sock = openSession(fx, "s", { serverEpoch: 1, committed: 10, oldestIndex: 0 });
    expect(fx.engine.connection.sendBinary(new Uint8Array([65, 66]))).toBe(true);
    expect(onResumeBounds).toHaveBeenCalledTimes(1);
    const inputsBefore = inputFrames(sock).length;
    const textsBefore = sock.sent.filter((a) => typeof a === "string").length;
    const resync = vi.spyOn(fx.engine.mouse, "resyncGesture");
    const applied = vi.spyOn(fx.engine.modes, "applySnapshot");

    sock.fireMessage(
      resumeAckFrame({ serverEpoch: 2, received: 0, committed: 12, oldestIndex: 0 }),
    );
    sock.fireMessage(modesFrame(0));

    expect(onResumeBounds).toHaveBeenCalledTimes(1);
    expect(inputFrames(sock)).toHaveLength(inputsBefore);
    expect(sock.sent.filter((a) => typeof a === "string")).toHaveLength(textsBefore);
    expect(controlsOfType(sock, "history")).toEqual([]);
    expect(resync).not.toHaveBeenCalled();
    expect(applied).not.toHaveBeenCalled();
  });
});

describe("a Blob frame resolving after dispose", () => {
  it("reaches neither the consumer nor the renderer", async () => {
    const onMessage = vi.fn();
    const fx = createEngineFixture({ callbacks: { onMessage } });
    const sock = openSession(fx, "s");
    const painted = vi.spyOn(fx.engine.renderer, "handleScreen");

    sock.fireMessage(new Blob([new Uint8Array(screenFrame(2))]));
    fx.engine.dispose();
    await new Promise((r) => setTimeout(r, 50));

    expect(onMessage).not.toHaveBeenCalled();
    expect(painted).not.toHaveBeenCalled();
  });
});
