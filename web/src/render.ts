// Render layer: single-container model.
//
// One flat list of term-row divs inside #term-output. The last N rows
// are the "live zone" (overwritten by screen frames). Everything above
// is frozen history — either from scroll messages (lines that fell off
// the server's VT screen) or from snapshots of live content before it
// was overwritten. Users scroll up through this history naturally.
//
// Cap: MAX_HISTORY rows of frozen history. Oldest get evicted.

import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";
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

const MAX_HISTORY = 1000;
let allRows: HTMLDivElement[] = [];
let liveCount = 0;
let cursorRow = 0;
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
let firstScreen = true;
let onCursorMove: (() => void) | null = null;

export function init(opts: {
  output: HTMLElement;
  termWrap: HTMLElement;
  onCursorMove?: () => void;
}): void {
  output = opts.output;
  termWrap = opts.termWrap;
  onCursorMove = opts.onCursorMove ?? null;
  // A fresh output element is empty, so the next frame must be a full repaint
  // (firstScreen rebuilds allRows/liveCount and wipes the DOM); and any flush
  // pending from a previous render context targets the old element. Resetting
  // both is correct on re-init and also prevents render-module state leaking
  // across test files under vitest isolate:false.
  firstScreen = true;
  if (pendingFrame !== undefined) {
    cancelAnimationFrame(pendingFrame);
    pendingFrame = undefined;
  }
  startCursorBlink();
}

export function resetScreen(): void {
  firstScreen = true;
}

export function resetScrollback(): void {
  const historyCount = allRows.length - liveCount;
  for (let i = 0; i < historyCount; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- index within bounds
    allRows[i]!.remove();
  }
  allRows = allRows.slice(historyCount);
}

/** Number of frozen history (scrollback) rows currently in the DOM —
 *  excludes the live screen zone. Sent on the resume control message
 *  so the server replays only the rows the client doesn't have. iOS
 *  Safari can preserve sessionStorage (and therefore the sessionId)
 *  while evicting the page entirely; on reload the scrollback count
 *  is zero and we want a full replay. A WS drop within the same page
 *  lifetime keeps the count and avoids duplicate scrollback rows. */
export function getScrollbackRowCount(): number {
  return allRows.length - liveCount;
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
    if (run.u) {
      const a = document.createElement("a");
      a.href = run.u;
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

// --- Live zone management ---
function ensureLiveZone(count: number): void {
  while (liveCount < count) {
    const div = document.createElement("div");
    div.className = "term-row";
    output.appendChild(div);
    allRows.push(div);
    liveCount++;
  }
  while (liveCount > count) {
    const el = allRows.pop();
    if (el) {
      el.remove();
    }
    liveCount--;
  }
}

function trimHistory(): void {
  const historyCount = allRows.length - liveCount;
  if (historyCount > MAX_HISTORY) {
    const excess = historyCount - MAX_HISTORY;
    for (let i = 0; i < excess; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- index within bounds
      allRows[i]!.remove();
    }
    allRows = allRows.slice(excess);
  }
}

// --- Screen frame handling ---
let pendingRows: WireRun[][] | null = null;
let pendingCursor: [number, number] | null = null;
let pendingChanged = new Set<number>();
let pendingCursorHidden = false;
let pendingCursorStyle = 0;
let pendingCursorBlink = true;
let pendingBell = false;
let pendingFrame: number | undefined;

export function handleScreen(msg: ScreenMessage): void {
  // Merge row data: if a previous frame's rows haven't been flushed yet,
  // overlay the new frame's changed rows onto the existing pending data
  // so rows from the earlier frame aren't lost when their indices aren't
  // in the newer frame's changed set.
  if (pendingRows !== null && pendingRows.length === msg.rows.length) {
    for (const idx of msg.changed) {
      const row = msg.rows[idx];
      if (row !== undefined) {
        pendingRows[idx] = row;
      }
    }
  } else {
    pendingRows = msg.rows;
  }
  pendingCursor = msg.cursor;
  pendingCursorHidden = msg.cursorHidden ?? false;
  pendingCursorStyle = msg.cursorStyle ?? 0;
  pendingCursorBlink = msg.cursorBlink ?? true;
  if (msg.bell) {
    pendingBell = true;
  }
  for (const idx of msg.changed) {
    pendingChanged.add(idx);
  }
  scheduleFlush();
}

function scheduleFlush(): void {
  if (pendingFrame !== undefined) {
    return;
  }
  pendingFrame = requestAnimationFrame(flushAll);
}

function flushAll(): void {
  pendingFrame = undefined;

  // Process scroll lines FIRST (insert above live zone) so the live
  // zone indices remain stable for the screen update that follows.
  // Doing both in one rAF eliminates the visual duplication that
  // occurred when scroll insertions and screen rewrites painted in
  // separate frames.
  // Skip if firstScreen is still true — the first screen frame will
  // wipe the DOM via innerHTML="", so any scroll lines inserted now
  // would be lost. They'll arrive again via scrollback replay.
  if (pendingScrollback.length > 0 && !firstScreen) {
    const batch = pendingScrollback.splice(0);
    const liveStart = allRows.length - liveCount;
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- liveStart < length guarantees element exists
    const refNode = liveStart < allRows.length ? allRows[liveStart]! : null;

    // Scroll anchoring for a scrolled-up reader: pin the row at the
    // viewport top to its on-screen position across the trim + insert
    // below, so history added below the viewport (the reconnect /
    // multi-device replay) doesn't move what they're reading. The
    // formula self-corrects whether the anchor is frozen history (which
    // doesn't move) or a live row pushed down by the insert. If the
    // anchor row is trimmed away entirely, their place is gone — fall
    // back to the bottom. When following (not scrolled up), the
    // stickToBottomIfFollowing() at the end of flushAll handles it.
    const anchor = scroll.isUserScrolledUp() ? rowAtViewportTop() : null;
    const anchorOffset = anchor ? anchor.offsetTop - termWrap.scrollTop : 0;

    // Trim history BEFORE inserting to avoid double-counting heights;
    // the anchor restore below absorbs any shift the trim causes.
    trimHistory();

    for (const line of batch) {
      const div = document.createElement("div");
      div.className = "term-row";
      div.replaceChildren(...buildRowSpans(line, -1));
      output.insertBefore(div, refNode);
      allRows.splice(allRows.length - liveCount, 0, div);
    }

    if (anchor) {
      if (anchor.isConnected) {
        termWrap.scrollTop = anchor.offsetTop - anchorOffset;
      } else {
        scroll.scrollToBottom();
      }
    }
  }

  if (pendingRows !== null && pendingCursor !== null) {
    const rows = pendingRows;
    const cursor = pendingCursor;
    const changed = pendingChanged;
    const hidden = pendingCursorHidden;
    const style = pendingCursorStyle;
    const blink = pendingCursorBlink;
    const bell = pendingBell;
    pendingRows = null;
    pendingCursor = null;
    pendingChanged = new Set();
    pendingBell = false;

    try {
      flushScreenInner(rows, cursor, changed, hidden, style, bell, blink);
    } catch (err) {
      console.error("vterm: render error", err);
    }
  }

  // Single auto-follow invariant. After any DOM change this frame
  // (scrollback insertion and/or a screen repaint), re-pin to the
  // bottom if the user is following. A scrollback-only flush — the
  // history replay on reload/reconnect, which is large when another
  // device was active in the background — previously left the viewport
  // parked in replayed history because only screen frames re-pinned.
  stickToBottomIfFollowing();
}

function flushScreenInner(
  rows: WireRun[][],
  cursor: [number, number],
  changed: Set<number>,
  msg_cursorHidden: boolean,
  msg_cursorStyle: number,
  bell: boolean,
  msg_cursorBlink: boolean,
): void {
  if (firstScreen) {
    output.innerHTML = "";
    allRows = [];
    liveCount = 0;
    firstScreen = false;
  }
  // Trim trailing empty rows from the DOM live zone. The screen buffer
  // is always pty-height tall, but the host application's TUI typically draws
  // content + a bottom status row and leaves the rows in between (and
  // any rows below content when content reflowed shorter after a
  // resize) as default empty cells. Those rows render as a visible
  // "black gap" between content and the bottom-of-viewport status —
  // most reliably reproduced by switching device (iPhone → iPad), where
  // the host's SIGWINCH-driven repaint may not touch every row of the new
  // larger screen until the user sends fresh input.
  //
  // Visible row count = max(cursor row + 1, last non-empty row + 1).
  // Always include the cursor row so we never trim it. New content
  // arriving for a previously-trimmed row index just grows the live
  // zone again on the next frame.
  let lastNonEmpty = -1;
  for (let i = rows.length - 1; i >= 0; i--) {
    const row = rows[i];
    if (row !== undefined && row.length > 0 && row.some((r) => r.t !== "" && r.t.trim() !== "")) {
      lastNonEmpty = i;
      break;
    }
  }
  const visibleEnd = Math.max(cursor[0] + 1, lastNonEmpty + 1, 1);
  ensureLiveZone(visibleEnd);

  const newCursorRow = cursor[0];
  const newCursorCol = cursor[1];

  if (newCursorRow !== cursorRow) {
    changed.add(cursorRow);
    changed.add(newCursorRow);
  } else if (newCursorCol !== cursorCol) {
    changed.add(newCursorRow);
  }

  cursorRow = newCursorRow;
  cursorCol = newCursorCol;
  cursorHidden = msg_cursorHidden;
  cursorStyleVal = msg_cursorStyle;
  setCursorBlink(msg_cursorBlink);

  const liveStart = allRows.length - liveCount;
  for (const idx of changed) {
    const row = rows[idx];
    const el = allRows[liveStart + idx];
    if (row !== undefined && el) {
      const cursorAt = !cursorHidden && idx === cursorRow ? cursorCol : -1;
      const newSpans = buildRowSpans(row, cursorAt);
      el.replaceChildren(...newSpans);
    }
  }
  if (onCursorMove) {
    onCursorMove();
  }

  // Collapse the browser's internal caret to the end of the
  // contenteditable so typing doesn't scroll to the top — but only
  // when the user has no active text selection (preserves copy ability
  // during spinner/progress updates) AND the user hasn't scrolled up
  // (collapseToEnd triggers browser scroll-into-view on the caret,
  // which yanks the viewport to the bottom).
  const sel = window.getSelection();
  if (sel && document.activeElement === output && sel.isCollapsed && !scroll.isUserScrolledUp()) {
    sel.selectAllChildren(output);
    sel.collapseToEnd();
  }

  if (bell) {
    output.classList.add("term-bell");
    setTimeout(() => {
      output.classList.remove("term-bell");
    }, 150);
  }
}

/** Pin the viewport to the bottom iff the user is "following" — not
 *  scrolled up and not mid-gesture. The single source of truth for
 *  auto-follow; called once per frame at the end of flushAll so it
 *  covers both screen repaints and scrollback-only flushes. */
function stickToBottomIfFollowing(): void {
  if (!scroll.isUserScrolledUp() && !scroll.isInUserScroll()) {
    scroll.scrollToBottom();
  }
}

/** The row currently at the top of the viewport, used as a scroll
 *  anchor so rows inserted (scrollback) or trimmed elsewhere don't move
 *  what a scrolled-up user is reading. Null when there are no rows. */
function rowAtViewportTop(): HTMLDivElement | null {
  const top = termWrap.scrollTop;
  for (const el of allRows) {
    if (el.offsetTop + el.offsetHeight > top) {
      return el;
    }
  }
  return null;
}

// --- Scroll message handling (lines that fell off the server's screen) ---
// These get inserted as frozen history above the live zone.
const pendingScrollback: WireRun[][] = [];

export function handleScroll(msg: ScrollMessage): void {
  if (msg.lines.length === 0) {
    return;
  }
  pendingScrollback.push(...msg.lines);
  scheduleFlush();
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

/** Called from flushScreenInner when cursorBlink state changes. */
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

export function getCursorPx(): { left: number; top: number; cellH: number } {
  const cs = window.getComputedStyle(termWrap);
  const padL = parseFloat(cs.paddingLeft);
  const padT = parseFloat(cs.paddingTop);
  // Cursor position relative to the output container's top, offset by
  // the history height above the live zone.
  const liveStart = allRows.length - liveCount;
  const historyHeight = liveStart > 0 ? (allRows[liveStart]?.offsetTop ?? 0) : 0;
  return {
    left: Math.round(padL + cursorCol * cellWidth),
    top: Math.round(padT + historyHeight + cursorRow * cellHeight),
    cellH: cellHeight,
  };
}

export function setPredictedCursor(row: number, col: number, active: boolean): void {
  const el = predCursorEl ?? (predCursorEl = document.getElementById("pred-cursor"));
  if (!el) {
    return;
  }
  if (!active || (row === cursorRow && col === cursorCol)) {
    el.classList.remove("visible");
    return;
  }
  const cs = window.getComputedStyle(termWrap);
  const padL = parseFloat(cs.paddingLeft);
  const padT = parseFloat(cs.paddingTop);
  const liveStart = allRows.length - liveCount;
  const historyHeight = liveStart > 0 ? (allRows[liveStart]?.offsetTop ?? 0) : 0;
  el.style.left = `${Math.round(padL + col * cellWidth)}px`;
  el.style.top = `${Math.round(padT + historyHeight + row * cellHeight)}px`;
  el.style.width = `${cellWidth}px`;
  el.style.height = `${cellHeight}px`;
  el.classList.add("visible");
}

let predCursorEl: HTMLElement | null = null;

/** Apply or remove the reverse-video class on the terminal output.
 *  When DECSCNM (mode 5) is active, default fg/bg are swapped via CSS. */
export function updateReverseVideo(): void {
  if (isReverseVideo()) {
    termWrap.classList.add("term-reverse-video");
  } else {
    termWrap.classList.remove("term-reverse-video");
  }
}
