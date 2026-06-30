import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { decodeWireBinary } from "./wire-binary.js";

describe("wire-binary fuzz: random ArrayBuffers", () => {
  it("never throws on arbitrary input; parsed messages have well-typed fields", () => {
    fc.assert(
      fc.property(fc.uint8Array({ minLength: 0, maxLength: 512 }), (bytes) => {
        const buf = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
        const msg = decodeWireBinary(buf);
        if (msg === null) {
          return true;
        }
        if (msg.type === "screen") {
          if (!Number.isFinite(msg.cursor[0])) {
            return false;
          }
          if (!Number.isFinite(msg.cursor[1])) {
            return false;
          }
          for (const idx of msg.changed) {
            if (!Number.isFinite(idx)) {
              return false;
            }
          }
          for (const row of msg.rows) {
            if (!row) {
              continue;
            }
            for (const run of row) {
              if (typeof run.t !== "string") {
                return false;
              }
              if (!Number.isFinite(run.f)) {
                return false;
              }
              if (!Number.isFinite(run.b)) {
                return false;
              }
              if (run.u !== undefined && typeof run.u !== "string") {
                return false;
              }
            }
          }
        } else if (msg.type === "scroll") {
          for (const line of msg.lines) {
            for (const run of line) {
              if (typeof run.t !== "string") {
                return false;
              }
              if (!Number.isFinite(run.f)) {
                return false;
              }
            }
          }
        } else if (msg.type === "resumeAck") {
          if (!Number.isFinite(msg.received)) {
            return false;
          }
        } else if (msg.type === "modes") {
          if (typeof msg.bracketedPaste !== "boolean") {
            return false;
          }
          if (!Number.isFinite(msg.mouseMode)) {
            return false;
          }
        } else if (msg.type === "title") {
          if (typeof msg.title !== "string") {
            return false;
          }
        }
        return true;
      }),
    );
    expect(true).toBe(true);
  });
});

function buildScreenFrame(o: {
  base: number;
  cursorRow: number;
  cursorCol: number;
  screenHeight: number;
  changedIdx: number;
  text: string;
}): ArrayBuffer {
  const tb = new TextEncoder().encode(o.text);
  const rowLen = 2 + (2 + tb.length + 4 + 4 + 2 + 4 + 2);
  const buf = new ArrayBuffer(1 + 8 + 8 + 2 + 2 + 2 + 2 + 1 + 1 + 2 + rowLen);
  const dv = new DataView(buf);
  const u8 = new Uint8Array(buf);
  let off = 0;
  dv.setUint8(off, 0);
  off += 1; // MSG_SCREEN
  dv.setBigUint64(off, 0n, true);
  off += 8; // inputAck
  dv.setBigUint64(off, BigInt(o.base), true);
  off += 8; // base
  dv.setUint16(off, o.cursorRow, true);
  off += 2;
  dv.setUint16(off, o.cursorCol, true);
  off += 2;
  dv.setUint16(off, o.screenHeight, true);
  off += 2;
  dv.setUint16(off, 1, true);
  off += 2; // numChanged
  dv.setUint8(off, 0);
  off += 1; // cursorStyle
  dv.setUint8(off, 0);
  off += 1; // cursorFlags
  dv.setUint16(off, o.changedIdx, true);
  off += 2; // changed row index
  dv.setUint16(off, 1, true);
  off += 2; // numRuns
  dv.setUint16(off, tb.length, true);
  off += 2; // tlen
  u8.set(tb, off);
  off += tb.length;
  dv.setInt32(off, -1, true);
  off += 4; // fg
  dv.setInt32(off, -1, true);
  off += 4; // bg
  dv.setUint16(off, 0, true);
  off += 2; // attrs
  dv.setInt32(off, -1, true);
  off += 4; // underline color
  dv.setUint16(off, 0, true); // url len
  return buf;
}

describe("wire-binary fuzz: structured frames and truncation robustness", () => {
  it("decodes structurally-valid screen frames built from fuzzed fields", () => {
    fc.assert(
      fc.property(
        fc.record({
          base: fc.nat(1_000_000),
          cursorRow: fc.nat(65535),
          cursorCol: fc.nat(65535),
          screenHeight: fc.integer({ min: 1, max: 200 }),
          text: fc.string({ maxLength: 20 }),
        }),
        (r) => {
          const changedIdx = r.screenHeight - 1;
          const msg = decodeWireBinary(buildScreenFrame({ ...r, changedIdx }));
          expect(msg?.type).toBe("screen");
          if (msg?.type !== "screen") {
            return;
          }
          expect(msg.base).toBe(r.base);
          expect(msg.cursor).toEqual([r.cursorRow, r.cursorCol]);
          expect(msg.changed).toEqual([changedIdx]);
          expect(msg.rows[changedIdx]?.[0]?.t).toBe(r.text);
        },
      ),
    );
  });

  it("never throws when a valid frame is truncated at any byte offset", () => {
    const full = buildScreenFrame({
      base: 12345,
      cursorRow: 2,
      cursorCol: 3,
      screenHeight: 10,
      changedIdx: 5,
      text: "hi \u00e9\u4e16",
    });
    fc.assert(
      fc.property(fc.nat(full.byteLength), (len) => {
        expect(() => decodeWireBinary(full.slice(0, len))).not.toThrow();
      }),
    );
  });
});
