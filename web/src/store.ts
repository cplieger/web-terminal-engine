// Absolute-index line store: the client's model of the terminal. Lines are
// keyed by absolute index with the live screen window sliding along the tail,
// so there is no separate scrollback. Pure: no DOM, no globals.

import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";
import { interiorGaps, retainedIntervals, type Interval } from "./intervals.js";
import { replayStart } from "./replay.js";

/**
 * The tail cap held while paging is unconfirmed, so a server that cannot serve
 * history back never has its reachable history cut. `MAX_LINES` derives from it.
 */
export const COMPATIBILITY_TAIL_CAP = 5000;

/** Maximum lines retained client-side when the consumer sets no cap. */
const MAX_LINES = COMPATIBILITY_TAIL_CAP;

/**
 * Shape version of a persisted snapshot; `fromSnapshot` discards any other, so a
 * stale snapshot costs one full resume instead of being misread. An OPTIONAL
 * field whose absence means the old behaviour needs no bump (`unconfirmedFrom`):
 * the test is that a previous release's entry still hydrates to the same store.
 */
const SNAPSHOT_VERSION = 1;

/**
 * A persisted line store as plain, structuredClone-safe data. Deliberately
 * absent: the live window (`inWindow()` exempts its rows from the staleness
 * guard and from eviction, so a stale one misbehaves; the first screen frame
 * restores it), `serverOldest` (the next resumeAck supplies it) and the
 * alternate screen (not history).
 */
export interface StoreSnapshot {
  /** Shape version; must equal SNAPSHOT_VERSION or the snapshot is discarded. */
  v: number;
  /**
   * The server boot epoch these lines belong to, always positive. Indices are
   * only meaningful within one server process, so a restored store's first
   * resumeAck under another epoch wipes the lines instead of showing them as live.
   */
  serverEpoch: number;
  /** Lowest absolute index in `lines`. Write-side metadata for the consumer's
   *  storage layer; `fromSnapshot` derives the bounds from the pairs instead. */
  oldest: number;
  /** Highest absolute index in `lines`. Write-side metadata; see `oldest`. */
  highest: number;
  /**
   * The saving store's provisional floor (`LineStore.unconfirmedFrom`), absent
   * when everything held was confirmed, which is also how an older snapshot
   * reads. Without it a reloaded page would tell the server not to re-send screen
   * rows it only saw the app draw. A malformed value is read as absent.
   */
  unconfirmedFrom?: number;
  /** The retained lines as ascending [absoluteIndex, runs] pairs (object keys
   *  would stringify the indices). */
  lines: [number, WireRun[]][];
}

/**
 * isWireRunArray validates a run array decoded from outside this program (a
 * snapshot). The wire decoder builds runs field by field, so only this path can
 * admit a run whose `t` is not a string, which throws in the renderer's row
 * build, blocks every row above it and persists across reloads. Optional
 * numeric fields are checked, not coerced.
 */
function isWireRunArray(value: unknown): value is WireRun[] {
  if (!Array.isArray(value)) {
    return false;
  }
  for (const run of value) {
    if (typeof run !== "object" || run === null) {
      return false;
    }
    const r = run as Record<string, unknown>;
    if (typeof r["t"] !== "string") {
      return false;
    }
    for (const field of ["f", "b", "uc", "a"]) {
      const v = r[field];
      if (v !== undefined && (typeof v !== "number" || !Number.isFinite(v))) {
        return false;
      }
    }
    if (r["u"] !== undefined && typeof r["u"] !== "string") {
      return false;
    }
  }
  return true;
}

/**
 * evictionBatch is how many lines an at-cap eviction pass nominally frees: 1 for
 * tiny test stores, 256 for the real 5000-line store. Each evicted line removes a
 * DOM row from the top of the scroller and invalidates the whole scrolled layer
 * (WebKit re-rasterizes its tiles), so batching shifts the layer once per batch.
 * Headroom comes from history only, never the live window.
 */
function evictionBatch(maxLines: number): number {
  return Math.min(256, Math.max(1, Math.floor(maxLines / 16)));
}

// Demand-paged scrollback: a resident TAIL plus a disposable BROWSE CACHE of
// fetched pages, each with its own budget; neither can evict the other's lines.

/** The resident-tail target once the server declares paging. */
export const RESIDENT_TAIL_CAP = 1500;

/** Lines of paged-in history retained as disposable cache. Engine-internal. */
export const BROWSE_CACHE_CAP = 2500;

/**
 * How near an absent range's edge the viewport comes before a fetch, and the
 * radius exempt from eviction around the reader: the same band.
 */
export const PREFETCH_THRESHOLD = 500;

/** Lines requested per page (the server's, wire-golden/limits.json), and the ceiling on one
 *  eviction pass's removals. */
export const PAGE_SIZE = 1000;

/** The viewport exemption stays below the cache cap, or an eviction pass with
 *  work to do could find no victim. */
if (2 * PREFETCH_THRESHOLD + 1 >= BROWSE_CACHE_CAP) {
  throw new Error(
    `paged-scrollback invariant violated: the viewport exemption (${2 * PREFETCH_THRESHOLD + 1} lines) ` +
      `must stay below browseCacheCap (${BROWSE_CACHE_CAP})`,
  );
}

/** The live screen window at the tail of the buffer. It describes the MAIN
 *  screen even while alt is active (the alt grid's geometry lives in altRows). */
export interface WindowState {
  /** Absolute index of window row 0 (the MAIN screen's; frozen during alt). */
  base: number;
  /** Rows in the MAIN screen's window, kept through an alt session's resize. */
  height: number;
  /** Cursor row within the window (0..height-1). */
  cursorRow: number;
  /** Cursor column within the window. */
  cursorCol: number;
  /** DECSCUSR cursor style (0-6). */
  cursorStyle: number;
  /** Cursor hidden (DECTCEM off). */
  cursorHidden: boolean;
  /** Cursor blinking. */
  cursorBlink: boolean;
}

/** What changed since the last drain, for the renderer to apply to the DOM. */
export interface StoreChanges {
  /** Absolute indices whose content changed (need a DOM row build/update). */
  dirtyLines: number[];
  /** Absolute indices removed from the store (need their DOM row dropped). */
  evictedLines: number[];
  /** The window descriptor or cursor changed. */
  windowChanged: boolean;
  /** The alternate-screen grid or its active state changed. */
  altChanged: boolean;
  /** A full reset happened (server restart): the renderer must wipe all rows. */
  fullReset: boolean;
}

function emptyWindow(): WindowState {
  return {
    base: 0,
    height: 0,
    cursorRow: 0,
    cursorCol: 0,
    cursorStyle: 0,
    cursorHidden: false,
    cursorBlink: false,
  };
}

/** Deep-equality for two style runs (all wire fields). */
function runEqual(a: WireRun, b: WireRun): boolean {
  return a.t === b.t && a.f === b.f && a.b === b.b && a.a === b.a && a.uc === b.uc && a.u === b.u;
}

/** Deep-equality for two rows of runs. */
function rowEqual(a: WireRun[], b: WireRun[]): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- index < length
    if (!runEqual(a[i]!, b[i]!)) {
      return false;
    }
  }
  return true;
}

/**
 * The client's model of one terminal: lines keyed by absolute index, the live
 * window at the tail, and the change set a renderer drains each frame. Applying
 * is idempotent by index; a frame that breaks an invariant is dropped silently,
 * so callers ask what is held (oldestIndex, retainedRanges). Feed every frame in
 * arrival order, drain once per frame (nothing is re-reported), and treat read
 * runs as views valid until the next apply.
 */
export class LineStore {
  private lines = new Map<number, WireRun[]>();
  private oldest = -1; // lowest retained absolute index (-1 = empty)
  private highest = -1; // highest retained absolute index (-1 = empty)
  private everEvictedThrough = -1; // highest absolute index ever evicted; lines <= this are stale
  private serverOldest = -1; // oldest index the server still retains (from resumeAck); for trim marker

  private win: WindowState = emptyWindow();
  private alt = false;
  private altRows: WireRun[][] = [];

  // Change tracking, drained by the renderer each frame.
  private dirty = new Set<number>();
  private evicted = new Set<number>();
  private windowDirty = false;
  private altDirty = false;
  private resetPending = false;

  // --- demand-paged scrollback ---

  /** The resident-tail cap: the compatibility value until `applyResumeAck` flips
   *  it one-way to the supported target; every residency reader uses it. */
  private effectiveTailCap: number;

  /** The post-flip target: what the tail shrinks to once paging is declared. */
  private readonly supportedTarget: number;

  /** The pre-flip cap: what the tail holds while paging is unconfirmed. */
  private readonly compatibilityCap: number;

  /**
   * The keys held as BROWSE CACHE, a subset of `lines`' keys: membership, size
   * and eviction candidates all read it, so every query is O(cache), never
   * O(span) (a replay-jump hull can span millions of indices). Not a guard-2
   * bypass. Invariant `browse ⊆ lines.keys()`: `forget` is the only deletion path
   * and deletes from both.
   */
  private browse = new Set<number>();

  /** The in-flight request's window, or null: lines inside it apply even below
   *  the eviction watermark (a page is a legitimate re-fetch). One slot. */
  private solicitedPending: Interval | null = null;

  /** The lowest index still worth requesting: raised by a clamped or empty reply,
   *  lowered only by a resumeAck reporting an older retained edge. */
  private pagingFloor = 0;

  /** When browse cache was last created or read, for the consumer's TTL. */
  private browseActivityMs = 0;

  /** Whether the last resumeAck declared paging (the renderer's top marker). */
  private paging = false;

  /**
   * The highest index the application erased (ED3), or -1. Separate from
   * `everEvictedThrough`, which marks budget trims and drives the "earlier output
   * trimmed" marker ED3 must not show; without it a late page reply lands as tail
   * and never leaves. Safe within an epoch: the server never reuses an index.
   */
  private erasedThrough = -1;

  /**
   * Memo for `retainedRanges()`: the scroll seam asks on every scroll event and
   * the renderer on every flush, and coalescing sorts the residency (95 us at
   * 4500 rows). Cleared by the only insertion (`applyLine`), the only deletion
   * (`forget`) and `reset`; a snapshot hydrate builds a store with no memo yet.
   */
  private rangesMemo: Interval[] | null = null;

  /** The server process (boot epoch) the content belongs to, or 0 when unknown. */
  private epoch = 0;

  /** The epoch came from a snapshot, so the next resumeAck must confirm it. */
  private epochSeeded = false;

  /**
   * @param maxLines  the history budget, floored at the live window, or OMITTED
   *                  for the engine's choice. Omitted is not a default value: an
   *                  explicit 5000 holds in every capability state, while omitted
   *                  flips to the smaller target once paging is declared.
   */
  constructor(maxLines?: number) {
    // Omitted: the legacy cap until paging is declared, then the small target.
    // Supplied: the consumer's memory decision, held in every state.
    this.compatibilityCap = maxLines ?? MAX_LINES;
    this.supportedTarget = maxLines ?? RESIDENT_TAIL_CAP;
    this.effectiveTailCap = this.compatibilityCap;
  }

  /** Lines held in the resident tail (everything not classified browse). */
  private get tailCount(): number {
    return this.lines.size - this.browse.size;
  }

  /** The effective resident-tail cap, floored at the live window's height. */
  private get tailBound(): number {
    return Math.max(this.effectiveTailCap, this.win.height);
  }

  /** Highest absolute index held, or -1 if empty. */
  highestIndex(): number {
    return this.highest;
  }

  /**
   * Lowest index the server has not confirmed as committed (+Infinity when all
   * is). Screen-frame rows are provisional until they scroll off; scroll and
   * history rows are committed. They form a suffix, so one number suffices: it
   * lowers on any screen frame and rises only when a durable frame reaches it.
   */
  private unconfirmedFrom = Number.POSITIVE_INFINITY;

  /**
   * The highest index this client asks the server not to re-send: send it as
   * `haveThrough`, never `highestIndex()`, whose window rows hold what the app
   * drew and would surface a stale screen as scrollback. Always `<= highestIndex()`.
   */
  replayBoundary(): number {
    if (this.highest < 0) {
      return this.highest;
    }
    return Math.min(this.highest, this.unconfirmedFrom - 1);
  }

  /**
   * The boot epoch of the server process the content belongs to, from the last
   * resumeAck that reported one or from the snapshot it was restored from; 0
   * when unknown.
   */
  serverEpoch(): number {
    return this.epoch;
  }

  /** True while the store holds content restored by `fromSnapshot` that no
   *  resumeAck has confirmed or discarded yet. */
  isUnconfirmedRestore(): boolean {
    return this.epochSeeded && this.highest >= 0;
  }

  /** Lowest absolute index held, or -1 if empty. */
  oldestIndex(): number {
    return this.oldest;
  }

  /** Whether older history was trimmed, here or by the server (the top marker). */
  hasTrimmedHistory(): boolean {
    if (this.oldest > 0 && this.everEvictedThrough >= 0) {
      return true; // we evicted the oldest lines ourselves
    }
    return this.serverOldest > 0 && this.oldest >= this.serverOldest;
  }

  /** Current live-window descriptor (cursor, base, height). */
  getWindow(): WindowState {
    return { ...this.win };
  }

  /** Whether the alternate screen is active. */
  isAlt(): boolean {
    return this.alt;
  }

  /** The alt grid's rows, uncopied and read-only: an unchanged row keeps its
   *  identity across frames, which lets the renderer reconcile row by row. */
  getAltRows(): readonly (readonly WireRun[])[] {
    return this.altRows;
  }

  /** Iterate retained lines in ascending index order, skipping holes. */
  forEachLine(cb: (abs: number, runs: WireRun[]) => void): void {
    if (this.oldest < 0) {
      return;
    }
    // Sorted keys, not the integer range: a far-jumping base would make the range
    // ~2^53 wide and freeze the tab.
    const keys = [...this.lines.keys()].sort((a, b) => a - b);
    for (const abs of keys) {
      const runs = this.lines.get(abs);
      if (runs !== undefined) {
        cb(abs, runs);
      }
    }
  }

  /** Read a single retained line by absolute index, or undefined. */
  getLine(abs: number): WireRun[] | undefined {
    return this.lines.get(abs);
  }

  /**
   * Apply a decoded screen frame: update the window descriptor and cursor,
   * route to the alt grid when the alternate screen is active, and apply each
   * changed window row at its absolute index (base + y).
   */
  applyScreen(msg: ScreenMessage): void {
    // ED3 before the alt dispatch: the server attaches it to whatever frame it
    // builds next, alt included. `msg.base` bounds either buffer.
    if (msg.scrollbackCleared) {
      this.applyScrollbackCleared(msg.base);
    }
    // A rows-less frame is a signal (a pre-ack ED3), not geometry: a zero-height
    // window would hand truncateBelowWindow every row and collapse the alt grid.
    if (msg.rows.length === 0) {
      return;
    }
    if (msg.altActive) {
      this.enterAltIfNeeded(msg.rows.length);
      for (const y of msg.changed) {
        const row = msg.rows[y];
        if (y >= 0 && y < this.altRows.length && row !== undefined) {
          this.altRows[y] = row;
          this.altDirty = true;
        }
      }
      this.updateWindowCursor(msg);
      return;
    }
    this.exitAltIfNeeded();
    this.updateWindow(msg);
    // Every row of this window is provisional, changed or not: the app repaints
    // these positions in place.
    this.markUnconfirmedFrom(msg.base);
    for (const y of msg.changed) {
      const row = msg.rows[y];
      if (row !== undefined) {
        this.applyLine(msg.base + y, row);
      }
    }
    this.truncateBelowWindow();
    // A height shrink with identical rows applies no line, yet lowers the bound.
    this.enforceCap();
  }

  /** Apply a decoded scroll/history frame: each line at firstIndex + i. */
  applyScroll(msg: ScrollMessage): void {
    if (this.alt) {
      // In alt, rows below the frozen MAIN window base are main-screen history
      // (a resume replay, lines committed in the pass that entered alt, a
      // durable chunk that raced an alt flip) and are stored; they surface at
      // alt exit's rebuild. Rows at or above the base break the protocol (no
      // live scroll during alt) and are dropped to keep the frozen window
      // intact. With no main window seen (attaching straight into alt) every
      // row is accepted; the alt-exit full frame overwrites the window region
      // either way.
      const base = this.win.height > 0 ? this.win.base : Number.POSITIVE_INFINITY;
      for (let i = 0; i < msg.lines.length; i++) {
        const abs = msg.firstIndex + i;
        const row = msg.lines[i];
        if (row !== undefined && abs < base) {
          this.applyLine(abs, row);
        }
      }
      // Confirm only the accepted prefix: rows dropped at or above the base are not
      // committed history here.
      this.confirmRange(msg.firstIndex, Math.min(msg.lines.length, base - msg.firstIndex));
      return;
    }
    for (let i = 0; i < msg.lines.length; i++) {
      const row = msg.lines[i];
      if (row !== undefined) {
        this.applyLine(msg.firstIndex + i, row);
      }
    }
    this.confirmRange(msg.firstIndex, msg.lines.length);
  }

  /** Lower the provisional floor to `base`: the app drew everything from there up. */
  private markUnconfirmedFrom(base: number): void {
    if (!Number.isInteger(base) || base < 0) {
      return; // a malformed frame must not move the floor
    }
    if (base < this.unconfirmedFrom) {
      this.unconfirmedFrom = base;
    }
  }

  /**
   * Raise the provisional floor past a range the server committed. A range above
   * the floor leaves rows between still provisional, so the floor stays and the
   * next resume asks again. Heal: a floor below `oldest` (after ED3, a clamped
   * replay or ring eviction) protects nothing held, so a range above `oldest`
   * raises it; without it every attach replays maximally forever.
   */
  private confirmRange(firstIndex: number, count: number): void {
    if (!Number.isInteger(firstIndex) || firstIndex < 0 || count <= 0) {
      return;
    }
    const end = firstIndex + count;
    if (firstIndex > this.unconfirmedFrom) {
      if (this.oldest >= 0 && this.unconfirmedFrom < this.oldest) {
        this.unconfirmedFrom = Math.max(end, this.oldest);
      }
      return; // otherwise a gap below stays provisional
    }
    if (end > this.unconfirmedFrom) {
      this.unconfirmedFrom = end;
    }
  }

  /** Record the server's oldest retained index from a resumeAck, so the trim
   *  marker tells a real trim from loading. */
  noteResumeBounds(oldestIndex: number): void {
    if (Number.isInteger(oldestIndex) && oldestIndex >= 0) {
      this.serverOldest = oldestIndex;
      // A ring's oldest only rises within an epoch, so a lower report repairs a floor
      // raised by a mis-correlated clamp.
      if (oldestIndex < this.pagingFloor) {
        this.pagingFloor = oldestIndex;
      }
    }
  }

  // --- demand-paged scrollback: public surface ---

  /** Whether the last resumeAck declared demand paging. */
  pagingDeclared(): boolean {
    return this.paging;
  }

  /** The oldest index the server reported retaining, or -1 if unknown. */
  serverOldestIndex(): number {
    return this.serverOldest;
  }

  /** The lowest index still worth requesting. */
  pagingFloorIndex(): number {
    return this.pagingFloor;
  }

  /** Lines currently classified as browse cache. */
  browseCacheSize(): number {
    return this.browse.size;
  }

  /** The effective resident-tail cap (post-flip once paging is declared). */
  tailCap(): number {
    return this.effectiveTailCap;
  }

  /** When browse cache was last created or refreshed, for the consumer's TTL. */
  lastBrowseActivityMs(): number {
    return this.browseActivityMs;
  }

  /** Record the window of a request about to go out; a second call replaces it. */
  noteSolicited(fromAbs: number, end: number): void {
    if (!Number.isSafeInteger(fromAbs) || !Number.isSafeInteger(end) || end <= fromAbs) {
      return;
    }
    this.solicitedPending = { lo: fromAbs, hi: end };
  }

  /** Release the in-flight window (reply applied, timed out, or socket gone). */
  clearSolicited(): void {
    this.solicitedPending = null;
  }

  /** The retained ranges coalesced from the key set: the gap geometry's authority. */
  retainedRanges(): Interval[] {
    this.rangesMemo ??= retainedIntervals(this.lines.keys());
    // A copy: a caller mutating the result must not corrupt the memo.
    return this.rangesMemo.map((iv) => ({ ...iv }));
  }

  /**
   * The absent edges within `threshold` of `abs`, nearest first: interior holes
   * and the bottom frontier `[pagingFloor, oldestHeld)`, the latter only while the
   * server still retains something older. Gaps at or below the floor are omitted.
   */
  absentEdgesNear(abs: number, threshold: number): Interval[] {
    const runs = this.retainedRanges();
    if (runs.length === 0) {
      return [];
    }
    const candidates = interiorGaps(runs);
    const lowest = runs[0]?.lo ?? 0;
    if (
      lowest > 0 &&
      lowest > this.pagingFloor &&
      (this.serverOldest < 0 || lowest > this.serverOldest)
    ) {
      candidates.push({ lo: this.pagingFloor, hi: lowest });
    }
    // Test the high edge: after ring exhaustion and a tail trim the frontier's low
    // edge equals the floor, and it must stay fetchable.
    const live = candidates.filter((g) => g.hi > this.pagingFloor && g.hi > g.lo);
    const near = live.filter((g) => abs >= g.lo - threshold && abs <= g.hi + threshold);
    near.sort((a, b) => Math.abs(abs - a.hi) - Math.abs(abs - b.hi));
    return near;
  }

  /**
   * Apply a correlated history page: what it stores is BROWSE cache, the cap is
   * enforced once at the end, and only the solicited window gets a page's
   * exemptions. `viewportAbs`, from the renderer, picks which cache end to evict.
   */
  applyHistoryScroll(msg: ScrollMessage, viewportAbs: number): void {
    // The alt gate, as in applyScroll: only rows below the frozen main base.
    const altBase = this.alt && this.win.height > 0 ? this.win.base : Number.POSITIVE_INFINITY;

    // Only lines inside the in-flight window get solicited treatment; the rest of a
    // stale, wider reply goes through the ordinary guards.
    const s = this.solicitedPending;
    if (s === null) {
      // No request out: drop the reply whole. The ordinary path would admit it (after
      // ED3 a reprint at the same indices must apply), and only here is it known to
      // answer a retracted request (ED3, an epoch reset, a closed socket).
      return;
    }
    const applied: number[] = [];
    for (let i = 0; i < msg.lines.length; i++) {
      const abs = msg.firstIndex + i;
      const row = msg.lines[i];
      if (row === undefined || abs >= altBase) {
        continue;
      }
      if (abs < s.lo || abs >= s.hi) {
        // Outside the solicited window: not this request's content.
        this.applyLine(abs, row);
        continue;
      }
      this.applyLine(abs, row, true);
      if (this.lines.has(abs)) {
        applied.push(abs);
      }
    }
    if (applied.length === 0) {
      // An empty or fully-refused reply changes no classification. The caller
      // still owns the floor raise for an empty reply.
      this.enforceCap();
      return;
    }
    // Keys already cached are no-ops, so overlap cannot inflate the cache.
    this.classifyAsBrowse(applied);
    // One budget pass: the invariants hold at operation boundaries, not mid-apply.
    this.enforceBrowseBudget(BROWSE_CACHE_CAP, viewportAbs);
    this.enforceCap();
  }

  /**
   * Move held keys into the browse classification: a reclassification, not a
   * trim (nothing deleted, `everEvictedThrough` untouched). It takes keys, not a
   * range, so no caller walks a span. Unheld keys are skipped.
   */
  private classifyAsBrowse(keys: Iterable<number>): number {
    let added = 0;
    for (const abs of keys) {
      if (this.lines.has(abs) && !this.browse.has(abs)) {
        this.browse.add(abs);
        added++;
      }
    }
    if (added > 0) {
      this.browseActivityMs = Date.now();
    }
    return added;
  }

  /**
   * Evict browse cache down to `target`: exactly the overflow (at most a page per
   * step; more thrashes fetch and evict), from the cache end farther from
   * `viewportAbs`, never within PREFETCH_THRESHOLD of it. When only the reader's
   * neighbourhood remains the pass stops and accepts the bounded overshoot.
   */
  private enforceBrowseBudget(target: number, viewportAbs: number): void {
    // Reachable: the resume-ack pass targets PREFETCH_THRESHOLD, below the band's
    // width, so runs either side of a reader can all be exempt yet over target.
    const exemptLo = viewportAbs - PREFETCH_THRESHOLD;
    const exemptHi = viewportAbs + PREFETCH_THRESHOLD + 1;
    const exempt = (abs: number) => abs >= exemptLo && abs < exemptHi;

    while (this.browse.size > target) {
      const keys = [...this.browse].sort((a, b) => a - b);
      const budget = Math.min(this.browse.size - target, PAGE_SIZE);
      // The end farther from the reader goes; on a tie the older end.
      const lowest = keys[0] ?? 0;
      const highest = keys[keys.length - 1] ?? 0;
      const fromTop = Math.abs(highest - viewportAbs) > Math.abs(viewportAbs - lowest);
      const victims: number[] = [];
      const walk = fromTop ? keys.reverse() : keys;
      for (const abs of walk) {
        if (victims.length >= budget || exempt(abs)) {
          break; // the exemption is contiguous, so the rest of this end is too
        }
        victims.push(abs);
      }
      if (victims.length === 0) {
        break; // the reader's own neighbourhood is all that is left: overshoot
      }
      for (const abs of victims) {
        // A cache drop, not a trim: everEvictedThrough stays, so it can be re-fetched.
        this.forget(abs);
      }
      this.recomputeBounds();
    }
  }

  /**
   * The resume-ack transition as one operation, because its step order matters.
   * `committed`/`serverOldest` are null as a pair on an older server's ack, which
   * then runs only the epoch check and the capability read. Returns true when the
   * content was discarded: another process's, or restored and not confirmed.
   */
  applyResumeAck(ack: {
    /** The acking server's boot epoch, or null when the ack carries none. */
    serverEpoch: number | null;
    /** One past the server's newest committed line, or null on an old ack. */
    committed: number | null;
    /** The server's oldest retained index, or null on an old ack. */
    serverOldest: number | null;
    /** The server DECLARED demand paging (ackFlags bit1). */
    paging: boolean;
    /** The `haveThrough` this socket SENT, which the reply is a function of. */
    sentHaveThrough: number;
    /** The `replayMax` this socket SENT (already clamped), or null. */
    sentReplayMax: number | null;
    /** Where the reader is, from the renderer. */
    viewportAbs: number;
    /** Whether the reader is at the live tail, from the renderer, which knows; the
     *  window descriptor cannot tell (step 4 retires it, and a first ack precedes
     *  the window frame). */
    following: boolean;
  }): boolean {
    // (1) Content from another server process goes: a different process shares
    // no absolute indices with the old one.
    const foreign = this.adoptServerEpoch(ack.serverEpoch);
    // (2) The capability, stated both ways: only an ack knows, so an unsupported
    // pairing restores the compatibility cap (when declared, step 3 owns it).
    this.paging = ack.paging;
    if (!ack.paging) {
      this.effectiveTailCap = this.compatibilityCap;
    }
    // (2b) Bounds, when the ack carries them, before the batch's replay meets
    // the alt gate.
    if (ack.committed !== null && ack.serverOldest !== null) {
      this.noteResumeBounds(ack.serverOldest);
      this.raiseAltBase(ack.committed);
    }
    // (3) Capability: the cap flip, bookkeeping only.
    let reclassified = false;
    if (ack.paging) {
      reclassified = this.confirmPaging();
    }
    // (4) The replay jump, predicted from what this socket SENT, not the current
    // `highest` (a frame between send and ack moves it). Only under paging: else
    // the band stays tail, since no fetch could restore it.
    if (ack.paging && ack.committed !== null && ack.serverOldest !== null) {
      const jumped = this.predictReplayJump(
        ack.committed,
        ack.serverOldest,
        ack.sentHaveThrough,
        ack.sentReplayMax,
      );
      reclassified = reclassified || jumped;
    }
    // (5) One budget pass if either step moved anything into the cache.
    if (reclassified) {
      // A following reader drains the band to the small target; a reader in history
      // keeps the full cache and the TTL cleans up.
      this.enforceBrowseBudget(
        ack.following ? PREFETCH_THRESHOLD : BROWSE_CACHE_CAP,
        ack.viewportAbs,
      );
      this.enforceCap();
    }
    return foreign;
  }

  /**
   * Whether the content is another server process's: a reported epoch must equal
   * the known one, and a restored store's epoch is a claim that only a reporting
   * server can confirm. Foreign content is reset; a reported epoch is adopted.
   */
  private adoptServerEpoch(reported: number | null): boolean {
    const foreign =
      reported === null ? this.epochSeeded : this.epoch !== 0 && this.epoch !== reported;
    if (foreign) {
      this.reset();
      this.epoch = 0;
    }
    if (reported !== null) {
      this.epoch = reported;
    }
    this.epochSeeded = false;
    return foreign;
  }

  /** The cap flip: the supported target becomes the cap and the excess tail turns
   *  into browse cache, never crossing `win.base`. Returns whether anything moved. */
  private confirmPaging(): boolean {
    this.effectiveTailCap = this.supportedTarget;
    const keep = Math.max(this.supportedTarget, this.win.height);
    if (this.tailCount <= keep || this.oldest < 0) {
      return false;
    }
    const tailKeys = [...this.lines.keys()]
      .filter((k) => !this.browse.has(k))
      .sort((a, b) => a - b);
    // excess >= 1: tailKeys.length >= tailCount > keep.
    const excess = tailKeys.length - keep;
    const winFloor = this.win.height > 0 ? this.win.base : Number.POSITIVE_INFINITY;
    return this.classifyAsBrowse(tailKeys.slice(0, excess).filter((k) => k < winFloor)) > 0;
  }

  /**
   * Predict where the replay starts; if above the sent `haveThrough`, reclassify
   * every held key at or below it as browse cache before the batch applies, and
   * retire the window descriptor so the batch's window frame cannot claim the band.
   */
  private predictReplayJump(
    committed: number,
    serverOldest: number,
    sentHaveThrough: number,
    sentReplayMax: number | null,
  ): boolean {
    const base = sentHaveThrough + 1;
    const predicted = replayStart(committed, serverOldest, sentHaveThrough, sentReplayMax);
    if (predicted <= base || this.oldest < 0 || sentHaveThrough < this.oldest) {
      // No jump, or nothing claimed is still held (a post-ED3 store claims
      // `win.base - 1`, below `oldest`): the band is empty.
      return false;
    }
    // From the key set: a client far behind costs its residency, not the gap's width.
    const stranded: number[] = [];
    for (const abs of this.lines.keys()) {
      if (abs <= sentHaveThrough) {
        stranded.push(abs);
      }
    }
    this.classifyAsBrowse(stranded);
    this.win = emptyWindow();
    this.windowDirty = true;
    return true;
  }

  /** Raise the paging floor: the server proved nothing at or below `end` survives. */
  raisePagingFloor(end: number): void {
    if (Number.isSafeInteger(end) && end > this.pagingFloor) {
      this.pagingFloor = end;
    }
  }

  /**
   * Drop the browse cache; the consumer owns the TTL that calls it. A visible page
   * whose reader sits on cache rows skips (the reader is looking at them); a
   * hidden page drops unconditionally.
   */
  dropBrowseCache(viewportAbs: number, pageVisible: boolean): void {
    if (this.browse.size === 0) {
      return;
    }
    if (pageVisible && this.browse.has(viewportAbs)) {
      return; // skip and re-arm; the consumer's timer retries
    }
    for (const abs of [...this.browse]) {
      this.forget(abs);
    }
    this.recomputeBounds();
  }

  /** Remove one line and its browse classification: the only deletion path, which
   *  keeps `browse ⊆ lines.keys()`. Returns whether the line was held. */
  private forget(abs: number): boolean {
    const held = this.lines.delete(abs);
    if (held) {
      this.rangesMemo = null;
      this.evicted.add(abs);
      this.dirty.delete(abs);
    }
    this.browse.delete(abs);
    return held;
  }

  /** Recompute oldest/highest after a bulk removal. Bounded by map size. */
  private recomputeBounds(): void {
    let min = -1;
    let max = -1;
    for (const k of this.lines.keys()) {
      if (min < 0 || k < min) {
        min = k;
      }
      if (k > max) {
        max = k;
      }
    }
    this.oldest = min;
    this.highest = max;
  }

  /** Full reset for a new server epoch: indices start over, so all content goes. */
  reset(): void {
    this.lines.clear();
    this.rangesMemo = null;
    this.oldest = -1;
    this.highest = -1;
    this.everEvictedThrough = -1;
    this.erasedThrough = -1;
    this.unconfirmedFrom = Number.POSITIVE_INFINITY;
    this.serverOldest = -1;
    this.win = emptyWindow();
    this.alt = false;
    this.altRows = [];
    this.dirty.clear();
    this.evicted.clear();
    this.windowDirty = true;
    this.altDirty = true;
    this.resetPending = true;
    // Content-derived paging state goes with the content.
    this.browse.clear();
    this.solicitedPending = null;
    this.pagingFloor = 0;
    this.browseActivityMs = 0;
    // The capability and the epoch stay: they describe the server, only a
    // resumeAck restates them, and the renderer's resets (resetScreen,
    // resetScrollback) reach here with no resume.
  }

  /**
   * Serialize the newest tail lines, filed under `serverEpoch()`, as
   * structuredClone-safe data. Null when empty (an empty snapshot must never
   * overwrite a good one) or when the epoch is unknown (`fromSnapshot` refuses
   * what no server can confirm). `maxLines` bounds the tail, defaulting to the
   * store's cap.
   */
  snapshot(maxLines: number = this.effectiveTailCap): StoreSnapshot | null {
    if (this.oldest < 0 || this.highest < 0 || this.epoch === 0) {
      return null;
    }
    const cap = Number.isInteger(maxLines) && maxLines > 0 ? maxLines : this.effectiveTailCap;
    // The live tail only, down to the first hole or cache line: a hydrated store
    // must be one contiguous tail for `everEvictedThrough = oldest - 1` to hold.
    const tail: number[] = [];
    for (let abs = this.highest; abs >= 0 && tail.length < cap; abs--) {
      if (!this.lines.has(abs) || this.browse.has(abs)) {
        break;
      }
      tail.push(abs);
    }
    tail.reverse();
    const lines: [number, WireRun[]][] = [];
    let first = -1;
    let last = -1;
    for (const abs of tail) {
      const runs = this.lines.get(abs);
      if (runs !== undefined) {
        lines.push([abs, runs]);
        if (first < 0) {
          first = abs;
        }
        last = abs;
      }
    }
    if (lines.length === 0) {
      return null;
    }
    return {
      v: SNAPSHOT_VERSION,
      serverEpoch: this.epoch,
      // What is written, not this.oldest: a bounded tail starts above it.
      oldest: first,
      highest: last,
      lines,
      // Omitted when all is confirmed, the state an older snapshot reads as.
      ...(Number.isFinite(this.unconfirmedFrom) ? { unconfirmedFrom: this.unconfirmedFrom } : {}),
    };
  }

  /**
   * Rehydrate a store, or null for a missing, malformed or other-version snapshot,
   * or one with no server epoch: each starts empty and takes a full resume. Lines
   * below the tail read as trimmed (`everEvictedThrough = oldest - 1`), and the
   * restore is bounded to `maxLines`, the store's own cap. The restored epoch is
   * unconfirmed: the first resumeAck applied to the store keeps the content only
   * when it reports the same epoch.
   */
  static fromSnapshot(snap: unknown, maxLines?: number): LineStore | null {
    if (typeof snap !== "object" || snap === null) {
      return null;
    }
    const rec = snap as Record<string, unknown>;
    // Read as unknown: this data was outside the program.
    if (rec["v"] !== SNAPSHOT_VERSION || !Array.isArray(rec["lines"])) {
      return null;
    }
    // A process-start timestamp: any positive integer, including the values
    // above 2^53 that a u64 nanosecond epoch decodes to.
    const epoch: unknown = rec["serverEpoch"];
    if (typeof epoch !== "number" || !Number.isInteger(epoch) || epoch <= 0) {
      return null;
    }
    const raw = rec["lines"] as unknown[];
    if (raw.length === 0) {
      return null;
    }
    // An absent or invalid cap is forwarded as absent (the engine's choice) and
    // must also be the trim bound.
    const cap =
      maxLines !== undefined && Number.isInteger(maxLines) && maxLines > 0 ? maxLines : undefined;
    const bound = cap ?? MAX_LINES;
    const store = new LineStore(cap);
    const pairs: [number, WireRun[]][] = [];
    let highest = -1;
    for (const entry of raw) {
      // A partly applied restore is worse than none: one bad row rejects it.
      if (!Array.isArray(entry) || entry.length !== 2) {
        return null;
      }
      const abs: unknown = entry[0];
      const runs: unknown = entry[1];
      if (typeof abs !== "number" || !Number.isInteger(abs) || abs < 0) {
        return null;
      }
      if (!isWireRunArray(runs)) {
        return null;
      }
      if (abs <= highest) {
        return null; // not ascending, or duplicated: the index space is broken
      }
      pairs.push([abs, runs]);
      highest = abs;
    }
    // Trimmed after the whole payload validated, so a corrupt head still rejects.
    const kept = pairs.length > bound ? pairs.slice(pairs.length - bound) : pairs;
    // Unreachable: raw is non-empty and bound >= 1.
    const first = kept[0];
    if (first === undefined) {
      throw new Error("restore: no line pairs left after validation");
    }
    const oldest = first[0];
    for (const [abs, runs] of kept) {
      store.lines.set(abs, runs);
    }
    store.oldest = oldest;
    store.highest = highest;
    store.epoch = epoch;
    store.epochSeeded = true;
    // Everything below the tail is gone as far as this store is concerned.
    store.everEvictedThrough = oldest - 1;
    // A malformed or absent floor leaves +Infinity: the whole tail reads as
    // confirmed. A floor below the kept tail is healed by the next confirmRange.
    const savedFloor: unknown = rec["unconfirmedFrom"];
    if (typeof savedFloor === "number" && Number.isInteger(savedFloor) && savedFloor >= 0) {
      store.unconfirmedFrom = savedFloor;
    }
    // The renderer must build every hydrated row: nothing is on screen yet.
    for (const abs of store.lines.keys()) {
      store.dirty.add(abs);
    }
    return store;
  }

  /** Drain accumulated changes for the renderer and clear the tracking sets. */
  drainChanges(): StoreChanges {
    const out: StoreChanges = {
      dirtyLines: [...this.dirty],
      evictedLines: [...this.evicted],
      windowChanged: this.windowDirty,
      altChanged: this.altDirty,
      fullReset: this.resetPending,
    };
    this.dirty.clear();
    this.evicted.clear();
    this.windowDirty = false;
    this.altDirty = false;
    this.resetPending = false;
    return out;
  }

  // --- internals ---

  /** Whether an absolute index falls inside the current live window. */
  private inWindow(abs: number): boolean {
    return this.win.height > 0 && abs >= this.win.base && abs < this.win.base + this.win.height;
  }

  /** The guarded core of every apply; effects land in the dirty/evicted sets. */
  private applyLine(abs: number, runs: WireRun[], bulk = false): void {
    // Guard 1: a valid, non-negative integer index.
    if (!Number.isInteger(abs) || abs < 0) {
      return;
    }
    // Guard 2: refuse a stale re-send at or below the eviction watermark, except
    // inside the live window and inside the solicited request window.
    if (abs <= this.everEvictedThrough && !this.inWindow(abs) && !this.isSolicited(abs)) {
      return;
    }
    // Guard 2b: not an index the application erased (see erasedThrough).
    if (abs <= this.erasedThrough && !this.inWindow(abs) && !this.isSolicited(abs)) {
      return;
    }
    // Guard 3: a well-formed run array.
    if (!Array.isArray(runs)) {
      return;
    }
    // Guard 5: identical content is a no-op (no DOM churn). Guards 4, 6, 8 and 9
    // live at the callers and the renderer.
    const existing = this.lines.get(abs);
    if (existing !== undefined && rowEqual(existing, runs)) {
      return;
    }
    this.lines.set(abs, runs);
    this.rangesMemo = null;
    this.evicted.delete(abs);
    this.dirty.add(abs);
    if (this.oldest < 0 || abs < this.oldest) {
      this.oldest = abs;
    }
    if (abs > this.highest) {
      this.highest = abs;
    }
    // Guard 10: the cap, suppressed in a bulk page apply, which runs one pass at
    // the end.
    if (!bulk) {
      this.enforceCap();
    }
  }

  /** Whether `abs` lies inside the request currently in flight. */
  private isSolicited(abs: number): boolean {
    const s = this.solicitedPending;
    return s !== null && abs >= s.lo && abs < s.hi;
  }

  // applyScrollbackCleared handles ED3: drop lines below `base` without moving
  // everEvictedThrough (an erase is not a trim), cancel the in-flight request (its
  // reply would re-admit erased rows) and raise the paging floor to `base`.
  private applyScrollbackCleared(base: number): void {
    this.solicitedPending = null;
    this.raisePagingFloor(base);
    // The provisional floor rises with the erase, never lowers.
    if (base > this.unconfirmedFrom) {
      this.unconfirmedFrom = base;
    }
    if (this.oldest < 0 || this.oldest >= base) {
      return;
    }
    // A key scan through forget(), so cache lines lose their classification too.
    for (const abs of [...this.lines.keys()]) {
      if (abs < base) {
        this.forget(abs);
        // The watermark tracks what was actually DROPPED, never the cleared
        // bound: the gap between them is where the app's own reprint lands.
        this.erasedThrough = Math.max(this.erasedThrough, abs);
      }
    }
    this.recomputeBounds();
  }

  // truncateBelowWindow drops lines below the window's bottom row, the newest
  // line: a shrinking resize (a soft keyboard opening) strands the taller screen's
  // bottom rows there as phantom blank rows that cap eviction never trims.
  private truncateBelowWindow(): void {
    if (this.highest < 0) {
      return;
    }
    // No window yet: a resume replays history before its window frame.
    if (this.win.height <= 0) {
      return;
    }
    const windowBottom = this.win.base + this.win.height - 1;
    if (this.highest <= windowBottom) {
      return;
    }
    // A key scan, not an integer walk: a far-dropped base would loop ~2^53 times.
    let newHighest = -1;
    for (const abs of [...this.lines.keys()]) {
      if (abs > windowBottom) {
        this.forget(abs);
      } else if (abs > newHighest) {
        newHighest = abs;
      }
    }
    if (newHighest >= 0) {
      this.highest = newHighest;
    } else {
      this.highest = -1;
      this.oldest = -1;
    }
  }

  private enforceCap(): void {
    // Tail count only: browse cache has its own budget, enforced at page apply.
    if (this.tailCount <= this.tailBound) {
      return;
    }
    const target = this.tailBound - evictionBatch(this.tailBound) + 1;
    // One oldest-first walk that skips the live window: the cap is a history budget
    // floored at the screen height, and oldest-first leaves no hole under the screen
    // that resume cannot backfill. everEvictedThrough also covers a hopped band,
    // safe because guard 2 exempts the window.
    const winBottom = this.win.height > 0 ? this.win.base + this.win.height - 1 : -1;
    const hasWindow = this.win.height > 0;
    let cursor = this.oldest;
    let hopped = false;
    // `cursor < highest` keeps the newest line of a scroll-only store; with a
    // window, a line above its bottom (a malformed stream) is evictable even then.
    while (
      this.tailCount > target &&
      cursor >= 0 &&
      (cursor < this.highest || (hasWindow && cursor > winBottom))
    ) {
      if (hasWindow && cursor >= this.win.base && cursor <= winBottom) {
        // Hop over the protected window (a scan bounded by map size).
        if (winBottom >= this.highest) {
          break; // the window is the tail: nothing evictable above it
        }
        cursor = this.lines.has(winBottom + 1) ? winBottom + 1 : this.minKeyAbove(winBottom);
        hopped = true;
        if (cursor < 0) {
          break;
        }
        continue;
      }
      // Hop browse keys like the window, so `oldest` stays the global minimum key.
      if (this.browse.has(cursor)) {
        const next = this.minKeyAboveNonBrowse(cursor);
        hopped = true;
        if (next < 0) {
          break;
        }
        cursor = next;
        continue;
      }
      const victim = cursor;
      if (this.forget(victim)) {
        // Only tail victims advance the watermark: a cache drop is not a trim.
        this.everEvictedThrough = Math.max(this.everEvictedThrough, victim);
      }
      // The loop guard keeps the last line even at target 0 (`new LineStore(0)`).
      // Next key: an O(1) probe for contiguous history, else a scan bounded by map
      // size, never by the integer gap (a far-jumping base would be a DoS).
      cursor = this.lines.has(victim + 1) ? victim + 1 : this.minKeyAbove(victim);
      if (!hopped) {
        // Classic head eviction: the cursor IS the minimum retained key.
        this.oldest = cursor;
      }
      if (cursor < 0) {
        break;
      }
    }
    if (hopped) {
      // After a hop the cursor is not the minimum key: recompute once per pass.
      this.recomputeBounds();
    }
  }

  /** The lowest retained non-browse key above k, or -1: steps over a cache run in
   *  one move. Bounded by map size. */
  private minKeyAboveNonBrowse(k: number): number {
    let min = -1;
    for (const key of this.lines.keys()) {
      if (key > k && !this.browse.has(key) && (min < 0 || key < min)) {
        min = key;
      }
    }
    return min;
  }

  /** The lowest retained key strictly above k, or -1. Bounded by map size. */
  private minKeyAbove(k: number): number {
    let min = -1;
    for (const key of this.lines.keys()) {
      if (key > k && (min < 0 || key < min)) {
        min = key;
      }
    }
    return min;
  }

  private updateWindow(msg: ScreenMessage): void {
    // A malformed base must not redefine the window, or truncateBelowWindow would
    // silently wipe the store; its rows still meet applyLine's guard 1.
    if (!Number.isInteger(msg.base) || msg.base < 0) {
      return;
    }
    const next: WindowState = {
      base: msg.base,
      height: msg.rows.length,
      cursorRow: msg.cursor[0],
      cursorCol: msg.cursor[1],
      cursorStyle: msg.cursorStyle ?? 0,
      cursorHidden: msg.cursorHidden ?? false,
      cursorBlink: msg.cursorBlink ?? false,
    };
    if (!windowEqual(this.win, next)) {
      this.win = next;
      this.windowDirty = true;
    }
  }

  private updateWindowCursor(msg: ScreenMessage): void {
    // In alt the height stays the MAIN window's, so the bound and the guard-2
    // exemption describe the screen alt exit restores.
    this.raiseAltBase(msg.base);
    const next: WindowState = {
      ...this.win,
      cursorRow: msg.cursor[0],
      cursorCol: msg.cursor[1],
      cursorStyle: msg.cursorStyle ?? 0,
      cursorHidden: msg.cursorHidden ?? false,
      cursorBlink: msg.cursorBlink ?? false,
    };
    if (!windowEqual(this.win, next)) {
      this.win = next;
      this.windowDirty = true;
    }
  }

  /** Raise the frozen main base to `committed` (every alt frame's base and every
   *  resumeAck report it): the server commits main lines while alt is active. */
  private raiseAltBase(committed: number): void {
    if (!this.alt || this.win.height === 0 || !Number.isSafeInteger(committed)) {
      return;
    }
    if (committed > this.win.base) {
      this.win = { ...this.win, base: committed };
      this.windowDirty = true;
    }
  }

  private enterAltIfNeeded(height: number): void {
    if (!this.alt) {
      this.alt = true;
      this.altDirty = true;
    }
    if (this.altRows.length !== height) {
      const next: WireRun[][] = new Array<WireRun[]>(height);
      for (let i = 0; i < height; i++) {
        next[i] = this.altRows[i] ?? [];
      }
      this.altRows = next;
      this.altDirty = true;
    }
  }

  private exitAltIfNeeded(): void {
    if (this.alt) {
      this.alt = false;
      this.altRows = [];
      this.altDirty = true;
    }
  }
}

function windowEqual(a: WindowState, b: WindowState): boolean {
  return (
    a.base === b.base &&
    a.height === b.height &&
    a.cursorRow === b.cursorRow &&
    a.cursorCol === b.cursorCol &&
    a.cursorStyle === b.cursorStyle &&
    a.cursorHidden === b.cursorHidden &&
    a.cursorBlink === b.cursorBlink
  );
}
