// Render layer: store-backed, absolute-index DOM rows. Every terminal line is a
// `div.term-row` with a `data-abs` attribute equal to its absolute index, in one
// natively-scrolled container in absolute order; the live window is simply the
// last `height` indices, so there is no live/history reconciliation to get
// wrong. Frames feed the store; one requestAnimationFrame flush drains its
// change set (evicted indices drop their row, dirty ones build in place). The
// window block always has `height` rows, so scrollHeight grows only when real
// history commits.

import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";
import { COMPATIBILITY_TAIL_CAP, LineStore, PAGE_SIZE, PREFETCH_THRESHOLD } from "./store.js";
import type { ScrollController } from "./scroll.js";
import type { ModeState } from "./modes.js";
import { interiorGaps } from "./intervals.js";

// --- Width cache (two-tier, xterm.js style) ---
const WIDTH_FLAT_SIZE = 256;
const WIDTH_FLAT_UNSET = -9999;
const VARIANT_REGULAR = 0;
const VARIANT_BOLD = 1;
const VARIANT_ITALIC = 2;
// widthMap holds one entry per unique (bold, italic, glyph) measured — bounded
// by the rendered repertoire in practice, but a long CJK/emoji-heavy session
// can accumulate tens of thousands of keys that survive eviction, reset, and
// tab close (only a font change clears it). Cap it: a clear on overflow costs
// an occasional re-measure and changes no rendered output.
const WIDTH_MAP_MAX = 20_000;

const MAX_ROWS_PER_FRAME = 300;
const MAX_RENDER_NO_PROGRESS_RETRIES = 3;
const MIN_COLS = 20;
const MIN_ROWS = 5;

const RESTORE_SETTLE_MS = 250;
const RESTORE_MAX_MS = 30000;
// A position within this many px of what we last wrote is still ours: a
// fractional-layout or subpixel-DPR readback is not a user gesture.
const RESTORE_OWN_WRITE_EPSILON_PX = 1;

const CURSOR_BLINK_MS = 530;
// While the application hides the cursor (DECTCM — e.g. a full-screen TUI
// that paints its own cursor cell, like an agent front-end), the fast toggle
// would only restyle a display:none overlay, so the interval downshifts to
// this slow re-check: still polling for the cursor's return (a self-healing
// backstop even if a transition frame were somehow missed — the flush hook is
// the primary, instant restart), at a fraction of the 530ms blink's wakeups.
// For agent consumers the hidden-cursor state is the session's steady state.
const CURSOR_RECHECK_MS = 4000;
type BlinkMode = "off" | "idle" | "fast";

/** The permanent label: nothing below the oldest held index can be recovered. */
const TRIM_LABEL_GONE = "earlier output trimmed";
/** The recoverable label: history above exists and a fetch can bring it back. */
const TRIM_LABEL_PENDING = "earlier output not loaded";

/**
 * A saved reading position in the only terms that survive the content growing
 * underneath it. Opaque to the consumer: read one out of the renderer and hand
 * the same value back to restore it. A memory is only meaningful to the store
 * it was taken from, since `abs` names a line in THAT index space, and a
 * restore is re-asserted until the named row has been built.
 */
export interface ViewMemory {
  /** Absolute line index of the content row at the viewport top. */
  abs: number;
  /** That row's on-screen position: offsetTop minus the scroll offset. */
  screenTop: number;
  /** Whether auto-follow was engaged. */
  following: boolean;
}

/** What `createRenderer` binds to, reads and calls back. */
export interface RendererOptions {
  /** Inner element that receives the row children. */
  output: HTMLElement;
  /** Outer scroll container; the overlays, `--char-w` and the blink class live on it. */
  termWrap: HTMLElement;
  scroll: ScrollController;
  modes: Pick<ModeState, "isReverseVideo">;
  /** Invoked when the cursor moves. */
  onCursorMove?: () => void;
  /**
   * Retained-line cap for the implicit store (default 5000): a HISTORY budget
   * floored at the live screen, which is never evicted. A non-positive or
   * non-integer value warns and the default applies.
   */
  maxLines?: number;
  /**
   * Ask the transport for a page of history (`connection.requestHistory`).
   * Absent means paging is not available and the controller stays dormant.
   */
  requestHistory?: (fromAbs: number, maxLines: number) => boolean;
  /**
   * The transport's current adaptive request budget (`connection.historyBudget`),
   * read at fire time for both the request's length and its anchor.
   */
  historyBudget?: () => number;
}

/**
 * The DOM renderer: it owns a `LineStore` of absolute-index lines and reflects
 * it to one `div.term-row` per line inside `termWrap`, so there is no
 * live-zone/scrollback split to reconcile. Usable from construction with
 * fallback cell metrics; `updateFontMetrics` measures the real font.
 */
export interface Renderer {
  /** Reset internal screen state so the next frame performs a full repaint. */
  resetScreen(): void;
  /** Clear all rows (history + window); equivalent to `resetScreen`. */
  resetScrollback(): void;
  /**
   * Bind the renderer to a different store and rebuild the surface from it.
   * `opts.view` is the per-view memory `captureViewMemory()` returned when the
   * consumer last left this store; passing `opts` at all means the caller owns
   * the view, and a null view is "no memory", whose follow state is the tail.
   */
  bind(next: LineStore, opts?: { view?: ViewMemory | null }): void;
  /** The store the renderer is currently bound to; a fresh detached store once disposed. */
  boundStore(): LineStore;
  /** Wipe the DOM and rebuild it from the current store, viewport-first and budgeted. */
  rebuild(): void;
  /**
   * Highest absolute line index the client holds, or -1 if empty. NOT the resume
   * `haveThrough`; that is `getReplayBoundary`.
   */
  getHighestIndex(): number;
  /**
   * The resume `haveThrough`: the highest index this client will not ask the
   * server to re-send. Differs from `getHighestIndex` by the provisional rows
   * the application drew and the server has not committed.
   */
  getReplayBoundary(): number;
  /** Record the server's retained-history bounds from a resumeAck. */
  noteResumeBounds(committed: number, oldest: number): void;
  /**
   * Apply a correlated history page; when the reply proved history is gone,
   * raise the paging floor so nothing below it is requested again.
   */
  handleHistoryReply(msg: ScrollMessage, raiseFloorTo: number | null): void;
  /** Run the store's single resume-ack transition, supplying the viewport the store cannot see. */
  applyResumeTransition(ack: {
    epochChanged: boolean;
    committed: number | null;
    serverOldest: number | null;
    paging: boolean;
    sentHaveThrough: number;
    sentReplayMax: number | null;
  }): void;
  /** Record the window of a history request going out. */
  noteSolicited(fromAbs: number, end: number): void;
  /** Release the in-flight window (reply applied, timed out, socket gone). */
  clearSolicited(): void;
  /** Drop the browse cache; the consumer owns the TTL and the visibility state. */
  dropBrowseCache(pageVisible: boolean): void;
  /** When the browse cache was last created or refreshed, for a consumer's TTL. */
  lastBrowseActivityMs(): number;
  /** Lines currently held as disposable browse cache. */
  browseCacheSize(): number;
  /** The resume replay bound to send: residency minus the window about to arrive anyway. */
  replayMaxForResume(): number;
  /**
   * Rows queued for a DOM (re)build but not yet built. A render-side measure
   * that reaches zero between replay chunks, so not a restore-complete signal.
   */
  pendingRowCount(): number;
  /** Apply a `ScreenMessage` to the store and schedule a flush. */
  handleScreen(msg: ScreenMessage): void;
  /** Apply a `ScrollMessage` to the store and schedule a flush. */
  handleScroll(msg: ScrollMessage): void;
  /**
   * The renderer's half of a scroll-position event: re-evaluate the paging
   * trigger, then resume a drain that stopped with rows still queued.
   */
  handleScrollPosition(): void;
  /**
   * Capture the current view as per-view scroll memory a consumer can hand back
   * to `bind`; null when there are no content rows or the alternate screen is active.
   */
  captureViewMemory(): ViewMemory | null;
  /** The absolute index of an ARMED view restore, or null. */
  pendingRestoreAbs(): number | null;
  /** The fetch trigger: decide whether to ask for a page, and for which range. Cheap to over-call. */
  maybeFetchHistory(): void;
  /**
   * Re-measure the cell width/height from the rendered DOM. Call after any font
   * or zoom change so subsequent `computeSize()` and `getCursorPx()` use fresh
   * metrics.
   */
  updateFontMetrics(): void;
  /** The integer (cols, rows) the terminal element can fit, clamped to 20x5; `{0,0}` once disposed. */
  computeSize(): { cols: number; rows: number };
  /**
   * The grid currently ON SCREEN (rows from the last frame, 0 before the first),
   * for `MouseInputHandler.gridSize`; `{0,0}` once disposed.
   */
  gridSize(): { cols: number; rows: number };
  /** The cursor's pixel position in `termWrap`'s coordinate space plus the cell height. */
  getCursorPx(): { left: number; top: number; cellH: number };
  /** The measured pixel size of one cell; the 8x17 fallback before `updateFontMetrics`. */
  cellSize(): { width: number; height: number };
  /** Show or hide the predicted-cursor overlay at window-relative (row, col). */
  setPredictedCursor(row: number, col: number, active: boolean): void;
  /** Apply or remove the DECSCNM reverse-video class on `termWrap`. */
  updateReverseVideo(): void;
  /**
   * Release every timer, listener, element and callback the renderer holds and
   * clear its DOM; every method stays callable and answers its empty value.
   */
  dispose(): void;
}

/**
 * Creates a renderer over `opts.output` inside `opts.termWrap`. Throws, holding
 * nothing, when a listener or timer cannot be acquired.
 */
export function createRenderer(opts: RendererOptions): Renderer {
  // Invariant: `scroll` and `modes` are read by member call at each call site, never as captured methods.
  const { scroll, modes } = opts;
  const validCap =
    opts.maxLines !== undefined && Number.isInteger(opts.maxLines) && opts.maxLines > 0;
  if (opts.maxLines !== undefined && !validCap) {
    console.warn(`vterm: ignoring invalid maxLines ${String(opts.maxLines)}`);
  }
  let disposed = false;
  const widthFlat = new Float32Array(WIDTH_FLAT_SIZE).fill(WIDTH_FLAT_UNSET);
  const widthMap = new Map<string, number>();
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
    if (widthMap.size >= WIDTH_MAP_MAX) {
      widthMap.clear();
    }
    widthMap.set(key, w);
    return w;
  }

  // Measured against termWrap so the web font applied by CSS is what is measured.
  function measureCellWidth(el: HTMLElement): number {
    const span = document.createElement("span");
    span.style.visibility = "hidden";
    span.style.position = "absolute";
    span.style.whiteSpace = "pre";
    span.textContent = "MMMMMMMMMM";
    el.appendChild(span);
    const width = span.getBoundingClientRect().width / 10;
    el.removeChild(span);
    return width;
  }

  // --- State --- (the nullable fields are null once disposed)
  let output: HTMLElement | null = opts.output;
  let termWrap: HTMLElement | null = opts.termWrap;

  // An absent OR invalid cap is forwarded as absent, which is how the store
  // spells "engine's choice"; a value here would be indistinguishable from a
  // consumer decision and would pin the tail at the compatibility cap forever.
  let store: LineStore | null = new LineStore(validCap ? opts.maxLines : undefined);
  // abs index -> its row element, kept in ascending data-abs order in `output`.
  const rowEls = new Map<number, HTMLDivElement>();

  // The "earlier output trimmed" marker, a data-abs-less first child of output.
  let trimMarkerEl: HTMLDivElement | null = null;

  // Per-gap "earlier output not loaded" markers, keyed by the gap's LOW index.
  // Each carries that index as `data-abs` so `insertRowInOrder` and the
  // read-anchor binary searches keep their monotonic-`data-abs` invariant; an
  // INTERIOR marker cannot be data-abs-less the way the top trim marker is. A
  // marker is a PROJECTION of the store's gap geometry, re-derived whenever
  // either edge moves and removed when the gap closes (docs/paged-scrollback.md §5.4).
  const gapMarkerEls = new Map<number, HTMLDivElement>();

  let requestHistoryFn: ((fromAbs: number, maxLines: number) => boolean) | null =
    opts.requestHistory ?? null;
  let historyBudgetFn: (() => number) | null = opts.historyBudget ?? null;

  // Rows awaiting a DOM (re)build, drained at most MAX_ROWS_PER_FRAME per frame:
  // a session restore or `cat bigfile` dumps thousands of lines in one frame,
  // and building them in one rAF hangs a constrained device. The drain is
  // VIEWPORT-FIRST (live-window rows, then scrollback newest->oldest so the
  // backlog fills upward, offscreen above the pinned viewport), and the cursor
  // row is always built so the caret never lags.
  const renderQueue = new Set<number>();

  // The caret is a dedicated overlay (positionCursorOverlay), NOT a restyled
  // span: rows are pure content, so cursor motion never rewrites row DOM and a
  // native selection in the old or new cursor row survives.
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
  let onCursorMove: (() => void) | null = opts.onCursorMove ?? null;
  let pendingFrame: number | undefined;

  // termWrap's padding, cached: the overlay positioners need it EVERY flush,
  // and a live getComputedStyle after the flush's DOM writes forces a style
  // recalc (the caret positioner alone measured ~11% of flush CPU). Refreshed
  // by updateFontMetrics, the same staleness contract as cellWidth/cellHeight.
  let padLeft = 0;
  let padTop = 0;
  let padValid = false;

  function termPadding(): { padL: number; padT: number } {
    if (!padValid && termWrap) {
      const cs = window.getComputedStyle(termWrap);
      padLeft = parseFloat(cs.paddingLeft);
      padTop = parseFloat(cs.paddingTop);
      padValid = true;
    }
    return { padL: padLeft, padT: padTop };
  }

  // Bounded error-path reschedule. A row whose build throws stays queued, and
  // flushRender's catch reschedules to retry a transient throw (a measureText
  // race); a deterministic throw would turn that into a ~60fps busy loop.
  // `flushDrainedThisPass` records rows built this pass (visible to the catch
  // mid-drain); once `renderNoProgressStreak` passes the cap the catch stops
  // rescheduling and lets the next inbound frame retry.
  let flushDrainedThisPass = 0;
  let renderNoProgressStreak = 0;
  // The stall canary's latch, reset by any healthy flush so it suppresses a
  // repeat of ONE stall rather than every stall for the life of the instance.
  let unnamedStallReported = false;
  // Whether the CURRENT flush performed a full reset. Read by restoreReadAnchor:
  // a re-anchor across a reset would match unrelated content from the new index space.
  let fullResetThisPass = false;
  // The ED3 base observed since the last flush, or -1; consumed by the pass that
  // applies it. Renderer-local because the renderer calls every path that
  // discards a REGION rather than trimming the cap (docs/scroll-position-fidelity.md §5).
  let discardedBelowPending = -1;
  let discardedBelowThisPass = -1;
  // Whether THIS pass removed rows. Announcing a shrink for a pass that did not
  // is unsound on Safari, which moves scrollTop PAST the maximum during an
  // overscroll bounce: the settle back is a downward move with no content
  // change, and arming for it would hand the user's own gesture a pass-through.
  let removedRowsThisPass = false;

  function resetScreen(): void {
    if (!store) {
      return;
    }
    store.reset();
    scheduleFlush();
  }

  function resetScrollback(): void {
    if (!store) {
      return;
    }
    store.reset();
    scheduleFlush();
  }

  // The FOLLOW half of the view is adopted SYNCHRONOUSLY, before the wipe, so
  // the first flush's bottom pin is gated on the INCOMING view rather than the
  // tab just left. The POSITION half is armed and re-asserted across the
  // rebuild's frames (applyPendingRestore) because the row it names is usually
  // not built yet; a FOLLOWING view arms nothing, the per-flush pin answers.
  function bind(next: LineStore, bindOpts?: { view?: ViewMemory | null }): void {
    if (!store) {
      return;
    }
    const view = bindOpts?.view ?? null;
    // A NULL view under `bindOpts` is "no memory", whose follow state is the
    // tail; otherwise such a tab inherits the OUTGOING tab's follow flag.
    if (bindOpts !== undefined) {
      scroll.restoreView({
        top: scroll.currentScrollTop(),
        following: view === null ? true : view.following,
      });
    }
    // The transport's own `clearSolicited` lands on whichever store is bound
    // WHEN IT FIRES, and the order of `bind` against the consumer's teardown is
    // the consumer's choice. Closed here, or the outgoing store's window strands
    // open with no socket or timer to close it, and any later frame in that
    // range can resurrect an evicted row and be classified as browse cache.
    store.clearSolicited();
    store = next;
    // Cancel, then arm, one slot: a second switch mid-drain must not land the
    // first tab's anchor into this store.
    bindGen++;
    clearPendingRestore();
    rebuild();
    if (view !== null && !view.following) {
      pendingRestore = {
        view,
        gen: bindGen,
        lastWrote: scroll.currentScrollTop(),
        deadline: Date.now() + RESTORE_MAX_MS,
      };
    }
  }

  function boundStore(): LineStore {
    return store ?? new LineStore();
  }

  // Queue every retained line viewport-first: the live window ascending, then
  // scrollback newest->oldest. Iterates the retained key set, NOT the integer
  // range [oldest, highest], so a sparse store (a frame whose base jumped far
  // from a retained index) never freezes the drain.
  function queueRowsViewportFirst(): void {
    if (!store) {
      return;
    }
    const winBase = store.getWindow().base;
    const inWindow: number[] = [];
    const belowWindow: number[] = [];
    store.forEachLine((abs) => {
      if (abs >= winBase) {
        inWindow.push(abs);
      } else {
        belowWindow.push(abs);
      }
    });
    for (const abs of inWindow) {
      renderQueue.add(abs);
    }
    for (let i = belowWindow.length - 1; i >= 0; i--) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- index in range
      renderQueue.add(belowWindow[i]!);
    }
  }

  function rebuild(): void {
    // The wipe is the largest content shrink this module performs and happens
    // OUTSIDE a flush (bind calls it synchronously), so it announces its own
    // clamp; unannounced, the clamp falls back to the epsilon inference.
    if (!store || !output) {
      return;
    }
    const scrollTopBeforeWipe = scroll.currentScrollTop();
    output.replaceChildren();
    rowEls.clear();
    renderQueue.clear();
    trimMarkerEl = null;
    for (const el of gapMarkerEls.values()) {
      el.remove();
    }
    gapMarkerEls.clear();
    cursorAbs = -1;
    altRendered = false;
    altPrevRows = [];
    // A stale give-up streak must not deny the rebuilt surface its retry budget.
    renderNoProgressStreak = 0;
    // The overlays share content space with the rows; collapse them BEFORE the
    // offset below is read.
    collapseContentSpaceOverlays();
    // Alt screen paints from the ephemeral grid in the flush.
    if (!store.isAlt()) {
      queueRowsViewportFirst();
    }
    scroll.noteContentShrink(scrollTopBeforeWipe);
    scheduleFlush();
  }

  // Four elements INSIDE the scroll container carry a `top` in content
  // coordinates (the caret, the predicted cursor, the IME view, the consumer's
  // hidden textarea) and hold its scrollable overflow after the rows are gone:
  // measured, an 81081px range over zero rows, a black pane with `stickToBottom`
  // reading "already at the bottom". Collapsing them here also makes the wipe's
  // clamp SYNCHRONOUS, which `noteContentShrink` (arms only on an observed move)
  // and `bind` (records `pendingRestore.lastWrote` from the same offset) need.
  function collapseContentSpaceOverlays(): void {
    // `cursorAbs` is -1 by now, the state positionCursorOverlay hides for.
    positionCursorOverlay(undefined);
    predCursorEl?.classList.remove("visible");
    // The IME view and the textarea are the CONSUMER's, reachable through the
    // cursor seam; `getCursorPx` reports the content origin while `rowEls` is empty.
    onCursorMove?.();
  }

  function getHighestIndex(): number {
    return store ? store.highestIndex() : -1;
  }

  // Claiming provisional rows on the wire is what leaves a stale copy of the last
  // screen parked in scrollback after a reattach. See LineStore.replayBoundary.
  function getReplayBoundary(): number {
    return store ? store.replayBoundary() : -1;
  }

  function noteResumeBounds(committed: number, oldest: number): void {
    if (!store) {
      return;
    }
    store.noteResumeBounds(committed, oldest);
    scheduleFlush();
  }

  // --- demand-paged scrollback: the consumer's seams (docs/paged-scrollback.md) ---

  // The viewport index is supplied here rather than by the caller because the
  // renderer is the only layer that knows it, and the store's eviction needs it
  // to decide which end of the cache is safe to drop.
  function handleHistoryReply(msg: ScrollMessage, raiseFloorTo: number | null): void {
    if (!store) {
      return;
    }
    if (raiseFloorTo !== null) {
      store.raisePagingFloor(raiseFloorTo);
    }
    store.applyHistoryScroll(msg, viewportAbs());
    scheduleFlush();
  }

  function applyResumeTransition(ack: {
    epochChanged: boolean;
    committed: number | null;
    serverOldest: number | null;
    paging: boolean;
    sentHaveThrough: number;
    sentReplayMax: number | null;
  }): void {
    if (!store) {
      return;
    }
    // A tab switch reconnects, so this transition routinely runs mid-rebuild at
    // a clamped offset over rows still being built. The store's reclassify pass
    // uses the viewport to decide which rows survive, and the live transient
    // would evict the rows an armed restore is about to bring back; the restore
    // names the position the user is REGAINING, so it wins, and it also
    // overrides the live follow flag (docs/scroll-position-fidelity.md §7.2).
    const pending = pendingRestoreAbs();
    const pendingRestoreArmed = pending !== null;
    store.applyResumeAck({
      ...ack,
      viewportAbs: pending ?? viewportAbs(),
      following: !pendingRestoreArmed && !scroll.isUserScrolledUp(),
    });
    scheduleFlush();
  }

  function noteSolicited(fromAbs: number, end: number): void {
    store?.noteSolicited(fromAbs, end);
  }

  function clearSolicited(): void {
    store?.clearSolicited();
  }

  function dropBrowseCache(pageVisible: boolean): void {
    if (!store) {
      return;
    }
    store.dropBrowseCache(viewportAbs(), pageVisible);
    scheduleFlush();
  }

  function lastBrowseActivityMs(): number {
    return store ? store.lastBrowseActivityMs() : 0;
  }

  function browseCacheSize(): number {
    return store ? store.browseCacheSize() : 0;
  }

  // Residency minus the window about to be sent anyway, so an attach does not
  // download rows the cap would immediately trim.
  function replayMaxForResume(): number {
    if (!store) {
      return COMPATIBILITY_TAIL_CAP;
    }
    return Math.max(1, store.tailCap() - store.getWindow().height);
  }

  function pendingRowCount(): number {
    return renderQueue.size;
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
      // An OSC 8 anchor's href is authoritative: re-scanning its VISIBLE text
      // would rebuild a URL that wraps across rows from a fragment.
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
        // `.term-autolink` keeps a persistent underline because the match is
        // scoped to the URL text; an OSC 8 link can span a padded region (a URL
        // wrapping inside a table cell), so it gets only the hover underline.
        a.className = "term-link term-autolink";
        a.textContent = match[0];
        // Property-by-property, never cssText: a cssText assignment is a
        // string-PARSED style write, the one kind a style-src CSP governs.
        for (let i = 0; i < span.style.length; i++) {
          const prop = span.style.item(i);
          a.style.setProperty(
            prop,
            span.style.getPropertyValue(prop),
            span.style.getPropertyPriority(prop),
          );
        }
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

  // A hyperlink run is "link text" only if it has a glyph that is not whitespace
  // and not box-drawing or block-element (U+2500–U+259F): an app may keep an
  // OSC 8 link open across table borders and padding while a URL wraps, and
  // anchoring those cells would bleed the underline across the cell and row.
  function runHasLinkText(spans: (HTMLSpanElement | HTMLAnchorElement)[]): boolean {
    for (const s of spans) {
      for (const ch of s.textContent) {
        if (/\s/.test(ch)) {
          continue;
        }
        const cp = ch.codePointAt(0) ?? 0;
        if (cp >= 0x2500 && cp <= 0x259f) {
          continue;
        }
        return true;
      }
    }
    return false;
  }

  // --- Build row DOM ---
  function buildRowSpans(runs: readonly WireRun[]): (HTMLSpanElement | HTMLAnchorElement)[] {
    const out: (HTMLSpanElement | HTMLAnchorElement)[] = [];
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
          // The spacer occupies the wide char's second cell. (Column arithmetic
          // for the caret lives in glyphAt, which mirrors this advance rule —
          // the engine reports cursor_col in true cell coordinates, a wide
          // glyph moving curX by 2.)
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
      }
      flush();
      // Decorative cells inside an open OSC 8 link stay plain spans (see
      // runHasLinkText); every text run of a wrapped URL still carries the full href.
      const href = run.u && /^https?:\/\//i.test(run.u) ? run.u : null;
      const runSpans = out.splice(runStartIdx);
      if (href && runHasLinkText(runSpans)) {
        const a = document.createElement("a");
        a.href = href;
        a.target = "_blank";
        a.rel = "noopener noreferrer";
        // Attr bit 1024 (vt.AttrAutolink) is a server-detected bare URL, styled
        // like the client's own autolinks; an OSC 8 link keeps the hover-only base.
        a.className = (attrs & 1024) !== 0 ? "term-link term-autolink" : "term-link";
        for (const s of runSpans) {
          a.appendChild(s);
        }
        out.push(a);
      } else {
        for (const s of runSpans) {
          out.push(s);
        }
      }
    }
    if (out.length === 0) {
      const span = document.createElement("span");
      span.textContent = "\u00a0";
      out.push(span);
    }
    return linkifySpans(out);
  }

  // --- Frame handling: feed the store, then flush to DOM ---

  function handleScreen(msg: ScreenMessage): void {
    if (!store) {
      return;
    }
    lastInboundMs = Date.now();
    if (msg.scrollbackCleared) {
      // ED3. Recorded at the one place that sees the frame before the store
      // consumes it, so restoreReadAnchor can tell a region DISCARD from a cap
      // trim; the two need opposite recoveries (docs/scroll-position-fidelity.md §5).
      // Max, because several frames can land between flushes.
      discardedBelowPending = Math.max(discardedBelowPending, msg.base);
    }
    store.applyScreen(msg);
    scheduleFlush();
  }

  function handleScroll(msg: ScrollMessage): void {
    if (!store) {
      return;
    }
    lastInboundMs = Date.now();
    store.applyScroll(msg);
    scheduleFlush();
  }

  function scheduleFlush(): void {
    if (pendingFrame !== undefined) {
      return;
    }
    pendingFrame = requestAnimationFrame(flushRender);
  }

  // One hook rather than two, because a consumer wiring `maybeFetchHistory` and
  // not a public `resumeDrain` would get paging and no drain recovery with no
  // type error. The resume is reachable on an idle session: the bounded error
  // path stops rescheduling, and no inbound frame arrives to restart it.
  function handleScrollPosition(): void {
    maybeFetchHistory();
    resumeDrain();
  }

  // Three refusals, each load-bearing. An EMPTY QUEUE: the flush runs the
  // position invariants unconditionally, so a flush from a scroll handler would
  // snap a downward scroll landing just short of the tail to the bottom one
  // frame later. ALT SCREEN: a named suspension of the drain; alt exit re-queues
  // everything. A GIVE-UP ALREADY RETRIED: a flick is one scroll event per
  // frame, so retrying a deterministically throwing row per event would move
  // the 60fps loop outside the module; one retry per give-up, re-earned by
  // whatever clears the streak, never by the give-up log itself.
  function resumeDrain(): void {
    if (!store || renderQueue.size === 0 || store.isAlt()) {
      return;
    }
    // The catch reaches `streak === MAX` from its progress-less branch and
    // schedules a frame on the way out, so for one frame the streak sits at the
    // cap with a flush pending; a scroll landing there would burn the retry
    // while `scheduleFlush` no-ops. An empty frame slot is what proves the
    // give-up HAPPENED.
    if (pendingFrame !== undefined) {
      return;
    }
    if (renderNoProgressStreak >= MAX_RENDER_NO_PROGRESS_RETRIES) {
      if (renderNoProgressStreak > MAX_RENDER_NO_PROGRESS_RETRIES) {
        return;
      }
      // The retry is carried in the streak itself rather than a second flag,
      // so every `renderNoProgressStreak = 0` re-arms it by construction; a flag
      // had to be cleared at every such site and was missed at the common ones.
      renderNoProgressStreak = MAX_RENDER_NO_PROGRESS_RETRIES + 1;
    }
    scheduleFlush();
  }

  // --- Read-position anchoring (manual scroll anchoring) ---
  // A flush can change the content height ABOVE the reading position (top-of-
  // history eviction at the cap, the trim marker appearing). WebKit has no
  // `overflow-anchor`, so on Safari a scrolled-up reader was dragged one line per
  // evicted row. The anchor is the first content row at or below the viewport
  // top; a following viewport never takes this path, the bottom pin owns it.
  interface ReadAnchor {
    el: HTMLElement;
    /** The anchor row's index, so an eviction of the element itself re-resolves to the nearest survivor. */
    abs: number;
    /** Where the row sat ON SCREEN (offsetTop minus scroll offset), which makes the correction idempotent. */
    screenTop: number;
  }

  // "Content row" is decided by IDENTITY in rowEls, not by a data-abs attribute:
  // a gap marker carries its gap's LOW index (so insertRowInOrder stays
  // monotonic), and an attribute test would return it as a reading position
  // naming a line the store does not hold.
  function isContentRow(el: HTMLElement): boolean {
    const abs = rowAbs(el);
    return abs >= 0 && rowEls.get(abs) === el;
  }

  // The ONE row-selection primitive every "where is the reader" question
  // resolves through (the read anchor, the paging trigger, the per-view memory):
  // two definitions would drift exactly during a rebuild, when the answer
  // matters most (docs/scroll-position-fidelity.md §7.2). Binary search, since
  // the children are in document order with monotonic offsetTop.
  function rowAtViewportTop(): HTMLElement | null {
    if (!output) {
      return null;
    }
    const kids = output.children;
    if (kids.length === 0) {
      return null;
    }
    const offset = scroll.currentScrollTop();
    let lo = 0;
    let hi = kids.length - 1;
    let found: HTMLElement | null = null;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const el = kids[mid] as HTMLElement;
      if (el.offsetTop >= offset) {
        found = el;
        hi = mid - 1;
      } else {
        lo = mid + 1;
      }
    }
    // Walk past markers. The bound is O(gaps + 1), not 1: two gap markers CAN
    // be adjacent siblings once predictReplayJump retires the window.
    while (found !== null && !isContentRow(found)) {
      found = found.nextElementSibling as HTMLElement | null;
    }
    return found;
  }

  function captureReadAnchor(): ReadAnchor | null {
    if (!scroll.isUserScrolledUp()) {
      return null; // following: stickToBottom owns the position
    }
    const el = rowAtViewportTop();
    if (el === null) {
      // Stand down rather than fall back to the tail: a tail proxy is what
      // turned a large content shrink into a tail-drag (docs/scroll-position-fidelity.md §1.2).
      return null;
    }
    return { el, abs: rowAbs(el), screenTop: el.offsetTop - scroll.currentScrollTop() };
  }

  // The nearest surviving CONTENT row at or below a lost reading position, by
  // binary search over the ascending-data-abs children; markers are skipped by
  // identity (isContentRow).
  function firstRowAtOrAfter(abs: number): HTMLElement | null {
    if (!output) {
      return null;
    }
    const kids = output.children;
    let lo = 0;
    let hi = kids.length - 1;
    let found: HTMLElement | null = null;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const el = kids[mid] as HTMLElement;
      if (rowAbs(el) >= abs) {
        found = el;
        hi = mid - 1;
      } else {
        lo = mid + 1;
      }
    }
    while (found !== null && !isContentRow(found)) {
      found = found.nextElementSibling as HTMLElement | null;
    }
    return found;
  }

  function restoreReadAnchor(anchor: ReadAnchor | null): void {
    if (anchor === null) {
      return; // following: stickToBottom owns the position
    }
    let el: HTMLElement | null = anchor.el;
    if (el.parentElement !== output) {
      if (fullResetThisPass) {
        // Indices restarted from 0, so a child with a matching data-abs is
        // UNRELATED content; the fresh session's own follow state owns the viewport.
        return;
      }
      // Batched cap eviction frees up to a whole batch in one pass, so a reader
      // parked near the buffer top loses the anchored ELEMENT while unread rows
      // survive below it; hold the nearest survivor at the anchor's screen position.
      el = firstRowAtOrAfter(anchor.abs);
      if (el === null) {
        return; // nothing survives (reset/clear): nothing to hold
      }
      // A region DISCARD (ED3), not a cap trim: nothing surviving is guaranteed
      // ADJACENT to what the reader saw. An inline TUI that reprints on resize
      // brings the same text back at new indices, and holding the survivor at
      // the old screen position is the "random jump on resize" symptom
      // (docs/scroll-position-fidelity.md §1.2, §5). The test is the ANCHOR's
      // index only: requiring the SURVIVOR above the base too never held, since
      // the reprint re-delivers lines below the base in the same frame.
      if (discardedBelowThisPass >= 0 && anchor.abs < discardedBelowThisPass) {
        return;
      }
    }
    // Correct by how far the row DRIFTED ON SCREEN, not by the content delta:
    // Chrome and Firefox anchor natively, so their offsetTop change comes with
    // a matching scrollTop change and this measures zero; correcting the content
    // delta there would double-compensate. On Safari the drift is the whole delta.
    const drift = el.offsetTop - scroll.currentScrollTop() - anchor.screenTop;
    scroll.adjustForContentShift(drift);
  }

  function flushRender(): void {
    pendingFrame = undefined;
    // Read BEFORE any mutation, twice over: the anchor needs the pre-mutation
    // screen position, and noteContentShrink needs the pre-mutation offset to tell
    // whether this pass's row removals actually moved the viewport.
    const scrollTopBefore = scroll.currentScrollTop();
    const anchor = captureReadAnchor();
    try {
      flushRenderInner();
      // Clean pass: give a later transient error its full retry budget again.
      renderNoProgressStreak = 0;
    } catch (err) {
      console.error("vterm: render error", err);
      // The throw skipped flushRenderInner's own reschedule, so finish the drain
      // here, BOUNDED: reschedule while the pass made progress or the
      // no-progress streak is under the cap, then stop and let the next inbound
      // frame retry.
      if (renderQueue.size === 0) {
        renderNoProgressStreak = 0;
      } else if (flushDrainedThisPass > 0) {
        renderNoProgressStreak = 0;
        scheduleFlush();
      } else if (renderNoProgressStreak < MAX_RENDER_NO_PROGRESS_RETRIES) {
        renderNoProgressStreak++;
        scheduleFlush();
      } else {
        console.error("vterm: giving up render retry after repeated no-progress errors");
      }
    }
    // Announce the shrink BEFORE any write of our own, or the comparison
    // measures our write instead of the browser's clamp; then reconcile an
    // out-of-range offset before the invariants below measure the geometry.
    if (removedRowsThisPass) {
      scroll.noteContentShrink(scrollTopBefore);
      scroll.reconcileScrollRange();
    }
    // Three position invariants in this order: an armed restore OWNS the
    // position, the read anchor holds the line on screen, the bottom pin last.
    // The anchor is skipped ONLY in the frame the restore LANDED: captured before
    // this frame's mutations, its drift would measure the restore's own write and
    // correct it straight back out. Skipping it while merely ARMED would
    // reintroduce the WebKit slide for every frame of a multi-frame rebuild.
    const restoreLanded = applyPendingRestore();
    if (!restoreLanded) {
      restoreReadAnchor(anchor);
    }
    // Single auto-follow invariant, applied after every DOM mutation.
    stickToBottomIfFollowing();
    // Absorb every write this frame made — ours and the browser's native scroll
    // anchoring — into the restore's baseline, so only a move that happens BETWEEN
    // frames (a real gesture) reads as one.
    if (pendingRestore !== null) {
      pendingRestore.lastWrote = scroll.currentScrollTop();
    }
    // The post-flush trigger. The reader's position relative to the store's gaps
    // can change without any scroll event — a tail trim moves the frontier up
    // under a stationary reader, and a byte-short page leaves a fresh sub-gap
    // beside them — so the flush is the other place the trigger must run
    // (docs/paged-scrollback.md §5.4).
    maybeFetchHistory();
    reportUnnamedDrainStall();
  }

  // After a flush, owed rows are in one of four states: nothing owed, a frame
  // scheduled, suspended on the alt screen (renderAlt replaces the whole output
  // subtree; alt exit re-queues everything), or stopped and logged by the bounded
  // error path. Anything else is a stall nobody owns: `scheduleFlush` is a single
  // slot, so a non-empty queue with an empty slot drops every later request
  // silently. A future suspension point must be named here.
  function reportUnnamedDrainStall(): void {
    if (!store || renderQueue.size === 0 || pendingFrame !== undefined || store.isAlt()) {
      unnamedStallReported = false;
      return;
    }
    if (renderNoProgressStreak >= MAX_RENDER_NO_PROGRESS_RETRIES) {
      // Named and already logged by the catch; not healthy, so the latch stays.
      return;
    }
    if (unnamedStallReported) {
      return;
    }
    unnamedStallReported = true;
    console.warn(
      `vterm: ${String(renderQueue.size)} rows queued with no scheduled frame and no named suspension`,
    );
  }

  function flushRenderInner(): void {
    flushDrainedThisPass = 0;
    if (!store || !output) {
      return;
    }
    const ch = store.drainChanges();
    fullResetThisPass = ch.fullReset;
    discardedBelowThisPass = discardedBelowPending;
    discardedBelowPending = -1;
    removedRowsThisPass = false;

    if (ch.fullReset) {
      removedRowsThisPass = true;
      output.replaceChildren();
      rowEls.clear();
      renderQueue.clear();
      trimMarkerEl = null;
      for (const el of gapMarkerEls.values()) {
        el.remove();
      }
      gapMarkerEls.clear();
      cursorAbs = -1;
      // Collapse the overlays HERE rather than in the flush tail, so the shrink
      // this pass announces measures the real height and not a phantom one.
      collapseContentSpaceOverlays();
    } else {
      for (const abs of ch.evictedLines) {
        const el = rowEls.get(abs);
        if (el) {
          el.remove();
          removedRowsThisPass = true;
        }
        rowEls.delete(abs);
        renderQueue.delete(abs);
      }
    }

    const win = store.getWindow();
    const newCursorAbs = win.base + win.cursorRow;
    cursorAbs = newCursorAbs;
    cursorCol = win.cursorCol;
    cursorHidden = win.cursorHidden;
    cursorStyleVal = win.cursorStyle;
    blinkEnabled = win.cursorBlink;
    syncCursorBlink();

    // Skipping the dirtyLines queueing is safe: alt exit rebuilds from the
    // store, repainting any line dirtied during the alt session.
    if (store.isAlt()) {
      const altRows = store.getAltRows();
      renderAlt(altRows);
      positionCursorOverlay(altRows[win.cursorRow]);
      if (onCursorMove) {
        onCursorMove();
      }
      return;
    }
    if (altRendered) {
      // Just exited alt: drop the ephemeral rows and rebuild from the store.
      altRendered = false;
      altPrevRows = [];
      removedRowsThisPass = true;
      output.replaceChildren();
      rowEls.clear();
      renderQueue.clear();
      trimMarkerEl = null;
      for (const el of gapMarkerEls.values()) {
        el.remove();
      }
      gapMarkerEls.clear();
      queueRowsViewportFirst();
    }

    for (const abs of ch.dirtyLines) {
      renderQueue.add(abs);
    }

    // The cursor's row is built regardless of the budget: the caret overlay
    // positions off the row element's offsetTop, so a backlog must never leave
    // it floating over a not-yet-built row.
    if (renderQueue.has(newCursorAbs) || !rowEls.has(newCursorAbs)) {
      upsertRow(newCursorAbs);
      renderQueue.delete(newCursorAbs);
    }

    // Viewport-first under one budget: a Set's insertion order queued a
    // multi-thousand-row backlog ahead of freshly dirtied window rows, so the
    // visible screen churned through history or froze for seconds on a slow
    // device. flushDrainedThisPass doubles as the budget counter and the
    // forward-progress signal the error-path reschedule reads.
    const inWindow: number[] = [];
    const belowWindow: number[] = [];
    for (const abs of renderQueue) {
      if (abs >= win.base) {
        inWindow.push(abs);
      } else {
        belowWindow.push(abs);
      }
    }
    inWindow.sort((a, b) => a - b);
    belowWindow.sort((a, b) => b - a);
    // No concatenated copy: a spread re-allocated a backlog-sized array per frame.
    const drainRow = (abs: number): boolean => {
      if (flushDrainedThisPass >= MAX_ROWS_PER_FRAME) {
        return false;
      }
      upsertRow(abs);
      renderQueue.delete(abs);
      flushDrainedThisPass++;
      return true;
    };
    for (const abs of inWindow) {
      if (!drainRow(abs)) {
        break;
      }
    }
    for (const abs of belowWindow) {
      if (!drainRow(abs)) {
        break;
      }
    }

    updateTrimMarker();
    // Re-derived every flush rather than maintained incrementally: a page apply,
    // a browse eviction and a tail trim all move gap edges.
    updateGapMarkers();

    if (renderQueue.size > 0) {
      scheduleFlush();
    }

    positionCursorOverlay(store.getLine(cursorAbs));

    if (onCursorMove) {
      onCursorMove();
    }
  }

  // --- Per-view scroll memory (docs/scroll-position-fidelity.md §3) ---
  // A reading position is a LINE, not a pixel offset: a replayed `scrollTop` is
  // silently CLAMPED while a rebuild has built only the live window plus one
  // frame's budget, and it stops meaning the same line once the content grows in
  // the background. `screenTop` is a DIFFERENCE (offsetTop - scrollTop) because
  // rows report offsets in `.term-output`'s space while scrollTop belongs to
  // `.term`; an absolute value would be one padding off.

  // `gen` is the bind generation, so a second switch mid-drain cannot land the
  // first tab's anchor into the second tab's store. `lastWrote` is the offset
  // this module last left behind: a position that does NOT match it was moved by
  // the user, which cancels. Knowing its own writes is how the library never
  // fights a gesture; `onScrollPosition` also fires for the browser's clamps.
  interface PendingRestore {
    view: ViewMemory;
    gen: number;
    lastWrote: number;
    deadline: number;
  }
  let pendingRestore: PendingRestore | null = null;
  let bindGen = 0;
  // Wall-clock of the last inbound frame. `renderQueue.size === 0` alone is NOT
  // "the rebuild finished": it reaches zero BETWEEN a resume batch's replay
  // chunks, so the settle needs transport quiet too.
  let lastInboundMs = 0;

  // Null on the alternate screen: an alt grid has no absolute indices worth
  // restoring, and measuring one would overwrite a tab's real saved position.
  function captureViewMemory(): ViewMemory | null {
    if (!store || store.isAlt()) {
      return null;
    }
    const el = rowAtViewportTop();
    if (el === null) {
      return null;
    }
    const abs = rowAbs(el);
    if (abs < 0) {
      return null;
    }
    return {
      abs,
      screenTop: el.offsetTop - scroll.currentScrollTop(),
      following: !scroll.isUserScrolledUp(),
    };
  }

  /** Drop any armed restore. Called on every bind. */
  function clearPendingRestore(): void {
    pendingRestore = null;
  }

  // Re-asserts an armed view restore, because the anchored row may not be BUILT
  // yet: a reader parked deep in history has their row materialize several
  // frames after the switch. Returns true when it landed (and disarmed).
  function applyPendingRestore(): boolean {
    const p = pendingRestore;
    if (p === null) {
      return false;
    }
    // A newer bind owns the surface, or indices restarted: the saved anchor
    // describes content that is no longer here.
    if (p.gen !== bindGen || fullResetThisPass) {
      clearPendingRestore();
      return false;
    }
    const now = scroll.currentScrollTop();
    if (Math.abs(now - p.lastWrote) > RESTORE_OWN_WRITE_EPSILON_PX) {
      clearPendingRestore(); // the user moved: never fight a gesture
      return false;
    }
    const el = rowEls.get(p.view.abs);
    if (el === undefined) {
      // Not built yet, or gone for good. Give up only once the surface has
      // genuinely settled (queue drained AND transport quiet) or the bound
      // lapses; until then stay armed and try again next frame.
      const quiet = renderQueue.size === 0 && Date.now() - lastInboundMs > RESTORE_SETTLE_MS;
      if (quiet || Date.now() > p.deadline) {
        clearPendingRestore();
      }
      return false;
    }
    // Same arithmetic as the read anchor's drift correction, for the same
    // space-cancelling reason, and idempotent: an already-satisfied restore
    // measures zero and writes nothing.
    const drift = el.offsetTop - now - p.view.screenTop;
    scroll.adjustForContentShift(drift);
    clearPendingRestore();
    return true;
  }

  // The viewport's absolute index for the paging layer's questions about NOW;
  // `pendingRestoreAbs` answers the resume transition, which asks which rows
  // must SURVIVE a switch (docs/scroll-position-fidelity.md §7.2).
  function viewportAbs(): number {
    if (!store) {
      return 0;
    }
    if (!scroll.isUserScrolledUp()) {
      return store.getWindow().base;
    }
    const el = rowAtViewportTop();
    const abs = el === null ? -1 : rowAbs(el);
    return abs >= 0 ? abs : store.getWindow().base;
  }

  function pendingRestoreAbs(): number | null {
    const p = pendingRestore;
    if (p === null) {
      return null;
    }
    // Expire on READ as well as in the flush: an idle surface schedules no
    // flush and would otherwise hold an arm indefinitely.
    if (Date.now() > p.deadline) {
      clearPendingRestore();
      return null;
    }
    return p.view.abs;
  }

  // Every guard below is a pure read and pacing makes a spurious run free, which
  // is deliberate: firing it only when needed is how a trigger ends up never
  // firing on the idle session that needs it most.
  function maybeFetchHistory(): void {
    // The pending-demand timer fires from a CLOCK, so this alt guard is the
    // load-bearing one: without it a vim session would fetch pages nobody can
    // see, and each denial would re-arm the timer (docs/paged-scrollback.md §5.5).
    if (!store || store.isAlt()) {
      return;
    }
    if (requestHistoryFn === null) {
      return; // paging not wired by this consumer
    }
    const budget = historyBudgetFn === null ? PAGE_SIZE : historyBudgetFn();
    if (!Number.isInteger(budget) || budget < 1) {
      return;
    }
    const abs = viewportAbs();
    const edges = store.absentEdgesNear(abs, PREFETCH_THRESHOLD);
    const gap = edges[0];
    if (gap === undefined) {
      return;
    }
    // APPROACH-ANCHORED: always fetch the end NEAREST the reader, so a wide gap
    // heals from the side being read. Fetching a fixed end would land pages up to
    // (gapWidth - budget) lines away from the viewport, leaving the rows under the
    // reader blank while the far end filled in.
    const floor = store.pagingFloorIndex();
    const fromAbs =
      abs >= gap.hi
        ? Math.max(gap.lo, gap.hi - budget, floor) // approaching from BELOW the gap's top
        : Math.max(gap.lo, floor); // approaching from above: start at the gap's low edge
    const maxLines = Math.min(budget, gap.hi - fromAbs);
    if (maxLines < 1) {
      return;
    }
    requestHistoryFn(fromAbs, maxLines);
  }

  // Re-derive the gap markers from the store's geometry, through intervals.ts,
  // the single source of gap geometry the fetch trigger reads too: a second
  // derivation here would have the renderer marking a hole the trigger will not
  // fetch, or omitting one over a hole it will.
  function updateGapMarkers(): void {
    if (!store || !output) {
      return;
    }
    const gaps = new Map<number, number>(); // low index -> high index
    for (const gap of interiorGaps(store.retainedRanges())) {
      gaps.set(gap.lo, gap.hi);
    }
    // Remove markers whose gap closed.
    for (const [lo, el] of [...gapMarkerEls]) {
      if (!gaps.has(lo)) {
        el.remove();
        gapMarkerEls.delete(lo);
      }
    }
    const floor = store.pagingFloorIndex();
    for (const [lo, hi] of gaps) {
      let el = gapMarkerEls.get(lo);
      if (el === undefined) {
        el = document.createElement("div");
        el.className = "term-gap-marker";
        el.setAttribute("role", "status");
        gapMarkerEls.set(lo, el);
      }
      // A gap marker exists to say "do not read these two regions as
      // contiguous", so it cannot fall back to asserting nothing.
      const condemned = floor >= hi;
      const label = condemned ? "earlier output trimmed" : "earlier output not loaded";
      if (el.textContent !== label) {
        el.textContent = label;
        el.setAttribute("aria-label", label);
      }
      el.dataset["abs"] = String(lo);
      el.classList.toggle("term-gap-trimmed", condemned);
      // Position by data-abs order, the same invariant the row inserts keep.
      const next = firstRowAtOrAfter(hi);
      if (el.parentElement !== output || el.nextElementSibling !== next) {
        output.insertBefore(el, next);
      }
    }
  }

  // The top-of-store marker carries no data-abs, so insertRowInOrder never
  // places a row before it. Its frontier is deliberately NOT sourced from gap
  // geometry: the lower edge is a policy question (what is still worth
  // requesting), so an exhausted frontier still renders a marker rather than
  // vanishing when the gap closes (docs/paged-scrollback.md §5.4).
  function updateTrimMarker(): void {
    if (!output) {
      return;
    }
    const label = topMarkerLabel();
    if (label === null) {
      if (trimMarkerEl !== null && trimMarkerEl.parentElement === output) {
        trimMarkerEl.remove();
      }
      return;
    }
    if (trimMarkerEl === null) {
      trimMarkerEl = document.createElement("div");
      trimMarkerEl.className = "term-trim-marker";
      trimMarkerEl.setAttribute("role", "status");
    }
    if (trimMarkerEl.textContent !== label) {
      trimMarkerEl.textContent = label;
      trimMarkerEl.setAttribute("aria-label", label);
    }
    // The interior gap marker's class, so one rule styles "gone" in both places.
    trimMarkerEl.classList.toggle("term-gap-trimmed", label === TRIM_LABEL_GONE);
    if (trimMarkerEl.parentElement !== output || output.firstChild !== trimMarkerEl) {
      output.insertBefore(trimMarkerEl, output.firstChild);
    }
  }

  // Three honest statements about the history above what is held: nothing when
  // index 0 is held; "trimmed" when the client evicted the rows itself with no
  // paging, or when paging proved nothing below survives; "not loaded" when
  // paging is declared and neither proof is in hand (docs/paged-scrollback.md §5.4).
  function topMarkerLabel(): string | null {
    if (!store) {
      return null;
    }
    const oldest = store.oldestIndex();
    if (oldest <= 0) {
      return null; // holds index 0, or holds nothing: no history above it
    }
    if (!store.pagingDeclared()) {
      return store.hasTrimmedHistory() ? TRIM_LABEL_GONE : null;
    }
    const serverOldest = store.serverOldestIndex();
    const condemned =
      store.pagingFloorIndex() >= oldest || (serverOldest >= 0 && serverOldest >= oldest);
    return condemned ? TRIM_LABEL_GONE : TRIM_LABEL_PENDING;
  }

  // upsertRow builds or updates the DOM row for an absolute index, or removes it
  // if the store no longer holds it. New rows are inserted in ascending data-abs
  // order.
  function upsertRow(abs: number): void {
    if (!store) {
      return;
    }
    const runs = store.getLine(abs);
    if (runs === undefined) {
      const stale = rowEls.get(abs);
      if (stale) {
        stale.remove();
        removedRowsThisPass = true;
        rowEls.delete(abs);
      }
      return;
    }
    const spans = buildRowSpans(runs);
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
    if (!output) {
      return;
    }
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
  // The alt row arrays rendered by the last flush, by grid index. Row identity
  // is the store's change signal (applyScreen swaps exactly the changed rows'
  // arrays), so `prev[y] === rows[y]` means row y's DOM is current. A separate
  // mutable container from the store's array, which is mutated in place: the
  // full build snapshots it once and the reconcile path updates only the
  // entries it rebuilt, instead of a fresh `rows.slice()` per alt flush.
  let altPrevRows: (readonly WireRun[])[] = [];

  function renderAlt(rows: readonly (readonly WireRun[])[]): void {
    if (!output) {
      return;
    }
    rowEls.clear();
    // Full (re)build: first alt frame, a grid-height change, or a desynced DOM.
    if (!altRendered || output.children.length !== rows.length) {
      altRendered = true;
      const els: HTMLDivElement[] = [];
      for (const runs of rows) {
        const div = document.createElement("div");
        div.className = "term-row";
        div.replaceChildren(...buildRowSpans(runs));
        els.push(div);
      }
      // The first alt frame removes the entire main-buffer scrollback for one
      // screen of grid, and the alt branch in flushRenderInner returns before
      // the shared bookkeeping, so the flag is set here or not at all. Over-
      // announcing on a resize is harmless: both seams are position-gated.
      removedRowsThisPass = true;
      output.replaceChildren(...els);
      altPrevRows = rows.slice();
      return;
    }
    // Reconcile in place: a TUI that repaints a few lines (vim, a progress bar)
    // touches only those rows' DOM, measured ~50x less flush CPU.
    for (let y = 0; y < rows.length; y++) {
      if (altPrevRows[y] === rows[y]) {
        continue;
      }
      const div = output.children[y] as HTMLDivElement;
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- y < rows.length === children.length
      div.replaceChildren(...buildRowSpans(rows[y]!));
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- same bound as above
      altPrevRows[y] = rows[y]!;
    }
  }

  function stickToBottomIfFollowing(): void {
    scroll.stickToBottom();
  }

  // --- Cursor blink ---
  let blinkInterval: ReturnType<typeof setInterval> | null = null;
  let blinkEnabled = true;
  // What the running interval (if any) is configured as; syncCursorBlink()
  // reconfigures only on mode change, so it is safe to call on every flush.
  let blinkMode: BlinkMode = "off";

  // rAF-driven flushes freeze in a hidden tab, but a plain setInterval keeps
  // firing (throttled), so an idle hidden terminal would keep toggling a class:
  // pointless wakeups that cost battery on mobile.
  function hiddenDoc(): boolean {
    return document.visibilityState === "hidden";
  }

  // No timer when blinking is disabled or the page is hidden (both have
  // event-driven restarts), the fast toggle when the cursor is visible, the slow
  // re-check while the application hides it.
  function desiredBlinkMode(): BlinkMode {
    if (!blinkEnabled || hiddenDoc()) {
      return "off";
    }
    return cursorHidden ? "idle" : "fast";
  }

  // Every mode change resets the phase to solid. The blink class lives on
  // termWrap because the caret overlay is a termWrap child, not inside output,
  // and the `.cursor-blink-off .term-cursor` descendant selector must reach it.
  function syncCursorBlink(): void {
    const el = termWrap;
    if (!el) {
      return;
    }
    const mode = desiredBlinkMode();
    if (mode === blinkMode) {
      return;
    }
    blinkMode = mode;
    if (blinkInterval !== null) {
      clearInterval(blinkInterval);
      blinkInterval = null;
    }
    el.classList.remove("cursor-blink-off");
    if (mode === "fast") {
      blinkInterval = setInterval(() => {
        el.classList.toggle("cursor-blink-off");
      }, CURSOR_BLINK_MS);
    } else if (mode === "idle") {
      blinkInterval = setInterval(syncCursorBlink, CURSOR_RECHECK_MS);
    }
  }

  // --- Font metrics & sizing ---
  function updateFontMetrics(): void {
    // Capture BEFORE the metrics change: a row-height change rescales every
    // offsetTop, and the read anchor only measures drift across a FLUSH's own
    // mutations, which a pure restyle may not schedule at all.
    if (!output || !termWrap) {
      return;
    }
    const before = captureViewMemory();
    const cs = window.getComputedStyle(termWrap);
    padLeft = parseFloat(cs.paddingLeft);
    padTop = parseFloat(cs.paddingTop);
    padValid = true;
    const fontSize = cs.fontSize;
    const family = cs.fontFamily;
    fontString = `${fontSize} ${family}`;
    widthFlat.fill(WIDTH_FLAT_UNSET);
    widthMap.clear();
    resetVariantContexts();
    const measuredW = measureCellWidth(termWrap);
    cellWidth = Math.round(measuredW);
    cellHeight = parseFloat(cs.lineHeight) || 17;
    measuredCols = 0;
    defaultSpacing = cellWidth - measuredW;
    output.style.letterSpacing = `${defaultSpacing}px`;
    termWrap.style.setProperty("--char-w", `${cellWidth}px`);
    // An armed restore SURVIVES this call and its baseline must be refreshed:
    // the reflow that provoked the call has already moved scrollTop, and a stale
    // lastWrote makes the next flush read that as a gesture and cancel it.
    if (pendingRestore !== null) {
      pendingRestore.lastWrote = scroll.currentScrollTop();
    }
    // A holding reader's line is restored through the tab-switch machinery, but
    // NEVER over an armed restore: a resize settle and a tab switch routinely
    // overlap, and an anchor measured mid-rebuild is a transient.
    if (before !== null && !before.following && pendingRestore === null) {
      pendingRestore = {
        view: before,
        gen: bindGen,
        lastWrote: scroll.currentScrollTop(),
        deadline: Date.now() + RESTORE_MAX_MS,
      };
      scheduleFlush();
    }
  }

  // The column count of the most recent measurement. gridSize() reads it
  // instead of measuring because it runs on every raw mousemove under motion
  // tracking, and computeSize() costs a computed-style read plus two layout reads.
  let measuredCols = 0;

  function computeSize(): { cols: number; rows: number } {
    if (!termWrap) {
      return { cols: 0, rows: 0 };
    }
    const cs = window.getComputedStyle(termWrap);
    const padX = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
    const padY = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    const contentW = termWrap.clientWidth - padX;
    const contentH = termWrap.clientHeight - padY;
    const cols = Math.max(MIN_COLS, Math.floor(contentW / cellWidth));
    const rows = Math.max(MIN_ROWS, Math.floor(contentH / cellHeight));
    measuredCols = cols;
    return { cols, rows };
  }

  // Rows are the store's, not `computeSize()`'s: the server's screen is
  // last-writer-wins across attached clients, so the measured height can
  // describe a grid nothing is painting, and the mouse hit test ANCHORS on this
  // number. Columns stay measured: the store has none (trailing blanks are
  // trimmed off the wire) and that axis only bounds a report.
  function gridSize(): { cols: number; rows: number } {
    if (!store) {
      return { cols: 0, rows: 0 };
    }
    const rows = store.isAlt() ? store.getAltRows().length : store.getWindow().height;
    return { cols: measuredCols > 0 ? measuredCols : computeSize().cols, rows };
  }

  // A row's top in termWrap's coordinate space, which every absolutely
  // positioned overlay resolves against. A bare `offsetTop` is relative to the
  // offsetParent, and a stylesheet that positions `.term-output` puts rows in
  // output-space, one padding short (the caret floated 4px above every glyph).
  // Walking the offsetParent chain is correct under either stylesheet.
  function rowTopInTermWrap(el: HTMLElement): number {
    const wrap = termWrap;
    if (!wrap) {
      return 0;
    }
    let top = 0;
    let node: Element | null = el;
    while (node instanceof HTMLElement && node !== wrap) {
      top += node.offsetTop;
      node = node.offsetParent;
      // An intermediate parent's offsetTop locates its BORDER box while
      // offsetTop is measured from the padding edge; termWrap's own absolute
      // children resolve from its padding edge already.
      if (node instanceof HTMLElement && node !== wrap) {
        top += node.clientTop;
      }
    }
    // Chain ended off termWrap (display:none, detached): the rect delta is
    // space-independent, re-based into content space by scrollTop.
    if (node !== wrap) {
      return el.getBoundingClientRect().top - wrap.getBoundingClientRect().top + wrap.scrollTop;
    }
    return top;
  }

  // The ONE resolver for the caret, the predicted cursor and `getCursorPx`, so
  // the consumer's IME view is never told a different row than the caret paints
  // on (`rowEls` is empty for a whole alt-screen session). A built row answers
  // from its DOM offset; a real index with no row from grid arithmetic; `abs <
  // 0` is not a row at all and the content origin IS the accurate answer.
  function rowTopFor(abs: number): number {
    const el = rowEls.get(abs);
    if (el) {
      return rowTopInTermWrap(el);
    }
    const { padT } = termPadding();
    if (abs < 0 || !store) {
      return padT;
    }
    return padT + (abs - store.getWindow().base) * cellHeight;
  }

  function getCursorPx(): { left: number; top: number; cellH: number } {
    if (disposed) {
      return { left: 0, top: 0, cellH: 0 };
    }
    const { padL } = termPadding();
    return {
      left: Math.round(padL + cursorCol * cellWidth),
      top: Math.round(rowTopFor(cursorAbs)),
      cellH: cellHeight,
    };
  }

  function cellSize(): { width: number; height: number } {
    return disposed ? { width: 0, height: 0 } : { width: cellWidth, height: cellHeight };
  }

  // --- Caret overlay: one absolutely positioned child of termWrap ---
  let cursorEl: HTMLElement | null = null;

  function ensureCursorEl(): HTMLElement {
    if (cursorEl === null) {
      cursorEl = document.createElement("div");
      cursorEl.setAttribute("aria-hidden", "true");
      termWrap?.appendChild(cursorEl);
    }
    return cursorEl;
  }

  // The character at `col`, advancing columns exactly like buildRowSpans (the
  // \uFFFF placeholder occupies a wide char's second cell). A miss reads as a
  // space, so the block cursor paints an inverted blank like a real terminal.
  function glyphAt(runs: readonly WireRun[] | undefined, col: number): string {
    if (!runs) {
      return " ";
    }
    let c = 0;
    for (const run of runs) {
      if (!run.t) {
        continue;
      }
      for (const ch of run.t) {
        if (c === col) {
          return ch === "\uFFFF" ? " " : ch;
        }
        c++;
      }
    }
    return " ";
  }

  // `runs` is the cursor row's content (main: store.getLine; alt: the grid row);
  // the block style copies the glyph under the cursor so the inverted cell reads
  // as text. Hidden when the cursor is hidden or the screen is empty.
  function positionCursorOverlay(runs: readonly WireRun[] | undefined): void {
    const el = ensureCursorEl();
    if (cursorHidden || cursorAbs < 0) {
      el.className = "term-cursor-overlay";
      return;
    }
    const { padL } = termPadding();
    const top = rowTopFor(cursorAbs);
    const ch = glyphAt(runs, cursorCol);
    // A wide glyph (CJK, emoji) owns two cells; the overlay covers both.
    const wide = measureChar(ch, false, false) > cellWidth * 1.5;
    el.textContent = ch === " " ? "\u00a0" : ch;
    el.className = `term-cursor-overlay visible ${cursorClassName()}`;
    el.style.left = `${Math.round(padL + cursorCol * cellWidth)}px`;
    el.style.top = `${Math.round(top)}px`;
    el.style.width = `${wide ? cellWidth * 2 : cellWidth}px`;
    el.style.height = `${cellHeight}px`;
    el.style.lineHeight = `${cellHeight}px`;
  }

  let predCursorEl: HTMLElement | null = null;

  // Renderer-owned, so the engine never depends on a host-provided scaffold
  // element; styled by the consumer's `.pred-cursor` rule.
  function createPredCursorEl(): HTMLElement {
    const el = document.createElement("div");
    el.className = "pred-cursor";
    el.setAttribute("aria-hidden", "true");
    termWrap?.appendChild(el);
    return el;
  }

  function setPredictedCursor(row: number, col: number, active: boolean): void {
    if (!store) {
      return;
    }
    const el = predCursorEl ?? (predCursorEl = createPredCursorEl());
    const win = store.getWindow();
    const predAbs = win.base + row;
    if (!active || (predAbs === cursorAbs && col === cursorCol)) {
      el.classList.remove("visible");
      return;
    }
    const { padL } = termPadding();
    // A prediction below the first line of the session (`predAbs < 0`) lands on
    // the content origin; a negative top would paint over the top padding.
    const top = rowTopFor(predAbs);
    el.style.left = `${Math.round(padL + col * cellWidth)}px`;
    el.style.top = `${Math.round(top)}px`;
    el.style.width = `${cellWidth}px`;
    el.style.height = `${cellHeight}px`;
    el.classList.add("visible");
  }

  function updateReverseVideo(): void {
    if (!termWrap) {
      return;
    }
    if (modes.isReverseVideo()) {
      termWrap.classList.add("term-reverse-video");
    } else {
      termWrap.classList.remove("term-reverse-video");
    }
  }

  // Pause the blink interval while hidden; resume (cursor solid, phase reset)
  // when the tab is foregrounded again. blinkEnabled/cursorHidden keep their
  // server-driven state across a background stint.
  const onVisibilityChange = (): void => {
    syncCursorBlink();
  };

  function dispose(): void {
    if (disposed) {
      return;
    }
    disposed = true;
    if (pendingFrame !== undefined) {
      cancelAnimationFrame(pendingFrame);
      pendingFrame = undefined;
    }
    if (blinkInterval !== null) {
      clearInterval(blinkInterval);
      blinkInterval = null;
    }
    blinkMode = "off";
    document.removeEventListener("visibilitychange", onVisibilityChange);
    resetVariantContexts();
    widthFlat.fill(WIDTH_FLAT_UNSET);
    widthMap.clear();
    rowEls.clear();
    for (const el of gapMarkerEls.values()) {
      el.remove();
    }
    gapMarkerEls.clear();
    renderQueue.clear();
    requestHistoryFn = null;
    historyBudgetFn = null;
    onCursorMove = null;
    pendingRestore = null;
    store = null;
    altPrevRows = [];
    altRendered = false;
    trimMarkerEl?.remove();
    trimMarkerEl = null;
    cursorEl?.remove();
    cursorEl = null;
    predCursorEl?.remove();
    predCursorEl = null;
    if (output) {
      output.replaceChildren();
      output.style.letterSpacing = "";
    }
    if (termWrap) {
      termWrap.classList.remove("cursor-blink-off", "term-reverse-video");
      termWrap.style.removeProperty("--char-w");
    }
    output = null;
    termWrap = null;
  }

  try {
    document.addEventListener("visibilitychange", onVisibilityChange);
    syncCursorBlink();
  } catch (err) {
    dispose();
    throw err;
  }

  return {
    resetScreen,
    resetScrollback,
    bind,
    boundStore,
    rebuild,
    getHighestIndex,
    getReplayBoundary,
    noteResumeBounds,
    handleHistoryReply,
    applyResumeTransition,
    noteSolicited,
    clearSolicited,
    dropBrowseCache,
    lastBrowseActivityMs,
    browseCacheSize,
    replayMaxForResume,
    pendingRowCount,
    handleScreen,
    handleScroll,
    handleScrollPosition,
    captureViewMemory,
    pendingRestoreAbs,
    maybeFetchHistory,
    updateFontMetrics,
    computeSize,
    gridSize,
    getCursorPx,
    cellSize,
    setPredictedCursor,
    updateReverseVideo,
    dispose,
  };
}
