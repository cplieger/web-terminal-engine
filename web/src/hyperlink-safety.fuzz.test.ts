// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import fc from "fast-check";
import * as render from "./render.js";
import type { ScreenMessage, WireRun } from "./types.js";

HTMLCanvasElement.prototype.getContext = function (): unknown {
  return { font: "", measureText: (t: string) => ({ width: t.length * 8 }) };
} as typeof HTMLCanvasElement.prototype.getContext;

describe("hyperlink safety fuzz: no javascript:/data: hrefs", () => {
  it("never renders a non-http(s) anchor href for arbitrary OSC 8 URIs", async () => {
    const output = document.createElement("div");
    output.id = "term-output";
    output.contentEditable = "true";
    const termWrap = document.createElement("div");
    termWrap.id = "term-wrap";
    termWrap.appendChild(output);
    document.body.innerHTML = "";
    document.body.appendChild(termWrap);
    render.init({ output, termWrap });
    render.updateFontMetrics();

    // Bias the generator toward real URI scheme prefixes so both the
    // dangerous and the safe branches of render's OSC 8 href guard are
    // reached (a purely random string almost never forms a scheme).
    const scheme = fc.constantFrom(
      "javascript:",
      "JavaScript:",
      "  javascript:",
      "data:text/html,",
      "vbscript:",
      "file:///",
      "ftp://",
      "http://",
      "https://",
      "HTTPS://",
      "",
    );
    const uriArb = fc.tuple(scheme, fc.string({ maxLength: 40 })).map(([s, tail]) => s + tail);

    // Two known-safe links guarantee at least two anchors render, so the
    // invariant below can never pass vacuously; the rest are fuzzed.
    const uris = ["http://safe.test/a", "https://safe.test/b", ...fc.sample(uriArb, 300)];
    const runs: WireRun[] = uris.map((u) => ({ t: "x", f: -1, b: -1, a: 0, uc: -1, u }));
    const msg: ScreenMessage = {
      type: "screen",
      base: 0,
      rows: [runs, [], [], [], []],
      cursor: [0, 0],
      changed: [0],
      cursorHidden: true,
      cursorStyle: 0,
      cursorBlink: false,
    };
    render.handleScreen(msg);
    await new Promise((r) => setTimeout(r, 32));

    const anchors = output.querySelectorAll("a");
    expect(anchors.length).toBeGreaterThanOrEqual(2);
    for (const a of anchors) {
      // Every anchor the renderer produced is an http(s) link: a
      // javascript:/data:/vbscript:/file: URI never becomes clickable.
      expect((a as HTMLAnchorElement).href.toLowerCase()).toMatch(/^https?:\/\//);
    }
  });

  it("end-to-end: rendered anchors have only http(s) hrefs", async () => {
    const output = document.createElement("div");
    output.id = "term-output";
    output.contentEditable = "true";
    const termWrap = document.createElement("div");
    termWrap.id = "term-wrap";
    termWrap.appendChild(output);
    document.body.innerHTML = "";
    document.body.appendChild(termWrap);
    render.init({ output, termWrap });
    render.updateFontMetrics();

    // Test a small batch of dangerous URIs directly
    const dangerous = [
      "javascript:alert(1)",
      "data:text/html,<script>alert(1)</script>",
      "JAVASCRIPT:void(0)",
      "Data:text/html;base64,PHNjcmlwdD4=",
      "jAvAsCrIpT:fetch('/steal')",
      "  javascript:x",
      "vbscript:run",
    ];
    for (const uri of dangerous) {
      const runs: WireRun[] = [{ t: "x", f: -1, b: -1, a: 0, uc: -1, u: uri }];
      const msg: ScreenMessage = {
        type: "screen",
        base: 0,
        rows: [runs, [], [], [], []],
        cursor: [0, 0],
        changed: [0],
        cursorHidden: true,
        cursorStyle: 0,
        cursorBlink: false,
      };
      render.handleScreen(msg);
      await new Promise((r) => setTimeout(r, 32));
      const anchors = output.querySelectorAll("a");
      // A dangerous scheme must not become a clickable link at all.
      expect(anchors.length, `dangerous URI must not render an anchor: ${uri}`).toBe(0);
    }
  });
});
