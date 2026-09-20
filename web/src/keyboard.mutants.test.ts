// Four decisions in mapKeyboardEvent the keyboard tables cannot ask, because each
// table walks ONE key with ONE modifier: which rule wins when Ctrl and Alt are
// held over Space, what Alt does over a key with no legacy encoding, what the
// kitty encoder does with a key that is not a character, and the encoder's
// floor, the codepoint 0 the kitty grammar has no room for. Sources: xterm
// ctlseqs (Ctrl+Space is NUL, Alt+<char> is the ESC prefix) and the kitty key
// codes section (https://sw.kovidgoyal.net/kitty/keyboard-protocol/), where
// unicode-key-code is the unshifted codepoint, a value only text keys have.

import { describe, it, expect, beforeEach } from "vitest";

import { mapKeyboardEvent, type KeyboardResult } from "./keyboard.js";
import { createModeState, POWER_ON_MODES } from "./modes.js";

const modes = createModeState();

const ESC = "\x1b";
const KITTY_DISAMBIGUATE = 1;

function ev(init: KeyboardEventInit & { key: string; code?: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

/** Map under the LEGACY encodings (no kitty flag). */
function legacy(init: KeyboardEventInit & { key: string; code?: string }): KeyboardResult {
  modes.applySnapshot(POWER_ON_MODES);
  return mapKeyboardEvent(ev(init), modes);
}

/** Map under the kitty disambiguate flag, via the injected-modes seam. */
function underKitty(init: KeyboardEventInit & { key: string; code?: string }): KeyboardResult {
  modes.applySnapshot({ ...POWER_ON_MODES, keyboardFlags: KITTY_DISAMBIGUATE });
  return mapKeyboardEvent(ev(init), modes);
}

beforeEach(() => {
  modes.applySnapshot(POWER_ON_MODES);
});

describe("legacy encoding: Ctrl and Alt held together", () => {
  it("sends NUL for Ctrl+Alt+Space, the Ctrl rule winning over the Alt prefix", () => {
    // Space has its own rule ahead of the generic printable path, and that rule
    // reads Ctrl alone: xterm sends NUL for Ctrl+Space whatever else is held
    // (ctlseqs, "PC-Style Function Keys"). The generic printable path is the
    // opposite — its Ctrl arm requires Alt to be absent — so Space's precedence
    // is only visible with both modifiers down, and dropping the Space rule
    // silently turns this into an ignored keypress.
    expect(legacy({ key: " ", code: "Space", ctrlKey: true, altKey: true })).toEqual({
      kind: "send",
      bytes: "\x00",
    });
  });

  it("keeps the single-modifier Space rules: Ctrl+Space NUL, Alt+Space ESC SP", () => {
    // The neighbours of the case above, so a change to the precedence shows up
    // as a difference between these three and not as a silent re-routing.
    expect(legacy({ key: " ", code: "Space", ctrlKey: true })).toEqual({
      kind: "send",
      bytes: "\x00",
    });
    expect(legacy({ key: " ", code: "Space", altKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC} `,
    });
    expect(legacy({ key: " ", code: "Space" })).toEqual({ kind: "ignore" });
  });
});

describe("legacy encoding: Alt over a key with no legacy encoding", () => {
  it("ignores Alt+F21 rather than sending ESC followed by the key NAME", () => {
    // F21-F24 have no legacy encoding (xterm stops at F20), so they fall past
    // every table to the deferred-to-`input` return. The Alt-prefix rule is
    // gated on a SINGLE-character key for exactly this reason: without the
    // gate the prefix would splice the key's name into the stream and the PTY
    // would receive ESC F 2 1 — three stray printable bytes.
    expect(legacy({ key: "F21", code: "F21", altKey: true })).toEqual({ kind: "ignore" });
  });

  it("ignores Alt+Dead and Alt over a media key on the same rule", () => {
    // The other two shapes that reach the same fall-through: a dead key
    // mid-composition (the browser reports the name "Dead", and the composed
    // character arrives later on `input`) and a key that is not text at all.
    expect(legacy({ key: "Dead", code: "Backquote", altKey: true })).toEqual({ kind: "ignore" });
    expect(legacy({ key: "AudioVolumeUp", code: "AudioVolumeUp", altKey: true })).toEqual({
      kind: "ignore",
    });
  });
});

describe("the modifier-only preamble runs before the application-keypad rule", () => {
  // The keypad rule keys off ev.code (the PHYSICAL key), the modifier-only rule
  // off ev.key (what the key does now), and the two disagree on a remapped
  // keyboard: a layout mapping the KP0 scancode to Shift_L reports code "Numpad0"
  // with key "Shift". The press produced no character, so under DECKPAM it must
  // stay silent; the keypad's SS3 form would send ESC O p for a shift. The
  // preamble's ordering decides that, and both encoder paths share it.
  function underKeypad(init: KeyboardEventInit & { key: string; code?: string }): KeyboardResult {
    modes.applySnapshot({ ...POWER_ON_MODES, applicationKeypad: true });
    return mapKeyboardEvent(ev(init), modes);
  }

  it("ignores a modifier press whose physical code is a keypad key", () => {
    expect(underKeypad({ key: "Shift", code: "Numpad0", shiftKey: true })).toEqual({
      kind: "ignore",
    });
    // The other three, with the modifier state a real keydown of each reports.
    expect(underKeypad({ key: "Control", code: "Numpad0", ctrlKey: true })).toEqual({
      kind: "ignore",
    });
    expect(underKeypad({ key: "Alt", code: "Numpad0", altKey: true })).toEqual({ kind: "ignore" });
    expect(underKeypad({ key: "Meta", code: "Numpad0", metaKey: true })).toEqual({
      kind: "ignore",
    });
  });

  it("still SS3-encodes the same physical key when it acts as the keypad zero", () => {
    // The control: same code, same mode, and the only difference is that the key
    // does what its label says. VT100 User Guide table 3-8: keypad 0 is ESC O p.
    expect(underKeypad({ key: "0", code: "Numpad0" })).toEqual({
      kind: "send",
      bytes: `${ESC}Op`,
    });
  });
});

describe("kitty disambiguate: keys that are not characters", () => {
  it("ignores Alt+Dead instead of reporting the physical key under it", () => {
    // A dead key carries a physical code with a perfectly good unshifted
    // codepoint (Backquote -> 96), so the codepoint derivation would happily
    // encode CSI 96;3u here. It must not: the key produced no character, the
    // composition it started will arrive on `input`, and reporting the base key
    // would send the app a backtick the user never typed.
    expect(underKitty({ key: "Dead", code: "Backquote", altKey: true })).toEqual({
      kind: "ignore",
    });
  });

  it("still reports Alt+` when the key IS the character", () => {
    // The control for the case above: same physical key, same modifier, and the
    // only difference is that this press produced a character. 3 = 1 + Alt(2).
    expect(underKitty({ key: "`", code: "Backquote", altKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}[96;3u`,
    });
  });

  it("never encodes a zero unicode-key-code", () => {
    // An event whose key is a single NUL character (and whose code the browser
    // did not report) leaves the codepoint derivation with nothing: it answers
    // 0, which the kitty grammar has no room for — `CSI 0 u` names no key, and
    // a decoder that reads it gets a key event for U+0000. The guard is what
    // keeps the encoder from emitting one, so the press is dropped instead.
    expect(underKitty({ key: "\u0000", ctrlKey: true })).toEqual({ kind: "ignore" });
  });
});
