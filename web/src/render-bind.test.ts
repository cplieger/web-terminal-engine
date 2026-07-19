// @vitest-environment happy-dom
//
// render.bind / render.rebuild: the per-tab store swap the tabs feature uses on
// every switch (design sections 5, 6, 8). Behaviors pinned here:
// 1. bind() points the one renderer at a different, independently-populated
//    LineStore and rebuilds the DOM from it; the previous store's content is
//    gone and the new store's content is shown.
// 2. The rebuild is viewport-first: the live-window row is present after the
//    first frame even when scrollback exceeds the per-frame build budget, so a
//    switch paints the visible screen immediately.
// 3. A bound store already in the alternate screen rebuilds into the alt grid.

import { describe, it, expect, beforeEach } from "vitest";
import * as render from "./render.js";
import { LineStore } from "./store.js";
import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";

interface FakeCtx {
  font: string;
  measureText: (t: string) => { width: number };
}
HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
  const ctx: FakeCtx = { font: "", measureText: (t: string) => ({ width: t.length * 8 }) };
  return ctx;
} as typeof HTMLCanvasElement.prototype.getContext;

function row(text: string): WireRun[] {
  return [{ t: text, f: -1, b: -1, a: 0, uc: -1 }];
}
function screenMsg(
  base: number,
  rows: WireRun[][],
  changed: number[],
  cursor: [number, number] = [0, 0],
): ScreenMessage {
  return {
    type: "screen",
    base,
    rows,
    changed,
    cursor,
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: false,
  };
}
function scrollMsg(firstIndex: number, texts: string[]): ScrollMessage {
  return { type: "scroll", firstIndex, lines: texts.map(row) };
}
const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 20));
function texts(out: HTMLElement): string[] {
  return Array.from(out.children)
    .map((c) => (c.textContent ?? "").trim())
    .filter((t) => t.length > 0);
}

describe("render.bind / rebuild (per-tab store swap)", () => {
  let outputEl: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    document.body.innerHTML = `<div id="term"><div id="term-output"></div></div>`;
    termWrap = document.getElementById("term") as HTMLDivElement;
    outputEl = document.getElementById("term-output") as HTMLDivElement;
    render.init({ output: outputEl, termWrap });
    render.updateFontMetrics();
  });

  it("swaps to a pre-populated store and rebuilds the DOM from it", async () => {
    // Tab A: fed through the renderer's own (default) store.
    render.handleScroll(scrollMsg(0, ["a", "b", "c"]));
    await tick();
    expect(texts(outputEl)).toEqual(["a", "b", "c"]);

    // Tab B: an independent cache, populated directly.
    const other = new LineStore();
    other.applyScroll(scrollMsg(0, ["x", "y"]));
    other.applyScreen(screenMsg(2, [row("z")], [0]));

    render.bind(other);
    await tick();

    const shown = texts(outputEl);
    expect(shown).toContain("x");
    expect(shown).toContain("y");
    expect(shown).toContain("z");
    expect(shown).not.toContain("a"); // the previous store's content is gone
    expect(render.boundStore()).toBe(other);
  });

  it("rebuilds viewport-first: the live-window row paints in the first frame", () => {
    // Pump animation frames by hand: a real-timer tick() can span a variable
    // number of rAF callbacks on a loaded CI runner, making "exactly one
    // frame" racy. Capturing the callbacks makes each pump exactly one flush.
    const frames: FrameRequestCallback[] = [];
    const realRaf = globalThis.requestAnimationFrame;
    const realCaf = globalThis.cancelAnimationFrame;
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback): number => {
      frames.push(cb);
      return frames.length;
    };
    globalThis.cancelAnimationFrame = (id: number) => {
      void id; // captured frames are never cancelled in this test
    };
    try {
      const s = new LineStore();
      const N = 700; // > MAX_ROWS_PER_FRAME (300)
      s.applyScroll(
        scrollMsg(
          0,
          Array.from({ length: N }, (_, i) => `h${i}`),
        ),
      );
      s.applyScreen(screenMsg(N, [row("LIVE")], [0]));

      render.bind(s);
      frames.shift()?.(performance.now()); // exactly one frame

      // Even though 700 scrollback rows exceed one frame's budget, the live
      // window row builds first, so it is present after a single frame.
      const liveRow = outputEl.querySelector(`[data-abs="${String(N)}"]`);
      expect(liveRow).not.toBeNull();
      expect((liveRow?.textContent ?? "").trim()).toBe("LIVE");

      // The deepest scrollback has not all been built yet (budgeted across frames).
      expect(outputEl.querySelector(`[data-abs="0"]`)).toBeNull();

      // Let the remaining frames drain one at a time; everything lands exactly once.
      for (let i = 0; i < 15 && outputEl.querySelector(`[data-abs="0"]`) === null; i++) {
        const cb = frames.shift();
        if (!cb) {
          break;
        }
        cb(performance.now());
      }
      expect(outputEl.querySelector(`[data-abs="0"]`)).not.toBeNull();
    } finally {
      globalThis.requestAnimationFrame = realRaf;
      globalThis.cancelAnimationFrame = realCaf;
    }
  });

  it("rebuilds a store that is in the alternate screen into the alt grid", async () => {
    const s = new LineStore();
    s.applyScreen({
      type: "screen",
      base: 0,
      rows: [row("alt0"), row("alt1")],
      changed: [0, 1],
      cursor: [0, 0],
      altActive: true,
      cursorHidden: true,
      cursorStyle: 0,
      cursorBlink: false,
    });

    render.bind(s);
    await tick();

    const shown = texts(outputEl);
    expect(shown).toContain("alt0");
    expect(shown).toContain("alt1");
  });
});
