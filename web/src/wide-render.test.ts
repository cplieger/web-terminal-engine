// @vitest-environment happy-dom
//
// Round 4 adversarial: exercises the renderer with wide-char (CJK/emoji)
// and zero-width (combining mark placeholder \uFFFF) rows to ensure the
// DOM spans are built correctly without errors.

import { describe, it, expect, beforeEach } from "vitest";
import * as render from "./render.js";
import type { ScreenMessage, WireRun } from "./types.js";

interface FakeCtx {
  font: string;
  measureText: (t: string) => { width: number };
}
HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
  const ctx: FakeCtx = {
    font: "",
    measureText: (text: string) => ({ width: text.length * 8 }),
  };
  return ctx;
} as typeof HTMLCanvasElement.prototype.getContext;

function makeMsg(
  rows: WireRun[][],
  cursor: [number, number],
  changed?: number[],
): ScreenMessage {
  return {
    type: "screen",
    rows,
    cursor,
    changed: changed ?? rows.map((_, i) => i),
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: true,
  };
}

async function flush(msg: ScreenMessage): Promise<void> {
  render.handleScreen(msg);
  await new Promise((r) => setTimeout(r, 16));
}

describe("render: wide-char and zero-width handling", () => {
  let outputEl: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    document.body.innerHTML = `<div id="term"><div id="term-output"></div></div>`;
    termWrap = document.getElementById("term") as HTMLDivElement;
    outputEl = document.getElementById("term-output") as HTMLDivElement;
    render.resetScreen();
    render.init({ output: outputEl, termWrap });
    render.updateFontMetrics();
  });

  it("renders wide char with \\uFFFF continuation placeholder", async () => {
    // Server sends: "A漢\uFFFFB" + trailing spaces for a 10-col row
    const row: WireRun[] = [{ t: "A漢\uFFFFB" + " ".repeat(6), f: -1, b: -1, a: 0, uc: -1 }];
    const rows = [row];
    await flush(makeMsg(rows, [0, 0]));

    const rowEl = outputEl.children[0] as HTMLElement;
    expect(rowEl).toBeDefined();
    // The \uFFFF should NOT appear as visible text — it's consumed by
    // the letterSpacing logic. Verify the full text doesn't contain \uFFFF.
    const fullText = rowEl.textContent ?? "";
    expect(fullText).not.toContain("\uFFFF");
    // "漢" should be present
    expect(fullText).toContain("漢");
  });

  it("renders multiple consecutive wide chars correctly", async () => {
    // "漢\uFFFF字\uFFFF" + 6 spaces = 10 cols
    const row: WireRun[] = [{ t: "漢\uFFFF字\uFFFF" + " ".repeat(6), f: -1, b: -1, a: 0, uc: -1 }];
    await flush(makeMsg([row], [0, 0]));

    const rowEl = outputEl.children[0] as HTMLElement;
    const fullText = rowEl.textContent ?? "";
    expect(fullText).not.toContain("\uFFFF");
    expect(fullText).toContain("漢");
    expect(fullText).toContain("字");
  });

  it("renders zero-width combining mark result (no crash)", async () => {
    // A combining mark in the input would be width-0 on the server
    // and not occupy a cell. But if it somehow ends up in wire text,
    // the renderer shouldn't crash.
    const row: WireRun[] = [{ t: "e\u0301" + " ".repeat(9), f: -1, b: -1, a: 0, uc: -1 }];
    await flush(makeMsg([row], [0, 0]));

    const rowEl = outputEl.children[0] as HTMLElement;
    expect(rowEl).toBeDefined();
    // Should render without throwing
    expect(rowEl.textContent).toContain("e");
  });

  it("cursor on wide char renders correctly", async () => {
    // Cursor at col 1 which is the start of a wide char "漢"
    // Row: "A漢\uFFFFB      "
    const row: WireRun[] = [{ t: "A漢\uFFFFB" + " ".repeat(6), f: -1, b: -1, a: 0, uc: -1 }];
    await flush(
      makeMsg([row], [0, 1], [0]),
    );

    // The cursor span should exist at the right position
    const rowEl = outputEl.children[0] as HTMLElement;
    expect(rowEl).toBeDefined();
    const spans = Array.from(rowEl.children) as HTMLElement[];
    // Find cursor span
    const cursorSpan = spans.find(
      (s) =>
        s.classList.contains("term-cursor") ||
        s.classList.contains("term-cursor-underline") ||
        s.classList.contains("term-cursor-bar"),
    );
    // cursorHidden is true in our msg, so no cursor span is expected
    // (the server-side cursor is hidden because Ink draws its own).
    // But the row should still render without error.
    expect(cursorSpan).toBeUndefined();
  });

  it("non-hidden cursor on wide char", async () => {
    // Cursor visible at col 1 (on wide char "漢")
    const row: WireRun[] = [{ t: "A漢\uFFFFB" + " ".repeat(6), f: -1, b: -1, a: 0, uc: -1 }];
    const msg: ScreenMessage = {
      type: "screen",
      rows: [row],
      cursor: [0, 1],
      changed: [0],
      cursorHidden: false,
      cursorStyle: 0,
      cursorBlink: true,
    };
    await flush(msg);

    const rowEl = outputEl.children[0] as HTMLElement;
    expect(rowEl).toBeDefined();
    // Cursor should be rendered at col 1
    const spans = Array.from(rowEl.children) as HTMLElement[];
    let col = 0;
    for (const span of spans) {
      if (
        span.classList.contains("term-cursor") ||
        span.classList.contains("term-cursor-underline") ||
        span.classList.contains("term-cursor-bar")
      ) {
        expect(col).toBe(1);
        // The cursor span text should be "漢"
        expect(span.textContent).toBe("漢");
        return;
      }
      col += [...(span.textContent ?? "")].length;
    }
    // If we get here, cursor span was not found — that's a problem
    expect.fail("cursor span not found at col 1");
  });

  it("emoji (4-byte wide char) renders without crash", async () => {
    // 😀 is width-2: "😀\uFFFF" + 8 spaces = 10 cols
    const row: WireRun[] = [{ t: "😀\uFFFF" + " ".repeat(8), f: -1, b: -1, a: 0, uc: -1 }];
    await flush(makeMsg([row], [0, 0]));

    const rowEl = outputEl.children[0] as HTMLElement;
    const text = rowEl.textContent ?? "";
    expect(text).not.toContain("\uFFFF");
    expect(text).toContain("😀");
  });

  it("row of all wide chars", async () => {
    // 5 wide chars = 10 cols: "漢\uFFFF字\uFFFF漢\uFFFF字\uFFFF漢\uFFFF"
    const t = "漢\uFFFF字\uFFFF漢\uFFFF字\uFFFF漢\uFFFF";
    const row: WireRun[] = [{ t, f: -1, b: -1, a: 0, uc: -1 }];
    await flush(makeMsg([row], [0, 0]));

    const rowEl = outputEl.children[0] as HTMLElement;
    const text = rowEl.textContent ?? "";
    expect(text).not.toContain("\uFFFF");
    expect([...text].filter((c) => c === "漢").length).toBe(3);
    expect([...text].filter((c) => c === "字").length).toBe(2);
  });
});
