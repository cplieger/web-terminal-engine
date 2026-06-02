import { describe, it, expect } from "vitest";
import { decodeWireBinary } from "./wire-binary.js";
import type { ModesMessage } from "./types.js";
import * as modes from "./modes.js";

describe("DECSCNM (reverse video) wire decoding", () => {
  it("decodes reverseVideo=true from modes message with bit 5 set", () => {
    // Build a modes message: type=3, ack=0 (8 bytes), flags=0x20 (bit 5), mouseMode=0
    const buf = new ArrayBuffer(12);
    const view = new DataView(buf);
    view.setUint8(0, 3); // MSG_MODES
    // inputAck = 0 (bytes 1-8, already zero)
    view.setUint8(9, 0x20); // flags: bit 5 = reverse video
    view.setUint16(10, 0, true); // mouseMode = 0

    const msg = decodeWireBinary(buf) as ModesMessage;
    expect(msg).not.toBeNull();
    expect(msg.type).toBe("modes");
    expect(msg.reverseVideo).toBe(true);
    expect(msg.bracketedPaste).toBe(false);
  });

  it("decodes reverseVideo=false when bit 5 is not set", () => {
    const buf = new ArrayBuffer(12);
    const view = new DataView(buf);
    view.setUint8(0, 3); // MSG_MODES
    view.setUint8(9, 0x01); // flags: bit 0 only (bracketed paste)
    view.setUint16(10, 0, true);

    const msg = decodeWireBinary(buf) as ModesMessage;
    expect(msg).not.toBeNull();
    expect(msg.type).toBe("modes");
    expect(msg.reverseVideo).toBe(false);
    expect(msg.bracketedPaste).toBe(true);
  });

  it("modes.setModes stores and exposes reverseVideo", () => {
    modes.setModes(true, false, false, false, 0, false, true);
    expect(modes.isReverseVideo()).toBe(true);
    modes.setModes(true, false, false, false, 0, false, false);
    expect(modes.isReverseVideo()).toBe(false);
  });
});
