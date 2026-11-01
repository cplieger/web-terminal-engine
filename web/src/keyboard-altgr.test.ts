// AltGr and Option characters under the kitty disambiguate flag and the legacy
// encoding, in the modifier shapes current engines dispatch: on Windows,
// Chromium (https://chromestatus.com/feature/5059438092222464) and Firefox
// (widget/windows/KeyboardLayout.cpp) clear ctrlKey and altKey and set AltGraph
// when AltGr, or Ctrl+Alt standing in for it, types a character; Linux reports
// AltGr as AltGraph alone; Firefox on macOS sets AltGraph on every Option chord
// (widget/cocoa/nsCocoaUtils.mm). Kitty spec, "Disambiguate escape codes": text
// keys stay text, ctrl and alt chords move to CSI u.

import { describe, it, expect, vi } from "vitest";
import { mapKeyboardEvent, type KeyboardModes, type KeyboardResult } from "./keyboard.js";

const ESC = "\x1b";
const LEGACY = 0;
const DISAMBIGUATE = 1;
// Disambiguate with report-event-types and report-alternate-keys: the spec
// applies 0x2 and 0x4 only to keys already sent as escape codes, so a text key
// stays text under all three.
const DISAMBIGUATE_EVENTS_ALTERNATES = 0b111;

function modesWith(flags: number): KeyboardModes {
  return {
    isApplicationCursor: () => false,
    isApplicationKeypad: () => false,
    getKeyboardFlags: () => flags,
  };
}

type Init = KeyboardEventInit & { key: string; code: string };

function map(init: Init, flags: number): KeyboardResult {
  const ev = new KeyboardEvent("keydown", init);
  // The fixture is only honest if the browser carries the AltGraph state.
  expect(ev.getModifierState("AltGraph")).toBe(init.modifierAltGraph === true);
  return mapKeyboardEvent(ev, modesWith(flags));
}

const ALTGR_CHARACTERS: Init[] = [
  { key: "@", code: "KeyQ", modifierAltGraph: true }, // German AltGr+Q
  { key: "\u20AC", code: "KeyE", modifierAltGraph: true }, // German AltGr+E
  { key: "{", code: "Digit7", modifierAltGraph: true }, // German AltGr+7
  { key: "\u0104", code: "KeyA", shiftKey: true, modifierAltGraph: true }, // Polish AltGr+Shift+A
];

describe("an AltGr character stays text for the textarea to type", () => {
  for (const flags of [LEGACY, DISAMBIGUATE, DISAMBIGUATE_EVENTS_ALTERNATES]) {
    for (const init of ALTGR_CHARACTERS) {
      it(`${init.key} on ${init.code} with keyboard flags ${String(flags)}`, () => {
        expect(map(init, flags)).toEqual({ kind: "ignore" });
      });
    }
  }
});

describe("a Ctrl+Alt chord stays CSI u, with or without AltGraph", () => {
  it("encodes Ctrl+Alt+a as ctrl+alt+a", () => {
    const init = { key: "a", code: "KeyA", ctrlKey: true, altKey: true };
    expect(map(init, DISAMBIGUATE)).toEqual({ kind: "send", bytes: `${ESC}[97;7u` });
    expect(map(init, LEGACY)).toEqual({ kind: "ignore" });
  });

  it("encodes Ctrl+Alt+a as ctrl+alt+a when AltGraph is also reported", () => {
    const init = { key: "a", code: "KeyA", ctrlKey: true, altKey: true, modifierAltGraph: true };
    expect(map(init, DISAMBIGUATE)).toEqual({ kind: "send", bytes: `${ESC}[97;7u` });
    expect(map(init, LEGACY)).toEqual({ kind: "ignore" });
  });

  it("encodes Firefox's macOS Ctrl+Option+a, whose key is the composed glyph", () => {
    // Firefox computes the key without Ctrl but with Option ("å") and sets
    // AltGraph. The chord is ctrl+alt+a, which the app bound, never text.
    const init = {
      key: "\u00E5",
      code: "KeyA",
      ctrlKey: true,
      altKey: true,
      modifierAltGraph: true,
    };
    expect(map(init, DISAMBIGUATE)).toEqual({ kind: "send", bytes: `${ESC}[97;7u` });
    expect(map(init, LEGACY)).toEqual({ kind: "ignore" });
  });

  it("encodes a Ctrl+Option chord whose glyph is ASCII from its physical key", () => {
    // German macOS layout: Option+L types "@". With Ctrl and Firefox's AltGraph
    // this is the same shape as a Windows AltGr "@" that kept its modifiers,
    // which no current engine dispatches.
    const init = { key: "@", code: "KeyL", ctrlKey: true, altKey: true, modifierAltGraph: true };
    expect(map(init, DISAMBIGUATE)).toEqual({ kind: "send", bytes: `${ESC}[108;7u` });
    expect(map(init, LEGACY)).toEqual({ kind: "ignore" });
  });
});

describe("macOS Option stays Alt, with or without Firefox's AltGraph", () => {
  for (const modifierAltGraph of [false, true]) {
    it(`Option+a types "\u00E5"; AltGraph ${String(modifierAltGraph)}`, () => {
      vi.stubGlobal("navigator", { platform: "MacIntel", userAgent: "Mozilla/5.0 (Macintosh)" });
      const init = { key: "\u00E5", code: "KeyA", altKey: true, modifierAltGraph };
      expect(map(init, DISAMBIGUATE)).toEqual({ kind: "send", bytes: `${ESC}[97;3u` });
      expect(map(init, LEGACY)).toEqual({ kind: "send", bytes: `${ESC}\u00E5` });
    });
  }
});
