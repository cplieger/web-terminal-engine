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
  pixelDiffFraction,
  type Rect,
} from "./e2e-harness.js";

// Attribute bits, per render.ts buildRowSpans: 1 bold, 2 italic, 4 underline,
// 8 inverse, 16 strike, 32 dim, 64 hidden.
const BOLD = 1;
const UNDERLINE = 4;
const INVERSE = 8;
const DIM = 32;
const HIDDEN = 64;
const BLINK = 128;
const RED = 0xff0000;
const GREEN = 0x00ff00;
const BLUE = 0x0000ff;

interface WireRun {
  t: string;
  f: number;
  b: number;
  a: number;
  uc: number;
  u?: string; // OSC 8 hyperlink URI
}
function run(t: string, opts: Partial<WireRun> = {}): WireRun {
  const r: WireRun = { t, f: opts.f ?? -1, b: opts.b ?? -1, a: opts.a ?? 0, uc: opts.uc ?? -1 };
  if (opts.u !== undefined) {
    r.u = opts.u;
  }
  return r;
}
function screenMsg(
  rows: WireRun[][],
  cursor: [number, number],
  cursorHidden = true,
  cursorStyle = 0,
): unknown {
  return {
    type: "screen",
    base: 0,
    rows,
    cursor,
    changed: rows.map((_, i) => i),
    cursorHidden,
    cursorStyle,
    cursorBlink: false,
  };
}
// altMsg builds an alternate-screen frame (ephemeral grid, no history).
function altMsg(rows: WireRun[][], cursor: [number, number]): unknown {
  return {
    type: "screen",
    base: 0,
    rows,
    cursor,
    changed: rows.map((_, i) => i),
    altActive: true,
    cursorHidden: true,
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

  test("glyph fidelity: distinct glyphs paint distinct, correctly-shaped ink", async ({ page }) => {
    // Each glyph in its own cell. Light text on dark bg, so more ink -> higher
    // luminance. This proves the renderer actually draws each codepoint as a
    // distinct, correctly-positioned shape (not the same blob for everything).
    const rows = [
      [
        run(" "), // 0: blank
        run("A"), // 1: letter
        run("B"), // 2: different letter
        run("\u2588"), // 3: FULL BLOCK — fills the cell
        run("\u2500"), // 4: HORIZONTAL line — mid row
        run("\u2502"), // 5: VERTICAL line — mid column
        run("."), // 6: period — sparse, low
        run(" ".repeat(6)),
      ],
      [run(" ".repeat(13))],
    ];
    await renderMsg(page, screenMsg(rows, [1, 0]));
    const c = await page.evaluate(() => {
      const out = document.getElementById("out")!;
      const r = (col: number): Rect => {
        const b = (out.children[0]!.children[col] as HTMLElement).getBoundingClientRect();
        return { x: b.x, y: b.y, width: b.width, height: b.height };
      };
      return {
        space: r(0),
        a: r(1),
        b: r(2),
        block: r(3),
        hline: r(4),
        vline: r(5),
        period: r(6),
      };
    });
    const png = decodePng(await page.screenshot());
    const ink = (rect: Rect): number => avgLuminance(png, rect);

    // A blank cell has essentially no ink; a full block is nearly saturated.
    expect(ink(c.space), "space paints ~nothing").toBeLessThan(3);
    expect(ink(c.block), "full block is far more ink than a letter").toBeGreaterThan(ink(c.a) + 40);
    // A letter has real ink, more than a sparse period, more than a space.
    expect(ink(c.a)).toBeGreaterThan(ink(c.period));
    expect(ink(c.period)).toBeGreaterThan(ink(c.space));
    // Distinct glyphs render distinctly: 'A' and 'B' differ across many pixels.
    expect(pixelDiffFraction(png, c.a, c.b), "A and B are visually distinct").toBeGreaterThan(0.05);

    // Box-drawing glyphs land in the right place: the horizontal line's ink is
    // concentrated in the middle ROW, the vertical line's in the middle COLUMN.
    const midRow = (r: Rect): Rect => ({
      x: r.x,
      y: r.y + r.height * 0.4,
      width: r.width,
      height: r.height * 0.2,
    });
    const topRow = (r: Rect): Rect => ({ x: r.x, y: r.y, width: r.width, height: r.height * 0.2 });
    const midCol = (r: Rect): Rect => ({
      x: r.x + r.width * 0.4,
      y: r.y,
      width: r.width * 0.2,
      height: r.height,
    });
    const leftCol = (r: Rect): Rect => ({ x: r.x, y: r.y, width: r.width * 0.2, height: r.height });
    expect(ink(midRow(c.hline)), "─ ink is mid-row").toBeGreaterThan(ink(topRow(c.hline)) + 8);
    expect(ink(midCol(c.vline)), "│ ink is mid-column").toBeGreaterThan(ink(leftCol(c.vline)) + 8);
  });

  test("blink (SGR 5): the engine's term-blink class drives an opacity animation", async ({
    page,
  }) => {
    // The engine ships no CSS — render.ts only ADDS the .term-blink class to a
    // blink cell (its class contract, also asserted in the all-codes dump). The
    // keyframes are the consumer's (web-terminal-ui). We inject the standard
    // blink CSS a consumer provides and verify end to end that the engine put the
    // class on the right element and that it actually animates opacity.
    await renderMsg(page, screenMsg([[run("M", { a: BLINK })], [run(" ".repeat(8))]], [1, 0]));
    await page.addStyleTag({
      content:
        "@keyframes wte-blink { 50% { opacity: 0 } } .term-blink { animation: wte-blink 1s step-end infinite }",
    });
    const g = await page.evaluate(() => {
      const el = document.querySelector<HTMLElement>(".term-blink");
      if (!el) {
        return { found: false, animName: "", op0: 1, op1: 1 };
      }
      const anims = el.getAnimations();
      const animName = getComputedStyle(el).animationName;
      let op0 = 1;
      let op1 = 1;
      if (anims.length > 0) {
        anims[0]!.currentTime = 0;
        op0 = parseFloat(getComputedStyle(el).opacity);
        anims[0]!.currentTime = 500; // mid-cycle of the 1s animation
        op1 = parseFloat(getComputedStyle(el).opacity);
      }
      return { found: true, animName, op0, op1 };
    });
    expect(g.found, "engine adds .term-blink to the blink cell").toBe(true);
    expect(g.animName, ".term-blink is animated").not.toBe("none");
    // Opacity differs across the animation cycle => the cell actually blinks.
    expect(Math.abs(g.op0 - g.op1), "opacity animates between phases").toBeGreaterThan(0.5);
  });

  test("reverse video (DECSCNM ?5): toggling the engine's class inverts the screen", async ({
    page,
  }) => {
    // render.ts toggles .term-reverse-video on the wrapper from the DEC mode-5
    // state (its contract, asserted in conformance.test.ts). The color inversion
    // itself is the consumer's CSS; inject the representative rule and verify the
    // toggle actually inverts the painted luminance of a text region.
    await renderMsg(page, screenMsg([[run("HELLO WORLD MMMM")], [run(" ".repeat(20))]], [1, 0]));
    await page.addStyleTag({ content: ".term-reverse-video { filter: invert(1) }" });
    const rect = await page.evaluate(() => {
      const b = (
        document.getElementById("out")!.children[0]!.children[0] as HTMLElement
      ).getBoundingClientRect();
      return { x: b.x, y: b.y, width: b.width, height: b.height };
    });
    const normal = avgLuminance(decodePng(await page.screenshot()), rect);
    // Turn DEC mode 5 (reverse video) on and let the engine apply its class.
    await page.evaluate(() => {
      WTE.modes.setModes(true, false, false, false, 0, false, true, false);
      WTE.render.updateReverseVideo();
    });
    const reversed = avgLuminance(decodePng(await page.screenshot()), rect);
    // Light-text-on-dark (low mean) must invert to dark-text-on-light (high mean).
    expect(normal, "normal text region is mostly dark").toBeLessThan(80);
    expect(reversed, "reverse-video inverts to a mostly-light region").toBeGreaterThan(
      normal + 100,
    );
  });

  test("alt screen (?1049): entering shows the alt grid, exiting restores the main buffer", async ({
    page,
  }) => {
    const readGrid = (): string[] => {
      const out = document.getElementById("out")!;
      return Array.from(out.children).map((r) =>
        (r.textContent ?? "").replace(/\u00a0/g, " ").replace(/[ ]+$/, ""),
      );
    };
    const mainRows = [[run("MAIN-A")], [run("MAIN-B")], [run(" ".repeat(8))], [run(" ".repeat(8))]];
    // Main buffer content.
    await renderMsg(page, screenMsg(mainRows, [3, 0]));
    const main = await page.evaluate(readGrid);
    expect(main.slice(0, 2), "main buffer shows main content").toEqual(["MAIN-A", "MAIN-B"]);

    // Enter the alternate screen: an ephemeral grid with different content that
    // must NOT disturb the retained main buffer.
    await page.evaluate(
      (m) => WTE.render.handleScreen(m),
      altMsg([[run("ALT-1")], [run("ALT-2")], [run(" ".repeat(8))], [run(" ".repeat(8))]], [0, 0]),
    );
    await page.waitForTimeout(150);
    const alt = await page.evaluate(readGrid);
    expect(alt.slice(0, 2), "alt screen shows the ephemeral alt grid").toEqual(["ALT-1", "ALT-2"]);

    // Exit the alternate screen: a normal frame restores the main buffer.
    await page.evaluate((m) => WTE.render.handleScreen(m), screenMsg(mainRows, [3, 0]));
    await page.waitForTimeout(150);
    const restored = await page.evaluate(readGrid);
    expect(restored.slice(0, 2), "exiting alt restores the main buffer").toEqual([
      "MAIN-A",
      "MAIN-B",
    ]);
  });

  test("cursor styles (DECSCUSR): block / underline / bar map to distinct cursor classes", async ({
    page,
  }) => {
    // A visible cursor at column 2 of "ABCDEFGH"; the DECSCUSR style value drives
    // the class render.ts puts on the cursor span (block 0-2, underline 3-4, bar
    // 5-6). The shape CSS is the consumer's; the CLASS is the engine's contract.
    const cases: readonly [number, string][] = [
      [2, "term-cursor"], // steady block
      [4, "term-cursor-underline"], // steady underline
      [6, "term-cursor-bar"], // steady bar
    ];
    for (const [style, cls] of cases) {
      await renderMsg(
        page,
        screenMsg([[run("ABCDEFGH")], [run(" ".repeat(8))]], [0, 2], false, style),
      );
      const info = await page.evaluate(() => {
        const cur = document.querySelector<HTMLElement>(
          ".term-cursor, .term-cursor-underline, .term-cursor-bar",
        );
        return { cls: cur?.className ?? null, text: cur?.textContent ?? null };
      });
      expect(info.cls, `DECSCUSR ${style} cursor class`).toBe(cls);
      expect(info.text, `DECSCUSR ${style} cursor sits on column 2 ("C")`).toBe("C");
    }
  });

  test("OSC 8 hyperlink: a run carrying a URI renders as a real anchor", async ({ page }) => {
    await renderMsg(
      page,
      screenMsg([[run("LINK", { u: "https://example.com/x" })], [run(" ".repeat(8))]], [1, 0]),
    );
    const a = await page.evaluate(() => {
      const anchor = document.getElementById("out")!.querySelector("a");
      return anchor
        ? {
            href: anchor.getAttribute("href"),
            text: anchor.textContent,
            target: anchor.getAttribute("target"),
          }
        : null;
    });
    expect(a, "an anchor element is rendered").not.toBeNull();
    expect(a?.href, "href is the OSC 8 URI").toBe("https://example.com/x");
    expect(a?.text, "the anchor wraps the run text").toBe("LINK");
    expect(a?.target, "the link opens in a new tab").toBe("_blank");
  });
});

// WTE is the esbuild IIFE global injected via addScriptTag.
declare const WTE: {
  render: {
    init: (opts: { output: unknown; termWrap: unknown; onCursorMove?: () => void }) => void;
    updateFontMetrics: () => void;
    handleScreen: (msg: unknown) => void;
    updateReverseVideo: () => void;
  };
  modes: {
    setModes: (
      bracketed: boolean,
      appCursor: boolean,
      mouseSGR: boolean,
      focus: boolean,
      mouseMode: number,
      appKeypad: boolean,
      reverse: boolean,
      pixels: boolean,
    ) => void;
  };
};
