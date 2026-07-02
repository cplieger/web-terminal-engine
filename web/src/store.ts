// Absolute-index line store: the client's authoritative model of the
// terminal. One buffer keyed by absolute line index, with the live screen
// window sliding along it. This is the data model that resolves the
// live/history split (see the #web-terminal-engine steering doc, "Design
// rationale"): there is no separate
// "live zone" and "scrollback" here, only lines addressed by absolute index,
// the last `height` of which happen to still be changing.
//
// The store is pure (no DOM). The renderer (render.ts) reads from it and
// reflects changes to the DOM; the connection layer feeds decoded wire
// messages into it. Applying a line is idempotent by absolute index, which is
// what makes re-delivery (resume replay, fast-burst re-send, a doubled frame
// from a zombie socket) incapable of duplicating a row.

import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";

/** Maximum lines retained client-side. Older lines are evicted from the top. */
const MAX_LINES = 5000;

/** The live screen window: a fixed `height`-row block at the tail of the buffer. */
export interface WindowState {
  /** Absolute index of window row 0. */
  base: number;
  /** Number of rows in the window (terminal height). */
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

  /**
   * @param maxLines  retained-line cap; defaults to MAX_LINES. Injectable so
   *                  eviction is testable without allocating thousands of rows.
   */
  constructor(private readonly maxLines: number = MAX_LINES) {}

  /** Highest absolute index held, or -1 if empty. Used as resume `haveThrough`. */
  highestIndex(): number {
    return this.highest;
  }

  /** Lowest absolute index held, or -1 if empty. */
  oldestIndex(): number {
    return this.oldest;
  }

  /**
   * True if history older than what the store holds was trimmed (evicted
   * client-side, or the server reported it no longer retains it). The
   * renderer shows a "history trimmed" marker at the top in this case.
   */
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

  /** A copy of the ephemeral alt-screen grid rows. */
  getAltRows(): WireRun[][] {
    return this.altRows.map((r) => r.slice());
  }

  /**
   * Iterate retained lines from oldest to highest in absolute-index order,
   * skipping holes. The renderer uses this to build and order DOM rows; a hole
   * (a jump in abs between consecutive lines) is a trimmed-history gap the
   * renderer marks.
   */
  forEachLine(cb: (abs: number, runs: WireRun[]) => void): void {
    if (this.oldest < 0) {
      return;
    }
    for (let abs = this.oldest; abs <= this.highest; abs++) {
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
    if (msg.scrollbackCleared) {
      // ED3 (erase scrollback): the app cleared its saved lines (kiro-cli does
      // this on every resize redraw). Drop our matching history below the live
      // window so the previous frame doesn't accumulate beneath the new one.
      this.dropHistoryBelow(msg.base);
    }
    this.updateWindow(msg);
    for (const y of msg.changed) {
      const row = msg.rows[y];
      if (row !== undefined) {
        this.applyLine(msg.base + y, row);
      }
    }
    this.truncateBelowWindow();
  }

  /** Apply a decoded scroll/history frame: each line at firstIndex + i. */
  applyScroll(msg: ScrollMessage): void {
    if (this.alt) {
      // Protocol invariant: the server does not emit scroll frames while the
      // alternate screen is active. Drop rather than corrupt the abs store.
      return;
    }
    for (let i = 0; i < msg.lines.length; i++) {
      const row = msg.lines[i];
      if (row !== undefined) {
        this.applyLine(msg.firstIndex + i, row);
      }
    }
  }

  /**
   * Record the server's retained-history bounds from a resumeAck so the
   * renderer can tell a genuine trim from a still-loading state.
   */
  noteResumeBounds(_committed: number, oldestIndex: number): void {
    if (Number.isInteger(oldestIndex) && oldestIndex >= 0) {
      this.serverOldest = oldestIndex;
    }
  }

  /**
   * Full reset: drop all lines and window state. Used on server restart (a new
   * boot epoch), where absolute indices start over from 0 and any retained
   * content is stale. The renderer wipes all DOM on the next drain.
   */
  reset(): void {
    this.lines.clear();
    this.oldest = -1;
    this.highest = -1;
    this.everEvictedThrough = -1;
    this.serverOldest = -1;
    this.win = emptyWindow();
    this.alt = false;
    this.altRows = [];
    this.dirty.clear();
    this.evicted.clear();
    this.windowDirty = true;
    this.altDirty = true;
    this.resetPending = true;
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

  /**
   * applyLine is the guarded core. It enforces the apply-line guard set
   * (see the #web-terminal-engine steering doc, "Design rationale"):
   * valid index, not stale, idempotent, and
   * cap-bounded. Returns nothing; effects are recorded in the dirty/evicted
   * sets for the next drain.
   */
  private applyLine(abs: number, runs: WireRun[]): void {
    // Guard 1: a valid, non-negative integer index.
    if (!Number.isInteger(abs) || abs < 0) {
      return;
    }
    // Guard 2: not below what we have permanently evicted (stale re-send).
    if (abs <= this.everEvictedThrough) {
      return;
    }
    // Guard 3: a well-formed run array.
    if (!Array.isArray(runs)) {
      return;
    }
    // Guard 5: idempotent — identical content is a no-op (no DOM churn, no
    // selection disturbance). Guards 4/6/8/9 (gap, alt-consistency, cell
    // width, row-element integrity) live at the callers and the renderer.
    const existing = this.lines.get(abs);
    if (existing !== undefined && rowEqual(existing, runs)) {
      return;
    }
    this.lines.set(abs, runs);
    this.evicted.delete(abs);
    this.dirty.add(abs);
    if (this.oldest < 0 || abs < this.oldest) {
      this.oldest = abs;
    }
    if (abs > this.highest) {
      this.highest = abs;
    }
    // Guard 10: enforce the cap by evicting from the oldest end.
    this.enforceCap();
  }

  // dropHistoryBelow evicts all retained lines with an absolute index below
  // `base` — the scrollback history under the live window. Called when the
  // server signals ED3 (erase scrollback): an inline TUI (kiro-cli) clears its
  // saved lines on every resize redraw, so the client drops its matching
  // history to stop the previous frame accumulating beneath the new one. Window
  // rows (>= base) are kept and refreshed by the frame carrying the signal.
  // everEvictedThrough is left untouched: the app discarded the lines
  // deliberately (not a cap trim), so no "earlier output trimmed" marker fits.
  private dropHistoryBelow(base: number): void {
    if (this.oldest < 0 || this.oldest >= base) {
      return;
    }
    for (let abs = this.oldest; abs < base; abs++) {
      if (this.lines.delete(abs)) {
        this.evicted.add(abs);
        this.dirty.delete(abs);
      }
    }
    if (base > this.highest) {
      this.oldest = -1;
      this.highest = -1;
    } else {
      this.oldest = base;
    }
  }

  // truncateBelowWindow evicts every retained line past the window's bottom row.
  // The window's bottom row is the most recent line in the terminal, so no line
  // can exist at a higher absolute index. A resize that SHRINKS the screen (the
  // iOS soft keyboard opening) moves the window bottom up while the taller
  // screen's former bottom rows stay in the store at higher indices — stranded
  // below the live window. Cap eviction only trims the top/oldest, so nothing
  // removes them: they linger as phantom blank rows beneath the real content, an
  // "empty" region the user can scroll into and the reason the content never
  // appears to shrink to fit on a short viewport. Evicting them keeps the tail
  // invariant (highest === window bottom); it is the tail-side complement to the
  // top-side cap eviction.
  private truncateBelowWindow(): void {
    if (this.highest < 0) {
      return;
    }
    const windowBottom = this.win.base + this.win.height - 1;
    if (this.highest <= windowBottom) {
      return;
    }
    for (let abs = windowBottom + 1; abs <= this.highest; abs++) {
      if (this.lines.delete(abs)) {
        this.evicted.add(abs);
        this.dirty.delete(abs);
      }
    }
    // New highest is the greatest retained index at or below the window bottom.
    let h = windowBottom;
    while (h >= this.oldest && !this.lines.has(h)) {
      h--;
    }
    if (h >= this.oldest) {
      this.highest = h;
    } else {
      this.highest = -1;
      this.oldest = -1;
    }
  }

  private enforceCap(): void {
    while (this.lines.size > this.maxLines && this.oldest >= 0 && this.oldest < this.highest) {
      const victim = this.oldest;
      if (this.lines.delete(victim)) {
        this.everEvictedThrough = Math.max(this.everEvictedThrough, victim);
        this.evicted.add(victim);
        this.dirty.delete(victim);
      }
      // Advance oldest to the lowest remaining key. After evicting the current
      // oldest, contiguous history (the common case: live scroll-off, resume
      // replay, a cat-bigfile burst) leaves victim + 1 present -- an O(1) probe.
      // Only a hole at the boundary falls back to a full key scan. That scan is
      // bounded by map size (<= maxLines), NOT by the integer gap to the next
      // retained line, so a malformed/compromised frame whose base jumps far
      // ahead of a retained low index cannot make eviction walk billions of
      // indices (an algorithmic-complexity DoS that freezes the tab).
      if (this.lines.has(victim + 1)) {
        this.oldest = victim + 1;
      } else {
        let min = -1;
        for (const k of this.lines.keys()) {
          if (min < 0 || k < min) {
            min = k;
          }
        }
        this.oldest = min;
      }
      if (this.lines.size === 0) {
        this.oldest = -1;
        this.highest = -1;
        break;
      }
    }
  }

  private updateWindow(msg: ScreenMessage): void {
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
    // In the alt screen the base is frozen but the cursor still moves.
    const next: WindowState = {
      ...this.win,
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
