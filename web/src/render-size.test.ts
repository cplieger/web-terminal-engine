// The three numbers the renderer derives for the transport and its consumers
// rather than for the screen, all otherwise unexercised against the real DOM:
//
//   - computeSize(): the (cols, rows) a `resize` control message carries. It is
//     the CONTENT box divided by the cell, so the terminal's padding must come
//     off the measured box first — a size computed over the padding asks the
//     server for a screen wider and taller than the one that fits, and every
//     row then soft-wraps. Clamped to a floor so a collapsed or mid-layout
//     element can never ask for a zero-column pty.
//   - replayMaxForResume(): how much history to ask a server to replay on
//     attach. The client's own retention cap minus the live window it is about
//     to be sent anyway, so a reconnect does not download rows the cap would
//     immediately trim.
//   - cellSize(): the measured cell, which is what a consumer hands to
//     mouse.init as `cellSize` to turn pointer pixels into grid cells. It has
//     to answer the CURRENT measurement rather than the module's power-on
//     fallback, because a consumer that gets a stale or zero divisor here gets
//     total mouse silence and nothing says why.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as render from "./render.js";
import type { ScreenMessage, WireRun } from "./types.js";

// The stubbed glyph advance, in px. Mutable so a test can restyle the terminal
// and re-measure: a fixed advance cannot exercise a cache invalidation.
let cellPx = 8;

let realGetContext: typeof HTMLCanvasElement.prototype.getContext;
let realRect: typeof HTMLElement.prototype.getBoundingClientRect;
let realRAF: typeof globalThis.requestAnimationFrame;
let realCAF: typeof globalThis.cancelAnimationFrame;

let termWrap: HTMLDivElement;
let output: HTMLDivElement;

function installStubs(): void {
  realGetContext = HTMLCanvasElement.prototype.getContext;
  realRect = HTMLElement.prototype.getBoundingClientRect;
  HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
    return { font: "", measureText: (t: string) => ({ width: [...t].length * cellPx }) };
  } as typeof HTMLCanvasElement.prototype.getContext;
  HTMLElement.prototype.getBoundingClientRect = function fakeRect(this: HTMLElement): DOMRect {
    const width = [...(this.textContent ?? "")].length * cellPx;
    return {
      x: 0,
      y: 0,
      width,
      height: 17,
      top: 0,
      left: 0,
      right: width,
      bottom: 17,
      toJSON: () => ({}),
    } as DOMRect;
  };
  realRAF = globalThis.requestAnimationFrame;
  realCAF = globalThis.cancelAnimationFrame;
  globalThis.requestAnimationFrame = ((cb: FrameRequestCallback): number => {
    cb(0);
    return undefined as unknown as number;
  }) as typeof globalThis.requestAnimationFrame;
  globalThis.cancelAnimationFrame = (() => undefined) as typeof globalThis.cancelAnimationFrame;
}

function restoreStubs(): void {
  HTMLCanvasElement.prototype.getContext = realGetContext;
  HTMLElement.prototype.getBoundingClientRect = realRect;
  globalThis.requestAnimationFrame = realRAF;
  globalThis.cancelAnimationFrame = realCAF;
}

/**
 * Attach the renderer to a terminal element of a known BORDER box with known
 * padding. clientWidth/clientHeight are declared here the way a browser reports
 * them — the padding box, padding included — because the box IS the subject and
 * a real one would be whatever the fixture markup happens to lay out at.
 */
function attachSized(opts: {
  clientWidth: number;
  clientHeight: number;
  padding: string;
  maxLines?: number;
}): void {
  document.body.innerHTML = `<div class="term"><div class="term-output"></div></div>`;
  termWrap = document.querySelector<HTMLDivElement>(".term")!;
  output = document.querySelector<HTMLDivElement>(".term-output")!;
  termWrap.style.fontSize = "16px";
  termWrap.style.fontFamily = "monospace";
  termWrap.style.lineHeight = "17px";
  termWrap.style.padding = opts.padding;
  Object.defineProperty(termWrap, "clientWidth", {
    configurable: true,
    get: () => opts.clientWidth,
  });
  Object.defineProperty(termWrap, "clientHeight", {
    configurable: true,
    get: () => opts.clientHeight,
  });
  render.init(
    opts.maxLines === undefined
      ? { output, termWrap }
      : { output, termWrap, maxLines: opts.maxLines },
  );
  render.updateFontMetrics();
}

beforeEach(() => {
  cellPx = 8;
  installStubs();
});

afterEach(() => {
  restoreStubs();
});

describe("computeSize reports the terminal's grid", () => {
  it("divides the content box, with the padding taken off both axes", () => {
    // A 500x300 padding box with 10px of padding on every side leaves a
    // 480x280 content box. At an 8px cell and a 17px line that is 60 columns
    // and 16 rows (280/17 = 16.47, and a partial row is not a row).
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "10px" });
    expect(render.computeSize()).toEqual({ cols: 60, rows: 16 });
  });

  it("never asks for fewer than 20 columns or 5 rows", () => {
    // A collapsed or mid-layout element measures near zero. A pty sized from
    // that is unusable, so the size is floored.
    attachSized({ clientWidth: 40, clientHeight: 20, padding: "0px" });
    expect(render.computeSize()).toEqual({ cols: 20, rows: 5 });
  });
});

describe("cellSize exposes the measured cell to a pointer hit test", () => {
  it("answers the width and line height updateFontMetrics measured", () => {
    // The stub measures an 8px advance and the element declares a 17px line, so
    // those are the numbers a consumer must get — not the module's fallback.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px" });
    expect(render.cellSize()).toEqual({ width: 8, height: 17 });
  });

  it("follows a re-measurement rather than freezing at the first one", () => {
    // A consumer restyling the terminal calls updateFontMetrics; a cell size
    // that did not move with it would divide pointer pixels by the old line
    // height and report the wrong row for every click below the first.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px" });
    termWrap.style.lineHeight = "24px";
    render.updateFontMetrics();
    expect(render.cellSize()).toEqual({ width: 8, height: 24 });
  });

  it("agrees with the divisor computeSize used for the same box", () => {
    // The grid the server is told about and the grid a click is resolved against
    // must be one grid. Both halves are stated rather than derived: an 8x17 cell,
    // and the 480x280 content box that divides into 60 columns and 16 rows.
    // Recomputing the expectation from cellSize() here would restate the
    // production formula and pass whatever that formula became.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "10px" });
    expect(render.cellSize()).toEqual({ width: 8, height: 17 });
    expect(render.computeSize()).toEqual({ cols: 60, rows: 16 });
  });
});

describe("gridSize reports the grid on screen, not the one this client measured", () => {
  /** A screen frame of `rows` rows, on the main buffer or the alt one. */
  function screenFrame(rows: number, altActive: boolean): ScreenMessage {
    const content: WireRun[][] = [];
    for (let i = 0; i < rows; i++) {
      content.push([{ t: `line ${String(i)}`, f: -1, b: -1, a: 0, uc: -1 }]);
    }
    return {
      type: "screen",
      base: 0,
      rows: content,
      cursor: [0, 0],
      changed: [0],
      cursorHidden: true,
      cursorStyle: 0,
      cursorBlink: false,
      altActive,
    };
  }

  it("answers the rendered row count where the measured box would fit more", () => {
    // The 300px box fits 17 rows at a 17px line, and the server negotiated 8 —
    // the size some other attached client asked for. The row hit test anchors on
    // this number, so answering 17 would shift every reported row by 9.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px" });
    render.handleScreen(screenFrame(8, false));
    expect(render.gridSize()).toEqual({ cols: 62, rows: 8 });
  });

  it("answers the alt grid's height while an alt session is active", () => {
    // A TUI runs on the alt screen, and the main window descriptor keeps its own
    // height as the restore target, so reading it would report the pre-alt size.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px" });
    render.handleScreen(screenFrame(12, false));
    render.handleScreen(screenFrame(6, true));
    expect(render.gridSize().rows).toBe(6);
  });

  it("keeps the measured column count, which only bounds a report", () => {
    // Columns feed the clamp and nothing else, and the store has no column count
    // to answer with (a row's trailing blank cells are trimmed off the wire).
    // 480/8 = 60, stated rather than read back out of computeSize().
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "10px" });
    render.handleScreen(screenFrame(8, false));
    expect(render.gridSize().cols).toBe(60);
  });

  it("re-measures the columns after a font change, rather than serving the cached count", () => {
    // gridSize() reads the count computeSize() cached instead of measuring, so
    // updateFontMetrics has to invalidate it: 500/8 = 62 columns at the 8px cell,
    // and 500/16 = 31 once the cell doubles. Serving 62 there would clamp every
    // report to a grid twice as wide as the one on screen.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px" });
    render.handleScreen(screenFrame(8, false));
    expect(render.gridSize().cols).toBe(62);
    termWrap.style.fontSize = "32px";
    cellPx = 16;
    render.updateFontMetrics();
    expect(render.gridSize().cols).toBe(31);
  });
});

describe("replayMaxForResume bounds the resume replay", () => {
  it("asks for the retention cap minus the live window", () => {
    // A 100-line client cap with a 10-row screen: the screen arrives with the
    // resume anyway, so at most 90 lines of history are worth replaying.
    attachSized({ clientWidth: 500, clientHeight: 300, padding: "0px", maxLines: 100 });
    const rows: WireRun[][] = [];
    for (let i = 0; i < 10; i++) {
      rows.push([{ t: `line ${String(i)}`, f: -1, b: -1, a: 0, uc: -1 }]);
    }
    const msg: ScreenMessage = {
      type: "screen",
      base: 0,
      rows,
      cursor: [0, 0],
      changed: [0],
      cursorHidden: true,
      cursorStyle: 0,
      cursorBlink: false,
    };
    render.handleScreen(msg);
    expect(render.replayMaxForResume()).toBe(90);
  });
});
