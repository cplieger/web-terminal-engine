// Unit tests for the absolute-index line store (brick 2). These pin the
// properties the rebuild depends on: idempotent apply (no duplicates on
// re-delivery), correct absolute-index accounting, eviction with stale-drop,
// hole-skipping iteration, and alt-screen routing. Pure data structure, no DOM.

import { describe, it, expect } from "vitest";
import { LineStore } from "./store.js";
import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";

function row(text: string): WireRun[] {
  return [{ t: text, f: -1, b: -1, a: 0, uc: -1 }];
}

// scrollMsg builds a scroll/history frame starting at firstIndex.
function scrollMsg(firstIndex: number, texts: string[]): ScrollMessage {
  return { type: "scroll", firstIndex, lines: texts.map(row) };
}

// screenMsg builds a screen frame: a window of `height` rows at `base`, with
// the given rows marked changed (sparse). cursor defaults to [0,0].
function screenMsg(
  base: number,
  height: number,
  changedRows: Record<number, string>,
  opts: Partial<{ cursor: [number, number]; altActive: boolean }> = {},
): ScreenMessage {
  const rows: WireRun[][] = new Array<WireRun[]>(height);
  const changed: number[] = [];
  for (const [k, v] of Object.entries(changedRows)) {
    const y = Number(k);
    rows[y] = row(v);
    changed.push(y);
  }
  return {
    type: "screen",
    base,
    rows,
    changed,
    cursor: opts.cursor ?? [0, 0],
    altActive: opts.altActive ?? false,
  };
}

function lineTexts(store: LineStore): { abs: number; text: string }[] {
  const out: { abs: number; text: string }[] = [];
  store.forEachLine((abs, runs) => out.push({ abs, text: runs.map((r) => r.t).join("") }));
  return out;
}

describe("LineStore", () => {
  it("applies screen window rows at base + y", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(10, 3, { 0: "a", 1: "b", 2: "c" }, { cursor: [2, 1] }));
    expect(lineTexts(s)).toEqual([
      { abs: 10, text: "a" },
      { abs: 11, text: "b" },
      { abs: 12, text: "c" },
    ]);
    expect(s.highestIndex()).toBe(12);
    expect(s.oldestIndex()).toBe(10);
    const w = s.getWindow();
    expect(w.base).toBe(10);
    expect(w.height).toBe(3);
    expect(w.cursorRow).toBe(2);
    expect(w.cursorCol).toBe(1);
  });

  it("applies scroll history lines at firstIndex + i", () => {
    const s = new LineStore();
    s.applyScroll(scrollMsg(0, ["l0", "l1", "l2"]));
    expect(lineTexts(s)).toEqual([
      { abs: 0, text: "l0" },
      { abs: 1, text: "l1" },
      { abs: 2, text: "l2" },
    ]);
  });

  it("is idempotent: re-applying identical content is a no-op (the dup-prevention property)", () => {
    const s = new LineStore();
    s.applyScroll(scrollMsg(0, ["x", "y", "z"]));
    s.drainChanges(); // clear initial dirty
    // Re-deliver the exact same batch (simulating a fast-burst re-send or a
    // doubled frame on reconnect).
    s.applyScroll(scrollMsg(0, ["x", "y", "z"]));
    const ch = s.drainChanges();
    expect(ch.dirtyLines).toEqual([]); // nothing re-rendered
    // And each index still appears exactly once with the right content.
    expect(lineTexts(s)).toEqual([
      { abs: 0, text: "x" },
      { abs: 1, text: "y" },
      { abs: 2, text: "z" },
    ]);
  });

  it("updates a line in place when content changes (Ink rewriting a window row)", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 2, { 0: "spin -", 1: "" }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 2, { 0: "spin \\" })); // row 0 redrawn
    const ch = s.drainChanges();
    expect(ch.dirtyLines).toEqual([0]);
    expect(s.getLine(0)?.[0]?.t).toBe("spin \\");
  });

  it("evicts from the oldest end at the cap and drops stale re-sends", () => {
    const s = new LineStore(3); // tiny cap
    s.applyScroll(scrollMsg(0, ["0", "1", "2", "3", "4"])); // 5 lines, cap 3
    // Oldest two (abs 0,1) evicted; 2,3,4 retained.
    expect(lineTexts(s)).toEqual([
      { abs: 2, text: "2" },
      { abs: 3, text: "3" },
      { abs: 4, text: "4" },
    ]);
    expect(s.oldestIndex()).toBe(2);
    const ch = s.drainChanges();
    expect(ch.evictedLines.sort((a, b) => a - b)).toEqual([0, 1]);

    // A stale re-send of an evicted index is dropped (not resurrected).
    s.applyScroll(scrollMsg(0, ["0-stale"]));
    const ch2 = s.drainChanges();
    expect(ch2.dirtyLines).toEqual([]);
    expect(s.getLine(0)).toBeUndefined();
  });

  it("advances oldest across a large index gap when evicting at the cap (bounded scan, no integer walk)", () => {
    // A compromised or malformed server frame can deliver content at an
    // absolute index far above the retained low index. When the cap then
    // forces eviction of that low line, advancing `oldest` must scan the small
    // retained key set, never walk the integer gap to the next index -- a naive
    // `while (!has(oldest)) oldest++` fallback would iterate ~1e9 times here and
    // freeze the tab (an algorithmic-complexity DoS). The bounded key-scan lands
    // oldest on the surviving far block immediately.
    const s = new LineStore(3); // tiny cap
    s.applyScroll(scrollMsg(0, ["low"]));
    const far = 1_000_000_000;
    s.applyScroll(scrollMsg(far, ["a", "b", "c"])); // 4 lines, cap 3 -> evict abs 0
    expect(s.getLine(0)).toBeUndefined();
    expect(s.oldestIndex()).toBe(far);
    expect(s.highestIndex()).toBe(far + 2);
    expect(lineTexts(s)).toEqual([
      { abs: far, text: "a" },
      { abs: far + 1, text: "b" },
      { abs: far + 2, text: "c" },
    ]);
  });

  it("skips holes when iterating (trimmed-history gap shows as a jump in abs)", () => {
    const s = new LineStore();
    s.applyScroll(scrollMsg(0, ["a", "b"]));
    // Jump to abs 5 (3,4 missing — e.g. an eviction-gap resume).
    s.applyScroll(scrollMsg(5, ["f", "g"]));
    expect(lineTexts(s)).toEqual([
      { abs: 0, text: "a" },
      { abs: 1, text: "b" },
      { abs: 5, text: "f" },
      { abs: 6, text: "g" },
    ]);
    expect(s.highestIndex()).toBe(6);
  });

  it("highestIndex is -1 when empty (resume haveThrough cold-start signal)", () => {
    const s = new LineStore();
    expect(s.highestIndex()).toBe(-1);
    s.applyScroll(scrollMsg(0, ["a"]));
    expect(s.highestIndex()).toBe(0);
  });

  it("reset clears everything and flags a full reset for the renderer", () => {
    const s = new LineStore();
    s.applyScroll(scrollMsg(0, ["a", "b", "c"]));
    s.drainChanges();
    s.reset();
    expect(s.highestIndex()).toBe(-1);
    expect(lineTexts(s)).toEqual([]);
    const ch = s.drainChanges();
    expect(ch.fullReset).toBe(true);
    // After a reset, index 0 is valid again (new server boot).
    s.applyScroll(scrollMsg(0, ["fresh"]));
    expect(s.getLine(0)?.[0]?.t).toBe("fresh");
  });

  it("routes alt-screen frames to an ephemeral grid without touching the abs store", () => {
    const s = new LineStore();
    s.applyScroll(scrollMsg(0, ["history0", "history1"]));
    s.drainChanges();
    // Enter alt screen (e.g. vim): a 2-row ephemeral grid.
    s.applyScreen(screenMsg(2, 2, { 0: "~ alt 0", 1: "~ alt 1" }, { altActive: true }));
    expect(s.isAlt()).toBe(true);
    expect(s.getAltRows().map((r) => r.map((x) => x.t).join(""))).toEqual(["~ alt 0", "~ alt 1"]);
    // The history buffer is untouched while in alt.
    expect(lineTexts(s)).toEqual([
      { abs: 0, text: "history0" },
      { abs: 1, text: "history1" },
    ]);
    // A scroll frame during alt is dropped (protocol invariant).
    s.applyScroll(scrollMsg(2, ["should-not-apply"]));
    expect(s.getLine(2)).toBeUndefined();
    // Exit alt: grid cleared, history intact.
    s.applyScreen(screenMsg(2, 2, { 0: "history0", 1: "history1" }));
    expect(s.isAlt()).toBe(false);
    expect(s.getAltRows()).toEqual([]);
  });

  it("rejects invalid indices", () => {
    const s = new LineStore();
    // Negative and non-integer indices via a hand-built scroll frame.
    s.applyScroll({ type: "scroll", firstIndex: -5, lines: [row("neg")] });
    expect(s.highestIndex()).toBe(-1); // -5 rejected
  });

  it("drops a malformed line whose runs are not an array (apply-line guard 3)", () => {
    const s = new LineStore();
    // A malformed scroll frame (reachable via the JSON text-frame path, which
    // is parsed without structural validation) carries a line payload that is
    // not a WireRun array. Guard 3 must drop it rather than store a corrupt row.
    s.applyScroll({
      type: "scroll",
      firstIndex: 0,
      lines: ["not-a-run-array" as unknown as WireRun[]],
    });
    expect(s.highestIndex()).toBe(-1);
    expect(s.getLine(0)).toBeUndefined();
  });

  it("reports trimmed history from a client-side eviction (resync guard 8.2.2)", () => {
    const s = new LineStore(3); // tiny cap to force eviction
    s.applyScroll(scrollMsg(0, ["a", "b", "c", "d", "e"])); // evicts 0,1
    expect(s.oldestIndex()).toBeGreaterThan(0);
    expect(s.hasTrimmedHistory()).toBe(true);
  });

  it("reports trimmed history when the server retains less than the client asks for", () => {
    const s = new LineStore();
    // Fresh client; nothing evicted locally.
    expect(s.hasTrimmedHistory()).toBe(false);
    // Resume: the server's oldest retained line is 100 and it replays from there.
    s.noteResumeBounds(150, 100);
    s.applyScroll(scrollMsg(100, ["x", "y"]));
    // The client cannot show lines 0..99 — they were trimmed server-side.
    expect(s.hasTrimmedHistory()).toBe(true);
    // A later resume where the server still has everything clears the flag.
    s.noteResumeBounds(150, 0);
    expect(s.hasTrimmedHistory()).toBe(false);
  });

  it("ignores invalid resume bounds", () => {
    const s = new LineStore();
    s.noteResumeBounds(10, -1); // negative oldest ignored
    s.applyScroll(scrollMsg(5, ["a"]));
    expect(s.hasTrimmedHistory()).toBe(false);
  });
});
