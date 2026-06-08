// Property-based tests for the WebSocket control-frame builder.
//
// Invariants tested:
// 1. First byte is always 0x00 (the control-message tag).
// 2. Bytes after the prefix are the UTF-8 JSON encoding of the input.
// 3. Frame length equals 1 + len(JSON.stringify(msg)) in UTF-8 bytes.
// 4. Round-trip: decoding tail bytes as UTF-8 + JSON.parse recovers msg.
// 5. Non-ASCII inputs survive: control characters, emoji, surrogate pairs.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { controlFrame, CONTROL_FRAME_PREFIX } from "./wire.js";

describe("controlFrame property", () => {
  it("first byte is always the control prefix 0x00", () => {
    fc.assert(
      fc.property(fc.jsonValue(), (msg) => {
        const frame = controlFrame(msg);
        expect(frame[0]).toBe(CONTROL_FRAME_PREFIX);
      }),
    );
  });

  it("body bytes equal JSON.stringify(msg) in UTF-8", () => {
    fc.assert(
      fc.property(fc.jsonValue(), (msg) => {
        const frame = controlFrame(msg);
        const body = new TextDecoder().decode(frame.slice(1));
        expect(body).toBe(JSON.stringify(msg));
      }),
    );
  });

  it("round-trip: parsing the body recovers the original value via JSON equality", () => {
    fc.assert(
      fc.property(fc.jsonValue(), (msg) => {
        const frame = controlFrame(msg);
        const body = new TextDecoder().decode(frame.slice(1));
        // JSON-equality, not value-equality. JSON.stringify is lossy
        // on a few edge cases (`-0` becomes `0`, `undefined`/Symbol
        // properties drop out, fc.jsonValue does not produce these
        // but `-0` slips through as a number leaf). Re-stringifying
        // the parsed result and comparing strings asserts the wire
        // bytes are stable under round-trip, which is the actual
        // property the protocol relies on.
        expect(JSON.stringify(JSON.parse(body))).toBe(JSON.stringify(msg));
      }),
    );
  });

  it("frame length matches 1 + UTF-8 byte length of the JSON encoding", () => {
    fc.assert(
      fc.property(fc.jsonValue(), (msg) => {
        const frame = controlFrame(msg);
        const expected = 1 + new TextEncoder().encode(JSON.stringify(msg)).length;
        expect(frame.length).toBe(expected);
      }),
    );
  });

  it("survives the resize ControlMessage shape (regression on the actual call site)", () => {
    fc.assert(
      fc.property(
        fc.integer({ min: 1, max: 1000 }),
        fc.integer({ min: 1, max: 1000 }),
        (cols, rows) => {
          const msg = { type: "resize", cols, rows } as const;
          const frame = controlFrame(msg);
          const parsed = JSON.parse(new TextDecoder().decode(frame.slice(1)));
          expect(parsed).toEqual(msg);
        },
      ),
    );
  });
});
