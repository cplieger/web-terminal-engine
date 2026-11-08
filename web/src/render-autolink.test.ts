// The link path at its edges. Links are the server's: a run carries a URI (OSC 8,
// or a bare URL the server stamped with attr bit 1024) or it renders as text.
// The OSC 8 gate joins the scheme allow-list (hyperlink-safety.fuzz.test.ts) with
// the link-TEXT rule: an application may hold one hyperlink open across a table
// cell, so runs of only whitespace, Box Drawing (U+2500..U+257F) and Block
// Elements (U+2580..U+259F) are not anchored and the decoration hugs the text.

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import type { Renderer } from "./render.js";
import { createEngineFixture } from "./test-helpers/engine-fixture.js";
import { cssColor } from "./test-helpers/spec-colors.js";
import type { ScreenMessage, WireRun } from "./types.js";

let realGetContext: typeof HTMLCanvasElement.prototype.getContext;
let realRAF: typeof globalThis.requestAnimationFrame;
let realCAF: typeof globalThis.cancelAnimationFrame;

let output: HTMLDivElement;
let render: Renderer;

beforeEach(() => {
  realGetContext = HTMLCanvasElement.prototype.getContext;
  // A fixed cell metric: a real Canvas2D measures whatever font the machine has
  // installed, and the width cache needs a deterministic measureText.
  HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
    return { font: "", measureText: (t: string) => ({ width: [...t].length * 8 }) };
  } as typeof HTMLCanvasElement.prototype.getContext;
  // Drive the rAF-batched flush synchronously so a frame renders on return.
  realRAF = globalThis.requestAnimationFrame;
  realCAF = globalThis.cancelAnimationFrame;
  globalThis.requestAnimationFrame = ((cb: FrameRequestCallback): number => {
    cb(0);
    return undefined as unknown as number;
  }) as typeof globalThis.requestAnimationFrame;
  globalThis.cancelAnimationFrame = (() => undefined) as typeof globalThis.cancelAnimationFrame;

  const fx = createEngineFixture();
  output = fx.output;
  render = fx.engine.renderer;
  render.updateFontMetrics();
});

afterEach(() => {
  HTMLCanvasElement.prototype.getContext = realGetContext;
  globalThis.requestAnimationFrame = realRAF;
  globalThis.cancelAnimationFrame = realCAF;
});

/** Render `runs` on the only row of a one-row screen and return its children. */
function renderRow(runs: WireRun[]): HTMLElement[] {
  const msg: ScreenMessage = {
    type: "screen",
    base: 0,
    rows: [runs],
    cursor: [0, 0],
    changed: [0],
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: false,
  };
  render.handleScreen(msg);
  const rowEl = output.children[0] as HTMLElement;
  return Array.from(rowEl.children) as HTMLElement[];
}

function run(text: string, over: Partial<WireRun> = {}): WireRun {
  return { t: text, f: -1, b: -1, uc: -1, a: 0, ...over };
}

function anchors(): HTMLAnchorElement[] {
  return Array.from(output.querySelectorAll("a"));
}

describe("links are the server's", () => {
  it("renders a bare URL the server did not mark as plain text", () => {
    // A row the server stripped at the row ceiling carries no link marks, and
    // re-scanning it here would link a prefix of a URL that wraps past it.
    const spans = renderRow([run("see https://example.com/x now")]);
    expect(anchors().length).toBe(0);
    expect(spans.map((s) => s.textContent).join("")).toBe("see https://example.com/x now");
  });

  it("carves no https anchor out of an OSC 8 link to another scheme", () => {
    // The program's link wins: its non-http target renders inert, and the URL
    // in its text is not the program's target.
    renderRow([run("docs at https://example.com/x", { u: "mailto:a@b.example" })]);
    expect(anchors().length).toBe(0);
  });

  it("anchors a run the server marked, carrying the run's styling", () => {
    const url = "https://example.com/x";
    const spans = renderRow([
      run("see ", {}),
      run(url, { u: url, a: 1024 | 1, f: 0xff0000 }),
      run(" now", {}),
    ]);
    expect(spans.map((s) => s.textContent)).toEqual(["see ", url, " now"]);
    const anchor = spans[1]!;
    expect(anchor.tagName).toBe("A");
    expect(anchor.className).toBe("term-link term-autolink");
    const inner = anchor.firstElementChild as HTMLElement;
    expect(inner.style.color).toBe(cssColor(0xff0000));
    expect(inner.style.fontWeight).toBe("bold");
  });
});

describe("the OSC 8 link-text rule", () => {
  it("does not anchor a run made only of the boundary box-drawing glyphs", () => {
    // U+2500 and U+259F are the first and last code points of the decorative
    // range (Box Drawing through Block Elements). A cell made only of those is
    // table structure an application kept one hyperlink open across, never the
    // link's text, so it must render as inert styling.
    const url = "https://example.com/wrapped";
    const spans = renderRow([run("\u2500\u259f", { u: url })]);
    expect(anchors().length).toBe(0);
    expect(spans.map((s) => s.textContent)).toEqual(["\u2500\u259f"]);
  });

  it("anchors a run whose text is non-Latin", () => {
    // The decorative exclusion is a narrow code-point range, not "anything
    // outside ASCII": CJK link text is link text.
    const url = "https://example.com/jp";
    renderRow([run("日本語", { u: url })]);
    expect(anchors().length).toBe(1);
    expect(anchors()[0]!.textContent).toBe("日本語");
  });
});

describe("the OSC 8 scheme allow-list", () => {
  it("does not anchor a dangerous scheme that merely contains an http(s) URL", () => {
    // SECURITY. The allow-list is a test of what the URI STARTS with. A
    // `javascript:` URI can quote an http(s) URL inside its payload, so a gate
    // that searched anywhere in the string would hand the application a
    // clickable script-injection link — exactly the vector the allow-list
    // exists to close.
    const uri = "javascript:window.open('https://example.com')";
    const spans = renderRow([run("linktext", { u: uri })]);
    expect(anchors().length).toBe(0);
    // The content is not dropped; it renders as inert text.
    expect(spans.map((s) => s.textContent)).toEqual(["linktext"]);
  });
});
