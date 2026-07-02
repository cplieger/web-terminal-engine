// Tier 3 — real DISPLAY OUTPUT (Playwright/chromium). The all-codes sibling
// test dumps getComputedStyle (proves the CSS is SET); this file goes to the
// actual rendered output that computed-style can't prove:
//   1. LAYOUT geometry via getBoundingClientRect — the monospace grid, a wide
//      char occupying two cells, the cursor's pixel position, contiguous rows.
//   2. PAINTED pixels via screenshot sampling — that chromium actually paints a
//      color, that bold is heavier ink than normal, dim is fainter, inverse is a
//      light block, hidden is blank, underline puts ink in the underline row.
//   3. VISUAL clear — text paints ink; re-rendering the row blank leaves no ink.
//
// Pixel checks sample a FEW known cells and assert SEMANTIC properties (is-red,
// more-ink, blank) — robust, unlike a full-screen baseline. Frames are built as
// ScreenMessage objects (the renderer's input contract), so no wire encoding is
// needed here. Run with `npm run test:e2e`.
import { test, expect, type Page } from "@playwright/test";
import {
  bundleEngine,
  HARNESS,
  avgLuminance,
  centerColor,
  decodePng,
  type Rect,
} from "./e2e-harness.js";

// Attribute bits, per render.ts buildRowSpans: 1 bold, 2 italic, 4 underline,
// 8 inverse, 16 strike, 32 dim, 64 hidden.
const BOLD = 1;
const UNDERLINE = 4;
const INVERSE = 8;
const DIM = 32;
const HIDDEN = 64;
const RED = 0xff0000;
const GREEN = 0x00ff00;
const BLUE = 0x0000ff;

interface WireRun {
  t: string;
  f: number;
  b: number;
  a: number;
  uc: number;
}
function run(t: string, opts: Partial<WireRun> = {}): WireRun {
  return { t, f: opts.f ?? -1, b: opts.b ?? -1, a: opts.a ?? 0, uc: opts.uc ?? -1 };
}
function screenMsg(rows: WireRun[][], cursor: [number, number], cursorHidden = true): unknown {
  return {
    type: "screen",
    base: 0,
    rows,
    cursor,
    changed: rows.map((_, i) => i),
    cursorHidden,
    cursorStyle: 0,
    cursorBlink: false,
  };
}

test.describe("real-browser display output (geometry + painted pixels + visual clear)", () => {
  let bundle = "";
  test.beforeAll(async () => {
    bundle = await bundleEngine();
  });

  async function renderMsg(page: Page, msg: unknown): Promise<void> {
    await page.setContent(HARNESS);
    await page.addScriptTag({ content: bundle });
    await page.evaluate((m) => {
      const out = document.getElementById("out")!;
      const wrap = document.getElementById("wrap")!;
      WTE.render.init({ output: out, termWrap: wrap });
      WTE.render.updateFontMetrics();
      WTE.render.handleScreen(m);
    }, msg);
    await page.waitForTimeout(200);
  }

  test("layout geometry: monospace grid, a wide char spans two cells, rows are contiguous", async ({
    page,
  }) => {
    const rows = [
      [run("ABCDEFGH")], // 8 narrow glyphs
      [run("\u6f22\uFFFF\u5b57\uFFFF")], // 漢 字 — two WIDE glyphs (4 cells)
      [run("MMMMMMMM")], // 8 narrow glyphs
      [run(" ".repeat(8))],
    ];
    await renderMsg(page, screenMsg(rows, [3, 0]));

    const g = await page.evaluate(() => {
      const out = document.getElementById("out")!;
      const cellW = parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue("--char-w"),
      );
      const span = (r: number, c: number): Rect & { text: string } => {
        const el = out.children[r]!.children[c] as HTMLElement;
        const b = el.getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height, text: el.textContent ?? "" };
      };
      const rowRect = (r: number): Rect => {
        const b = (out.children[r] as HTMLElement).getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height };
      };
      return {
        cellW,
        narrow0: span(0, 0),
        wide: span(1, 0),
        narrow2: span(2, 0),
        row0: rowRect(0),
        row1: rowRect(1),
      };
    });

    // The measured cell width must be a real, positive number (font loaded).
    expect(g.cellW).toBeGreaterThan(4);
    // Monospace: 8 narrow glyphs occupy 8 cells, in two different rows.
    expect(Math.abs(g.narrow0.width / 8 - g.cellW), "row0 cell width").toBeLessThan(1.5);
    expect(Math.abs(g.narrow2.width / 8 - g.cellW), "row2 cell width").toBeLessThan(1.5);
    // A wide (East Asian) glyph occupies exactly two cells.
    expect(g.wide.text).toBe("\u6f22");
    expect(Math.abs(g.wide.width - 2 * g.cellW), "wide glyph = 2 cells").toBeLessThan(1.5);
    // Rows are vertically contiguous (no gaps/overlap): row1 starts at row0's bottom.
    expect(Math.abs(g.row1.y - (g.row0.y + g.row0.height)), "rows contiguous").toBeLessThan(1.5);
  });

  test("cursor pixel position matches its cell column", async ({ page }) => {
    // Visible cursor at column 3; its painted span must sit at 3 * cellW from
    // the row's left edge.
    await renderMsg(page, screenMsg([[run("ABCDEFGH")], [run(" ".repeat(8))]], [0, 3], false));
    const g = await page.evaluate(() => {
      const out = document.getElementById("out")!;
      const cellW = parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue("--char-w"),
      );
      const rowLeft = (out.children[0] as HTMLElement).getBoundingClientRect().x;
      const cur = out.querySelector<HTMLElement>(
        ".term-cursor, .term-cursor-underline, .term-cursor-bar",
      );
      const b = cur?.getBoundingClientRect();
      return { cellW, rowLeft, curX: b ? b.x : null, curText: cur?.textContent ?? null };
    });
    expect(g.curX, "a cursor span is painted").not.toBeNull();
    expect(g.curText).toBe("D"); // column 3 (0-indexed) of "ABCDEFGH"
    expect(Math.abs(g.curX! - g.rowLeft - 3 * g.cellW), "cursor at col 3").toBeLessThan(1.5);
  });

  test("painted pixels: colors, bold/dim/inverse/hidden ink, underline", async ({ page }) => {
    // Each glyph under test is its own run so it renders as its own span.
    const rows = [
      // Solid background colors (spaces -> whole cell is the bg).
      [run("  ", { b: RED }), run("  ", { b: GREEN }), run("  ", { b: BLUE }), run(" ".repeat(10))],
      [run("M"), run("M", { a: BOLD }), run(" ".repeat(10))], // normal vs bold
      [run("M"), run("M", { a: DIM }), run(" ".repeat(10))], // normal vs dim
      [run("M", { a: INVERSE }), run(" ".repeat(10))], // inverse over defaults
      [run("M", { a: HIDDEN }), run("M"), run(" ".repeat(10))], // hidden vs visible
      [run("M", { a: UNDERLINE }), run("M"), run(" ".repeat(10))], // underline vs plain
    ];
    await renderMsg(page, screenMsg(rows, [5, 10]));

    const rects = await page.evaluate(() => {
      const out = document.getElementById("out")!;
      const r = (row: number, col: number): Rect => {
        const b = (out.children[row]!.children[col] as HTMLElement).getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height };
      };
      return {
        red: r(0, 0),
        green: r(0, 1),
        blue: r(0, 2),
        normalBold: r(1, 0),
        bold: r(1, 1),
        normalDim: r(2, 0),
        dim: r(2, 1),
        inverse: r(3, 0),
        hidden: r(4, 0),
        visible: r(4, 1),
        underline: r(5, 0),
        plain: r(5, 1),
      };
    });
    const png = decodePng(await page.screenshot());

    // SPEC: a truecolor/palette bg cell paints the requested color.
    const red = centerColor(png, rects.red);
    expect(red.r, "red cell R").toBeGreaterThan(180);
    expect(red.g + red.b, "red cell has little G/B").toBeLessThan(140);
    const green = centerColor(png, rects.green);
    expect(green.g).toBeGreaterThan(180);
    expect(green.r + green.b).toBeLessThan(160);
    const blue = centerColor(png, rects.blue);
    expect(blue.b).toBeGreaterThan(180);
    expect(blue.r + blue.g).toBeLessThan(160);

    // SPEC: bold is heavier ink than normal (light text on dark -> more lit).
    expect(avgLuminance(png, rects.bold), "bold heavier than normal").toBeGreaterThan(
      avgLuminance(png, rects.normalBold),
    );
    // SPEC: dim is fainter than normal.
    expect(avgLuminance(png, rects.dim), "dim fainter than normal").toBeLessThan(
      avgLuminance(png, rects.normalDim),
    );
    // SPEC: inverse over defaults paints the foreground as a filled light block.
    expect(avgLuminance(png, rects.inverse), "inverse is a light block").toBeGreaterThan(
      avgLuminance(png, rects.normalBold) + 40,
    );
    // SPEC: hidden paints nothing — near background (dark), darker than visible.
    expect(avgLuminance(png, rects.hidden), "hidden is near-black").toBeLessThan(12);
    expect(avgLuminance(png, rects.visible)).toBeGreaterThan(avgLuminance(png, rects.hidden));
    // SPEC: underline adds ink in the bottom strip of the cell.
    const bottomStrip = (c: Rect): Rect => ({
      x: c.x,
      y: c.y + c.height * 0.8,
      width: c.width,
      height: c.height * 0.2,
    });
    expect(
      avgLuminance(png, bottomStrip(rects.underline)),
      "underline adds ink at the cell bottom",
    ).toBeGreaterThan(avgLuminance(png, bottomStrip(rects.plain)) + 5);
  });

  test("visual clear: text paints ink, and a blank re-render leaves no ink", async ({ page }) => {
    // Write a row of text and confirm the glyph region paints ink. Measure the
    // tight text span (not the full-width row, which would dilute the mean).
    await renderMsg(page, screenMsg([[run("HELLO")], [run(" ".repeat(8))]], [1, 0]));
    const rowRect = await page.evaluate(() => {
      const span = document.getElementById("out")!.children[0]!.children[0] as HTMLElement;
      const b = span.getBoundingClientRect();
      return { x: b.x, y: b.y, width: b.width, height: b.height };
    });
    const withText = avgLuminance(decodePng(await page.screenshot()), rowRect);
    expect(withText, "text glyphs paint visible ink").toBeGreaterThan(15);

    // Re-render the same row blank (what a clear/erase produces) and confirm the
    // painted region goes dark — the renderer visually clears it.
    await page.evaluate(() => {
      WTE.render.handleScreen({
        type: "screen",
        base: 0,
        rows: [
          [{ t: "        ", f: -1, b: -1, a: 0, uc: -1 }],
          [{ t: "        ", f: -1, b: -1, a: 0, uc: -1 }],
        ],
        cursor: [1, 0],
        changed: [0, 1],
        cursorHidden: true,
        cursorStyle: 0,
        cursorBlink: false,
      });
    });
    await page.waitForTimeout(200);
    const afterClear = avgLuminance(decodePng(await page.screenshot()), rowRect);
    expect(afterClear, "cleared row paints no ink").toBeLessThan(2);
  });
});

// WTE is the esbuild IIFE global injected via addScriptTag.
declare const WTE: {
  render: {
    init: (opts: { output: unknown; termWrap: unknown; onCursorMove?: () => void }) => void;
    updateFontMetrics: () => void;
    handleScreen: (msg: unknown) => void;
  };
};
