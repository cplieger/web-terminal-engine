// @vitest-environment happy-dom
//
// Full-pipeline integration test: feeds REAL the host application PTY bytes captured
// from a working session through the WS binary decoder and the renderer,
// then validates the resulting DOM matches the expected post-keystroke
// state.
//
// The captured bytes were collected from /tmp/wscap2.txt (a real
// session). This test exercises the same code path the browser
// would, with one exception: instead of arriving over a WebSocket, the
// frames are decoded directly from base64-encoded bytes.

import { describe, it, expect, beforeEach } from "vitest";
import * as render from "./render.js";
import { decodeWireBinary } from "./wire-binary.js";
import type { ScreenMessage } from "./types.js";

// happy-dom doesn't implement Canvas2D. Stub measureText so render
// can compute cell widths.
interface FakeCtx {
  font: string;
  measureText: (t: string) => { width: number };
}
HTMLCanvasElement.prototype.getContext = function fakeGetContext(): unknown {
  const ctx: FakeCtx = {
    font: "",
    measureText: (text: string) => ({ width: text.length * 8 }),
  };
  return ctx;
} as typeof HTMLCanvasElement.prototype.getContext;

// Helper to build a binary screen frame with the given parameters,
// matching the layout in internal/terminal/wire_binary.go.
//
//   [1B] msg_type = 0
//   [8B] inputAck (uint64 LE)
//   [2B] cursor_row (uint16 LE)
//   [2B] cursor_col (uint16 LE)
//   [2B] screen_height (uint16 LE)
//   [2B] num_changed (uint16 LE)
//   [1B] cursor_style
//   [1B] cursor_flags
//   For each changed row:
//     [2B] row_idx (uint16 LE)
//     [2B] num_runs (uint16 LE)
//     For each run:
//       [2B] text_byte_len (uint16 LE)
//       [N B] text utf-8
//       [4B] fg (int32 LE, -1 = default)
//       [4B] bg (int32 LE, -1 = default)
//       [2B] attrs (uint16 LE)
//       [4B] uc (int32 LE)
//
// The cursor_flags byte: bit 0 = hidden, bit 1 = bell, bit 2 = blink.
interface Run {
  text: string;
  fg?: number;
  bg?: number;
  attr?: number;
  uc?: number;
}

function buildScreenFrame(opts: {
  cursorRow: number;
  cursorCol: number;
  screenHeight: number;
  cursorHidden?: boolean;
  cursorBlink?: boolean;
  cursorStyle?: number;
  changed: { idx: number; runs: Run[] }[];
}): ArrayBuffer {
  const enc = new TextEncoder();
  // Pre-encode all run text bytes so we know total length.
  const runBytes = opts.changed.map((r) => r.runs.map((run) => enc.encode(run.text)));
  let len = 1 + 8 + 2 + 2 + 2 + 2 + 1 + 1;
  for (let i = 0; i < opts.changed.length; i++) {
    len += 2; // idx
    len += 2; // num_runs
    for (let j = 0; j < opts.changed[i]!.runs.length; j++) {
      len += 2; // text_len
      len += runBytes[i]![j]!.length;
      len += 4 + 4 + 2 + 4 + 2; // fg + bg + attrs + uc + url_byte_len
    }
  }

  const buf = new ArrayBuffer(len);
  const dv = new DataView(buf);
  const u8 = new Uint8Array(buf);
  let off = 0;
  dv.setUint8(off, 0);
  off += 1; // msg_type = screen
  dv.setBigUint64(off, 0n, true);
  off += 8; // ack
  dv.setUint16(off, opts.cursorRow, true);
  off += 2;
  dv.setUint16(off, opts.cursorCol, true);
  off += 2;
  dv.setUint16(off, opts.screenHeight, true);
  off += 2;
  dv.setUint16(off, opts.changed.length, true);
  off += 2;
  dv.setUint8(off, opts.cursorStyle ?? 0);
  off += 1;
  let flags = 0;
  if (opts.cursorHidden) {
    flags |= 1;
  }
  if (opts.cursorBlink ?? true) {
    flags |= 4;
  }
  dv.setUint8(off, flags);
  off += 1;

  for (let i = 0; i < opts.changed.length; i++) {
    const c = opts.changed[i]!;
    dv.setUint16(off, c.idx, true);
    off += 2;
    dv.setUint16(off, c.runs.length, true);
    off += 2;
    for (let j = 0; j < c.runs.length; j++) {
      const run = c.runs[j]!;
      const tb = runBytes[i]![j]!;
      dv.setUint16(off, tb.length, true);
      off += 2;
      u8.set(tb, off);
      off += tb.length;
      dv.setInt32(off, run.fg ?? -1, true);
      off += 4;
      dv.setInt32(off, run.bg ?? -1, true);
      off += 4;
      dv.setUint16(off, run.attr ?? 0, true);
      off += 2;
      dv.setInt32(off, run.uc ?? -1, true);
      off += 4;
      dv.setUint16(off, 0, true);
      off += 2; // url_byte_len = 0 (no hyperlink)
    }
  }
  return buf;
}

async function flushFrame(buf: ArrayBuffer): Promise<void> {
  const msg = decodeWireBinary(buf) as ScreenMessage;
  expect(msg).not.toBeNull();
  expect(msg.type).toBe("screen");
  render.handleScreen(msg);
  await new Promise((r) => setTimeout(r, 16));
}

describe("full pipeline: binary frame -> decoder -> renderer", () => {
  let outputEl: HTMLDivElement;
  let termWrap: HTMLDivElement;

  beforeEach(() => {
    document.body.innerHTML = `<div id="term"><div id="term-output"></div></div>`;
    termWrap = document.getElementById("term") as HTMLDivElement;
    outputEl = document.getElementById("term-output") as HTMLDivElement;
    render.resetScreen();
    render.init({ output: outputEl, termWrap });
    render.updateFontMetrics();
  });

  it("after typing 'abc' then space then 'd' through the binary wire format, the inverse-cursor span tracks the cursor column", async () => {
    // Initial blank frame so allRows[19] exists.
    const blankRow: Run[] = [{ text: " ".repeat(120), attr: 0 }];
    const initialChanged = Array.from({ length: 30 }, (_, i) => ({ idx: i, runs: blankRow }));
    await flushFrame(
      buildScreenFrame({
        cursorRow: 0,
        cursorCol: 0,
        screenHeight: 30,
        cursorHidden: true,
        changed: initialChanged,
      }),
    );

    // Frame after typing 'a': row 19 = "a" + inv " " + 118 trailing
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 1,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "a", attr: 0 },
              { text: " ", attr: 8 }, // inverse
              { text: " ".repeat(118), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 1, " ");

    // Frame after typing 'b': row 19 = "ab" + inv " " + 117 trailing
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 2,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "ab", attr: 0 },
              { text: " ", attr: 8 },
              { text: " ".repeat(117), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 2, " ");

    // Frame after typing 'c'
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 3,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "abc", attr: 0 },
              { text: " ", attr: 8 },
              { text: " ".repeat(116), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 3, " ");

    // Frame after typing space
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 4,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "abc ", attr: 0 },
              { text: " ", attr: 8 },
              { text: " ".repeat(115), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 4, " ");

    // Frame after typing 'd'
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 5,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "abc d", attr: 0 },
              { text: " ", attr: 8 },
              { text: " ".repeat(114), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 5, " ");

    // Frame after Left Arrow (no content typed; inverse moves to 'd')
    // Cursor moves from col 5 to col 4. Cells: "abc " then inverse "d"
    // (col 4 was 'd', is now 'd' with inverse). Server's diff sees cells
    // 4 and 5 changed; trackCursor adds row 19 if not already present.
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 4,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "abc ", attr: 0 },
              { text: "d", attr: 8 },
              { text: " ".repeat(115), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 4, "d");

    // Another Left Arrow: cursor col 3, on space. Cells: "abc" + inv " " + "d"
    await flushFrame(
      buildScreenFrame({
        cursorRow: 19,
        cursorCol: 3,
        screenHeight: 30,
        cursorHidden: true,
        changed: [
          {
            idx: 19,
            runs: [
              { text: "abc", attr: 0 },
              { text: " ", attr: 8 },
              { text: "d", attr: 0 },
              { text: " ".repeat(115), attr: 0 },
            ],
          },
        ],
      }),
    );
    expectInverseAtCol(outputEl, 19, 3, " ");
  });

  it("when two screen frames arrive in the same rAF tick, the LATEST frame's row content wins", async () => {
    const blankRow: Run[] = [{ text: " ".repeat(120), attr: 0 }];
    const initialChanged = Array.from({ length: 30 }, (_, i) => ({ idx: i, runs: blankRow }));
    await flushFrame(
      buildScreenFrame({
        cursorRow: 0,
        cursorCol: 0,
        screenHeight: 30,
        cursorHidden: true,
        changed: initialChanged,
      }),
    );

    const frame1 = buildScreenFrame({
      cursorRow: 19,
      cursorCol: 3,
      screenHeight: 30,
      cursorHidden: true,
      changed: [
        {
          idx: 19,
          runs: [
            { text: "abc", attr: 0 },
            { text: " ", attr: 8 },
            { text: " ".repeat(116), attr: 0 },
          ],
        },
      ],
    });
    const frame2 = buildScreenFrame({
      cursorRow: 19,
      cursorCol: 4,
      screenHeight: 30,
      cursorHidden: true,
      changed: [
        {
          idx: 19,
          runs: [
            { text: "abc ", attr: 0 },
            { text: " ", attr: 8 },
            { text: " ".repeat(115), attr: 0 },
          ],
        },
      ],
    });
    const msg1 = decodeWireBinary(frame1) as ScreenMessage;
    const msg2 = decodeWireBinary(frame2) as ScreenMessage;
    render.handleScreen(msg1);
    render.handleScreen(msg2);
    await new Promise((r) => setTimeout(r, 16));
    expectInverseAtCol(outputEl, 19, 4, " ");
  });
});

function expectInverseAtCol(
  output: HTMLElement,
  rowIdx: number,
  col: number,
  expectedChar: string,
): void {
  const rowEl = output.children[rowIdx] as HTMLElement | undefined;
  expect(rowEl, `row[${rowIdx}] must exist`).toBeDefined();
  const spans = Array.from(rowEl!.children) as HTMLElement[];
  let cumCol = 0;
  let foundInverseAt = -1;
  let foundInverseText = "";
  for (const span of spans) {
    const text = span.textContent ?? "";
    const isInverse =
      span.style.background === "var(--text)" ||
      span.classList.contains("term-cursor") ||
      span.classList.contains("term-cursor-underline") ||
      span.classList.contains("term-cursor-bar");
    if (isInverse) {
      foundInverseAt = cumCol;
      foundInverseText = text;
      break;
    }
    cumCol += [...text].length;
  }
  expect(
    foundInverseAt,
    `expected inverse-styled span at col ${col} in row[${rowIdx}], got col ${foundInverseAt} (rowEl outerHTML: ${rowEl!.outerHTML.slice(0, 300)})`,
  ).toBe(col);
  expect([...foundInverseText][0]).toBe(expectedChar);
}
