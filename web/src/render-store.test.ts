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
    // Re-deliver the identical batch (fast-burst re-send / reconnect replay).
    render.handleScroll(scrollMsg(0, ["a", "b", "c"]));
    await tick();
    expect(outputEl.children.length).toBe(3);
    expect(absList(outputEl)).toEqual([0, 1, 2]);
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
});
