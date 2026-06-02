// @vitest-environment happy-dom

import { describe, it, expect } from "vitest";
import { decodeWireBinary } from "./wire-binary.js";
import type { TitleMessage } from "./types.js";

function encodeTitleMsg(title: string): ArrayBuffer {
  const titleBytes = new TextEncoder().encode(title);
  // msg_type(1) + inputAck(8) + title_byte_len(2) + title(N)
  const buf = new ArrayBuffer(1 + 8 + 2 + titleBytes.length);
  const view = new DataView(buf);
  const bytes = new Uint8Array(buf);
  view.setUint8(0, 4); // MSG_TITLE
  // inputAck = 0 (8 bytes, already zeroed)
  view.setUint16(9, titleBytes.length, true);
  bytes.set(titleBytes, 11);
  return buf;
}

describe("wire-binary: MSG_TITLE decoding", () => {
  it("decodes a title message", () => {
    const msg = decodeWireBinary(encodeTitleMsg("hello world"));
    expect(msg).not.toBeNull();
    expect(msg!.type).toBe("title");
    expect((msg as TitleMessage).title).toBe("hello world");
  });

  it("decodes an empty title", () => {
    const msg = decodeWireBinary(encodeTitleMsg(""));
    expect(msg).not.toBeNull();
    expect((msg as TitleMessage).title).toBe("");
  });

  it("decodes a title with unicode", () => {
    const msg = decodeWireBinary(encodeTitleMsg("日本語タイトル"));
    expect(msg).not.toBeNull();
    expect((msg as TitleMessage).title).toBe("日本語タイトル");
  });
});
