// @vitest-environment happy-dom
//
// OSC 8 hyperlink rendering: a run carrying an OSC 8 URI is painted as an <a>.
//
// Spec: xterm ctlseqs OSC 8 (`OSC 8 ; params ; URI ST`) — the URI in the
// sequence is authoritative and is what the link points at. Anchors are opened
// safely (target=_blank, rel=noopener guards reverse-tabnabbing). render.ts
// only linkifies http/https URIs (a conservative allow-list); the adversarial
// scheme sweep proving javascript:/data:/etc. never become clickable lives in
// hyperlink-safety.fuzz.test.ts. Expectations here derive from the OSC 8 spec,
// not from reading render.ts.

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

function frame(rowsByIdx: Record<number, WireRun[]>, cursor: [number, number]): ScreenMessage {
  const screenH = 5;
  const rows: WireRun[][] = new Array(screenH);
  const changed: number[] = [];
  for (const k of Object.keys(rowsByIdx)) {
    const idx = Number(k);
    rows[idx] = rowsByIdx[idx]!;
    changed.push(idx);
  }
  return {
    type: "screen",
    base: 0,
    rows,
    cursor,
    changed,
    cursorHidden: true,
    cursorStyle: 0,
    cursorBlink: true,
  };
}

async function flushFrame(msg: ScreenMessage): Promise<void> {
  render.handleScreen(msg);
  // render batches DOM updates via requestAnimationFrame (happy-dom
  // implements rAF as a ~16ms timer). Wait two frames on a plain timer
  // instead of racing the rAF-queue ordering, which is runtime/timing
  // dependent and flaked on CI while passing locally.
  await new Promise((r) => setTimeout(r, 32));
}

describe("OSC 8 hyperlink rendering", () => {
  let output: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    output = document.createElement("div");
    output.id = "term-output";
    output.contentEditable = "true";
    termWrap = document.createElement("div");
    termWrap.id = "term-wrap";
    termWrap.appendChild(output);
    document.body.innerHTML = "";
    document.body.appendChild(termWrap);
    render.init({ output, termWrap });
    render.updateFontMetrics();
  });

  it("renders a run with URL as an <a> element with correct attributes", async () => {
    const runs: WireRun[] = [
      { t: "click ", f: -1, b: -1, a: 0, uc: -1 },
      { t: "here", f: -1, b: -1, a: 0, uc: -1, u: "http://example.com" },
      { t: " end", f: -1, b: -1, a: 0, uc: -1 },
    ];
    const msg = frame({ 0: runs }, [0, 10]);
    await flushFrame(msg);

    const anchors = output.querySelectorAll("a.term-link");
    expect(anchors.length).toBeGreaterThanOrEqual(1);
    const a = anchors[0] as HTMLAnchorElement;
    // The OSC 8 target is carried verbatim on the attribute (spec: the URI in
    // the sequence is authoritative), and resolves to the absolute URL.
    expect(a.getAttribute("href")).toBe("http://example.com");
    expect(a.href).toBe("http://example.com/");
    // Opened safely: a new context with no window.opener handle back.
    expect(a.target).toBe("_blank");
    expect(a.rel).toBe("noopener");
    expect(a.textContent).toBe("here");
  });

  it("does not render <a> for runs without URL", async () => {
    const runs: WireRun[] = [{ t: "plain text", f: -1, b: -1, a: 0, uc: -1 }];
    const msg = frame({ 0: runs }, [0, 10]);
    await flushFrame(msg);

    const anchors = output.querySelectorAll("a.term-link");
    // linkifySpans may detect URLs in text, but "plain text" has none
    expect(anchors.length).toBe(0);
  });

  it("keeps the OSC 8 href when the visible text is itself a URL fragment", async () => {
    // First row of a URL that wraps across lines: the visible text is only
    // a fragment, but the full target is carried in `u`. The regex
    // autolinker must NOT rebuild the link from the truncated visible text.
    const full = "http://example.com/very/long/path/that/wraps/here";
    const runs: WireRun[] = [
      { t: "http://example.com/very/long/pa", f: -1, b: -1, a: 0, uc: -1, u: full },
    ];
    const msg = frame({ 0: runs }, [0, 0]);
    await flushFrame(msg);

    const anchors = output.querySelectorAll("a.term-link");
    expect(anchors.length).toBe(1);
    const a = anchors[0] as HTMLAnchorElement;
    // Raw attribute is the OSC 8 target, not the truncated visible fragment.
    expect(a.getAttribute("href")).toBe(full);
    expect(a.textContent).toBe("http://example.com/very/long/pa");
  });

  it("does not linkify a non-http(s) OSC 8 scheme (renders as inert text)", async () => {
    // render.ts uses a conservative http/https-only allow-list, so even a
    // benign non-http scheme like mailto: is NOT turned into a live anchor —
    // the text renders inert. (Dangerous schemes are swept in the fuzz file.)
    const runs: WireRun[] = [{ t: "mail me", f: -1, b: -1, a: 0, uc: -1, u: "mailto:a@b.example" }];
    const msg = frame({ 0: runs }, [0, 0]);
    await flushFrame(msg);

    expect(output.querySelectorAll("a").length).toBe(0);
    expect(output.textContent).toContain("mail me");
  });
});
