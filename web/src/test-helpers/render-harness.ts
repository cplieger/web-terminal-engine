import type { ScreenMessage, WireRun } from "../types.js";
import { createEngineFixture, type EngineFixture } from "./engine-fixture.js";

// Fixed cell metric so measureText is deterministic. A real Canvas2D measures
// the actual font, which varies with what the machine has installed, so the
// arithmetic every caller asserts against is pinned here instead.
const CELL_PX = 8;

// Rows in the rendered window. Content under test goes on row 0; the cursor is
// parked on the last row so it never adds a span to the row being asserted.
const SCREEN_H = 8;

function installCanvasStub(): void {
  HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
    return {
      font: "",
      measureText: (text: string): { width: number } => ({ width: text.length * CELL_PX }),
    };
  } as typeof HTMLCanvasElement.prototype.getContext;
}

/**
 * Waits for the frame the renderer scheduled, not for a duration: DOM writes
 * are batched behind `requestAnimationFrame(flushRender)`, so the DOM is current
 * only once that callback has run. Two frames deep because one rAF resolves at
 * the start of the frame the flush is queued in, which can be the same frame.
 * A fixed sleep races in both directions and Chromium throttles rAF when the
 * page is not visible, which no duration can account for.
 */
async function flushFrame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  });
}

let fixture: EngineFixture | null = null;

function active(): EngineFixture {
  if (fixture === null) {
    throw new Error("render harness: initHarness() has not run");
  }
  return fixture;
}

/**
 * Builds a fresh engine fixture with measured font metrics; the shared afterEach
 * disposes it. Call in beforeEach. Returns the output element.
 */
export function initHarness(): HTMLElement {
  installCanvasStub();
  fixture = createEngineFixture();
  fixture.engine.renderer.updateFontMetrics();
  return fixture.output;
}

/**
 * Renders `runs` on row 0 of a fresh window and returns that row's child
 * elements (spans / anchors), awaiting the render flush.
 */
export async function renderRow(runs: WireRun[]): Promise<HTMLElement[]> {
  const blank: WireRun[] = [{ t: " ".repeat(40), f: -1, b: -1, a: 0, uc: -1 }];
  const rows: WireRun[][] = [];
  const changed: number[] = [];
  for (let i = 0; i < SCREEN_H; i++) {
    rows[i] = i === 0 ? runs : blank;
    changed.push(i);
  }
  const msg: ScreenMessage = {
    type: "screen",
    base: 0,
    rows,
    cursor: [SCREEN_H - 1, 0],
    changed,
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: false,
  };
  const output = await renderScreen(msg);
  const rowEl = output.children[0] as HTMLElement;
  return Array.from(rowEl.children) as HTMLElement[];
}

/** Returns the first element whose text is non-blank. */
export function firstTextSpan(spans: HTMLElement[]): HTMLElement | undefined {
  return spans.find((s) => (s.textContent ?? "").trim().length > 0);
}

/** Renders a full decoded ScreenMessage and returns the output element, awaiting the flush. */
export async function renderScreen(msg: ScreenMessage): Promise<HTMLElement> {
  const fx = active();
  fx.engine.renderer.handleScreen(msg);
  await flushFrame();
  return fx.output;
}

/** Returns the child elements of the rendered row at absolute `index`. */
export function rowSpans(output: HTMLElement, index: number): HTMLElement[] {
  const rowEl = output.children[index] as HTMLElement | undefined;
  return rowEl ? (Array.from(rowEl.children) as HTMLElement[]) : [];
}
