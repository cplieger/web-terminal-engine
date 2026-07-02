// Shared helpers for the tier-3 real-browser (Playwright/chromium) display
// tests. Not a test file itself (no `.e2e.test.ts` suffix, so playwright's
// testMatch ignores it). It bundles the REAL render.ts + decodeWireBinary into
// one IIFE global, provides the terminal HTML harness, reads the Go-generated
// golden fixtures, and offers pixel-sampling helpers for the "does it actually
// paint" assertions.
import * as esbuild from "esbuild";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { PNG } from "pngjs";

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const goldenDir = path.resolve(webDir, "..", "render-golden");

// esbuild resolves ESM `.js` import specifiers to their `.ts` sources so
// render.ts (which imports "./store.js" etc.) bundles for the browser.
const jsToTs = {
  name: "js-to-ts",
  setup(build: esbuild.PluginBuild) {
    build.onResolve({ filter: /\.js$/ }, (args) => {
      if (!args.path.startsWith(".")) {
        return null;
      }
      const tsPath = path.resolve(args.resolveDir, args.path.replace(/\.js$/, ".ts"));
      return fs.existsSync(tsPath) ? { path: tsPath } : null;
    });
  },
};

/** bundleEngine bundles the real renderer + wire decoder into one IIFE global (WTE). */
export async function bundleEngine(): Promise<string> {
  const result = await esbuild.build({
    stdin: {
      contents: `export * as render from "./src/render.js";
export * as modes from "./src/modes.js";
export { decodeWireBinary } from "./src/wire-binary.js";`,
      resolveDir: webDir,
      loader: "ts",
    },
    bundle: true,
    format: "iife",
    globalName: "WTE",
    write: false,
    plugins: [jsToTs],
  });
  const out = result.outputFiles[0];
  if (!out) {
    throw new Error("esbuild produced no bundle output");
  }
  return out.text;
}

// Terminal HTML harness. The CSS vars mirror web-terminal-ui's defaults so
// inverse-on-default resolves to concrete colors: --text #dddee1
// (rgb(221,222,225)), --bg #000000. A real monospace font is required for the
// geometry/pixel assertions.
export const HARNESS = `<!doctype html><html><head><meta charset="utf-8"><style>
  :root { --bg: #000000; --text: #dddee1; }
  html, body { margin: 0; padding: 0; background: var(--bg); }
  #wrap { font: 16px/1.25 "DejaVu Sans Mono", "Liberation Mono", "Courier New", monospace;
          color: var(--text); background: var(--bg); padding: 0; width: 400px; }
  #out { white-space: pre; }
</style></head><body>
  <div id="wrap"><div id="out"></div></div>
</body></html>`;

/** readGolden reads a render-golden fixture (relative name) as bytes. */
export function readGolden(name: string): Buffer {
  return fs.readFileSync(path.join(goldenDir, name));
}

/** frameBytesArray reads a raw .bin fixture as a plain number[] (JSON-safe for page.evaluate). */
export function frameBytesArray(name: string): number[] {
  return Array.from(readGolden(name));
}

/** A cell rectangle in CSS pixels. */
export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** avgLuminance returns the mean perceptual luminance (0-255) over a rect of a PNG. */
export function avgLuminance(png: PNG, rect: Rect): number {
  let sum = 0;
  let n = 0;
  const x0 = Math.max(0, Math.round(rect.x));
  const y0 = Math.max(0, Math.round(rect.y));
  const x1 = Math.min(png.width, Math.round(rect.x + rect.width));
  const y1 = Math.min(png.height, Math.round(rect.y + rect.height));
  for (let y = y0; y < y1; y++) {
    for (let x = x0; x < x1; x++) {
      const i = (y * png.width + x) * 4;
      sum +=
        0.299 * (png.data[i] ?? 0) +
        0.587 * (png.data[i + 1] ?? 0) +
        0.114 * (png.data[i + 2] ?? 0);
      n++;
    }
  }
  return n > 0 ? sum / n : 0;
}

/** centerColor returns the RGB at a rect's center pixel. */
export function centerColor(png: PNG, rect: Rect): { r: number; g: number; b: number } {
  const x = Math.round(rect.x + rect.width / 2);
  const y = Math.round(rect.y + rect.height / 2);
  const i = (y * png.width + x) * 4;
  return { r: png.data[i] ?? 0, g: png.data[i + 1] ?? 0, b: png.data[i + 2] ?? 0 };
}

/** decodePng parses a screenshot buffer into a PNG for pixel sampling. */
export function decodePng(buf: Buffer): PNG {
  return PNG.sync.read(buf);
}

function lumAt(png: PNG, i: number): number {
  return (
    0.299 * (png.data[i] ?? 0) + 0.587 * (png.data[i + 1] ?? 0) + 0.114 * (png.data[i + 2] ?? 0)
  );
}

/**
 * pixelDiffFraction returns the fraction of pixels whose luminance differs by
 * more than a threshold between two equal-region rects — used to prove two
 * glyphs render as visually DISTINCT shapes (not the same blob).
 */
export function pixelDiffFraction(png: PNG, a: Rect, b: Rect): number {
  const w = Math.min(Math.round(a.width), Math.round(b.width));
  const h = Math.min(Math.round(a.height), Math.round(b.height));
  const ax = Math.round(a.x);
  const ay = Math.round(a.y);
  const bx = Math.round(b.x);
  const by = Math.round(b.y);
  let diff = 0;
  let n = 0;
  for (let dy = 0; dy < h; dy++) {
    for (let dx = 0; dx < w; dx++) {
      const la = lumAt(png, ((ay + dy) * png.width + (ax + dx)) * 4);
      const lb = lumAt(png, ((by + dy) * png.width + (bx + dx)) * 4);
      if (Math.abs(la - lb) > 24) {
        diff++;
      }
      n++;
    }
  }
  return n > 0 ? diff / n : 0;
}
