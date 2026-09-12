// Mouse hit-testing and mode-gating tests: the parts of mouse.ts that decide
// WHETHER to report and WHICH coordinate pair to report, as opposed to the
// button-byte composition mouse.test.ts already pins.
//
// Covered here and nowhere else:
//   - SGR-pixels (DEC 1016): the whole pixel-coordinate branch, which had no
//     test at all even though the wire decoder plumbs the flag.
//   - The terminal element's own viewport offset, which every existing test
//     hides by leaving getBoundingClientRect all-zero (their fixture element is
//     detached, so it genuinely has no box), so a sign error in the rect
//     subtraction is invisible to them.
//   - The refusals: tracking off, no supported encoding enabled, a degenerate
//     cell size, a pointer outside the element.
//   - The grid frame: the OPTIONAL gridElement/gridSize members, which move the
//     coordinate frame onto the element whose box really is the grid and clamp
//     every report into it. Both default to today's behaviour, so a consumer
//     that supplies neither is unaffected.
//   - The disposer detaching EVERY listener, not just mousedown.
//
// Spec source, quoted so the expectations below can be checked against it
// rather than against mouse.ts (xterm "Control Sequences", section Mouse
// Tracking, https://invisible-island.net/xterm/ctlseqs/ctlseqs.html):
//   - the origin: "The upper left character position on the terminal is
//     denoted as 1,1", and the terminal is the SCREEN, so its last row is
//     row `rows` whatever partial row the box shows at the top;
//   - SGR (1006): "CSI <" then the button value, "Px and Py ordinates" and a
//     final M for press, m for release;
//   - SGR-Pixels (1016): "the same mouse response format as the 1006
//     control, but report position in pixels rather than character cells".
//
// The CLAMP has no sentence of its own in that document, so it is an engine
// choice, recorded here as one: the spec numbers the cells of a cols x rows
// screen from 1,1, so a report outside that range names a cell the
// application does not have. Under 1016 the same reasoning bounds the report
// by the grid's pixel box.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { init as initMouse, type MouseInputHandler } from "./mouse.js";
import * as modes from "./modes.js";

const ESC = "\x1b";
const MOTION = 32;

// SGR-1006 grammar, mirrored from the spec rather than from encodeSGR.
const expectedSGR = (b: number, col: number, row: number, release: boolean): string =>
  `${ESC}[<${b};${col};${row}${release ? "m" : "M"}`;

beforeEach(() => {
  // modes is a module singleton and the suite runs with isolate:false: reset
  // every flag so nothing leaks in or out of this file.
  modes.setModes(true, false, false, false, 0, false, false, false);
});

interface Fixture {
  term: HTMLDivElement;
  sent: string[];
  dispose: () => void;
}

/**
 * A terminal element with an 8x16 cell and a settable viewport rect. The rect is
 * declared per element rather than measured, because the OFFSET is the subject:
 * with left/top 0 the hit test is `floor(clientX / 8) + 1`, and a non-zero rect
 * is how an element that is not at the viewport origin is expressed. Assigning
 * it on the instance keeps every other element in the page on real layout.
 */
function setup(rect: { left: number; top: number } = { left: 0, top: 0 }): Fixture {
  const term = document.createElement("div");
  Object.defineProperty(term, "getBoundingClientRect", {
    value: () => ({ left: rect.left, top: rect.top, right: 0, bottom: 0, width: 0, height: 0 }),
    configurable: true,
  });
  const sent: string[] = [];
  const handler: MouseInputHandler = {
    sendReport: (data) => {
      sent.push(data);
      return true;
    },
    cellSize: () => ({ width: 8, height: 16 }),
    termElement: () => term,
  };
  const dispose = initMouse(handler);
  return { term, sent, dispose };
}

/** A terminal element whose reported cell size is degenerate. */
function setupWithCell(width: number, height: number): Fixture {
  const term = document.createElement("div");
  Object.defineProperty(term, "getBoundingClientRect", {
    value: () => ({ left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 }),
    configurable: true,
  });
  const sent: string[] = [];
  const dispose = initMouse({
    sendReport: (data) => {
      sent.push(data);
      return true;
    },
    cellSize: () => ({ width, height }),
    termElement: () => term,
  });
  return { term, sent, dispose };
}

/**
 * The real two-element shape: listeners on the scroll container, coordinates
 * resolved against an inner element whose box IS the grid. The two rects are
 * declared separately and deliberately differ, so a hit test reading the wrong
 * one is visible. `bottom` is independent of `rows * cellHeight` because the
 * primary screen's grid is the TAIL of a taller box — the leftover at the top is
 * a partial row.
 */
function setupGrid(opts: {
  cols: number;
  rows: number;
  gridRect: { left: number; top: number; bottom: number };
  termRect?: { left: number; top: number };
}): Fixture & { grid: HTMLDivElement } {
  const termRect = opts.termRect ?? { left: 0, top: 0 };
  const term = document.createElement("div");
  Object.defineProperty(term, "getBoundingClientRect", {
    value: () => ({ left: termRect.left, top: termRect.top, right: 0, bottom: 0 }),
    configurable: true,
  });
  const grid = document.createElement("div");
  term.appendChild(grid);
  Object.defineProperty(grid, "getBoundingClientRect", {
    value: () => ({
      left: opts.gridRect.left,
      top: opts.gridRect.top,
      right: 0,
      bottom: opts.gridRect.bottom,
    }),
    configurable: true,
  });
  const sent: string[] = [];
  const dispose = initMouse({
    sendReport: (data) => {
      sent.push(data);
      return true;
    },
    cellSize: () => ({ width: 8, height: 16 }),
    termElement: () => term,
    gridElement: () => grid,
    gridSize: () => ({ cols: opts.cols, rows: opts.rows }),
  });
  return { term, grid, sent, dispose };
}

function mouseEvent(type: string, opts: Record<string, unknown>): MouseEvent {
  const e = new MouseEvent(type, { cancelable: true });
  for (const [key, value] of Object.entries(opts)) {
    Object.defineProperty(e, key, { value, configurable: true });
  }
  return e;
}

function wheelEvent(opts: Record<string, unknown>): WheelEvent {
  const e = new WheelEvent("wheel", { cancelable: true });
  for (const [key, value] of Object.entries(opts)) {
    Object.defineProperty(e, key, { value, configurable: true });
  }
  return e;
}

// Motion coalesces to one report per animation frame, so a motion assertion has
// to cross a frame boundary. Real Chromium, so requestAnimationFrame is real.
const nextFrame = (): Promise<void> =>
  new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

/** SGR 1006 + the given tracking mode; pixels and focus off. */
function enableSGR(mode: number): void {
  modes.setModes(true, false, true, false, mode, false, false, false);
}

/** DEC 1016 (SGR-pixels) + the given tracking mode; SGR 1006 off. */
function enablePixels(mode: number): void {
  modes.setModes(true, false, false, false, mode, false, false, true);
}

describe("SGR-pixels (DEC 1016): reports pixel offsets instead of cells", () => {
  it("maps the element's top-left pixel to 1;1", () => {
    // Same CSI < grammar as 1006, but Px/Py are 1-based PIXEL offsets within
    // the terminal element rather than cell coordinates.
    enablePixels(1002);
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 0, clientY: 0, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 1, 1, false)]);
  });

  it("reports the pixel offset itself, not the cell it falls in", () => {
    enablePixels(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 37, clientY: 82, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 38, 83, false)]);
  });

  it("subtracts the element's own viewport offset", () => {
    // A terminal that is not at the viewport origin: the report is relative to
    // the element, so the rect is subtracted, never added.
    enablePixels(1002);
    const { term, sent } = setup({ left: 10, top: 20 });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 30, clientY: 50, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 21, 31, false)]);
  });

  it("reports nothing for a pointer left of the element", () => {
    enablePixels(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: -1, clientY: 40, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports nothing for a pointer above the element", () => {
    enablePixels(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 40, clientY: -1, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports pixel offsets for a drag as well as a press", async () => {
    enablePixels(1002);
    const { term, sent } = setup();
    // The press establishes the gesture: a drag reports a held button only while
    // the application believes that button is down.
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 5, clientY: 9, button: 0, buttons: 1 }));
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 5, clientY: 9, buttons: 1 }));
    await nextFrame();
    expect(sent.at(-1)).toBe(expectedSGR(0 + MOTION, 6, 10, false));
  });
});

describe("cell hit-testing: the element's viewport offset and its edges", () => {
  it("subtracts the element's own viewport offset before dividing by the cell", () => {
    // rect.left 10 with an 8px cell: clientX 26 is 16px into the element, i.e.
    // column 3. Adding the rect instead would report column 5.
    enableSGR(1002);
    const { term, sent } = setup({ left: 10, top: 20 });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 26, clientY: 52, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 3, 3, false)]);
  });

  it("reports nothing for a pointer left of the element", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: -8, clientY: 32, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports nothing for a pointer above the element", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: -16, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports nothing when the cell width is not yet measured", () => {
    // cellSize() reads the renderer's font metrics, which are zero before the
    // first measurement; dividing by it would report an infinite column.
    enableSGR(1002);
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { term, sent } = setupWithCell(0, 16);
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports nothing when the cell height is not yet measured", () => {
    enableSGR(1002);
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { term, sent } = setupWithCell(8, 0);
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("says once why it went silent, rather than once per event", () => {
    // A zero divisor makes every report vanish, which looks exactly like mouse
    // tracking being off. A per-event warning would flood the console under a
    // drag, so the diagnosis is latched.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1003);
    const { term, sent } = setupWithCell(0, 0);
    for (const clientX of [8, 16, 24]) {
      term.dispatchEvent(mouseEvent("mousemove", { clientX, clientY: 32, buttons: 1 }));
    }
    expect(sent).toEqual([]);
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("says it again for a fresh wiring, so a re-init is diagnosable too", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1002);
    const first = setupWithCell(0, 16);
    first.term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    const second = setupWithCell(0, 16);
    second.term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    expect(warn).toHaveBeenCalledTimes(2);
  });
});

describe("the grid frame: gridElement resolves the coordinates, termElement gets the listeners", () => {
  it("measures from the grid element's box, not the element the listeners sit on", () => {
    // The scroll container's box includes its padding and the reserved scrollbar
    // gutter, so it is not the grid: 16px into the GRID is column 3, while the
    // same pointer measured against the outer element is column 8.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 80,
      rows: 24,
      gridRect: { left: 40, top: 0, bottom: 24 * 16 },
      termRect: { left: 0, top: 0 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 56, clientY: 8, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 3, 1, false)]);
  });

  it("reports the last cell for a pointer at the far bottom-right of the grid", () => {
    // The bottom-right corner of a 10x5 grid of 8x16 cells at origin (40,100):
    // pixel (119,179) is inside the last cell, and the last cell has to be
    // reachable — an off-by-one in either direction hides a whole row or column
    // from the application.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 40, top: 100, bottom: 180 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 119, clientY: 179, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 10, 5, false)]);
  });

  it("clamps a pointer past the right edge to the last column", () => {
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 400, clientY: 8, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 10, 1, false)]);
  });

  it("clamps a pointer below the last row to the last row", () => {
    // A press in the space beneath the grid, or a drag continuing past the
    // bottom edge: an unclamped report names a row the application does not have.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 8, clientY: 400, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 2, 5, false)]);
  });

  it("clamps a pointer in the padding above and left of the grid to cell (1,1)", () => {
    // The listeners are on the padded scroll container, so a press in its padding
    // is delivered. It belongs to the nearest cell, not to a refusal: the grid
    // frame's whole point is that every delivered pointer names a real cell.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 4, top: 4, bottom: 84 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 0, clientY: 0, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 1, 1, false)]);
  });

  it("keeps row 1 at the screen's first row when the box shows a partial row above it", () => {
    // The screen window is the TAIL of the content: with history above it and the
    // view pinned to the bottom, the box's top 8px is the remains of a partial
    // row and grid row 1 spans y 8..24. Pixel y=20 is therefore row 1, where
    // top-anchored arithmetic reports row 2 and every row below it is off by one.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 88 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 8, clientY: 20, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 2, 1, false)]);
  });

  it("reports the grid's top row while the reader is scrolled up into history", () => {
    // Scrolled into history on the primary screen the grid sits BELOW the
    // viewport, so a pointer in the viewport is above the grid box. The contract
    // is the clamp: report the top row rather than a negative one, and rather
    // than measuring against content the reader is not looking at.
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 400, bottom: 480 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 8, clientY: 100, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 2, 1, false)]);
  });

  it("clamps a drag as well as a press", async () => {
    enableSGR(1003);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    // The press establishes the gesture, so the drag reports its held button.
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 400, clientY: 400, button: 0, buttons: 1 }),
    );
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 400, clientY: 400, buttons: 1 }));
    await nextFrame();
    expect(sent.at(-1)).toBe(expectedSGR(0 + MOTION, 10, 5, false));
  });

  it("clamps a pixel report (DEC 1016) to the grid BOX, in pixels", () => {
    // SGR-pixels reports an offset inside the grid box, so the bound is the box's
    // own size (10 cols x 8px = 80px wide, 5 rows x 16px = 80px tall) rather than
    // a cell count.
    enablePixels(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 400, clientY: 400, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 80, 80, false)]);
  });

  it("leaves a pixel report inside the box untouched", () => {
    enablePixels(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 37, clientY: 41, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 38, 42, false)]);
  });

  it("puts a pixel press in the same row the cell branch reports for it", () => {
    // The box is 88px tall for a 5x16px grid, so its top 8px is the remains of a
    // partial row and the grid begins at y=8. A press at y=20 is 12px into the
    // grid, which is pixel 13 — measured from the box's top edge it would report
    // 21, and the same press reports cell row 1 either way, so the two encodings
    // would disagree about which row was pressed.
    enablePixels(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 88 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 8, clientY: 20, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 9, 13, false)]);
  });

  it("clamps a pixel report in the padding above the grid to its first pixel", () => {
    // The cell branch clamps a press in the scroll container's padding to cell
    // (1,1); the pixel branch has to answer for the same press rather than
    // refusing it, or one encoding reports the click and the other does not.
    enablePixels(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 5,
      gridRect: { left: 4, top: 4, bottom: 84 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 0, clientY: 0, button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 1, 1, false)]);
  });

  it("reports an unclamped pixel offset while the cell is unmeasured", () => {
    // The pixel branch needs the cell only to know where the box ENDS. Withholding
    // the report would lose a coordinate that is still correct, so an unmeasured
    // cell costs the bound and nothing else.
    enablePixels(1002);
    const term = document.createElement("div");
    Object.defineProperty(term, "getBoundingClientRect", {
      value: () => ({ left: 0, top: 0, right: 0, bottom: 80 }),
      configurable: true,
    });
    const sent: string[] = [];
    initMouse({
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => ({ width: 0, height: 0 }),
      termElement: () => term,
      gridSize: () => ({ cols: 10, rows: 5 }),
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 400, clientY: 400, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 401, 401, false)]);
  });
});

describe("a degenerate grid is refused and diagnosed, not clamped into", () => {
  it("reports nothing, rather than a confident (1,1), for an empty grid", () => {
    // clampToGrid(v, 0) answers max(0, min(v, -1)) = 0, so an unvalidated empty
    // grid turns every pointer position into cell (1,1) — a wrong report is worse
    // than none, and it is the silent-failure shape the cell diagnostic exists
    // to remove.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1002);
    const { term, sent } = setupGrid({
      cols: 0,
      rows: 0,
      gridRect: { left: 0, top: 0, bottom: 0 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 40, clientY: 40, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("refuses a pixel report for the same grid", () => {
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enablePixels(1002);
    const { term, sent } = setupGrid({
      cols: 10,
      rows: 0,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 40, clientY: 40, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("says nothing for the pre-first-frame grid, which is not degenerate", () => {
    // render.gridSize answers 0 rows until the server describes the screen, so a
    // pointer arriving between the modes frame and the first screen frame is
    // ordinary rather than a mis-wiring. Refusing is right; spending the latch on
    // it would leave a genuinely broken grid silent afterwards.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1003);
    const { term, sent } = setupGrid({
      cols: 80,
      rows: 0,
      gridRect: { left: 0, top: 0, bottom: 0 },
    });
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 8, clientY: 8, button: 0, buttons: 0 }));
    expect(sent).toEqual([]);
    expect(warn).not.toHaveBeenCalled();
  });

  it("says once why it went silent, rather than once per event", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1003);
    const { term } = setupGrid({
      cols: 0,
      rows: 5,
      gridRect: { left: 0, top: 0, bottom: 80 },
    });
    for (const clientX of [8, 16, 24]) {
      term.dispatchEvent(mouseEvent("mousemove", { clientX, clientY: 32, buttons: 1 }));
    }
    expect(warn).toHaveBeenCalledTimes(1);
  });
});

describe("the grid frame is opt-in: a consumer supplying neither member is unaffected", () => {
  it("keeps the top-anchored, unclamped hit test", () => {
    // Today's behaviour, and the reason both members are optional: adding a
    // required member to an exported interface is a source break for any
    // external implementor. y=200 with a 16px cell is row 13, past any real
    // screen, and that is what an unclamped consumer still gets.
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 8, clientY: 200, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([expectedSGR(0, 2, 13, false)]);
  });

  it("still refuses a pointer above the element rather than clamping it", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mousedown", { clientX: 8, clientY: -1, button: 0, buttons: 0 }));
    expect(sent).toEqual([]);
  });
});

describe("mode gating applies to every event type, not just the press", () => {
  it("reports no release while tracking is off", () => {
    modes.setModes(true, false, true, false, 0, false, false, false); // SGR on, tracking off
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0 }));
    expect(sent).toEqual([]);
  });

  it("reports no motion while tracking is off", () => {
    modes.setModes(true, false, true, false, 0, false, false, false);
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    expect(sent).toEqual([]);
  });

  it("reports no wheel while tracking is off", () => {
    modes.setModes(true, false, true, false, 0, false, false, false);
    const { term, sent } = setup();
    term.dispatchEvent(wheelEvent({ deltaY: -1, clientX: 16, clientY: 32 }));
    expect(sent).toEqual([]);
  });
});

describe("no supported encoding enabled: tracking alone reports nothing", () => {
  // Tracking on but neither SGR 1006 nor SGR-pixels: the legacy X10 / urxvt
  // encodings are deliberately unimplemented, so the module stays silent rather
  // than sending a report in an encoding the app did not ask for.
  const legacyOnly = (): void => {
    modes.setModes(true, false, false, false, 1002, false, false, false);
  };

  it("reports no press", () => {
    legacyOnly();
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    expect(sent).toEqual([]);
  });

  it("reports no release", () => {
    legacyOnly();
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0 }));
    expect(sent).toEqual([]);
  });

  it("reports no motion", () => {
    legacyOnly();
    const { term, sent } = setup();
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    expect(sent).toEqual([]);
  });

  it("reports no wheel", () => {
    legacyOnly();
    const { term, sent } = setup();
    term.dispatchEvent(wheelEvent({ deltaY: -1, clientX: 16, clientY: 32 }));
    expect(sent).toEqual([]);
  });
});

describe("reported events suppress the browser's own handling", () => {
  it("preventDefaults a press, a release and a wheel", () => {
    // Without this the browser also selects text under a TUI that is tracking
    // the mouse, and scrolls the page under one that handles the wheel.
    enableSGR(1002);
    const { term } = setup();
    const press = mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 });
    const release = mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0 });
    const wheel = wheelEvent({ deltaY: -1, clientX: 16, clientY: 32 });
    term.dispatchEvent(press);
    term.dispatchEvent(release);
    term.dispatchEvent(wheel);
    expect(press.defaultPrevented).toBe(true);
    expect(release.defaultPrevented).toBe(true);
    expect(wheel.defaultPrevented).toBe(true);
  });

  it("swallows a gesture it reports nothing for, rather than letting the page scroll", () => {
    // A horizontal gesture (deltaY 0) is not reportable — there is no sideways
    // wheel button in SGR 1006 — but a terminal that is tracking the wheel owns
    // the gesture: passing it through would scroll the page out from under the
    // TUI. Guarding preventDefault along with the report would trade the wire
    // bug for that one, so the two halves are asserted apart.
    enableSGR(1002);
    const { term, sent } = setup();
    const sideways = wheelEvent({ deltaY: 0, deltaX: -40, clientX: 16, clientY: 32 });
    term.dispatchEvent(sideways);
    expect(sent).toEqual([]);
    expect(sideways.defaultPrevented).toBe(true);
  });
});

describe("the disposer detaches every listener it attached", () => {
  it("stops reporting releases, motion and wheel after dispose", () => {
    enableSGR(1003);
    const { term, sent, dispose } = setup();
    dispose();
    term.dispatchEvent(mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0 }));
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    term.dispatchEvent(wheelEvent({ deltaY: -1, clientX: 16, clientY: 32 }));
    expect(sent).toEqual([]);
  });

  it("detaches every event type from the element a re-init supersedes", () => {
    // A re-mount on a new element self-heals by detaching the old one. Any event
    // type left attached there keeps reporting through the NEW handler — the
    // stale element is still in the old DOM and still receives events, so a
    // half-detach sends input from a terminal the consumer has thrown away.
    modes.setModes(true, false, true, false, 1003, false, false, false); // tracking on
    const sent: string[] = [];
    const elementWithRect = (): HTMLDivElement => {
      const el = document.createElement("div");
      Object.defineProperty(el, "getBoundingClientRect", {
        value: () => ({ left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0 }),
        configurable: true,
      });
      return el;
    };
    const first = elementWithRect();
    const second = elementWithRect();
    // One shared `sent`, so a report from a leaked listener on `first` — which
    // would go through the SECOND handler — is still visible here.
    const handlerFor = (el: HTMLElement): MouseInputHandler => ({
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => el,
    });
    initMouse(handlerFor(first));
    initMouse(handlerFor(second)); // supersedes: `first` must be fully detached

    first.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 0 }),
    );
    first.dispatchEvent(mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0 }));
    first.dispatchEvent(mouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    first.dispatchEvent(wheelEvent({ deltaY: -1, clientX: 16, clientY: 32 }));
    expect(sent).toEqual([]);
  });
});

describe("the Shift bypass does not outlive its own gesture", () => {
  it("reports a held-button drag that follows a bypassed release", async () => {
    // Shift+press hands the gesture to the browser for native selection, and
    // the release ends it. A drag after that is a new gesture and must report,
    // so the bypass flag has to be cleared by the release it belongs to.
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 1, shiftKey: true }),
    );
    term.dispatchEvent(
      mouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0, buttons: 0, shiftKey: true }),
    );
    expect(sent).toEqual([]);
    // A new, plain gesture: its press is delivered, so its drag reports.
    term.dispatchEvent(
      mouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0, buttons: 1 }),
    );
    term.dispatchEvent(mouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    await nextFrame();
    expect(sent.at(-1)).toBe(expectedSGR(0 + MOTION, 3, 3, false));
  });
});
