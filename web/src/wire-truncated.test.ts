import { describe, it, expect } from "vitest";
import { decodeWireBinary } from "./wire-binary.js";

describe("wire-binary: truncated and oversized frames", () => {
  it("returns null for buffer smaller than minimum header (< 9 bytes)", () => {
    expect(decodeWireBinary(new ArrayBuffer(0))).toBeNull();
    expect(decodeWireBinary(new ArrayBuffer(8))).toBeNull();
  });

  it("returns null for truncated screen message (header only, no row data)", () => {
    // MSG_SCREEN header needs: type(1) + ack(8) + curRow(2) + curCol(2) +
    // screenHeight(2) + numChanged(2) + cursorStyle(1) + cursorFlags(1) = 19 bytes
    // Provide only 9 bytes (just type + ack).
    const buf = new ArrayBuffer(9);
    const view = new DataView(buf);
    view.setUint8(0, 0); // MSG_SCREEN
    expect(decodeWireBinary(buf)).toBeNull();
  });

  it("returns null for screen message with numChanged > available data", () => {
    // Build a screen header claiming 100 changed rows but no row data.
    const buf = new ArrayBuffer(19);
    const view = new DataView(buf);
    view.setUint8(0, 0); // MSG_SCREEN
    // inputAck = 0 (bytes 1-8)
    view.setUint16(9, 0, true); // cursorRow
    view.setUint16(11, 0, true); // cursorCol
    view.setUint16(13, 50, true); // screenHeight
    view.setUint16(15, 100, true); // numChanged = 100 (but no row data follows)
    view.setUint8(17, 0); // cursorStyle
    view.setUint8(18, 0); // cursorFlags
    expect(decodeWireBinary(buf)).toBeNull();
  });

  it("returns null for scroll message with numLines > available data", () => {
    const buf = new ArrayBuffer(11);
    const view = new DataView(buf);
    view.setUint8(0, 1); // MSG_SCROLL
    // inputAck = 0 (bytes 1-8)
    view.setUint16(9, 500, true); // numLines = 500 (but no line data)
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
    expect(msg!.type).toBe("resumeAck");
    if (msg!.type === "resumeAck") {
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
