// The client's copies of the server's protocol limits, pinned to
// ../../wire-golden/limits.json, the hand-written file
// terminal/limits_golden_test.go pins the server's constants to. The input
// message size and the resize floors are pinned through behaviour in
// connection-outbox.test.ts and render-size.test.ts.
import { describe, it, expect } from "vitest";
import { MAX_REPLAY_LINES } from "./replay.js";
import { PAGE_SIZE } from "./store.js";
import limitsRaw from "../../wire-golden/limits.json?raw";

const limits = JSON.parse(limitsRaw) as { maxReplayLines: number; historyPageSize: number };

describe("protocol limits shared with the server", () => {
  it("clamps replayMax to the server's ceiling", () => {
    expect(MAX_REPLAY_LINES).toBe(limits.maxReplayLines);
  });

  it("pages history in the server's page size", () => {
    expect(PAGE_SIZE).toBe(limits.historyPageSize);
  });
});
