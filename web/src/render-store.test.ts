// @vitest-environment happy-dom
//
// Brick-3 renderer properties (store-backed, absolute-index DOM rows). These
// pin what the rewrite changed versus the old live-zone model: rows carry
// data-abs, the window is fixed-height (no trailing-blank trim, the bug-3
// oscillation fix), history + window render in one absolute-ordered list, and
// re-delivery never duplicates a row.

import { describe, it, expect, beforeEach } from "vitest";
import * as render from "./render.js";
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
function blank(): WireRun[] {
  return [{ t: "          ", f: -1, b: -1, a: 0, uc: -1 }];
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

function absList(out: HTMLElement): number[] {
  return Array.from(out.children).map((c) => Number((c as HTMLElement).dataset["abs"]));
}

describe("render (store-backed, brick 3)", () => {
  let outputEl: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    document.body.innerHTML = `<div id="term"><div id="term-output"></div></div>`;
    termWrap = document.getElementById("term") as HTMLDivElement;
    outputEl = document.getElementById("term-output") as HTMLDivElement;
    render.init({ output: outputEl, termWrap });
    render.updateFontMetrics();
  });

  it("renders the full fixed-height window without trimming trailing blanks", async () => {
    // 5-row window, only rows 0-1 have content; 2-4 are blank.
    const rows = [row("line A"), row("line B"), blank(), blank(), blank()];
    render.handleScreen(screenMsg(0, rows, [0, 1, 2, 3, 4]));
    await tick();
    // The old renderer trimmed to 2 visible rows (the oscillation source);
    // the new one keeps all 5 so scrollHeight is stable across redraws.
    expect(outputEl.children.length).toBe(5);
    expect(absList(outputEl)).toEqual([0, 1, 2, 3, 4]);
  });

  it("tags rows with their absolute index and keeps history + window in order", async () => {
    render.handleScroll(scrollMsg(0, ["h0", "h1", "h2"]));
    render.handleScreen(screenMsg(3, [row("w0"), row("w1")], [0, 1]));
    await tick();
    expect(absList(outputEl)).toEqual([0, 1, 2, 3, 4]);
    const texts = Array.from(outputEl.children).map((c) => (c.textContent ?? "").trim());
    expect(texts).toEqual(["h0", "h1", "h2", "w0", "w1"]);
  });

  it("does not duplicate rows when the same content is re-delivered", async () => {
    render.handleScroll(scrollMsg(0, ["a", "b", "c"]));
    await tick();
    expect(outputEl.children.length).toBe(3);
    // Capture each row's inner span. Idempotent re-delivery must skip the DOM
    // write entirely (the store treats byte-identical content as a no-op), so
    // the renderer never rebuilds these rows and the exact span nodes survive.
    // A rebuild would call replaceChildren with FRESH spans, changing identity
    // and discarding any text selection on the row. Pinning the inner-span
    // identity is what catches a lost-idempotency regression; a bare
    // count/order check passes even if every row is torn down and recreated.
    const span0 = outputEl.children[0]?.firstElementChild;
    const span1 = outputEl.children[1]?.firstElementChild;
    const span2 = outputEl.children[2]?.firstElementChild;
    expect(span0).not.toBeNull();

    // Re-deliver the identical batch (fast-burst re-send / reconnect replay).
    render.handleScroll(scrollMsg(0, ["a", "b", "c"]));
    await tick();
    expect(outputEl.children.length).toBe(3);
    expect(absList(outputEl)).toEqual([0, 1, 2]);
    // Same inner-span identities: no row was rebuilt on the redundant frame.
    expect(outputEl.children[0]?.firstElementChild).toBe(span0);
    expect(outputEl.children[1]?.firstElementChild).toBe(span1);
    expect(outputEl.children[2]?.firstElementChild).toBe(span2);
  });

  it("updates a row in place when its content changes (Ink redraw)", async () => {
    render.handleScreen(screenMsg(0, [row("spin -")], [0]));
    await tick();
    const firstEl = outputEl.children[0];
    render.handleScreen(screenMsg(0, [row("spin \\")], [0]));
    await tick();
    // Same DOM element reused (in-place update, not a new row).
    expect(outputEl.children.length).toBe(1);
    expect(outputEl.children[0]).toBe(firstEl);
    expect((outputEl.children[0]?.textContent ?? "").trim()).toBe("spin \\");
  });

  it("renders a visible cursor span on the cursor row when not hidden", async () => {
    const msg: ScreenMessage = {
      type: "screen",
      base: 0,
      rows: [row("ab"), row("cd")],
      changed: [0, 1],
      cursor: [1, 0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: true,
    };
    render.handleScreen(msg);
    await tick();
    const cursorRowEl = outputEl.children[1] as HTMLElement;
    expect(cursorRowEl.querySelector(".term-cursor")).not.toBeNull();
    // The non-cursor row has no cursor span.
    expect((outputEl.children[0] as HTMLElement).querySelector(".term-cursor")).toBeNull();
  });

  it("wipes all rows on a full reset (server restart)", async () => {
    render.handleScroll(scrollMsg(0, ["a", "b"]));
    await tick();
    expect(outputEl.children.length).toBe(2);
    render.resetScreen();
    await tick();
    expect(outputEl.children.length).toBe(0);
    // After reset, absolute index 0 is valid again.
    render.handleScroll(scrollMsg(0, ["fresh"]));
    await tick();
    expect(absList(outputEl)).toEqual([0]);
  });

  it("shows an 'earlier output trimmed' marker when history is gone, then removes it (guard 8.2.2)", async () => {
    // Resume where the server only retains from index 100: it replays from
    // there, so the client legitimately can't show 0..99.
    render.handleScroll(scrollMsg(100, ["h100", "h101"]));
    render.handleScreen(screenMsg(102, [row("w0")], [0]));
    render.noteResumeBounds(110, 100);
    await tick();

    const first = outputEl.firstElementChild as HTMLElement;
    expect(first.classList.contains("term-trim-marker")).toBe(true);
    // The marker carries no data-abs and the real rows follow it in order.
    const rowAbs = Array.from(outputEl.children)
      .filter((c) => (c as HTMLElement).dataset["abs"] !== undefined)
      .map((c) => Number((c as HTMLElement).dataset["abs"]));
    expect(rowAbs).toEqual([100, 101, 102]);

    // A later resume where the server still has everything clears the marker.
    render.noteResumeBounds(110, 0);
    await tick();
    expect(outputEl.querySelector(".term-trim-marker")).toBeNull();
    expect(absList(outputEl)).toEqual([100, 101, 102]);
  });

  it("renders a burst larger than the per-frame budget across multiple frames", async () => {
    // A /chat session restore (or `cat bigfile`) dumps thousands of lines in
    // one wire frame. The store ingests them at once; the renderer must drain
    // them in budgeted batches without losing or duplicating any.
    const N = 700; // > MAX_ROWS_PER_FRAME (300)
    const texts = Array.from({ length: N }, (_, i) => `burst ${i}`);
    render.handleScroll(scrollMsg(0, texts));

    // Let the budgeted flush run across however many frames it needs.
    for (let i = 0; i < 15 && outputEl.children.length < N; i++) {
      await tick();
    }

    const list = absList(outputEl);
    // Every line landed exactly once, contiguous and ascending 0..N-1.
    expect(list).toEqual(Array.from({ length: N }, (_, i) => i));
  });
});

describe("render: cursor-row tracking across frames (selective rebuild)", () => {
  let outputEl: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    document.body.innerHTML = `<div id="term"><div id="term-output"></div></div>`;
    termWrap = document.getElementById("term") as HTMLDivElement;
    outputEl = document.getElementById("term-output") as HTMLDivElement;
    render.init({ output: outputEl, termWrap });
    render.updateFontMetrics();
  });

  it("moves the visible cursor span to the new row and clears it from the row the cursor left", async () => {
    render.handleScreen({
      type: "screen",
      base: 0,
      rows: [row("aa"), row("bb")],
      changed: [0, 1],
      cursor: [0, 0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: false,
    });
    await tick();
    expect((outputEl.children[0] as HTMLElement).querySelector(".term-cursor")).not.toBeNull();
    expect((outputEl.children[1] as HTMLElement).querySelector(".term-cursor")).toBeNull();

    // Cursor moves to row 1 with NO row-content change (changed is empty): the
    // renderer must repaint the row the cursor left so its stale cursor span is
    // dropped, and paint the cursor onto the new row.
    render.handleScreen({
      type: "screen",
      base: 0,
      rows: [row("aa"), row("bb")],
      changed: [],
      cursor: [1, 0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: false,
    });
    await tick();
    expect((outputEl.children[0] as HTMLElement).querySelector(".term-cursor")).toBeNull();
    expect((outputEl.children[1] as HTMLElement).querySelector(".term-cursor")).not.toBeNull();
  });

  it("leaves the cursor row's DOM untouched when only another row changes (selection-preserving)", async () => {
    render.handleScreen({
      type: "screen",
      base: 0,
      rows: [row("hello"), row("world")],
      changed: [0, 1],
      cursor: [0, 0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: false,
    });
    await tick();
    const cursorRowEl = outputEl.children[0] as HTMLElement;
    const spanBefore = cursorRowEl.firstElementChild;
    expect(spanBefore).not.toBeNull();

    // A frame that changes ONLY the non-cursor row. The cursor stays put and the
    // cursor row's content is unchanged, so an unconditional rebuild would
    // replaceChildren() and discard a text selection on that row.
    render.handleScreen({
      type: "screen",
      base: 0,
      rows: [row("hello"), row("WORLD")],
      changed: [1],
      cursor: [0, 0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: false,
    });
    await tick();
    expect(outputEl.children[0]).toBe(cursorRowEl);
    expect(cursorRowEl.firstElementChild).toBe(spanBefore);
    expect((outputEl.children[1]?.textContent ?? "").trim()).toBe("WORLD");
  });
});
