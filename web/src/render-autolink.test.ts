// The two link paths at their edges. The AUTOLINKER (linkifySpans) splits a bare
// http(s) URL out of a run with no OSC 8 URI into an anchor, and the text on
// either side must survive in its own span with the run's styling: eating the
// surrounding text or dropping its color corrupts the screen to add a link. The
// OSC 8 gate joins the scheme allow-list (hyperlink-safety.fuzz.test.ts) with
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

describe("autolinking a bare URL in plain text", () => {
  it("keeps the text on each side of the URL in its own span", () => {
    // The row must read back exactly as the application printed it, with the
    // URL — and only the URL — carved out as the anchor.
    const spans = renderRow([run("see https://example.com/x now")]);
    expect(spans.map((s) => s.textContent)).toEqual(["see ", "https://example.com/x", " now"]);
    expect(spans[1]!.tagName).toBe("A");
  });

  it("emits only the anchor when the URL is the whole run", () => {
    // No text before or after it, so no empty filler spans either.
    const spans = renderRow([run("https://example.com/x")]);
    expect(spans.map((s) => s.textContent)).toEqual(["https://example.com/x"]);
    expect(spans[0]!.tagName).toBe("A");
  });

  it("anchors each of two URLs and keeps the separator between them", () => {
    const spans = renderRow([run("https://a.example https://b.example")]);
    expect(spans.map((s) => s.textContent)).toEqual([
      "https://a.example",
      " ",
      "https://b.example",
    ]);
    expect(anchors().map((a) => a.getAttribute("href"))).toEqual([
      "https://a.example",
      "https://b.example",
    ]);
  });

  it("carries the run's own styling onto the anchor", () => {
    // A URL printed inside colored, bold output is still that output: the
    // anchor replaces the span, so it has to take the span's inline properties
    // with it or the link renders in the terminal's default appearance.
    const spans = renderRow([run("https://example.com/x", { f: 0xff0000, a: 1 })]);
    const anchor = spans[0]!;
    expect(anchor.tagName).toBe("A");
    expect(anchor.style.color).toBe(cssColor(0xff0000));
    expect(anchor.style.fontWeight).toBe("bold");
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
