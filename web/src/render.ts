// Render layer: store-backed, absolute-index DOM rows.
//
// The renderer owns a LineStore (the authoritative client model) and reflects
// it to the DOM. Every terminal line is a `div.term-row` carrying a `data-abs`
// attribute equal to its absolute index; the rows sit in one natively-scrolled
// container in absolute order. There is no separate "live zone" and
// "scrollback": the live window is simply the last `height` absolute indices,
// and a line that scrolls into history just stops being updated. This is what
// removes the live/history reconciliation that caused the duplicate-rows and
// view-jumping bugs (see docs/REBUILD.md sections 2 and 6).
//
// Decode frames feed the store (handleScreen/handleScroll); a single
// requestAnimationFrame flush drains the store's change set and applies it:
// evicted indices drop their row, dirty indices build/update their row in
// place. The window block always has `height` rows, so scrollHeight only grows
// when real history commits — never oscillating mid-redraw.

import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";
import { LineStore } from "./store.js";
import * as scroll from "./scroll.js";
import { isReverseVideo } from "./modes.js";

// --- Width cache (two-tier, xterm.js style) ---
const WIDTH_FLAT_SIZE = 256;
const WIDTH_FLAT_UNSET = -9999;
const widthFlat = new Float32Array(WIDTH_FLAT_SIZE).fill(WIDTH_FLAT_UNSET);
const widthMap = new Map<string, number>();

const VARIANT_REGULAR = 0;
const VARIANT_BOLD = 1;
const VARIANT_ITALIC = 2;
const variantCtx: (CanvasRenderingContext2D | null)[] = [null, null, null, null];
let fontString = "";

function variantContext(variant: number): CanvasRenderingContext2D {
  let ctx = variantCtx[variant];
  if (ctx) {
    return ctx;
  }
  const canvas = document.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- 2d context always available on fresh canvas
  ctx = canvas.getContext("2d")!;
  let f = "";
  if (variant & VARIANT_ITALIC) {
    f += "italic ";
  }
  if (variant & VARIANT_BOLD) {
    f += "bold ";
  }
  f += fontString;
  ctx.font = f;
  variantCtx[variant] = ctx;
  return ctx;
}

function resetVariantContexts(): void {
  for (let i = 0; i < variantCtx.length; i++) {
    variantCtx[i] = null;
  }
}

function measureChar(ch: string, bold: boolean, italic: boolean): number {
  if (!bold && !italic && ch.length === 1) {
    const cp = ch.charCodeAt(0);
    if (cp < WIDTH_FLAT_SIZE) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- bounds checked above
      const cached = widthFlat[cp]!;
      if (cached !== WIDTH_FLAT_UNSET) {
        return cached;
      }
      const w = variantContext(VARIANT_REGULAR).measureText(ch).width;
      widthFlat[cp] = w;
      return w;
    }
  }
  const key = (bold ? "B" : "") + (italic ? "I" : "") + ch;
  const cached = widthMap.get(key);
  if (cached !== undefined) {
    return cached;
  }
  let variant = 0;
  if (bold) {
    variant |= VARIANT_BOLD;
  }
  if (italic) {
    variant |= VARIANT_ITALIC;
  }
  const w = variantContext(variant).measureText(ch).width;
  widthMap.set(key, w);
  return w;
}

function measureCellWidth(): number {
  // Measure using a span appended to termWrap (which already has the
  // font applied via CSS). This ensures the web font is used if loaded.
  const span = document.createElement("span");
  span.style.visibility = "hidden";
  span.style.position = "absolute";
  span.style.whiteSpace = "pre";
  span.textContent = "MMMMMMMMMM";
  termWrap.appendChild(span);
  const width = span.getBoundingClientRect().width / 10;
  termWrap.removeChild(span);
  return width;
}

// --- State ---
let output: HTMLElement;
let termWrap: HTMLElement;

const store = new LineStore();
// abs index -> its row element. The DOM children of `output` are these
// elements, kept in ascending data-abs order.
const rowEls = new Map<number, HTMLDivElement>();

// The "earlier output trimmed" marker (a non-data-abs first child of output,
// shown when the store reports history older than it holds was trimmed). Kept
// as a module ref so it is reused rather than recreated each flush.
let trimMarkerEl: HTMLDivElement | null = null;

// Rows awaiting a DOM (re)build, processed in budgeted batches across frames.
// A session restore (kiro-cli's /chat) or a `cat bigfile` dumps thousands of
// lines in one wire frame; building them all in a single rAF janks or, on a
// constrained device, hangs the tab. The store still ingests the whole burst
// at once (it is cheap, pure data); the renderer drains this queue at most
// MAX_ROWS_PER_FRAME per frame and reschedules until it is empty, so each
// frame stays short and the terminal fills smoothly. The cursor row is always
// built regardless of the budget so the caret never lags.
const renderQueue = new Set<number>();
const MAX_ROWS_PER_FRAME = 300;

// Cursor state, refreshed from the store window on each flush. Kept as module
// vars because buildRowSpans/cursorClassName read them.
let cursorAbs = -1; // absolute index of the row the cursor is on
let cursorCol = 0;
let cursorHidden = false;
let cursorStyleVal = 0; // 0-6: DECSCUSR

function cursorClassName(): string {
  // DECSCUSR: 0/1=blinking block, 2=steady block, 3=blinking underline,
  // 4=steady underline, 5=blinking bar, 6=steady bar
  if (cursorStyleVal === 3 || cursorStyleVal === 4) {
    return "term-cursor-underline";
  }
  if (cursorStyleVal === 5 || cursorStyleVal === 6) {
    return "term-cursor-bar";
  }
  return "term-cursor";
}
let cellWidth = 8;
let cellHeight = 17;
let defaultSpacing = 0;
let onCursorMove: (() => void) | null = null;
let pendingFrame: number | undefined;

/**
 * Initialize the renderer by attaching it to a pair of DOM elements: the
 * scrollable terminal wrapper and the inner output container that receives
 * row elements. Must be called once before any handleScreen/handleScroll call.
 *
 * @param opts.output      Inner element that holds row children.
 * @param opts.termWrap    Outer scroll container.
 * @param opts.onCursorMove Optional callback invoked when the cursor moves.
 */
export function init(opts: {
  output: HTMLElement;
  termWrap: HTMLElement;
  onCursorMove?: () => void;
}): void {
  output = opts.output;
  termWrap = opts.termWrap;
  onCursorMove = opts.onCursorMove ?? null;
  // Fresh attach: drop any prior model + DOM so re-init (and vitest's
  // non-isolated module reuse) starts clean.
  store.reset();
  rowEls.clear();
  renderQueue.clear();
  trimMarkerEl = null;
  output.replaceChildren();
  cursorAbs = -1;
  if (pendingFrame !== undefined) {
    cancelAnimationFrame(pendingFrame);
    pendingFrame = undefined;
  }
  startCursorBlink();
}

/**
 * Reset internal screen state so the next frame performs a full repaint.
 * With the store model this is a full reset (used on server restart): the
 * store clears and the next flush wipes and rebuilds the DOM.
 */
export function resetScreen(): void {
  store.reset();
  scheduleFlush();
}

/**
 * Clear all rows (history + window). Used on server restart alongside
 * resetScreen; both reset the store, so this is equivalent.
 */
export function resetScrollback(): void {
  store.reset();
  scheduleFlush();
}

/**
 * Highest absolute line index the client holds, or -1 if empty. This is the
 * resume `haveThrough` value (it replaces the old DOM-row count). Exposed so
 * the connection layer can request only the lines the client is missing.
 */
export function getHighestIndex(): number {
  return store.highestIndex();
}

/**
 * Record the server's retained-history bounds from a resumeAck (committed =
 * one past the newest retained, oldest = oldest retained absolute index). The
 * store uses these to tell a genuine history trim (the server evicted lines
 * the client was missing) from a still-loading state, which drives the
 * "earlier output trimmed" marker. Resync guard 8.2.2.
 */
export function noteResumeBounds(committed: number, oldest: number): void {
  store.noteResumeBounds(committed, oldest);
  scheduleFlush();
}

// --- Color helpers ---
function colorHex(c: number | undefined): string | null {
  if (c === undefined || c < 0) {
    return null;
  }
  return "#" + c.toString(16).padStart(6, "0");
}

// --- URL detection (xterm.js addon-web-links pattern) ---
const URL_RE = /(https?|HTTPS?):\/\/[^\s"'!*(){}|\\^<>`]*[^\s"':,.!?{}|\\^~[\]`()<>]/g;

function linkifySpans(
  spans: (HTMLSpanElement | HTMLAnchorElement)[],
): (HTMLSpanElement | HTMLAnchorElement)[] {
  const out: (HTMLSpanElement | HTMLAnchorElement)[] = [];
  for (const span of spans) {
    // Pass anchors through untouched. A span may already be an <a> from an
    // OSC 8 hyperlink emitted by the application (see buildRowSpans). The
    // app-provided href is authoritative and takes precedence over heuristic
    // autolinking — re-scanning it with URL_RE would rebuild the link from
    // the *visible* text, which for a URL that wraps across rows is only a
    // fragment. That truncates the href and defeats the terminal's
    // clickable-across-line-wraps behavior. Skip; only autolink plain text.
    if (span.tagName === "A") {
      out.push(span);
      continue;
    }
    const text = span.textContent;
    URL_RE.lastIndex = 0;
    let match: RegExpExecArray | null;
    let last = 0;
    let found = false;
    while ((match = URL_RE.exec(text)) !== null) {
      found = true;
      if (match.index > last) {
        const pre = span.cloneNode(false) as HTMLSpanElement;
        pre.textContent = text.slice(last, match.index);
        out.push(pre);
      }
      const a = document.createElement("a");
      a.href = match[0];
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      a.className = "term-link";
      a.textContent = match[0];
      // Copy inline styles from the source span
      a.style.cssText = span.style.cssText;
      out.push(a);
      last = match.index + match[0].length;
    }
    if (!found) {
      out.push(span);
    } else if (last < text.length) {
      const post = span.cloneNode(false) as HTMLSpanElement;
      post.textContent = text.slice(last);
      out.push(post);
    }
  }
  return out;
}

// --- Build row DOM ---
function buildRowSpans(runs: WireRun[], cursorAt: number): (HTMLSpanElement | HTMLAnchorElement)[] {
  const out: (HTMLSpanElement | HTMLAnchorElement)[] = [];
  let col = 0;
  for (const run of runs) {
    if (!run.t) {
      continue;
    }
    const runStartIdx = out.length;
    const attrs = run.a ?? 0;
    const isBold = (attrs & 1) !== 0;
    const isItalic = (attrs & 2) !== 0;
    const isUnderline = (attrs & 4) !== 0;
    const isInverse = (attrs & 8) !== 0;
    const isStrike = (attrs & 16) !== 0;
    const isDim = (attrs & 32) !== 0;
    const isHidden = (attrs & 64) !== 0;
    const isBlink = (attrs & 128) !== 0;
    const isOverline = (attrs & 256) !== 0;
    const isDoubleUnderline = (attrs & 512) !== 0;

    // Server swaps FG/BG for inverse in wire.go, but when both are
    // default (-1) the swap is a no-op. Detect inverse + defaults and
    // apply theme-inverted colors so the inverted space is visible.
    let fg = colorHex(run.f);
    let bg = colorHex(run.b);
    if (isInverse && fg === null && bg === null) {
      fg = "var(--bg)";
      bg = "var(--text)";
    }
    const ucColor = colorHex(run.uc);

    const applyStyle = (span: HTMLSpanElement, spacing: number): void => {
      if (isHidden) {
        span.style.visibility = "hidden";
      }
      if (fg !== null) {
        span.style.color = fg;
      }
      if (bg !== null) {
        span.style.background = bg;
      }
      if (isBold) {
        span.style.fontWeight = "bold";
      }
      if (isItalic) {
        span.style.fontStyle = "italic";
      }
      if (isDim) {
        span.style.opacity = ".5";
      }
      // Build text-decoration combining all line types.
      const decoLines: string[] = [];
      if (isDoubleUnderline) {
        decoLines.push("underline");
      } else if (isUnderline) {
        decoLines.push("underline");
      }
      if (isOverline) {
        decoLines.push("overline");
      }
      if (isStrike) {
        decoLines.push("line-through");
      }
      if (decoLines.length > 0) {
        let deco = decoLines.join(" ");
        if (isDoubleUnderline) {
          deco += " double";
        }
        span.style.textDecoration = deco;
      }
      if (ucColor !== null) {
        span.style.textDecorationColor = ucColor;
      }
      if (spacing !== defaultSpacing) {
        span.style.letterSpacing = `${spacing}px`;
      }
      if (isBlink) {
        span.classList.add("term-blink");
      }
    };

    let prevSpacing: number | null = null;
    let buffer = "";
    const flush = (): void => {
      if (buffer.length === 0) {
        return;
      }
      const span = document.createElement("span");
      span.textContent = buffer;
      applyStyle(span, prevSpacing ?? 0);
      out.push(span);
      buffer = "";
    };
    for (const ch of run.t) {
      if (ch === "\uFFFF") {
        // Wide-char continuation placeholder: mark previous span as double-width.
        // Flush any buffered text first so the wide char is in its own span.
        flush();
        if (out.length > 0) {
          // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- length checked above
          const prev = out[out.length - 1]!;

          const prevText = prev.textContent;
          if (prevText.length > 0) {
            // eslint-disable-next-line @typescript-eslint/no-non-null-assertion, @typescript-eslint/no-misused-spread -- terminal text is ASCII/CJK, safe to spread; .at(-1) guaranteed by length check
            const lastChar = [...prevText].at(-1)!;
            const w = measureChar(lastChar, isBold, isItalic);
            prev.style.letterSpacing = `${cellWidth * 2 - w}px`;
          }
        }
        continue;
      }
      if (col === cursorAt) {
        flush();
        const w = measureChar(ch, isBold, isItalic);
        const spacing = cellWidth - w;
        const span = document.createElement("span");
        span.className = cursorClassName();
        span.textContent = ch;
        if (spacing !== defaultSpacing) {
          span.style.letterSpacing = `${spacing}px`;
        }
        out.push(span);
        col++;
        continue;
      }
      const w = measureChar(ch, isBold, isItalic);
      const spacing = cellWidth - w;
      if (prevSpacing === null) {
        prevSpacing = spacing;
      } else if (spacing !== prevSpacing) {
        flush();
        prevSpacing = spacing;
      }
      buffer += ch;
      col++;
    }
    flush();
    // Wrap spans from this run in an <a> if it has a hyperlink URL.
    const href = run.u && /^https?:\/\//i.test(run.u) ? run.u : null;
    if (href) {
      const a = document.createElement("a");
      a.href = href;
      a.target = "_blank";
      a.rel = "noopener";
      a.className = "term-link";
      // Move spans from this run into the anchor.
      const runSpans = out.splice(runStartIdx);
      for (const s of runSpans) {
        a.appendChild(s);
      }
      out.push(a);
    }
  }
  if (cursorAt >= 0 && col <= cursorAt) {
    while (col < cursorAt) {
      const span = document.createElement("span");
      span.textContent = " ";
      out.push(span);
      col++;
    }
    const cursor = document.createElement("span");
    cursor.className = cursorClassName();
    cursor.textContent = " ";
    out.push(cursor);
  }
  if (out.length === 0) {
    const span = document.createElement("span");
    span.innerHTML = "&nbsp;";
    out.push(span);
  }
  return linkifySpans(out);
}

// --- Frame handling: feed the store, then flush to DOM ---

/**
 * Apply a `ScreenMessage`: update the store's window + changed rows, then
 * schedule a render flush. The store handles merging changed rows by absolute
 * index, so no client-side frame coalescing is needed.
 */
export function handleScreen(msg: ScreenMessage): void {
  store.applyScreen(msg);
  scheduleFlush();
}

/**
 * Apply a `ScrollMessage`: commit history lines into the store by absolute
 * index, then schedule a render flush.
 */
export function handleScroll(msg: ScrollMessage): void {
  store.applyScroll(msg);
  scheduleFlush();
}

function scheduleFlush(): void {
  if (pendingFrame !== undefined) {
    return;
  }
  pendingFrame = requestAnimationFrame(flushRender);
}

function flushRender(): void {
  pendingFrame = undefined;
  try {
    flushRenderInner();
  } catch (err) {
    console.error("vterm: render error", err);
  }
  // Single auto-follow invariant, applied after every DOM mutation.
  stickToBottomIfFollowing();
}

function flushRenderInner(): void {
  const ch = store.drainChanges();

  if (ch.fullReset) {
    output.replaceChildren();
    rowEls.clear();
    renderQueue.clear();
    trimMarkerEl = null;
    cursorAbs = -1;
  } else {
    for (const abs of ch.evictedLines) {
      const el = rowEls.get(abs);
      if (el) {
        el.remove();
      }
      rowEls.delete(abs);
      renderQueue.delete(abs);
    }
  }

  // Refresh cursor state from the window.
  const win = store.getWindow();
  const newCursorAbs = win.base + win.cursorRow;
  const prevCursorAbs = cursorAbs;
  cursorAbs = newCursorAbs;
  cursorCol = win.cursorCol;
  cursorHidden = win.cursorHidden;
  cursorStyleVal = win.cursorStyle;
  setCursorBlink(win.cursorBlink);

  // Alt screen: render the ephemeral grid instead of the absolute buffer.
  if (store.isAlt()) {
    renderAlt(store.getAltRows());
    if (onCursorMove) {
      onCursorMove();
    }
    return;
  }
  if (altRendered) {
    // Just exited alt: drop the ephemeral rows and rebuild from the store.
    altRendered = false;
    output.replaceChildren();
    rowEls.clear();
    renderQueue.clear();
    trimMarkerEl = null;
    store.forEachLine((abs) => renderQueue.add(abs));
  }

  // Queue this frame's changed rows for building.
  for (const abs of ch.dirtyLines) {
    renderQueue.add(abs);
  }

  // The cursor rows are built every frame regardless of the budget so the
  // inline cursor span is always current (it moves off the old row and onto
  // the new one); a huge backlog must never make the caret lag.
  if (prevCursorAbs !== newCursorAbs && prevCursorAbs >= 0) {
    upsertRow(prevCursorAbs);
    renderQueue.delete(prevCursorAbs);
  }
  upsertRow(newCursorAbs);
  renderQueue.delete(newCursorAbs);

  // Drain up to MAX_ROWS_PER_FRAME queued rows this frame; the rest carry over
  // to the next frame (scheduled below) so one big burst never blocks paint.
  let built = 0;
  for (const abs of renderQueue) {
    if (built >= MAX_ROWS_PER_FRAME) {
      break;
    }
    upsertRow(abs);
    renderQueue.delete(abs);
    built++;
  }

  updateTrimMarker();

  // More rows pending: keep draining on subsequent frames.
  if (renderQueue.size > 0) {
    scheduleFlush();
  }

  if (onCursorMove) {
    onCursorMove();
  }
}

// updateTrimMarker shows or hides the "earlier output trimmed" marker as the
// first child of output, driven by the store. It appears when history older
// than the store holds was trimmed — either the client evicted its oldest
// lines at the cap, or the server reported (via resumeAck bounds) that it no
// longer retains history the client was missing on resume. The marker carries
// no data-abs, so insertRowInOrder (which compares numeric data-abs) never
// places a row before it; it stays pinned at the top.
function updateTrimMarker(): void {
  if (store.hasTrimmedHistory()) {
    if (trimMarkerEl === null) {
      trimMarkerEl = document.createElement("div");
      trimMarkerEl.className = "term-trim-marker";
      trimMarkerEl.setAttribute("role", "status");
      trimMarkerEl.setAttribute("aria-label", "earlier output trimmed");
      trimMarkerEl.textContent = "earlier output trimmed";
    }
    if (trimMarkerEl.parentElement !== output || output.firstChild !== trimMarkerEl) {
      output.insertBefore(trimMarkerEl, output.firstChild);
    }
  } else if (trimMarkerEl !== null && trimMarkerEl.parentElement === output) {
    trimMarkerEl.remove();
  }
}

// upsertRow builds or updates the DOM row for an absolute index, or removes it
// if the store no longer holds it. New rows are inserted in ascending data-abs
// order.
function upsertRow(abs: number): void {
  const runs = store.getLine(abs);
  if (runs === undefined) {
    const stale = rowEls.get(abs);
    if (stale) {
      stale.remove();
      rowEls.delete(abs);
    }
    return;
  }
  const cursorAt = !cursorHidden && abs === cursorAbs ? cursorCol : -1;
  const spans = buildRowSpans(runs, cursorAt);
  let el = rowEls.get(abs);
  if (el === undefined) {
    el = document.createElement("div");
    el.className = "term-row";
    el.dataset["abs"] = String(abs);
    el.replaceChildren(...spans);
    insertRowInOrder(el, abs);
    rowEls.set(abs, el);
  } else {
    el.replaceChildren(...spans);
  }
}

// insertRowInOrder places a freshly-created row element among output's
// children so they stay in ascending data-abs order. The common case (a new
// highest index) is an O(1) append; out-of-order inserts scan for the slot.
function insertRowInOrder(el: HTMLDivElement, abs: number): void {
  const last = output.lastElementChild as HTMLElement | null;
  if (last === null || rowAbs(last) < abs) {
    output.appendChild(el);
    return;
  }
  for (const child of output.children) {
    if (rowAbs(child as HTMLElement) > abs) {
      output.insertBefore(el, child);
      return;
    }
  }
  output.appendChild(el);
}

function rowAbs(el: HTMLElement): number {
  const v = el.dataset["abs"];
  return v === undefined ? -1 : Number(v);
}

// --- Alt screen (ephemeral grid; no history) ---
let altRendered = false;

function renderAlt(rows: WireRun[][]): void {
  altRendered = true;
  rowEls.clear();
  const els: HTMLDivElement[] = [];
  for (let y = 0; y < rows.length; y++) {
    const div = document.createElement("div");
    div.className = "term-row";
    const cursorAt = !cursorHidden && y === cursorAbs - store.getWindow().base ? cursorCol : -1;
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- y < rows.length
    div.replaceChildren(...buildRowSpans(rows[y]!, cursorAt));
    els.push(div);
  }
  output.replaceChildren(...els);
}

/** Pin the viewport to the bottom iff the user is following. The scroll
 *  controller (scroll.ts) owns scrollTop and the follow/hold decision. */
function stickToBottomIfFollowing(): void {
  scroll.stickToBottom();
}

// --- Cursor blink ---
const CURSOR_BLINK_MS = 530;
let blinkInterval: ReturnType<typeof setInterval> | null = null;
let blinkEnabled = true;

function startCursorBlink(): void {
  if (blinkInterval !== null) {
    return;
  }
  output.classList.remove("cursor-blink-off");
  blinkInterval = setInterval(() => {
    output.classList.toggle("cursor-blink-off");
  }, CURSOR_BLINK_MS);
}

function stopCursorBlink(): void {
  if (blinkInterval !== null) {
    clearInterval(blinkInterval);
    blinkInterval = null;
  }
  output.classList.remove("cursor-blink-off");
}

/** Called from the flush when cursorBlink state changes. */
function setCursorBlink(enabled: boolean): void {
  if (enabled === blinkEnabled) {
    return;
  }
  blinkEnabled = enabled;
  if (enabled) {
    startCursorBlink();
  } else {
    stopCursorBlink();
  }
}

// --- Font metrics & sizing ---
/**
 * Re-measure the cell width/height from the rendered DOM. Call after any font
 * or zoom change so subsequent `computeSize()` and `getCursorPx()` use fresh
 * metrics.
 */
export function updateFontMetrics(): void {
  const cs = window.getComputedStyle(termWrap);
  const fontSize = cs.fontSize;
  const family = cs.fontFamily;
  fontString = `${fontSize} ${family}`;
  widthFlat.fill(WIDTH_FLAT_UNSET);
  widthMap.clear();
  resetVariantContexts();
  const measuredW = measureCellWidth();
  cellWidth = Math.round(measuredW);
  cellHeight = parseFloat(cs.lineHeight) || 17;
  defaultSpacing = cellWidth - measuredW;
  output.style.letterSpacing = `${defaultSpacing}px`;
  document.documentElement.style.setProperty("--char-w", `${cellWidth}px`);
}

const MIN_COLS = 20;
const MIN_ROWS = 5;

/**
 * Compute the integer (cols, rows) the terminal element can fit at current
 * font metrics, clamped to a minimum of 20×5. Used to decide what dimensions
 * to send to the server in a `resize` control message.
 */
export function computeSize(): { cols: number; rows: number } {
  const cs = window.getComputedStyle(termWrap);
  const padX = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
  const padY = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
  const contentW = termWrap.clientWidth - padX;
  const contentH = termWrap.clientHeight - padY;
  const cols = Math.max(MIN_COLS, Math.floor(contentW / cellWidth));
  const rows = Math.max(MIN_ROWS, Math.floor(contentH / cellHeight));
  return { cols, rows };
}

/**
 * Returns the cursor's pixel position relative to the output element, plus
 * the current cell height, for positioning custom overlays (predicted-cursor,
 * IME composition, etc.). Uses the cursor row's actual DOM offset.
 */
export function getCursorPx(): { left: number; top: number; cellH: number } {
  const cs = window.getComputedStyle(termWrap);
  const padL = parseFloat(cs.paddingLeft);
  const padT = parseFloat(cs.paddingTop);
  const el = rowEls.get(cursorAbs);
  const top = el ? el.offsetTop : padT;
  return {
    left: Math.round(padL + cursorCol * cellWidth),
    top: Math.round(top),
    cellH: cellHeight,
  };
}

let predCursorEl: HTMLElement | null = null;

/**
 * Show or hide a "predicted" cursor overlay at window-relative (row, col).
 * Useful for client-side echo of typed characters before the server
 * acknowledges them, over high-latency connections.
 */
export function setPredictedCursor(row: number, col: number, active: boolean): void {
  const el = predCursorEl ?? (predCursorEl = document.getElementById("pred-cursor"));
  if (!el) {
    return;
  }
  const win = store.getWindow();
  const predAbs = win.base + row;
  if (!active || (predAbs === cursorAbs && col === cursorCol)) {
    el.classList.remove("visible");
    return;
  }
  const cs = window.getComputedStyle(termWrap);
  const padL = parseFloat(cs.paddingLeft);
  const rowEl = rowEls.get(predAbs);
  const top = rowEl ? rowEl.offsetTop : parseFloat(cs.paddingTop) + row * cellHeight;
  el.style.left = `${Math.round(padL + col * cellWidth)}px`;
  el.style.top = `${Math.round(top)}px`;
  el.style.width = `${cellWidth}px`;
  el.style.height = `${cellHeight}px`;
  el.classList.add("visible");
}

/** Apply or remove the reverse-video class on the terminal output.
 *  When DECSCNM (mode 5) is active, default fg/bg are swapped via CSS. */
export function updateReverseVideo(): void {
  if (isReverseVideo()) {
    termWrap.classList.add("term-reverse-video");
  } else {
    termWrap.classList.remove("term-reverse-video");
  }
}
