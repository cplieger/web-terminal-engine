import { test, expect, type Page } from "@playwright/test";
import type { TerminalEngine } from "../src/index.js";
import { bundleEngine } from "./e2e-harness.js";

const PAGE = `<!doctype html><html><head><meta charset="utf-8"><style>
  html, body { margin: 0; background: #000; color: #ddd; }
  #wrap { position: relative; height: 340px; width: 600px; overflow-y: auto;
          font: 14px/17px "DejaVu Sans Mono", monospace; }
  #out { white-space: pre; }
</style></head><body><div id="wrap"><div id="out"></div></div></body></html>`;

const ROWS = 30;

async function startStream(page: Page, bundle: string): Promise<void> {
  await page.setContent(PAGE);
  await page.addScriptTag({ content: bundle });
  await page.evaluate((rows: number) => {
    const engine = WTE.createTerminalEngine({
      output: document.getElementById("out")!,
      termWrap: document.getElementById("wrap")!,
      callbacks: {
        onMessage: () => undefined,
        onOpen: () => undefined,
        onClose: () => undefined,
        computeSize: () => ({ cols: 80, rows }),
      },
    });
    engine.renderer.updateFontMetrics();
    window.__engine = engine;
    let base = 0;
    const frame = (): void => {
      const lines = Array.from({ length: rows }, (_, y) => [
        { t: `line ${String(base + y)}`, f: -1, b: -1, a: 0, uc: -1 },
      ]);
      engine.renderer.handleScreen({
        type: "screen",
        base,
        rows: lines,
        changed: lines.map((_, y) => y),
        cursor: [rows - 1, 0],
      });
      base += 1;
    };
    window.__stream = window.setInterval(frame, 16);
  }, ROWS);
  // Enough history that the reader has somewhere to scroll up to.
  await page.waitForFunction(() => {
    const wrap = document.getElementById("wrap")!;
    return wrap.scrollHeight - wrap.clientHeight > 600;
  });
}

async function readState(page: Page): Promise<{ top: number; max: number; scrolledUp: boolean }> {
  return page.evaluate(() => {
    const wrap = document.getElementById("wrap")!;
    return {
      top: wrap.scrollTop,
      max: wrap.scrollHeight - wrap.clientHeight,
      scrolledUp: window.__engine!.scroll.isUserScrolledUp(),
    };
  });
}

async function stopStream(page: Page): Promise<void> {
  await page.evaluate(() => {
    window.clearInterval(window.__stream);
    window.__engine?.dispose();
  });
}

test.describe("the reader holds while output streams", () => {
  let bundle = "";
  test.beforeAll(async () => {
    bundle = await bundleEngine();
  });

  for (let run = 1; run <= 5; run++) {
    test(`a slow trackpad scroll up mid-stream stays held (run ${String(run)})`, async ({
      page,
    }) => {
      await startStream(page, bundle);
      await page.mouse.move(300, 170);
      for (let k = 0; k < 40; k++) {
        await page.mouse.wheel(0, -10);
        await page.waitForTimeout(16);
      }
      await page.waitForTimeout(500);

      const after = await readState(page);
      await stopStream(page);
      expect(after.scrolledUp).toBe(true);
      expect(after.max - after.top).toBeGreaterThan(30);
    });
  }
});

test.describe("a touch-only reader", () => {
  test.use({ hasTouch: true });
  let bundle = "";
  test.beforeAll(async () => {
    bundle = await bundleEngine();
  });

  test("a finger drag up mid-stream stays held", async ({ page }) => {
    await startStream(page, bundle);
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x: 300, y: 60 }],
    });
    for (let k = 1; k <= 12; k++) {
      await cdp.send("Input.dispatchTouchEvent", {
        type: "touchMove",
        touchPoints: [{ x: 300, y: 60 + k * 20 }],
      });
      await page.waitForTimeout(16);
    }
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await page.waitForTimeout(800);

    const after = await readState(page);
    await stopStream(page);
    expect(after.scrolledUp).toBe(true);
    expect(after.max - after.top).toBeGreaterThan(30);
  });

  test("a tap mid-stream leaves the view following the tail", async ({ page }) => {
    await startStream(page, bundle);
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x: 300, y: 170 }],
    });
    await page.waitForTimeout(80);
    await cdp.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });
    await page.waitForTimeout(300);

    const after = await readState(page);
    await stopStream(page);
    expect(after.scrolledUp).toBe(false);
    expect(after.max - after.top).toBeLessThanOrEqual(17);
  });
});

/** The esbuild IIFE global `addScriptTag` injects; see `bundleEngine`. */
declare const WTE: Pick<typeof import("../src/index.js"), "createTerminalEngine">;
declare global {
  interface Window {
    __engine?: TerminalEngine;
    __stream?: number;
  }
}
