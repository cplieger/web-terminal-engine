// A store's content belongs to one server process (its boot epoch): absolute
// line indices start over in a new process, so content from another one must
// never be shown as live or kept under new output.

import { describe, expect, it } from "vitest";
import { LineStore } from "./store.js";
import { ackEpoch } from "./test-helpers/store-epoch.js";
import type { ScrollMessage } from "./types.js";

// A real epoch is process-start nanoseconds, far above 2^53.
const E1 = 1_777_000_000_000_000_000;
const E2 = 1_777_000_000_500_000_000;

function scrollMsg(firstIndex: number, texts: string[]): ScrollMessage {
  return { type: "scroll", firstIndex, lines: texts.map((t) => [{ t }]), inputAck: 0 };
}

function withLines(texts: string[]): LineStore {
  const store = new LineStore();
  store.applyScroll(scrollMsg(0, texts));
  return store;
}

function restored(epoch: number): LineStore {
  const source = withLines(["old-0", "old-1"]);
  ackEpoch(source, epoch);
  const back = LineStore.fromSnapshot(structuredClone(source.snapshot()));
  if (back === null) {
    throw new Error("the snapshot did not restore");
  }
  return back;
}

describe("a store's content and the server process it came from", () => {
  it("adopts the first epoch a server reports and keeps its content", () => {
    const store = withLines(["a", "b"]);
    expect(store.serverEpoch()).toBe(0);

    expect(ackEpoch(store, E1)).toBe(false);

    expect(store.serverEpoch()).toBe(E1);
    expect(store.highestIndex()).toBe(1);
  });

  it("keeps a learned epoch's content on the same epoch and on an ack that reports none", () => {
    const store = withLines(["a", "b"]);
    ackEpoch(store, E1);

    expect(ackEpoch(store, E1)).toBe(false);
    expect(ackEpoch(store, null)).toBe(false);

    expect(store.serverEpoch()).toBe(E1);
    expect(store.highestIndex()).toBe(1);
  });

  it("discards a learned epoch's content when another process answers", () => {
    const store = withLines(["a", "b"]);
    ackEpoch(store, E1);

    expect(ackEpoch(store, E2)).toBe(true);

    expect(store.highestIndex()).toBe(-1);
    expect(store.serverEpoch()).toBe(E2);
    expect(store.drainChanges().fullReset).toBe(true);
  });

  it("keeps restored content only when a server confirms its epoch", () => {
    const confirmed = restored(E1);
    expect(confirmed.serverEpoch()).toBe(E1);
    expect(ackEpoch(confirmed, E1)).toBe(false);
    expect(confirmed.getLine(1)?.[0]?.t).toBe("old-1");

    const contradicted = restored(E1);
    expect(ackEpoch(contradicted, E2)).toBe(true);
    expect(contradicted.highestIndex()).toBe(-1);
    expect(contradicted.serverEpoch()).toBe(E2);
  });

  it("discards restored content that a server reporting no epoch cannot confirm", () => {
    const store = restored(E1);

    expect(ackEpoch(store, null)).toBe(true);

    expect(store.highestIndex()).toBe(-1);
    expect(store.serverEpoch()).toBe(0);
  });

  it("treats a confirmed restore as learned: a later silent ack keeps it", () => {
    const store = restored(E1);
    ackEpoch(store, E1);

    expect(ackEpoch(store, null)).toBe(false);
    expect(store.getLine(0)?.[0]?.t).toBe("old-0");
  });

  it("keeps the epoch across a content reset, so later output files under it", () => {
    const store = withLines(["a"]);
    ackEpoch(store, E1);
    store.reset();
    store.applyScroll(scrollMsg(5, ["after-clear"]));

    expect(store.snapshot()?.serverEpoch).toBe(E1);
  });

  it("files a snapshot under the store's own epoch and writes none before one is known", () => {
    const store = withLines(["a", "b"]);
    expect(store.snapshot()).toBeNull();

    ackEpoch(store, E2);

    expect(store.snapshot()?.serverEpoch).toBe(E2);
  });

  it("restores only a snapshot carrying a positive integer epoch", () => {
    const store = withLines(["a"]);
    ackEpoch(store, E1);
    const good = store.snapshot();
    expect(good).not.toBeNull();
    expect(LineStore.fromSnapshot(good)).not.toBeNull();

    for (const bad of [0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, "7", null, undefined]) {
      expect(LineStore.fromSnapshot({ ...good, serverEpoch: bad })).toBeNull();
    }
    const { serverEpoch: _dropped, ...noEpoch } = good!;
    expect(LineStore.fromSnapshot(noEpoch)).toBeNull();
  });

  it("reports a restore as unconfirmed until a resumeAck settles it or its content goes", () => {
    expect(withLines(["a"]).isUnconfirmedRestore()).toBe(false);

    const kept = restored(E1);
    expect(kept.isUnconfirmedRestore()).toBe(true);
    ackEpoch(kept, E1);
    expect(kept.isUnconfirmedRestore()).toBe(false);

    const emptied = restored(E1);
    emptied.reset();
    expect(emptied.isUnconfirmedRestore()).toBe(false);
  });
});
