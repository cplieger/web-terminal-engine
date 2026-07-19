#!/usr/bin/env node
// Render-pipeline micro-benchmark (C3 perf experiment, 2026-07). Drives the
// REAL render.ts + store.ts through htop/vim-like workloads in headless
// Chromium and reports ms/frame + a CPU-profile attribution, so render-path
// optimizations are measured, never guessed. Not part of any test battery —
// run by hand: `node e2e/render-bench.mjs [runsPerScenario]`.
import * as esbuild from "esbuild";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

// Same js->ts resolution the e2e harness uses.
const jsToTs = {
  name: "js-to-ts",
  setup(build) {
    build.onResolve({ filter: /\.js$/ }, (args) => {
      if (!args.path.startsWith(".")) return null;
      const tsPath = path.resolve(args.resolveDir, args.path.replace(/\.js$/, ".ts"));
      return fs.existsSync(tsPath) ? { path: tsPath } : null;
    });
  },
};

const bundle = (
  await esbuild.build({
    stdin: {
      contents: `export * as render from "./src/render.js";`,
      resolveDir: webDir,
      loader: "ts",
    },
    bundle: true,
    format: "iife",
    globalName: "WTE",
    write: false,
    plugins: [jsToTs],
  })
).outputFiles[0].text;

const HARNESS = `<!doctype html><html><head><meta charset="utf-8"><style>
  :root { --bg: #000; --text: #dddee1; }
  html, body { margin: 0; padding: 0; background: var(--bg); }
  #wrap { font: 14px/17px "DejaVu Sans Mono", monospace; color: var(--text);
          background: var(--bg); padding: 4px; width: 1100px; height: 900px;
          position: relative; overflow: auto; }
  #out { white-space: pre; position: relative; }
  .term-cursor-overlay { position: absolute; pointer-events: none; white-space: pre;
                         overflow: hidden; box-sizing: border-box; }
  .term-cursor-overlay:not(.visible) { display: none; }
  .term-cursor { background: var(--text); color: var(--bg); }
</style></head><body><div id="wrap"><div id="out"></div></div></body></html>`;

// ---- workload builders (run IN the page) ----
const pageLib = `
  // run builder: cells with varied styling; some rows carry URLs for linkify.
  function makeRow(cols, seed, withUrl) {
    const runs = [];
    const chunk = 10;
    for (let c = 0; c < cols; c += chunk) {
      const n = (seed + c) % 97;
      runs.push({
        t: withUrl && c === 30 ? "https://x.io/" + n + "  " : "cell" + String(n).padStart(2, "0") + "    ",
        f: n % 5 === 0 ? 2 : -1,
        b: n % 11 === 0 ? 4 : -1,
        a: n % 7 === 0 ? 1 : 0,
        uc: -1,
      });
    }
    return runs;
  }
  function altFrame(rows, cols, tick, changedRows, urls) {
    const grid = new Array(rows);
    const changed = [];
    for (let y = 0; y < rows; y++) {
      if (changedRows === null || changedRows.includes(y)) {
        grid[y] = makeRow(cols, tick * 31 + y * 7, urls && y % 6 === 0);
        changed.push(y);
      }
    }
    return { type: "screen", rows: grid, base: 0, cursor: [rows - 1, 0], changed,
             altActive: true, cursorStyle: 0, cursorHidden: false, cursorBlink: false,
             bell: false, scrollbackCleared: false, inputAck: 0 };
  }
  function mainFrame(rows, cols, base, tick, urls) {
    const grid = new Array(rows);
    const changed = [];
    for (let y = 0; y < rows; y++) {
      grid[y] = makeRow(cols, tick * 13 + y * 3, urls && y % 6 === 0);
      changed.push(y);
    }
    return { type: "screen", rows: grid, base, cursor: [rows - 1, 0], changed,
             altActive: false, cursorStyle: 0, cursorHidden: false, cursorBlink: false,
             bell: false, scrollbackCleared: false, inputAck: 0 };
  }
  // CPU-bound harness: replace rAF with a microtask dispatcher that TIMES each
  // callback, so 120 frames process back-to-back at CPU speed and the result
  // is flush work (JS + forced layout), not vsync cadence. Forced layouts from
  // offsetTop/getComputedStyle still happen synchronously, so they are counted.
  let flushCpuMs = 0;
  window.requestAnimationFrame = (cb) => {
    queueMicrotask(() => {
      const t0 = performance.now();
      cb(t0);
      flushCpuMs += performance.now() - t0;
    });
    return 1;
  };
  window.cancelAnimationFrame = () => {};
  async function drive(frames) {
    flushCpuMs = 0;
    for (const f of frames) {
      WTE.render.handleScreen(f);
      // Let the microtask chain drain (one macrotask hop per frame).
      await new Promise((r) => setTimeout(r, 0));
    }
    for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
    return flushCpuMs;
  }
`;

const scenarios = {
  // htop-like: EVERY row repaints every frame, 50x140 alt grid.
  "alt-full-churn": `(async () => {
    const frames = [];
    for (let t = 0; t < 120; t++) frames.push(altFrame(50, 140, t, null, false));
    return drive(frames);
  })()`,
  // vim-like: one full paint, then 3 rows change per frame on a POPULATED
  // alt screen (the realistic editing/progress-bar cadence).
  "alt-partial-3rows": `(async () => {
    const frames = [altFrame(50, 140, 0, null, false)];
    for (let t = 1; t < 120; t++) frames.push(altFrame(50, 140, t, [10, 11, 48], false));
    return drive(frames);
  })()`,
  // alt full churn where some rows carry URLs (linkify on the hot path).
  "alt-full-churn-urls": `(async () => {
    const frames = [];
    for (let t = 0; t < 120; t++) frames.push(altFrame(50, 140, t, null, true));
    return drive(frames);
  })()`,
  // main-screen scroll: window slides forward each frame (new base), all rows change.
  "main-scroll-burst": `(async () => {
    const frames = [];
    for (let t = 0; t < 120; t++) frames.push(mainFrame(50, 140, t * 2, t, false));
    return drive(frames);
  })()`,
};

const runs = Number(process.argv[2] ?? 3);
const browser = await chromium.launch({ args: ["--js-flags=--expose-gc"] });
const page = await browser.newPage({ viewport: { width: 1200, height: 950 } });
const cdp = await page.context().newCDPSession(page);

async function boot() {
  await page.setContent(HARNESS);
  await page.addScriptTag({ content: bundle });
  await page.addScriptTag({ content: pageLib });
  await page.evaluate(() => {
    WTE.render.init({
      output: document.getElementById("out"),
      termWrap: document.getElementById("wrap"),
    });
    WTE.render.updateFontMetrics();
  });
}

const results = {};
for (const [name, code] of Object.entries(scenarios)) {
  const times = [];
  for (let r = 0; r < runs; r++) {
    await boot(); // fresh DOM + module state per run
    times.push(await page.evaluate(code));
  }
  times.sort((a, b) => a - b);
  const best = times[0];
  results[name] = best;
  console.log(
    `${name.padEnd(22)} best ${best.toFixed(1)}ms total, ${(best / 120).toFixed(3)}ms/frame  (all: ${times.map((t) => t.toFixed(0)).join(" ")})`,
  );
}

// ---- CPU-profile attribution for the heaviest alt scenario ----
await boot();
await cdp.send("Profiler.enable");
await cdp.send("Profiler.setSamplingInterval", { interval: 100 });
await cdp.send("Profiler.start");
await page.evaluate(scenarios["main-scroll-burst"]);
const { profile } = await cdp.send("Profiler.stop");
const self = new Map();
const total = profile.samples?.length ?? 0;
const byId = new Map(profile.nodes.map((n) => [n.id, n]));
for (const s of profile.samples ?? []) {
  const n = byId.get(s);
  const fn = n?.callFrame?.functionName || "(anonymous)";
  self.set(fn, (self.get(fn) ?? 0) + 1);
}
const top = [...self.entries()].sort((a, b) => b[1] - a[1]).slice(0, 14);
console.log("\nCPU self-time attribution (main-scroll-burst):");
for (const [fn, n] of top) {
  console.log(`  ${(((n / total) * 100) | 0).toString().padStart(3)}%  ${fn}`);
}

await browser.close();
// A private mkdtemp dir instead of a fixed /tmp name: a predictable shared
// path is symlink-attackable on multi-user machines (CodeQL js/insecure-temporary-file).
const outDir = fs.mkdtempSync(path.join(os.tmpdir(), "render-bench-"));
const outFile = path.join(outDir, "render-bench-results.json");
fs.writeFileSync(outFile, JSON.stringify(results, null, 2));
console.log(`\nresults written to ${outFile}`);
