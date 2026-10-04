/**
 * Gap geometry over the store's retained absolute indices (a live tail plus
 * browse-cache runs), derived from the retained KEY SET rather than cache
 * membership: an `unsupported` eviction-gap resume can leave a hole inside the
 * tail that membership would silently splice across. Intervals are half-open
 * `[lo, hi)`; sets are sorted and disjoint with no touching pair.
 */

/** A half-open range of absolute line indices: `[lo, hi)`. */
export interface Interval {
  /** First index in the range. */
  lo: number;
  /** One past the last index in the range. */
  hi: number;
}

/**
 * Coalesce a set of retained absolute indices into sorted, disjoint intervals.
 * This is the GAP source: the complement of the returned runs (between the
 * lowest and highest retained index) is exactly the set of holes the fetch
 * trigger may target and the renderer must mark.
 */
export function retainedIntervals(keys: Iterable<number>): Interval[] {
  const sorted = [...keys].sort((a, b) => a - b);
  const out: Interval[] = [];
  let run: Interval | null = null;
  for (const k of sorted) {
    if (run !== null && k === run.hi) {
      run.hi = k + 1; // contiguous: extend
      continue;
    }
    if (run !== null && k < run.hi) {
      continue; // duplicate key (defensive; a Map cannot produce one)
    }
    if (run !== null) {
      out.push(run);
    }
    run = { lo: k, hi: k + 1 };
  }
  if (run !== null) {
    out.push(run);
  }
  return out;
}

/**
 * The holes between a set's intervals: for `[[0,10), [20,30)]` the single gap
 * `[10,20)`. Only INTERIOR gaps are returned — the frontier below the lowest
 * retained index is a pseudo-gap the store derives from `pagingFloor` instead,
 * because its lower edge is a policy question (what is still worth requesting)
 * rather than a geometric one.
 */
export function interiorGaps(set: readonly Interval[]): Interval[] {
  const out: Interval[] = [];
  for (let i = 1; i < set.length; i++) {
    const prev = set[i - 1];
    const cur = set[i];
    if (prev !== undefined && cur !== undefined && cur.lo > prev.hi) {
      out.push({ lo: prev.hi, hi: cur.lo });
    }
  }
  return out;
}
