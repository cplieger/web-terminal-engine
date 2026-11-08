// Entering, resizing and leaving the alternate screen, what each repaints, and
// the frozen main window during it. The main window descriptor keeps the main
// screen's base and height through an alt session: alt exit restores it, the
// retention bound is defined over it, and an alt-time history frame must not
// overwrite it.

import { describe, it, expect } from "vitest";
import { LineStore } from "./store.js";
import type { ScreenMessage, ScrollMessage, WireRun } from "./types.js";

function row(text: string): WireRun[] {
  return [{ t: text, f: -1, b: -1, a: 0, uc: -1 }];
}

function screenMsg(
  base: number,
  height: number,
  opts: Partial<{
    altActive: boolean;
    cursor: [number, number];
    cursorStyle: number;
    cursorHidden: boolean;
    cursorBlink: boolean;
    changed: number[];
    label: string;
  }> = {},
): ScreenMessage {
  const rows: WireRun[][] = [];
  for (let y = 0; y < height; y++) {
    rows.push(row(`${opts.label ?? "W"}${base + y}`));
  }
  const msg: ScreenMessage = {
    type: "screen",
    base,
    rows,
    changed: opts.changed ?? rows.map((_, y) => y),
    cursor: opts.cursor ?? [0, 0],
    altActive: opts.altActive ?? false,
    scrollbackCleared: false,
  };
  if (opts.cursorStyle !== undefined) {
    msg.cursorStyle = opts.cursorStyle;
  }
  if (opts.cursorHidden !== undefined) {
    msg.cursorHidden = opts.cursorHidden;
  }
  if (opts.cursorBlink !== undefined) {
    msg.cursorBlink = opts.cursorBlink;
  }
  return msg;
}

function scrollMsg(firstIndex: number, texts: string[]): ScrollMessage {
  return { type: "scroll", firstIndex, lines: texts.map(row) };
}

function altTexts(s: LineStore): string[] {
  return s.getAltRows().map((r) => r.map((run) => run.t).join(""));
}

function textAt(s: LineStore, abs: number): string | undefined {
  const runs = s.getLine(abs);
  return runs === undefined ? undefined : runs.map((r) => r.t).join("");
}

describe("LineStore: entering, resizing and leaving the alternate screen", () => {
  it("allocates one empty row per screen row on entry", () => {
    // Every row of the grid has to be a readable run array even before a frame
    // marks it changed: the renderer walks the whole grid, not the changed set.
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    expect(s.isAlt()).toBe(true);
    expect(s.getAltRows()).toEqual([[], [], []]);
  });

  it("lands a changed alt row and marks the grid changed", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [1], label: "A" }));
    expect(altTexts(s)).toEqual(["", "A1", ""]);
    expect(s.drainChanges().altChanged).toBe(true);
  });

  it("reports no alt change for a repeated frame that changes no row", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    expect(s.drainChanges().altChanged).toBe(false);
  });

  it("marks the grid changed when the alt height changes", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 5, { altActive: true, changed: [] }));
    expect(s.getAltRows().length).toBe(5);
    expect(s.drainChanges().altChanged).toBe(true);
  });

  it("drops the grid and marks it changed on the way back to the main screen", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 3, { altActive: true, changed: [] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 3));
    expect(s.isAlt()).toBe(false);
    expect(s.getAltRows()).toEqual([]);
    expect(s.drainChanges().altChanged).toBe(true);
  });
});

describe("LineStore: the cursor during an alt session", () => {
  it("tracks the alt frame's cursor while the main base and height stay frozen", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3, { cursor: [0, 0] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 5, { altActive: true, cursor: [2, 7], changed: [] }));
    expect(s.getWindow()).toEqual({
      base: 100,
      height: 3,
      cursorRow: 2,
      cursorCol: 7,
      cursorStyle: 0,
      cursorHidden: false,
      cursorBlink: false,
    });
    expect(s.drainChanges().windowChanged).toBe(true);
  });

  it("reports no window change for an alt frame whose cursor did not move", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3, { cursor: [1, 1] }));
    s.applyScreen(screenMsg(0, 5, { altActive: true, cursor: [1, 1], changed: [] }));
    s.drainChanges();
    s.applyScreen(screenMsg(0, 5, { altActive: true, cursor: [1, 1], changed: [] }));
    expect(s.drainChanges().windowChanged).toBe(false);
  });

  it("defaults an alt frame's omitted cursor flags to a visible, steady cursor", () => {
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3, { cursorHidden: true, cursorBlink: true }));
    s.applyScreen(screenMsg(0, 5, { altActive: true, cursor: [0, 0], changed: [] }));
    const win = s.getWindow();
    expect(win.cursorHidden).toBe(false);
    expect(win.cursorBlink).toBe(false);
  });
});

describe("LineStore: history arriving while the alternate screen is up", () => {
  it("refuses a scroll line AT the frozen main base and keeps the one below it", () => {
    // At or above the frozen base is the window region alt exit restores;
    // overwriting it there is the 2026-08 write-ordering race. Strictly below is
    // main-screen scrollback and safe to store — it surfaces at alt exit.
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3));
    s.applyScreen(screenMsg(0, 5, { altActive: true, changed: [] }));

    s.applyScroll(scrollMsg(99, ["H99", "SPOOF100"]));

    expect(textAt(s, 100)).toBe("W100");
    expect(textAt(s, 99)).toBe("H99");
  });

  it("keeps the lines the server committed in the pass that entered alt", () => {
    // `seq 1 200; vim` in one read: the server commits the scrolled main lines
    // in the same pass as the alt enter, and the alt frame's base is the
    // committed count, then the scroll chunk follows.
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3));
    s.applyScreen(screenMsg(118, 5, { altActive: true, changed: [] }));

    s.applyScroll(
      scrollMsg(
        100,
        Array.from({ length: 19 }, (_, i) => `H${100 + i}`),
      ),
    );

    expect(textAt(s, 100)).toBe("H100");
    expect(textAt(s, 117)).toBe("H117");
    expect(textAt(s, 118)).toBeUndefined();
    expect(s.getWindow().base).toBe(118);
    expect(s.getWindow().height).toBe(3);
  });

  it("raises the frozen base to a resumeAck's committed count before its replay", () => {
    // Lines committed while no client watched, with the session back in alt
    // at the reconnect: the replay precedes the alt window frame.
    const s = new LineStore();
    s.applyScreen(screenMsg(100, 3));
    s.applyScreen(screenMsg(100, 5, { altActive: true, changed: [] }));
    s.applyResumeAck({
      serverEpoch: null,
      committed: 130,
      serverOldest: 0,
      paging: false,
      sentHaveThrough: 99,
      sentReplayMax: null,
      viewportAbs: 100,
      following: true,
    });

    s.applyScroll(
      scrollMsg(
        100,
        Array.from({ length: 30 }, (_, i) => `R${100 + i}`),
      ),
    );

    expect(textAt(s, 100)).toBe("R100");
    expect(textAt(s, 129)).toBe("R129");
  });

  it("stores a solicited page during an alt session that never saw a main window", () => {
    // A fresh tab attaching straight into an in-alt session has no window to
    // protect, so the whole page applies exactly as on the main-screen path.
    const s = new LineStore();
    s.applyScreen(screenMsg(0, 5, { altActive: true, changed: [] }));

    s.noteSolicited(0, 3);
    s.applyHistoryScroll(scrollMsg(0, ["P0", "P1", "P2"]), 0);
    s.clearSolicited();

    expect(s.browseCacheSize()).toBe(3);
    expect(textAt(s, 0)).toBe("P0");
  });
});
