// The mode state's getter contract: each getter reflects the last-applied
// snapshot and a later snapshot overrides an earlier one. The DEC mode numbers
// are spec: ?2004 bracketed paste, ?1 DECCKM cursor keys, ?1000/1002/1003 mouse
// tracking, ?1006 SGR mouse, ?1016 SGR-pixels mouse, ?1004 focus reporting, ?5
// reverse video (DECSCNM), DECKPAM application keypad. The arrow-key encoding
// these modes drive is covered in keyboard.test.ts and keyboard.property.test.ts.

import { describe, it, expect } from "vitest";
import { bracketTextForPaste } from "./keyboard.js";
import { createModeState, POWER_ON_MODES, type ModeState } from "./modes.js";

// Read every getter into one plain object so a full sync can be asserted with a
// single toEqual (full diff on failure, no assertion-roulette).
interface ReadModes {
  bracketed: boolean;
  appCursor: boolean;
  mouseSGR: boolean;
  focus: boolean;
  mouseMode: number;
  appKeypad: boolean;
  reverse: boolean;
  pixels: boolean;
}

function readModes(modes: ModeState): ReadModes {
  return {
    bracketed: modes.isBracketedPaste(),
    appCursor: modes.isApplicationCursor(),
    mouseSGR: modes.isMouseSGR(),
    focus: modes.isFocusReporting(),
    mouseMode: modes.getMouseMode(),
    appKeypad: modes.isApplicationKeypad(),
    reverse: modes.isReverseVideo(),
    pixels: modes.isMousePixels(),
  };
}

describe("modes: getters mirror the DEC private-mode state synced from the server", () => {
  it("a fresh instance reads the power-on defaults", () => {
    expect(readModes(createModeState())).toEqual({
      bracketed: true,
      appCursor: false,
      mouseSGR: false,
      focus: false,
      mouseMode: 0,
      appKeypad: false,
      reverse: false,
      pixels: false,
    });
  });

  it("each getter returns its own synced field (mixed snapshot)", () => {
    // Mixed values so a getter that reads a neighbouring flag is caught:
    // ?2004 on, DECCKM off, ?1006 on, ?1004 off, mouse=button-event(1002),
    // DECKPAM off, ?5 on, ?1016 off.
    const modes = createModeState();
    modes.applySnapshot({
      ...POWER_ON_MODES,
      bracketedPaste: true,
      applicationCursor: false,
      mouseSGR: true,
      focusReporting: false,
      mouseMode: 1002,
      applicationKeypad: false,
      reverseVideo: true,
      mousePixels: false,
    });
    expect(readModes(modes)).toEqual({
      bracketed: true,
      appCursor: false,
      mouseSGR: true,
      focus: false,
      mouseMode: 1002,
      appKeypad: false,
      reverse: true,
      pixels: false,
    });
  });

  it("a later sync overrides the earlier one (getters track the latest ModesMessage)", () => {
    const modes = createModeState();
    modes.applySnapshot({
      bracketedPaste: true,
      applicationCursor: true,
      mouseSGR: true,
      focusReporting: true,
      mouseMode: 1003,
      applicationKeypad: true,
      reverseVideo: true,
      mousePixels: true,
      keyboardFlags: 1,
    });
    modes.applySnapshot({
      bracketedPaste: false,
      applicationCursor: false,
      mouseSGR: false,
      focusReporting: false,
      mouseMode: 0,
      applicationKeypad: false,
      reverseVideo: false,
      mousePixels: false,
      keyboardFlags: 0,
    });
    expect(readModes(modes)).toEqual({
      bracketed: false,
      appCursor: false,
      mouseSGR: false,
      focus: false,
      mouseMode: 0,
      appKeypad: false,
      reverse: false,
      pixels: false,
    });
    expect(modes.getKeyboardFlags()).toBe(0);
  });

  it("the initial snapshot is copied, not aliased", () => {
    const initial = { ...POWER_ON_MODES, mouseMode: 1000 };
    const modes = createModeState(initial);
    initial.mouseMode = 1003;
    expect(modes.getMouseMode()).toBe(1000);
  });
});

describe("modes: getMouseMode reports the synced mouse-tracking mode", () => {
  // Spec mouse-tracking modes: 0 = off, ?1000 normal, ?1002 button-event,
  // ?1003 any-event.
  for (const mode of [0, 1000, 1002, 1003]) {
    it(`reflects mouse tracking mode ${mode}`, () => {
      const modes = createModeState();
      modes.applySnapshot({ ...POWER_ON_MODES, mouseMode: mode });
      expect(modes.getMouseMode()).toBe(mode);
    });
  }
});

describe("modes: a snapshot replaces every field", () => {
  it("leaves no field at its previous value", () => {
    const modes = createModeState();
    modes.applySnapshot({
      bracketedPaste: true,
      applicationCursor: true,
      mouseSGR: true,
      focusReporting: true,
      mouseMode: 1003,
      applicationKeypad: true,
      reverseVideo: true,
      mousePixels: true,
      keyboardFlags: 1,
    });
    modes.applySnapshot({ ...POWER_ON_MODES, bracketedPaste: false });
    expect(readModes(modes)).toEqual({
      bracketed: false,
      appCursor: false,
      mouseSGR: false,
      focus: false,
      mouseMode: 0,
      appKeypad: false,
      reverse: false,
      pixels: false,
    });
    expect(modes.getKeyboardFlags()).toBe(0);
  });

  it("snapshot() returns a copy the instance does not read back", () => {
    const modes = createModeState();
    const snap = modes.snapshot();
    snap.mouseMode = 1002;
    expect(modes.getMouseMode()).toBe(0);
    expect(modes.snapshot()).toEqual({ ...POWER_ON_MODES });
  });
});

describe("modes: isBracketedPaste gates the observable paste-bracketing behavior", () => {
  // The getter's downstream effect through its consumer (keyboard.ts). DEC
  // ?2004 on -> paste wrapped in ESC[200~..ESC[201~ with any embedded ESC
  // sanitised; off -> the helper is a pure pass-through.
  it("wraps and sanitises paste text when bracketed paste is enabled", () => {
    const modes = createModeState({ ...POWER_ON_MODES, bracketedPaste: true }); // ?2004 on
    expect(modes.isBracketedPaste()).toBe(true);
    expect(bracketTextForPaste("a\x1b[201~b", modes)).toBe(`\x1b[200~a\u241B[201~b\x1b[201~`);
  });

  it("passes paste text through unchanged when bracketed paste is disabled", () => {
    const modes = createModeState({ ...POWER_ON_MODES, bracketedPaste: false }); // ?2004 off
    expect(modes.isBracketedPaste()).toBe(false);
    // No sentinels, and the embedded ESC is left intact (sanitising only
    // happens while bracketing).
    expect(bracketTextForPaste("a\x1b[201~b", modes)).toBe("a\x1b[201~b");
  });
});
