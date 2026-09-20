// Behavioral display conformance through a SERVED page: the same Go-generated
// fixtures src/render-behavior.test.ts asserts inside the runner's page are run
// through the engine's own bundle in a Playwright-driven Chromium, so the grid is
// asserted against a full page load. Each frame is the engine's real wire output
// for a real escape sequence; the expected grid is spec-authored in
// terminal/render_golden_test.go TestRenderGoldenBehavior. Run with `npm run test:e2e`.
import { test, expect } from "@playwright/test";
import type { TerminalEngine } from "../src/index.js";
import { bundleEngine, HARNESS, readGolden, waitForRows } from "./e2e-harness.js";

interface BehaviorEntry {
  name: string;
  input: string;
  want: string[];
  frame: string; // base64 of the engine's real wire frame
}

const scenarios = JSON.parse(
  readGolden("behavior.manifest.json").toString("utf8"),
) as BehaviorEntry[];

test.describe("behavioral display conformance in a real browser (escape seq -> engine -> wire -> DOM)", () => {
  let bundle = "";
  test.beforeAll(async () => {
    bundle = await bundleEngine();
  });

  for (const sc of scenarios) {
    test(`renders the on-screen grid after ${sc.name}`, async ({ page }) => {
      const frameBytes = Array.from(Buffer.from(sc.frame, "base64"));
      await page.setContent(HARNESS);
      await page.addScriptTag({ content: bundle });
      await page.evaluate((bytes: number[]) => {
        const msg = WTE.decodeWireBinary(new Uint8Array(bytes).buffer);
        if (!msg || msg.type !== "screen") {
          throw new Error("fixture did not decode as a screen frame");
        }
        const output = document.getElementById("out")!;
        const termWrap = document.getElementById("wrap")!;
        window.__engine?.dispose();
        const engine = WTE.createTerminalEngine({
          output,
          termWrap,
          callbacks: {
            onMessage: () => undefined,
            onOpen: () => undefined,
            onClose: () => undefined,
            computeSize: () => ({ cols: 80, rows: 24 }),
          },
        });
        window.__engine = engine;
        engine.renderer.updateFontMetrics();
        engine.renderer.handleScreen(msg);
      }, frameBytes);
      // Deterministic flush wait: every spec grid's rows must exist as divs
      // (the fixture frame is a full repaint, so at least want.length rows
      // materialize; rows build in ascending order).
      await waitForRows(page, sc.want.length);

      const got = await page.evaluate(() => {
        const out = document.getElementById("out")!;
        // Each row's visible text: normalize the nbsp filler an empty row uses
        // and trim trailing blanks, matching the vt RowString spec-grid convention.
        return Array.from(out.children).map((rowEl) =>
          (rowEl.textContent ?? "").replace(/\u00a0/g, " ").replace(/[ ]+$/, ""),
        );
      });
      // The DOM grid rendered with REAL layout must equal the spec grid.
      expect(got.slice(0, sc.want.length), `input ${JSON.stringify(sc.input)}`).toEqual(sc.want);
    });
  }
});

/** The esbuild IIFE global `addScriptTag` injects; see `bundleEngine`. */
declare const WTE: Pick<
  typeof import("../src/index.js"),
  "createTerminalEngine" | "decodeWireBinary"
>;
declare global {
  interface Window {
    __engine?: TerminalEngine;
  }
}
