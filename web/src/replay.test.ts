// The client's prediction of the resume replay's start, pinned to
// ../../wire-golden/replay-start.json, the hand-written table
// terminal/replay_start_golden_test.go pins the server's replayStart to.
import { describe, it, expect } from "vitest";
import { replayStart } from "./replay.js";
import tableRaw from "../../wire-golden/replay-start.json?raw";

interface Row {
  name: string;
  committed: number;
  oldest: number;
  haveThrough: number;
  replayMax: number | null;
  start: number;
}

const rows = JSON.parse(tableRaw) as Row[];

describe("the resume replay start shared with the server", () => {
  it("reads a non-empty table", () => {
    expect(rows.length).toBeGreaterThan(0);
  });

  it.each(rows)("$name", (r) => {
    expect(replayStart(r.committed, r.oldest, r.haveThrough, r.replayMax)).toBe(r.start);
  });
});
