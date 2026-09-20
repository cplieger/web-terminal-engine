// The kitty protocol's "unicode-key-code" rule: the codepoint of the key's
// UNSHIFTED (base-layout) value, so Ctrl+Shift+A and Ctrl+A both report 'a' and
// a composed Alt/Option character reports the physical key it came from
// (https://sw.kovidgoyal.net/kitty/keyboard-protocol/, "Key codes"). Modifier
// digit = 1 + bitmask, 1:Shift 2:Alt 4:Ctrl 8:Meta. Beyond keyboard.test.ts's
// shifted symbols and lower-case letters: the UPPER-case range, the empty-string
// input (the toolbar's sticky-Ctrl path length-checks before calling), the
// ev.code-derived paths, and the ev.key fallback for a key with no code.

import { describe, it, expect, beforeEach } from "vitest";
import { kittyCtrlCharSeq, mapKeyboardEvent, type KeyboardResult } from "./keyboard.js";
import { createModeState, POWER_ON_MODES } from "./modes.js";

const modes = createModeState();

const ESC = "\x1b";
const KITTY_DISAMBIGUATE = 1;

function ev(init: KeyboardEventInit & { key: string; code?: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

/** Encode under the kitty disambiguate flag via the injected-modes seam. */
function underKitty(init: KeyboardEventInit & { key: string; code?: string }): KeyboardResult {
  modes.applySnapshot({ ...POWER_ON_MODES, keyboardFlags: KITTY_DISAMBIGUATE });
  return mapKeyboardEvent(ev(init), modes);
}

beforeEach(() => {
  modes.applySnapshot(POWER_ON_MODES);
});

describe("kittyCtrlCharSeq: the upper-case letter range", () => {
  it("encodes an upper-case letter as its lower-case codepoint with ctrl+shift", () => {
    // The glyph carries the Shift; the codepoint must not. Both ends of the
    // A-Z range, since the range test is what decides between 6 (ctrl+shift)
    // and 5 (ctrl).
    expect(kittyCtrlCharSeq("A")).toBe(`${ESC}[97;6u`);
    expect(kittyCtrlCharSeq("Z")).toBe(`${ESC}[122;6u`);
  });

  it("has nothing to encode for an empty string", () => {
    // The toolbar's sticky-Ctrl applier is the only caller that length-checks
    // its input, so this refusal is the encoder's own floor: without it the
    // sequence would carry a literal "undefined" as its codepoint.
    expect(kittyCtrlCharSeq("")).toBeNull();
  });
});

describe("kitty unicode-key-code: derived from the physical key", () => {
  it("reports the base key of a composed Alt character, not the composed glyph", () => {
    // macOS Option+e then e produces "é" with ev.code still "KeyE". The app must
    // see 'e' (101), which is what makes Alt combos usable on a composing layout.
    expect(underKitty({ key: "é", code: "KeyE", altKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}[101;3u`,
    });
  });

  it("reports a punctuation key's unshifted codepoint from its ev.code", () => {
    // "Minus" and "Period" are read from the physical-key table, not from the
    // produced character, so a shifted or remapped layout still reports the base.
    expect(underKitty({ key: "-", code: "Minus", ctrlKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}[45;5u`,
    });
    expect(underKitty({ key: ".", code: "Period", ctrlKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}[46;5u`,
    });
  });

  it("case-folds ev.key when the browser reports no physical code", () => {
    // Firefox reports an empty ev.code for some non-US layouts; the fallback
    // still owes the app the UNSHIFTED codepoint, so "Ä" reports 'ä' (228).
    expect(underKitty({ key: "Ä", code: "", ctrlKey: true, shiftKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}[228;6u`,
    });
  });
});

describe("kitty disambiguate supersedes application-keypad SS3", () => {
  it("does not SS3-encode a text keypad digit while the flag is active", () => {
    // DECKPAM would send ESC O u for Numpad5. Under the protocol the keypad's
    // TEXT keys stay text (the hidden textarea types them) — the SS3 branch is
    // skipped so keypad keys reach the kitty encoder at all.
    modes.applySnapshot({
      ...POWER_ON_MODES,
      applicationKeypad: true,
      keyboardFlags: KITTY_DISAMBIGUATE,
    });
    expect(mapKeyboardEvent(ev({ key: "5", code: "Numpad5" }), modes)).toEqual({ kind: "ignore" });
  });

  it("still SS3-encodes it when the protocol is off", () => {
    // The other half of the pair: with the flag clear, DECKPAM owns the numpad.
    modes.applySnapshot({ ...POWER_ON_MODES, applicationKeypad: true });
    expect(mapKeyboardEvent(ev({ key: "5", code: "Numpad5" }), modes)).toEqual({
      kind: "send",
      bytes: `${ESC}Ou`,
    });
  });
});
