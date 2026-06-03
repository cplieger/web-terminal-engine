import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { decodeWireBinary } from "./wire-binary.js";

describe("wire-binary fuzz: random ArrayBuffers", () => {
  it("never throws on arbitrary input; parsed messages have well-typed fields", () => {
    fc.assert(
      fc.property(fc.uint8Array({ minLength: 0, maxLength: 512 }), (bytes) => {
        const buf = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
        const msg = decodeWireBinary(buf);
        if (msg === null) return true;
        if (msg.type === "screen") {
          if (!Number.isFinite(msg.cursor[0])) return false;
          if (!Number.isFinite(msg.cursor[1])) return false;
          for (const idx of msg.changed) {
            if (!Number.isFinite(idx)) return false;
          }
          for (const row of msg.rows) {
            if (!row) continue;
            for (const run of row) {
              if (typeof run.t !== "string") return false;
              if (!Number.isFinite(run.f)) return false;
              if (!Number.isFinite(run.b)) return false;
              if (run.u !== undefined && typeof run.u !== "string") return false;
            }
          }
        } else if (msg.type === "scroll") {
          for (const line of msg.lines) {
            for (const run of line) {
              if (typeof run.t !== "string") return false;
              if (!Number.isFinite(run.f)) return false;
            }
          }
        } else if (msg.type === "resumeAck") {
          if (!Number.isFinite(msg.received)) return false;
        } else if (msg.type === "modes") {
          if (typeof msg.bracketedPaste !== "boolean") return false;
          if (!Number.isFinite(msg.mouseMode)) return false;
        } else if (msg.type === "title") {
          if (typeof msg.title !== "string") return false;
        }
        return true;
      }),
    );
    expect(true).toBe(true);
  });
});
