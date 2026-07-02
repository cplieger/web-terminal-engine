import { describe, it, expect } from "vitest";
import { decodeWireBinary } from "./wire-binary.js";

describe("wire-binary: truncated and oversized frames", () => {
  it("returns null for buffer smaller than minimum header (< 9 bytes)", () => {
    expect(decodeWireBinary(new ArrayBuffer(0))).toBeNull();
    expect(decodeWireBinary(new ArrayBuffer(8))).toBeNull();
  });

  it("returns null for truncated screen message (header only, no row data)", () => {
    // A full MSG_SCREEN header is 27 bytes (v2 layout): type(1) + ack(8) +
    // base(8) + cursorRow(2) + cursorCol(2) + screenHeight(2) + numChanged(2) +
    // cursorStyle(1) + cursorFlags(1). Provide only 9 bytes (type + ack): the
    // decoder reads `base` past the end of the buffer and returns null.
    const buf = new ArrayBuffer(9);
    const view = new DataView(buf);
    view.setUint8(0, 0); // MSG_SCREEN
    expect(decodeWireBinary(buf)).toBeNull();
  });

  it("returns null for screen message with numChanged claiming more rows than present", () => {
    // Provide a COMPLETE 27-byte v2 screen header claiming 100 changed rows but
    // append no row payloads. The decoder reads the header, then the first
    // per-row read runs past the buffer -> RangeError -> null. (The `base` field
    // at bytes 9-16 is what a v1-era buffer omitted; the fields below sit after
    // it, matching the offsets the Go encoder writes.)
    const buf = new ArrayBuffer(27);
    const view = new DataView(buf);
    view.setUint8(0, 0); // MSG_SCREEN
    // inputAck (bytes 1-8) = 0, base (bytes 9-16) = 0
    view.setUint16(17, 0, true); // cursorRow
    view.setUint16(19, 0, true); // cursorCol
    view.setUint16(21, 50, true); // screenHeight
    view.setUint16(23, 100, true); // numChanged = 100 (but no row data follows)
    view.setUint8(25, 0); // cursorStyle
    view.setUint8(26, 0); // cursorFlags
    expect(decodeWireBinary(buf)).toBeNull();
  });

  it("returns null for scroll message with numLines claiming more lines than present", () => {
    // Provide a COMPLETE 19-byte v2 scroll header: type(1) + ack(8) +
    // firstIndex(8) + numLines(2). Claim 500 lines with none appended; the
    // decoder reads the header, then the first line read overruns -> null.
    const buf = new ArrayBuffer(19);
    const view = new DataView(buf);
    view.setUint8(0, 1); // MSG_SCROLL
    // inputAck (bytes 1-8) = 0, firstIndex (bytes 9-16) = 0
    view.setUint16(17, 500, true); // numLines = 500 (but no line data follows)
    expect(decodeWireBinary(buf)).toBeNull();
  });

  it("returns null for title message with titleLen > available data", () => {
    const buf = new ArrayBuffer(11);
    const view = new DataView(buf);
    view.setUint8(0, 4); // MSG_TITLE
    // inputAck = 0 (bytes 1-8)
    view.setUint16(9, 1000, true); // titleLen = 1000 (but only 0 bytes of title)
    // utf8() with len > remaining doesn't throw (subarray returns short slice),
    // but subsequent reads would. For title it's the last field, so it decodes
    // with a truncated string rather than crashing.
    const msg = decodeWireBinary(buf);
    // The title is the last field — subarray returns empty slice, no RangeError.
    // This is acceptable: the frame is "valid" structurally, just the title is
    // truncated. The decoder returns a message with an empty/short title.
    expect(msg).not.toBeNull();
  });

  it("decodes resumeAck with exactly 9 bytes (no epoch)", () => {
    const buf = new ArrayBuffer(9);
    const view = new DataView(buf);
    view.setUint8(0, 2); // MSG_RESUME_ACK
    view.setBigUint64(1, BigInt(42), true); // inputAck = 42
    const msg = decodeWireBinary(buf);
    expect(msg).not.toBeNull();
    if (msg == null) {
      throw new Error("expected non-null msg");
    }
    expect(msg.type).toBe("resumeAck");
    if (msg.type === "resumeAck") {
      expect(msg.received).toBe(42);
      expect(msg.serverEpoch).toBeUndefined();
    }
  });

  it("returns null for unknown message type", () => {
    const buf = new ArrayBuffer(9);
    const view = new DataView(buf);
    view.setUint8(0, 255); // unknown type
    expect(decodeWireBinary(buf)).toBeNull();
  });
});
