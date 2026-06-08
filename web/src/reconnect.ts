// Pure reconnect-delay scheduler for the terminal WebSocket.
//
// Reconnect strategy: exponential backoff capped at MAX_DELAY_MS, with
// uniform random jitter in [0, JITTER_MS) added to each scheduled
// attempt. The per-attempt jitter prevents thundering-herd reconnects
// when several browser tabs lose connectivity together; the cap keeps
// the worst-case wait bounded so a long network outage doesn't push
// the next retry an hour out.
//
// Pulled into a dedicated module so the formula is testable without
// real timers: callers inject `random()` (typically `Math.random`)
// and receive both the delay to wait this attempt and the new base
// delay for the next attempt. This separates the math from the
// timer-side-effect plumbing in the connection module.

/** Initial backoff delay in milliseconds. Used by the first retry after a clean connection. */
export const INITIAL_DELAY_MS = 500;

/** Maximum (capped) base delay in milliseconds. */
export const MAX_DELAY_MS = 8000;

/** Maximum jitter added per attempt (uniform random, exclusive upper bound). */
export const JITTER_MS = 250;

/** Result of computing the next backoff step. */
interface BackoffStep {
  /** Milliseconds to wait before the next reconnect attempt. */
  scheduledMs: number;
  /** New base delay to feed into the subsequent call. */
  nextBaseMs: number;
}

/**
 * nextBackoffDelay computes the wait time for this attempt and the
 * new base delay for the next attempt. Pure given a deterministic
 * `random` function; pass `Math.random` for production.
 *
 * Formula:
 *   scheduledMs = currentBaseMs + random() * JITTER_MS
 *   nextBaseMs  = min(currentBaseMs * 2, MAX_DELAY_MS)
 *
 * `random` is expected to return a number in [0, 1). When undefined
 * or out of range, the implementation clamps to that interval so
 * malformed inputs don't yield NaN/negative delays.
 */
export function nextBackoffDelay(
  currentBaseMs: number,
  random: () => number = Math.random,
): BackoffStep {
  const r = clamp01(random());
  const scheduledMs = currentBaseMs + r * JITTER_MS;
  const nextBaseMs = Math.min(currentBaseMs * 2, MAX_DELAY_MS);
  return { scheduledMs, nextBaseMs };
}

function clamp01(x: number): number {
  if (!Number.isFinite(x) || x < 0) {
    return 0;
  }
  if (x >= 1) {
    return 0.999_999_999;
  }
  return x;
}
