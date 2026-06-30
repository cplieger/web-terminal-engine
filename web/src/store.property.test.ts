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
});
