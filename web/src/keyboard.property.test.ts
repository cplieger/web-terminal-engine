import { describe, it, expect, beforeEach } from "vitest";
import fc from "fast-check";
import { bracketTextForPaste } from "./keyboard.js";
import * as modes from "./modes.js";

// Paste-injection safety. When bracketed-paste mode is on, bracketTextForPaste
// must sanitise every ESC byte in the pasted text so an attacker-controlled
// paste cannot smuggle a premature paste-end (ESC [ 201 ~) and have the
// trailing bytes interpreted as typed commands. The example tests pin two
// fixed strings; this property asserts the invariant over arbitrary inputs
// biased toward the ESC and sentinel bytes the threat model cares about.
const OPEN = "\x1b[200~";
const CLOSE = "\x1b[201~";

const pasteText = fc
  .array(
    fc.oneof(
      fc.constantFrom("\x1b", "\x1b[201~", "\x1b[200~", "[", "201~", "x", "\n", "\t"),
      fc.string({ maxLength: 5 }),
    ),
    { maxLength: 20 },
  )
  .map((parts) => parts.join(""));

describe("bracketTextForPaste: paste-injection safety (property)", () => {
  beforeEach(() => {
    // modes is module-singleton state (isolate:false); force bracketed-paste on.
    modes.setModes(true, false);
  });

  it("wraps in sentinels and the body never contains a raw ESC or a literal paste-end", () => {
    fc.assert(
      fc.property(pasteText, (text) => {
        const out = bracketTextForPaste(text);
        expect(out.startsWith(OPEN)).toBe(true);
        expect(out.endsWith(CLOSE)).toBe(true);
        const body = out.slice(OPEN.length, out.length - CLOSE.length);
        // No raw ESC survives, so an embedded paste-end cannot terminate the region.
        expect(body.includes("\x1b")).toBe(false);
        expect(body.includes(CLOSE)).toBe(false);
      }),
    );
  });

  it("returns text unchanged when bracketed-paste mode is off", () => {
    modes.setModes(false, false);
    fc.assert(
      fc.property(pasteText, (text) => {
        expect(bracketTextForPaste(text)).toBe(text);
      }),
    );
  });
});
