// Property-based tests for the WebSocket reconnect-backoff scheduler.
//
// Invariants tested:
// 1. nextBaseMs doubles or saturates at MAX_DELAY_MS (never exceeds).
// 2. scheduledMs is in [currentBaseMs, currentBaseMs + JITTER_MS).
// 3. scheduledMs is always >= currentBaseMs (jitter is additive, not subtractive).
// 4. With deterministic random=0, the formula is exactly currentBaseMs.
// 5. Eventually-monotonic: from any starting base, repeated calls
//    converge to MAX_DELAY_MS as the new base.
// 6. Robustness: malformed random() (NaN, negative, >=1, +/-Infinity)
//    yields a finite, non-negative scheduledMs.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { nextBackoffDelay, INITIAL_DELAY_MS, MAX_DELAY_MS, JITTER_MS } from "./reconnect.js";

describe("nextBackoffDelay property", () => {
  it("nextBaseMs is min(currentBaseMs * 2, MAX_DELAY_MS)", () => {
    fc.assert(
      fc.property(
        fc.double({ min: 0, max: 100_000, noNaN: true, noDefaultInfinity: true }),
        (base) => {
          const { nextBaseMs } = nextBackoffDelay(base);
          expect(nextBaseMs).toBe(Math.min(base * 2, MAX_DELAY_MS));
        },
      ),
    );
  });

  it("nextBaseMs never exceeds MAX_DELAY_MS", () => {
    fc.assert(
      fc.property(
        fc.double({ min: 0, max: 1_000_000, noNaN: true, noDefaultInfinity: true }),
        (base) => {
          const { nextBaseMs } = nextBackoffDelay(base);
          expect(nextBaseMs).toBeLessThanOrEqual(MAX_DELAY_MS);
        },
      ),
    );
  });

  it("scheduledMs lies in [currentBaseMs, currentBaseMs + JITTER_MS)", () => {
    fc.assert(
      fc.property(
        fc.double({ min: 0, max: 100_000, noNaN: true, noDefaultInfinity: true }),
        fc.double({ min: 0, max: 0.999, noNaN: true, noDefaultInfinity: true }),
        (base, r) => {
          const { scheduledMs } = nextBackoffDelay(base, () => r);
          expect(scheduledMs).toBeGreaterThanOrEqual(base);
          expect(scheduledMs).toBeLessThan(base + JITTER_MS);
        },
      ),
    );
  });

  it("with random=0, scheduledMs equals currentBaseMs (no jitter)", () => {
    fc.assert(
      fc.property(
        fc.double({ min: 0, max: 100_000, noNaN: true, noDefaultInfinity: true }),
        (base) => {
          const { scheduledMs } = nextBackoffDelay(base, () => 0);
          expect(scheduledMs).toBe(base);
        },
      ),
    );
  });

  it("malformed random (NaN/negative/>=1/Infinity) still yields finite non-negative scheduledMs", () => {
    const malformed = [NaN, -0.5, -1, 1, 1.5, Infinity, -Infinity];
    for (const bad of malformed) {
      const { scheduledMs } = nextBackoffDelay(INITIAL_DELAY_MS, () => bad);
      expect(Number.isFinite(scheduledMs)).toBe(true);
      expect(scheduledMs).toBeGreaterThanOrEqual(INITIAL_DELAY_MS);
      expect(scheduledMs).toBeLessThan(INITIAL_DELAY_MS + JITTER_MS);
    }
  });

  it("repeated calls from any starting base eventually saturate at MAX_DELAY_MS", () => {
    fc.assert(
      fc.property(
        fc.double({ min: 0.5, max: 10_000, noNaN: true, noDefaultInfinity: true }),
        (start) => {
          let base = start;
          // 30 iterations is well above ceil(log2(MAX_DELAY_MS / start))
          // for any start >= 0.5, so this loop always saturates.
          for (let i = 0; i < 30; i++) {
            base = nextBackoffDelay(base).nextBaseMs;
          }
          expect(base).toBe(MAX_DELAY_MS);
        },
      ),
    );
  });

  it("INITIAL_DELAY_MS first call produces scheduledMs in [500, 750)", () => {
    fc.assert(
      fc.property(fc.double({ min: 0, max: 0.999, noNaN: true, noDefaultInfinity: true }), (r) => {
        const { scheduledMs, nextBaseMs } = nextBackoffDelay(INITIAL_DELAY_MS, () => r);
        expect(scheduledMs).toBeGreaterThanOrEqual(500);
        expect(scheduledMs).toBeLessThan(750);
        expect(nextBaseMs).toBe(1000); // 500 * 2
      }),
    );
  });
});
