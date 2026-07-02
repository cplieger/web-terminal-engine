import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { LineStore } from "./store.js";
import type { ScrollMessage, WireRun } from "./types.js";

function rowOf(t: string): WireRun[] {
  return [{ t, f: -1, b: -1, a: 0, uc: -1 }];
}
function scrollOf(firstIndex: number, texts: string[]): ScrollMessage {
  return { type: "scroll", firstIndex, lines: texts.map(rowOf) };
}
function snap(s: LineStore): { abs: number; text: string }[] {
  const out: { abs: number; text: string }[] = [];
  s.forEachLine((abs, runs) => out.push({ abs, text: runs.map((r) => r.t).join("") }));
  return out;
}

describe("LineStore invariants (property)", () => {
  it("retained line count never exceeds the cap, for any sequence of scroll batches", () => {
    const cap = 50;
    fc.assert(
      fc.property(
        fc.array(
          fc.record({
            first: fc.nat(2000),
            texts: fc.array(fc.string({ maxLength: 8 }), { maxLength: 15 }),
          }),
          { maxLength: 40 },
        ),
        (batches) => {
          const s = new LineStore(cap);
          for (const b of batches) {
            s.applyScroll(scrollOf(b.first, b.texts));
          }
          expect(snap(s).length).toBeLessThanOrEqual(cap);
          if (s.highestIndex() >= 0) {
            expect(s.oldestIndex()).toBeGreaterThanOrEqual(0);
            expect(s.oldestIndex()).toBeLessThanOrEqual(s.highestIndex());
          }
        },
      ),
    );
  });

  it("re-delivering an identical batch is a no-op (idempotency: the dedup property resume relies on)", () => {
    fc.assert(
      fc.property(
        fc.nat(1000),
        fc.array(fc.string({ maxLength: 8 }), { minLength: 1, maxLength: 20 }),
        (first, texts) => {
          const s = new LineStore();
          s.applyScroll(scrollOf(first, texts));
          const after1 = snap(s);
          s.drainChanges();
          s.applyScroll(scrollOf(first, texts));
          const ch = s.drainChanges();
          expect(ch.dirtyLines).toEqual([]);
          expect(snap(s)).toEqual(after1);
        },
      ),
    );
  });

  it("every retained line holds the most recent content written to its index (model-based)", () => {
    // Model-based property (stronger than the count invariant above). The model
    // is a plain last-writer-wins dictionary from absolute index to text: it
    // knows nothing about the cap, eviction, or the oldest/highest bookkeeping,
    // so it is a genuine simplification of the store and cannot share an
    // eviction bug with it. WHICH lines survive is the eviction policy (covered
    // by the cap invariant + the unit tests); here we assert that for every line
    // the store chose to RETAIN, its content equals the last text written to
    // that index. This catches content corruption and index misalignment (an
    // off-by-one in `firstIndex + i`, a stale dedup) that a count-only invariant
    // sails past. Small index range + generous batches so lines collide and the
    // cap actually evicts.
    const cap = 40;
    fc.assert(
      fc.property(
        fc.array(
          fc.record({
            first: fc.nat(200),
            texts: fc.array(fc.string({ maxLength: 8 }), { maxLength: 30 }),
          }),
          { maxLength: 30 },
        ),
        (batches) => {
          const s = new LineStore(cap);
          const model = new Map<number, string>();
          for (const b of batches) {
            s.applyScroll(scrollOf(b.first, b.texts));
            b.texts.forEach((t, i) => model.set(b.first + i, t));
          }
          // Project the model onto exactly the indices the store retained, in
          // the store's own iteration order, and compare content. This is a
          // single assertion even for an empty store, so it never trips
          // requireAssertions.
          const retained = snap(s);
          const expected = retained.map(({ abs }) => ({ abs, text: model.get(abs) }));
          expect(retained).toEqual(expected);
        },
      ),
    );
  });
});
