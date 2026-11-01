// Layout tables: Windows KBDUS, KBDDV, KBDFR, KBDGR and KBDRU from
// https://kbdlayout.info/, dead keys left out.

import { describe, it, expect, beforeEach, vi } from "vitest";
import { mapKeyboardEvent, type KeyboardModes, type KeyboardResult } from "./keyboard.js";

const ESC = "\x1b";

function modesWith(flags: number): KeyboardModes {
  return {
    isApplicationCursor: () => false,
    isApplicationKeypad: () => false,
    getKeyboardFlags: () => flags,
  };
}
const KITTY = modesWith(1);
const LEGACY = modesWith(0);

type Init = KeyboardEventInit & { key: string; code: string };

function kitty(init: Init): KeyboardResult {
  return mapKeyboardEvent(new KeyboardEvent("keydown", init), KITTY);
}
function legacy(init: Init): KeyboardResult {
  return mapKeyboardEvent(new KeyboardEvent("keydown", init), LEGACY);
}
function csiU(cp: number, mods: number): KeyboardResult {
  return { kind: "send", bytes: `${ESC}[${String(cp)};${String(mods)}u` };
}

const LINUX = {
  platform: "Linux x86_64",
  userAgent: "Mozilla/5.0 (X11; Linux x86_64)",
  maxTouchPoints: 0,
};
const MAC = {
  platform: "MacIntel",
  userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
  maxTouchPoints: 0,
};
const IPHONE = {
  platform: "",
  userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X)",
  maxTouchPoints: 5,
};
const ANDROID = {
  platform: "Linux aarch64",
  userAgent: "Mozilla/5.0 (Linux; Android 14; K)",
  maxTouchPoints: 5,
};
const ANDROID_NO_TOUCH = { ...ANDROID, maxTouchPoints: 0 };
const ANDROID_DESKTOP = { ...LINUX, maxTouchPoints: 5 };
const CHROMEOS_TOUCH = {
  platform: "Linux x86_64",
  userAgent: "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0)",
  maxTouchPoints: 10,
};
type Platform = typeof LINUX;

function onPlatform(p: Platform): void {
  vi.stubGlobal("navigator", p);
}

beforeEach(() => {
  onPlatform(LINUX);
});

type Layout = readonly (readonly [code: string, unshifted: string, shifted: string])[];

function letters(codes: string, chars: string): [string, string, string][] {
  return [...codes].map((c, i) => {
    const ch = chars[i] ?? "";
    return [`Key${c}`, ch, ch.toUpperCase()];
  });
}

const US: Layout = [
  ["Backquote", "`", "~"],
  ["Digit1", "1", "!"],
  ["Digit2", "2", "@"],
  ["Digit3", "3", "#"],
  ["Digit4", "4", "$"],
  ["Digit5", "5", "%"],
  ["Digit6", "6", "^"],
  ["Digit7", "7", "&"],
  ["Digit8", "8", "*"],
  ["Digit9", "9", "("],
  ["Digit0", "0", ")"],
  ["Minus", "-", "_"],
  ["Equal", "=", "+"],
  ["BracketLeft", "[", "{"],
  ["BracketRight", "]", "}"],
  ["Backslash", "\\", "|"],
  ["Semicolon", ";", ":"],
  ["Quote", "'", '"'],
  ["Comma", ",", "<"],
  ["Period", ".", ">"],
  ["Slash", "/", "?"],
  ...letters("ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz"),
];

const DVORAK: Layout = [
  ["Backquote", "`", "~"],
  ...US.filter(([c]) => c.startsWith("Digit")),
  ["Minus", "[", "{"],
  ["Equal", "]", "}"],
  ["KeyQ", "'", '"'],
  ["KeyW", ",", "<"],
  ["KeyE", ".", ">"],
  ["BracketLeft", "/", "?"],
  ["BracketRight", "=", "+"],
  ["Backslash", "\\", "|"],
  ["Semicolon", "s", "S"],
  ["Quote", "-", "_"],
  ["KeyZ", ";", ":"],
  ["Comma", "w", "W"],
  ["Period", "v", "V"],
  ["Slash", "z", "Z"],
  ...letters("RTYUIOPASDFGHJKLXCVBNM", "pyfgcrlaoeuidhtnqjkxbm"),
];

const AZERTY: Layout = [
  ["Backquote", "\u00B2", "\u00B2"],
  ["Digit1", "&", "1"],
  ["Digit2", "\u00E9", "2"],
  ["Digit3", '"', "3"],
  ["Digit4", "'", "4"],
  ["Digit5", "(", "5"],
  ["Digit6", "-", "6"],
  ["Digit7", "\u00E8", "7"],
  ["Digit8", "_", "8"],
  ["Digit9", "\u00E7", "9"],
  ["Digit0", "\u00E0", "0"],
  ["Minus", ")", "\u00B0"],
  ["Equal", "=", "+"],
  ["BracketRight", "$", "\u00A3"],
  ["Quote", "\u00F9", "%"],
  ["Backslash", "*", "\u00B5"],
  ["IntlBackslash", "<", ">"],
  ["KeyM", ",", "?"],
  ["Comma", ";", "."],
  ["Period", ":", "/"],
  ["Slash", "!", "\u00A7"],
  ["Semicolon", "m", "M"],
  ...letters("QWERTYUIOPASDFGHJKLZXCVBN", "azertyuiopqsdfghjklwxcvbn"),
];

const QWERTZ: Layout = [
  ["Digit1", "1", "!"],
  ["Digit2", "2", '"'],
  ["Digit3", "3", "\u00A7"],
  ["Digit4", "4", "$"],
  ["Digit5", "5", "%"],
  ["Digit6", "6", "&"],
  ["Digit7", "7", "/"],
  ["Digit8", "8", "("],
  ["Digit9", "9", ")"],
  ["Digit0", "0", "="],
  ["Minus", "\u00DF", "?"],
  ["BracketLeft", "\u00FC", "\u00DC"],
  ["BracketRight", "+", "*"],
  ["Backslash", "#", "'"],
  ["Semicolon", "\u00F6", "\u00D6"],
  ["Quote", "\u00E4", "\u00C4"],
  ["IntlBackslash", "<", ">"],
  ["Comma", ",", ";"],
  ["Period", ".", ":"],
  ["Slash", "-", "_"],
  ...letters("QWERTYUIOPASDFGHJKLZXCVBNM", "qwertzuiopasdfghjklyxcvbnm"),
];

const RUSSIAN: Layout = [
  ["Backquote", "\u0451", "\u0401"],
  ["Digit1", "1", "!"],
  ["Digit2", "2", '"'],
  ["Digit3", "3", "\u2116"],
  ["Digit4", "4", ";"],
  ["Digit5", "5", "%"],
  ["Digit6", "6", ":"],
  ["Digit7", "7", "?"],
  ["Digit8", "8", "*"],
  ["Digit9", "9", "("],
  ["Digit0", "0", ")"],
  ["Minus", "-", "_"],
  ["Equal", "=", "+"],
  ["BracketLeft", "\u0445", "\u0425"],
  ["BracketRight", "\u044A", "\u042A"],
  ["Backslash", "\\", "/"],
  ["Semicolon", "\u0436", "\u0416"],
  ["Quote", "\u044D", "\u042D"],
  ["Comma", "\u0431", "\u0411"],
  ["Period", "\u044E", "\u042E"],
  ["Slash", ".", ","],
  ...letters(
    "QWERTYUIOPASDFGHJKLZXCVBNM",
    "\u0439\u0446\u0443\u043A\u0435\u043D\u0433\u0448\u0449\u0437\u0444\u044B\u0432\u0430\u043F\u0440\u043E\u043B\u0434\u044F\u0447\u0441\u043C\u0438\u0442\u044C",
  ),
];

const LAYOUTS: [string, Layout][] = [
  ["QWERTY", US],
  ["Dvorak", DVORAK],
  ["AZERTY", AZERTY],
  ["QWERTZ", QWERTZ],
  ["Russian", RUSSIAN],
];

const US_POSITION = new Map(US.map(([code, base]) => [code, base.codePointAt(0) ?? 0]));

interface Chord {
  name: string;
  ctrl?: boolean;
  alt?: boolean;
  shift?: boolean;
  meta?: boolean;
  mods: number;
}
const CHORDS: Chord[] = [
  { name: "Ctrl", ctrl: true, mods: 5 },
  { name: "Alt", alt: true, mods: 3 },
  { name: "Ctrl+Alt", ctrl: true, alt: true, mods: 7 },
  { name: "Ctrl+Shift", ctrl: true, shift: true, mods: 6 },
  { name: "Alt+Shift", alt: true, shift: true, mods: 4 },
  { name: "Ctrl+Alt+Shift", ctrl: true, alt: true, shift: true, mods: 8 },
  { name: "Ctrl+Meta", ctrl: true, meta: true, mods: 13 },
  { name: "Alt+Meta", alt: true, meta: true, mods: 11 },
  { name: "Alt+Shift+Meta", alt: true, shift: true, meta: true, mods: 12 },
  { name: "Ctrl+Shift+Meta", ctrl: true, shift: true, meta: true, mods: 14 },
  { name: "Ctrl+Alt+Meta", ctrl: true, alt: true, meta: true, mods: 15 },
  { name: "Ctrl+Alt+Shift+Meta", ctrl: true, alt: true, shift: true, meta: true, mods: 16 },
];

function chordInit(
  [code, base, shifted]: readonly [string, string, string],
  chord: Chord,
  capsLock: boolean,
): Init {
  const uncapped = chord.shift === true ? shifted : base;
  const flipped = chord.shift === true ? uncapped.toLowerCase() : uncapped.toUpperCase();
  const key = capsLock && flipped.length === 1 ? flipped : uncapped;
  return {
    key,
    code,
    ctrlKey: chord.ctrl === true,
    altKey: chord.alt === true,
    shiftKey: chord.shift === true,
    metaKey: chord.meta === true,
    modifierCapsLock: capsLock,
  };
}

describe("QWERTY bytes are the US position of the key, for every chord", () => {
  for (const platform of [LINUX, MAC, ANDROID, ANDROID_DESKTOP]) {
    for (const chord of CHORDS) {
      for (const capsLock of [false, true]) {
        const on = `${platform.userAgent}, touch ${String(platform.maxTouchPoints)}`;
        it(`${chord.name}, Caps Lock ${String(capsLock)}, on ${on}`, () => {
          onPlatform(platform);
          for (const k of US) {
            const want = csiU(US_POSITION.get(k[0]) ?? 0, chord.mods);
            expect(kitty(chordInit(k, chord, capsLock)), `${k[0]}`).toEqual(want);
          }
        });
      }
    }
  }

  it("macOS Option reads the physical key, whatever glyph Option composed", () => {
    onPlatform(MAC);
    for (const [code, base] of US) {
      const init = { key: "\u2211", code, altKey: true };
      expect(kitty(init), code).toEqual(csiU(base.codePointAt(0) ?? 0, 3));
    }
  });
});

describe("an unshifted chord reports the layout's character", () => {
  it("AZERTY Ctrl+A and Alt+A are a, from the KeyQ position", () => {
    expect(kitty({ key: "a", code: "KeyQ", ctrlKey: true })).toEqual(csiU(97, 5));
    expect(kitty({ key: "a", code: "KeyQ", altKey: true })).toEqual(csiU(97, 3));
    expect(kitty({ key: "a", code: "KeyQ", ctrlKey: true, altKey: true })).toEqual(csiU(97, 7));
  });

  it("Dvorak Ctrl+C is c, from the KeyI position", () => {
    expect(kitty({ key: "c", code: "KeyI", ctrlKey: true })).toEqual(csiU(99, 5));
  });

  it("Dvorak Ctrl+= and Alt+[ report their symbols", () => {
    expect(kitty({ key: "=", code: "BracketRight", ctrlKey: true })).toEqual(csiU(61, 5));
    expect(kitty({ key: "[", code: "Minus", altKey: true })).toEqual(csiU(91, 3));
  });

  it("a space with no physical code is still a space", () => {
    // An on-screen keyboard's synthesized event can carry an empty ev.code.
    expect(kitty({ key: " ", code: "", ctrlKey: true })).toEqual(csiU(32, 5));
  });

  it("Dvorak Ctrl+, and Ctrl+W are distinct", () => {
    expect(kitty({ key: ",", code: "KeyW", ctrlKey: true })).toEqual(csiU(44, 5));
    expect(kitty({ key: "w", code: "Comma", ctrlKey: true })).toEqual(csiU(119, 5));
  });

  it("AZERTY's digit row reports its unshifted punctuation and accents", () => {
    expect(kitty({ key: "&", code: "Digit1", ctrlKey: true })).toEqual(csiU(38, 5));
    expect(kitty({ key: "\u00E9", code: "Digit2", ctrlKey: true })).toEqual(csiU(233, 5));
  });

  it("QWERTZ Ctrl+Z, Ctrl+ß and Ctrl+- report z, ß and -", () => {
    expect(kitty({ key: "z", code: "KeyY", ctrlKey: true })).toEqual(csiU(122, 5));
    expect(kitty({ key: "\u00DF", code: "Minus", ctrlKey: true })).toEqual(csiU(223, 5));
    expect(kitty({ key: "-", code: "Slash", ctrlKey: true })).toEqual(csiU(45, 5));
  });

  it("macOS Ctrl without Option reports the layout's character", () => {
    onPlatform(MAC);
    expect(kitty({ key: "c", code: "KeyI", ctrlKey: true })).toEqual(csiU(99, 5));
  });

  it("Dvorak Ctrl+Meta+C is c, on macOS too", () => {
    const init = { key: "c", code: "KeyI", ctrlKey: true, metaKey: true };
    expect(kitty(init)).toEqual(csiU(99, 13));
    onPlatform(MAC);
    expect(kitty(init)).toEqual(csiU(99, 13));
  });

  it("AZERTY Alt+Meta+A is a where Alt does not compose", () => {
    expect(kitty({ key: "a", code: "KeyQ", altKey: true, metaKey: true })).toEqual(csiU(97, 11));
  });

  it("Caps Lock capitalizing a letter does not change the code", () => {
    const capsA = { key: "A", code: "KeyQ", ctrlKey: true, modifierCapsLock: true };
    expect(kitty(capsA)).toEqual(csiU(97, 5));
    const capsC = { key: "C", code: "KeyI", altKey: true, modifierCapsLock: true };
    expect(kitty(capsC)).toEqual(csiU(99, 3));
    const capsU = { key: "\u00DC", code: "BracketLeft", ctrlKey: true, modifierCapsLock: true };
    expect(kitty(capsU)).toEqual(csiU(252, 5));
  });
});

describe("Shift, or an Alt that composes, reports the US position", () => {
  it("Dvorak Ctrl+Shift+C is the KeyI position, i", () => {
    expect(kitty({ key: "C", code: "KeyI", ctrlKey: true, shiftKey: true })).toEqual(csiU(105, 6));
  });

  it("AZERTY Ctrl+Shift+& is the Digit1 position, never the shifted 1 or &", () => {
    expect(kitty({ key: "1", code: "Digit1", ctrlKey: true, shiftKey: true })).toEqual(csiU(49, 6));
  });

  it("macOS Option+A on AZERTY is the KeyQ position", () => {
    onPlatform(MAC);
    expect(kitty({ key: "\u00E6", code: "KeyQ", altKey: true })).toEqual(csiU(113, 3));
    expect(kitty({ key: "\u00E6", code: "KeyQ", ctrlKey: true, altKey: true })).toEqual(
      csiU(113, 7),
    );
  });

  it("macOS Option+Cmd on AZERTY is the KeyQ position, whatever ev.key Cmd leaves", () => {
    onPlatform(MAC);
    const optionCmd = { code: "KeyQ", altKey: true, metaKey: true };
    expect(kitty({ key: "\u00E6", ...optionCmd })).toEqual(csiU(113, 11));
    expect(kitty({ key: "a", ...optionCmd })).toEqual(csiU(113, 11));
  });

  it("the same Alt event reads the layout off Apple and Android, the position on them", () => {
    const init = { key: "\u00FC", code: "BracketLeft", altKey: true };
    for (const p of [LINUX, CHROMEOS_TOUCH]) {
      onPlatform(p);
      expect(kitty(init), p.userAgent).toEqual(csiU(252, 3));
    }
    for (const p of [MAC, IPHONE, ANDROID, ANDROID_NO_TOUCH, ANDROID_DESKTOP]) {
      onPlatform(p);
      expect(kitty(init), p.userAgent).toEqual(csiU(91, 3));
    }
  });

  it("Android Alt+C composing ç is the KeyC position, desktop mode included", () => {
    for (const p of [ANDROID, ANDROID_NO_TOUCH, ANDROID_DESKTOP]) {
      onPlatform(p);
      expect(kitty({ key: "\u00E7", code: "KeyC", altKey: true }), p.userAgent).toEqual(
        csiU(99, 3),
      );
      expect(kitty({ key: "\u00DF", code: "KeyS", altKey: true }), p.userAgent).toEqual(
        csiU(115, 3),
      );
    }
  });

  it("Ctrl on an Android tablet in desktop mode reads the layout", () => {
    onPlatform(ANDROID_DESKTOP);
    expect(kitty({ key: "c", code: "KeyI", ctrlKey: true })).toEqual(csiU(99, 5));
  });

  it("Android Alt on a non-US layout is the US position; Ctrl alone is not (known limit)", () => {
    // The page cannot tell an Android Alt that composes from one that does not.
    onPlatform(ANDROID);
    expect(kitty({ key: "c", code: "KeyI", altKey: true })).toEqual(csiU(105, 3));
    expect(kitty({ key: "a", code: "KeyQ", altKey: true })).toEqual(csiU(113, 3));
    expect(kitty({ key: "c", code: "KeyI", ctrlKey: true })).toEqual(csiU(99, 5));
    expect(kitty({ key: "a", code: "KeyQ", ctrlKey: true })).toEqual(csiU(97, 5));
  });

  it("an AltGraph glyph under Ctrl is the physical key's position", () => {
    // Linux reports AltGr as AltGraph with altKey clear; German AltGr+Q is "@".
    const init = { key: "@", code: "KeyQ", ctrlKey: true, modifierAltGraph: true };
    expect(kitty(init)).toEqual(csiU(113, 5));
  });

  it("Russian Ctrl+Shift on IntlBackslash and Slash share 47 (known limit)", () => {
    // IntlBackslash has no US position, so under Shift it reports the "/" it types.
    expect(kitty({ key: "\\", code: "IntlBackslash", ctrlKey: true })).toEqual(csiU(92, 5));
    expect(kitty({ key: ".", code: "Slash", ctrlKey: true })).toEqual(csiU(46, 5));
    const shifted = { ctrlKey: true, shiftKey: true };
    expect(kitty({ key: "/", code: "IntlBackslash", ...shifted })).toEqual(csiU(47, 6));
    expect(kitty({ key: ",", code: "Slash", ...shifted })).toEqual(csiU(47, 6));
  });

  it("UK Ctrl+Shift on IntlBackslash reports the | it types, having no US position", () => {
    expect(kitty({ key: "\\", code: "IntlBackslash", ctrlKey: true })).toEqual(csiU(92, 5));
    const shifted = { ctrlKey: true, shiftKey: true };
    expect(kitty({ key: "|", code: "IntlBackslash", ...shifted })).toEqual(csiU(124, 6));
  });

  it("macOS Option on a key with no US position reports what Option types (JIS Option+¥ is \\)", () => {
    onPlatform(MAC);
    expect(kitty({ key: "\\", code: "IntlYen", altKey: true })).toEqual(csiU(92, 3));
  });

  it("with no ev.code to read, the case-folded ev.key answers", () => {
    onPlatform(MAC);
    expect(kitty({ key: "\u00E5", code: "", altKey: true })).toEqual(csiU(229, 3));
    const shifted = { key: "\u00C4", code: "", ctrlKey: true, shiftKey: true };
    expect(kitty(shifted)).toEqual(csiU(228, 6));
  });

  it("with no ev.code, a shifted İ reports itself, not the i its lowercase starts with", () => {
    const init = { key: "\u0130", code: "", ctrlKey: true, shiftKey: true };
    expect(kitty(init)).toEqual(csiU(304, 6));
  });
});

describe("an ev.key that cannot be the unshifted character falls back to the position", () => {
  it("a C0 or C1 control character", () => {
    expect(kitty({ key: "\x03", code: "KeyC", ctrlKey: true })).toEqual(csiU(99, 5));
    expect(kitty({ key: "\x85", code: "Period", altKey: true })).toEqual(csiU(46, 3));
    expect(kitty({ key: "\x7f", code: "KeyA", ctrlKey: true })).toEqual(csiU(97, 5));
  });

  it("a format character from a Windows Ctrl level (Hebrew Ctrl+[ is U+200E)", () => {
    expect(kitty({ key: "\u200E", code: "BracketLeft", ctrlKey: true })).toEqual(csiU(91, 5));
  });

  it("a format character on Space reports 32 (Lao Ctrl and Khmer base U+200B, Persian Shift U+200C)", () => {
    expect(kitty({ key: "\u200B", code: "Space", ctrlKey: true })).toEqual(csiU(32, 5));
    expect(kitty({ key: "\u200B", code: "Space", altKey: true })).toEqual(csiU(32, 3));
    expect(kitty({ key: "\u200C", code: "Space", ctrlKey: true, shiftKey: true })).toEqual(
      csiU(32, 6),
    );
  });

  it("QWERTY Ctrl+Space and Alt+Space report 32", () => {
    expect(kitty({ key: " ", code: "Space", ctrlKey: true })).toEqual(csiU(32, 5));
    expect(kitty({ key: " ", code: "Space", altKey: true })).toEqual(csiU(32, 3));
  });

  it("but a printable Windows Ctrl-level character reports itself (Thai Kedmanee Ctrl+[ is %)", () => {
    expect(kitty({ key: "%", code: "BracketLeft", ctrlKey: true })).toEqual(csiU(37, 5));
  });

  it("with no physical position either, a control character sends nothing", () => {
    expect(kitty({ key: "\x03", code: "", ctrlKey: true })).toEqual({ kind: "ignore" });
    expect(kitty({ key: "\u200E", code: "IntlBackslash", ctrlKey: true })).toEqual({
      kind: "ignore",
    });
  });

  it("macOS Option+Space reports the U+00A0 it types (known deviation)", () => {
    // The spec's code is 32. Space has no KITTY_CODE_BASE entry, and adding one
    // would change this QWERTY byte, which the US-position pin keeps.
    onPlatform(MAC);
    expect(kitty({ key: "\u00A0", code: "Space", altKey: true })).toEqual(csiU(160, 3));
  });
});

describe("Caps Lock changes the code where the capital does not lead back (known limits)", () => {
  function ctrl(key: string, code: string, capsLock: boolean): KeyboardResult {
    return kitty({ key, code, ctrlKey: true, modifierCapsLock: capsLock });
  }

  it("Turkish-Q KeyI types ı, and its capital I lowercases to i", () => {
    expect(ctrl("\u0131", "KeyI", false)).toEqual(csiU(305, 5));
    expect(ctrl("I", "KeyI", true)).toEqual(csiU(105, 5));
  });

  it("Turkish-Q Quote types i, and its capital İ, with no one-character lowercase, reports itself", () => {
    expect(ctrl("i", "Quote", false)).toEqual(csiU(105, 5));
    expect(ctrl("\u0130", "Quote", true)).toEqual(csiU(304, 5));
  });

  it("Turkish-F keeps Caps+i apart from s, whose US position the i key holds", () => {
    // kbdtuf.klc: i/İ on KeyS, s/S on KeyM, ı/I on KeyR.
    expect(ctrl("\u0130", "KeyS", true)).toEqual(csiU(304, 5));
    expect(ctrl("S", "KeyM", true)).toEqual(csiU(115, 5));
    expect(ctrl("I", "KeyR", true)).toEqual(csiU(105, 5));
  });

  it("Azerbaijani Standard keeps Caps+i apart from l, whose US position the i key holds", () => {
    // kbdazst.klc: i/İ on KeyL, l/L on KeyK.
    expect(ctrl("\u0130", "KeyL", true)).toEqual(csiU(304, 5));
    expect(ctrl("L", "KeyK", true)).toEqual(csiU(108, 5));
  });

  it("Windows AZERTY's Caps Lock shifts Digit1 from & to 1", () => {
    expect(ctrl("&", "Digit1", false)).toEqual(csiU(38, 5));
    expect(ctrl("1", "Digit1", true)).toEqual(csiU(49, 5));
  });
});

describe("a non-Latin letter on a letter key folds to that key's Latin letter", () => {
  it("Russian Ctrl+С is ctrl+c, with and without Caps Lock", () => {
    expect(kitty({ key: "\u0441", code: "KeyC", ctrlKey: true })).toEqual(csiU(99, 5));
    const caps = { key: "\u0421", code: "KeyC", ctrlKey: true, modifierCapsLock: true };
    expect(kitty(caps)).toEqual(csiU(99, 5));
  });

  it("a mixed-script layout's folded letter shares a code with a Latin letter (known limit)", () => {
    // Cyrillic с on KeyC folds to c, the Latin letter this layout types on KeyX.
    expect(kitty({ key: "\u0441", code: "KeyC", ctrlKey: true })).toEqual(csiU(99, 5));
    expect(kitty({ key: "c", code: "KeyX", ctrlKey: true })).toEqual(csiU(99, 5));
  });

  it("a Cyrillic letter off the letter keys keeps its codepoint (Russian ю and . differ)", () => {
    expect(kitty({ key: "\u044E", code: "Period", ctrlKey: true })).toEqual(csiU(1102, 5));
    expect(kitty({ key: ".", code: "Slash", ctrlKey: true })).toEqual(csiU(46, 5));
  });

  it("punctuation on a letter key and a Latin accent stay literal", () => {
    // Greek KeyQ types ";"; Turkish ı is Latin script.
    expect(kitty({ key: ";", code: "KeyQ", ctrlKey: true })).toEqual(csiU(59, 5));
    expect(kitty({ key: "\u0131", code: "KeyI", altKey: true })).toEqual(csiU(305, 3));
  });

  it("an astral-plane letter folds and reports like any other (ADLaM)", () => {
    expect(kitty({ key: "\u{1E922}", code: "KeyC", ctrlKey: true })).toEqual(csiU(99, 5));
    expect(kitty({ key: "\u{1E922}", code: "Period", altKey: true })).toEqual(csiU(0x1e922, 3));
    expect(kitty({ key: "\u{1E900}", code: "", ctrlKey: true, shiftKey: true })).toEqual(
      csiU(0x1e922, 6),
    );
    expect(kitty({ key: "\u{1E922}", code: "KeyC" })).toEqual({ kind: "ignore" });
    // Two BMP code points are not one key character, as legacy mode also treats
    // them; Windows Arabic 101 KeyB types the lam-alef ligature.
    expect(kitty({ key: "e\u0301", code: "KeyE", ctrlKey: true })).toEqual({ kind: "ignore" });
    expect(kitty({ key: "\u0644\u0627", code: "KeyB", altKey: true })).toEqual({
      kind: "ignore",
    });
  });

  it("a Thai mark on a letter key folds like a letter, and elsewhere stays itself", () => {
    // Thai Kedmanee KeyY types U+0E31 and Digit6 U+0E38, both non-spacing marks.
    expect(kitty({ key: "\u0E31", code: "KeyY", ctrlKey: true })).toEqual(csiU(121, 5));
    expect(kitty({ key: "\u0E38", code: "Digit6", ctrlKey: true })).toEqual(csiU(3640, 5));
  });
});

describe("within one chord on these layouts, keys typing different characters send different codes", () => {
  for (const [name, layout] of LAYOUTS) {
    for (const platform of [LINUX, MAC]) {
      for (const chord of CHORDS) {
        for (const capsLock of [false, true]) {
          it(`${name} ${chord.name}, Caps Lock ${String(capsLock)}, on ${platform.platform}`, () => {
            onPlatform(platform);
            const owner = new Map<string, string>();
            for (const k of layout) {
              const init = chordInit(k, chord, capsLock);
              const r = kitty(init);
              expect(r.kind, k[0]).toBe("send");
              const bytes = r.kind === "send" ? r.bytes : "";
              expect(owner.get(bytes), `${k[0]} and its twin both send ${bytes}`).toBeUndefined();
              owner.set(bytes, k[0]);
            }
          });
        }
      }
    }
  }
});

describe("legacy encoding keeps the layout's character", () => {
  it("Ctrl sends the C0 byte of the typed letter, Alt prefixes it with ESC", () => {
    expect(legacy({ key: "a", code: "KeyQ", ctrlKey: true })).toEqual({
      kind: "send",
      bytes: "\x01",
    });
    expect(legacy({ key: "c", code: "KeyI", ctrlKey: true })).toEqual({
      kind: "send",
      bytes: "\x03",
    });
    expect(legacy({ key: "a", code: "KeyQ", altKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}a`,
    });
    onPlatform(MAC);
    expect(legacy({ key: "\u00E6", code: "KeyQ", altKey: true })).toEqual({
      kind: "send",
      bytes: `${ESC}\u00E6`,
    });
  });

  it("a Cyrillic Ctrl chord has no C0 byte and stays with the browser", () => {
    expect(legacy({ key: "\u0441", code: "KeyC", ctrlKey: true })).toEqual({ kind: "ignore" });
  });
});
