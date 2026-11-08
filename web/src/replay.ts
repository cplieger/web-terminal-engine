// The resume replay's start, which the server decides (terminal/terminal.go
// replayStart) and the client predicts to tell a clamped replay from a gap.
// Both halves test against wire-golden/replay-start.json.

/**
 * The server's clamp on `replayMax` (wire-golden/limits.json), so the client
 * sends the value the server will actually honor. The equality is load-bearing:
 * a server honoring something smaller would place the real replay start above
 * the prediction and leave a genuine jump undetected.
 */
export const MAX_REPLAY_LINES = 2000;

/**
 * The index of the first line a resume replay carries: `haveThrough + 1`, raised
 * to the oldest retained line and to `committed` minus the bound (`replayMax`,
 * at most MAX_REPLAY_LINES; null is the ceiling).
 */
export function replayStart(
  committed: number,
  oldest: number,
  haveThrough: number,
  replayMax: number | null,
): number {
  const bound = Math.min(replayMax ?? MAX_REPLAY_LINES, MAX_REPLAY_LINES);
  return Math.max(haveThrough + 1, oldest, committed - bound);
}
