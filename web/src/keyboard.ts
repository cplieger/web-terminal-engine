// Keyboard event → terminal byte sequence mapping, mirroring xterm.js's
// evaluateKeyboardEvent (src/common/input/Keyboard.ts) so vim, readline and
// full-screen programs get the keys they expect. Modifier digit per the
// xterm/VT520 convention: 1=Shift, 2=Alt, 4=Ctrl, 8=Meta, summed then +1 for
// CSI 1;{n}{letter}. DECCKM and DECKPAM come from the injected mode state,
// which the server's modes frame keeps current.

import { isOneCodePoint } from "./code-point.js";

/** Result of mapping a keyboard event. */
export type KeyboardResult =
  | { kind: "send"; bytes: string }
  | { kind: "scroll-up" } // Shift+PageUp — handled locally
  | { kind: "scroll-down" } // Shift+PageDown — handled locally
  | { kind: "ignore" }; // Modifier-only press, etc.

/**
 * KeyboardModes is the mode state `mapKeyboardEvent` reads: DECCKM (application
 * cursor keys), DECKPAM (application keypad), and the kitty keyboard
 * progressive-enhancement flags. A `ModeState` satisfies it; a consumer passes
 * the state of the terminal the key belongs to.
 */
export interface KeyboardModes {
  isApplicationCursor: () => boolean;
  isApplicationKeypad: () => boolean;
  /**
   * Active kitty keyboard flags (bit0 disambiguate; higher bits reserved). When
   * the disambiguate bit is set, non-text keys are encoded as kitty CSI-u
   * sequences instead of the legacy encodings.
   */
  getKeyboardFlags: () => number;
}

const ESC = "\x1b";
const DEL = "\x7f";

/** Compute the xterm modifier digit (used in CSI 1;{n}letter sequences). */
function modifiersDigit(ev: KeyboardEvent): number {
  return (
    1 + (ev.shiftKey ? 1 : 0) + (ev.altKey ? 2 : 0) + (ev.ctrlKey ? 4 : 0) + (ev.metaKey ? 8 : 0)
  );
}

// -- Cursor / navigation keys -----------------------------------------------

/** The cursor-key trailing letters that honor DECCKM (arrows + Home/End). */
type CursorKeyLetter = "A" | "B" | "C" | "D" | "H" | "F";

/**
 * plainCursorKeySeq is THE encoding of an unmodified logical cursor-key press
 * (arrows, Home, End), shared by the physical-key path and the mobile toolbar's
 * arrow buttons so the two encoders cannot drift: the CSI form under kitty
 * disambiguate (the protocol supersedes cursor-key mode), the SS3 form
 * (ESC O letter) under DECCKM, the bare CSI form otherwise.
 */
export function plainCursorKeySeq(
  letter: CursorKeyLetter,
  kittyActive: boolean,
  appCursor: boolean,
): string {
  if (kittyActive) {
    return `${ESC}[${letter}`;
  }
  return appCursor ? `${ESC}O${letter}` : `${ESC}[${letter}`;
}

/**
 * plainEscapeSeq is THE encoding of an unmodified logical Escape press: CSI
 * 27 u under kitty disambiguate (the protocol's headline unambiguous-Esc
 * feature — the app never has to distinguish a lone ESC byte from the start
 * of an escape sequence), else the bare ESC byte. Shared by the physical-key
 * path and the toolbar's ESC button. The flag is a parameter for the same
 * injected-modes honesty as plainCursorKeySeq.
 */
export function plainEscapeSeq(kittyActive: boolean): string {
  return kittyActive ? `${ESC}[27u` : ESC;
}

/** True when the kitty disambiguate flag is active in `modes`. */
export function kittyDisambiguateActive(modes: KeyboardModes): boolean {
  return (modes.getKeyboardFlags() & KITTY_DISAMBIGUATE) !== 0;
}

// The keys that honor DECCKM (arrows, Home, End): bare CSI or SS3 unmodified,
// CSI 1;{mod}{letter} with modifiers, which have no SS3 equivalent. The
// `CursorKeyLetter` parameter type makes "every letter here honors DECCKM" a
// compiler guarantee; F1-F4's SS3/CSI pair is encoded at its own call site.
function csiLetter(letter: CursorKeyLetter, ev: KeyboardEvent, modes: KeyboardModes): string {
  const m = modifiersDigit(ev);
  if (m === 1) {
    // Read from `modes` rather than hard-coded false (mapKeyboardEvent returns
    // through encodeKittyDisambiguate first) so a change to that early return
    // cannot mis-encode silently.
    return plainCursorKeySeq(letter, kittyDisambiguateActive(modes), modes.isApplicationCursor());
  }
  return `${ESC}[1;${m}${letter}`;
}

// Tilde-form keys (Insert=2, Delete=3, PageUp=5, PageDown=6, F5+=15..24).
function csiTilde(num: number, ev: KeyboardEvent): string {
  const m = modifiersDigit(ev);
  return m === 1 ? `${ESC}[${num}~` : `${ESC}[${num};${m}~`;
}

const FN_LETTER: Record<string, string | undefined> = {
  F1: "P",
  F2: "Q",
  F3: "R",
  F4: "S",
};
const FN_TILDE: Record<string, number | undefined> = {
  F5: 15,
  F6: 17,
  F7: 18,
  F8: 19,
  F9: 20,
  F10: 21,
  F11: 23,
  F12: 24,
  // F13-F20 use xterm's own extended tilde codes (25-34, with 27 and 30
  // skipped historically). F21-F24 have NO standard legacy encoding — xterm
  // itself stops at F20 — so they stay silent here and are covered only by
  // the kitty path (dedicated codepoints; see KITTY_FN_HIGH). Note xterm.js
  // supports none of F13+ (xtermjs/xterm.js#1426, open since 2018).
  F13: 25,
  F14: 26,
  F15: 28,
  F16: 29,
  F17: 31,
  F18: 32,
  F19: 33,
  F20: 34,
};

const ARROW_LETTER: Record<string, CursorKeyLetter | undefined> = {
  ArrowUp: "A",
  ArrowDown: "B",
  ArrowRight: "C",
  ArrowLeft: "D",
};

// Application keypad mode (DECKPAM) SS3 mappings. When the server has
// sent ESC = (DECKPAM), numeric keypad keys send ESC O <letter> instead
// of their normal character. Mapping from VT100 User Guide Table 3-8
// (ANSI mode, application keypad). xterm extensions for *, +, / included.
const KEYPAD_SS3: Record<string, string | undefined> = {
  "0": "p",
  "1": "q",
  "2": "r",
  "3": "s",
  "4": "t",
  "5": "u",
  "6": "v",
  "7": "w",
  "8": "x",
  "9": "y",
  ".": "n",
  "-": "m",
  "+": "k",
  "*": "j",
  "/": "o",
  Enter: "M",
};

// -- Kitty keyboard protocol (progressive enhancement) ----------------------
// Under an active kitty flag, keys that do NOT produce text are encoded per the
// kitty protocol so an app that enabled it gets unambiguous key events. Only
// the disambiguate flag (0x1) is honored; the server masks off
// 0x2/0x4/0x8/0x10, so the CSI ?u query reports only 0x1.
// Text-producing keys still flow through the hidden textarea as text (this
// encoder returns "ignore" for them), per the spec and the IME model.

/** Kitty progressive-enhancement flag bits (mirror vt/kitty.go). */
const KITTY_DISAMBIGUATE = 1;

// Functional keys encoded as CSI 1;{mod}{letter} (the 1;{mod} is omitted when
// unmodified). Under kitty these use the CSI form (legacy uses SS3 for F1-F4)
// and even under DECCKM. F3 is deliberately absent — it has no letter form in
// the kitty table (CSI R collides with the Cursor Position Report), so it goes
// in KITTY_TILDE as 13 instead. Do not add F3 here.
const KITTY_LETTER: Record<string, string | undefined> = {
  ArrowUp: "A",
  ArrowDown: "B",
  ArrowRight: "C",
  ArrowLeft: "D",
  Home: "H",
  End: "F",
  F1: "P",
  F2: "Q",
  F4: "S",
};
// Functional keys encoded as CSI {num};{mod}~ (the ;{mod} omitted when unmodified).
const KITTY_TILDE: Record<string, number | undefined> = {
  Insert: 2,
  Delete: 3,
  PageUp: 5,
  PageDown: 6,
  F3: 13, // no letter form — CSI R conflicts with the cursor-position report
  F5: 15,
  F6: 17,
  F7: 18,
  F8: 19,
  F9: 20,
  F10: 21,
  F11: 23,
  F12: 24,
};

// F13-F24 under kitty: the spec assigns them dedicated functional-key
// codepoints (57376-57387, contiguous), encoded as CSI {code};{mod} u like
// the keypad keys. The legacy path covers only F13-F20 (xterm tilde codes;
// see FN_TILDE) — these twelve make the kitty path the complete one, which
// exceeds xterm.js (no F13+ support at all, xtermjs/xterm.js#1426).
const KITTY_FN_HIGH: Record<string, number | undefined> = {
  F13: 57376,
  F14: 57377,
  F15: 57378,
  F16: 57379,
  F17: 57380,
  F18: 57381,
  F19: 57382,
  F20: 57383,
  F21: 57384,
  F22: 57385,
  F23: 57386,
  F24: 57387,
};

// US-layout unshifted codepoints of the punctuation keys by ev.code, for the
// chords kittyKeyCodepoint reads by position: Ctrl+Shift+; is 59;6u, not 58;6u.
const KITTY_CODE_BASE: Record<string, number | undefined> = {
  Semicolon: 59, // ;
  Equal: 61, // =
  Comma: 44, // ,
  Minus: 45, // -
  Period: 46, // .
  Slash: 47, // /
  Backquote: 96, // `
  BracketLeft: 91, // [
  Backslash: 92, // \
  BracketRight: 93, // ]
  Quote: 39, // '
};

// US-layout shifted character -> unshifted codepoint, KITTY_CODE_BASE's twin for
// input with only the character (toolbar sticky Ctrl): Ctrl+':' is 59;6u, as a US
// keyboard's Ctrl+Shift+; is, not 58;5u.
const KITTY_SHIFTED_CHAR_BASE: Record<string, number | undefined> = {
  ":": 59, // ;
  "<": 44, // ,
  ">": 46, // .
  "?": 47, // /
  '"': 39, // '
  "{": 91, // [
  "}": 93, // ]
  "|": 92, // \
  "~": 96, // `
  "+": 61, // =
  _: 45, // -
  "!": 49, // 1
  "@": 50, // 2
  "#": 51, // 3
  $: 52, // 4
  "%": 53, // 5
  "^": 54, // 6
  "&": 55, // 7
  "*": 56, // 8
  "(": 57, // 9
  ")": 48, // 0
};

/**
 * kittyCtrlCharSeq encodes Ctrl+<typed character> in the kitty CSI-u form
 * using the spec's unshifted-key rule: a shifted glyph (':', 'A', '{') maps to
 * its US-layout base codepoint with the shift bit added to the modifier
 * (Ctrl+':' -> 59;6u), an unshifted one encodes directly (Ctrl+'s' -> 115;5u).
 * Shared by every path that has only the character (sticky-Ctrl toolbar); its
 * bytes equal the physical-keyboard encoder's for the same chord on a US layout.
 */
export function kittyCtrlCharSeq(ch: string): string | null {
  let cp = KITTY_SHIFTED_CHAR_BASE[ch];
  let shifted = cp !== undefined;
  if (cp === undefined && ch >= "A" && ch <= "Z") {
    cp = ch.toLowerCase().codePointAt(0);
    shifted = true;
  }
  cp ??= ch.toLowerCase().codePointAt(0);
  if (cp === undefined) {
    return null;
  }
  return `${ESC}[${cp};${shifted ? 6 : 5}u`; // 5 = ctrl, 6 = ctrl+shift
}

// Keypad NON-TEXT keys under disambiguate, keyed by ev.key (NumLock flips a
// numpad key between its text digit, which stays text under 0x1, and these
// functions), with the spec's dedicated KP_* codes (57414-57427). NumpadEnter
// is KP_ENTER, a distinct key from the legacy-byte main Enter. KP_BEGIN is
// emitted in the `u` form because the spec's alternate `CSI E` letter form is
// kitty-only (crossterm/ratatui cannot parse it). The KP_ digit/operator codes
// (57399-57416) are a report-all (0x8) feature and are deliberately absent.
const KITTY_KEYPAD: Record<string, number | undefined> = {
  ArrowLeft: 57417, // KP_LEFT
  ArrowRight: 57418, // KP_RIGHT
  ArrowUp: 57419, // KP_UP
  ArrowDown: 57420, // KP_DOWN
  PageUp: 57421, // KP_PAGE_UP
  PageDown: 57422, // KP_PAGE_DOWN
  Home: 57423, // KP_HOME
  End: 57424, // KP_END
  Insert: 57425, // KP_INSERT
  Delete: 57426, // KP_DELETE
  Clear: 57427, // KP_BEGIN
  Enter: 57414, // KP_ENTER
};

/** CSI 1;{mod}{letter}, omitting `1;{mod}` when there are no modifiers. */
function kittyLetterSeq(letter: string, ev: KeyboardEvent): string {
  const m = modifiersDigit(ev);
  return m === 1 ? `${ESC}[${letter}` : `${ESC}[1;${m}${letter}`;
}
/** CSI {num};{mod}~, omitting `;{mod}` when there are no modifiers. */
function kittyTildeSeq(num: number, ev: KeyboardEvent): string {
  const m = modifiersDigit(ev);
  return m === 1 ? `${ESC}[${num}~` : `${ESC}[${num};${m}~`;
}
/** CSI {num};{mod}u, omitting `;{mod}` when there are no modifiers. */
function kittyUSeq(num: number, ev: KeyboardEvent): string {
  const m = modifiersDigit(ev);
  return m === 1 ? `${ESC}[${num}u` : `${ESC}[${num};${m}u`;
}

/** True when any modifier (ctrl / alt / meta / shift) is held. */
function anyModifier(ev: KeyboardEvent): boolean {
  return ev.ctrlKey || ev.altKey || ev.metaKey || ev.shiftKey;
}

/**
 * The kitty key code (https://sw.kovidgoyal.net/kitty/keyboard-protocol/#key-codes).
 * With no Shift and no composing Alt, ev.key's keyCharacter, a printable Windows
 * Ctrl-level character included, unless layoutKeyCodepoint declines it (a
 * non-Latin letter or mark on a letter key folds to that key's Latin letter). Otherwise,
 * by policy, physicalKeyCodepoint's reading of the physical key.
 */
function kittyKeyCodepoint(ev: KeyboardEvent): number {
  if (!ev.shiftKey && !altComposes(ev)) {
    const cp = layoutKeyCodepoint(ev.key, ev.code);
    if (cp !== undefined) {
      return cp;
    }
  }
  return physicalKeyCodepoint(ev);
}

/**
 * No event field marks composition, so the platform decides: Option composes on
 * Apple, and Android Alt on keys the page cannot list
 * (https://android.googlesource.com/platform/frameworks/base/+/refs/heads/main/data/keyboards/Generic.kcm).
 * An Android tablet in Chrome's desktop mode says "X11; Linux" with touch
 * (https://developer.chrome.com/blog/desktop-mode). Firefox sets AltGraph on
 * every macOS Option chord (https://searchfox.org/mozilla-central/source/widget/cocoa/nsCocoaUtils.mm).
 */
function altComposes(ev: KeyboardEvent): boolean {
  if (ev.getModifierState("AltGraph")) {
    return true;
  }
  if (!ev.altKey) {
    return false;
  }
  const nav = (globalThis as { readonly navigator?: Navigator }).navigator;
  const ua = nav?.userAgent ?? "";
  return /Mac|Android/.test(ua) || (ua.includes("Linux") && (nav?.maxTouchPoints ?? 0) > 0);
}

// Keeps every key code printable: never a C0, C1 or format character.
const KEY_CHARACTER = /^[\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]$/u;

/**
 * ev.key lowercased, or ev.key itself when its lowercase is not one KEY_CHARACTER
 * (Turkish "İ" lowercases to i plus a combining dot); undefined when neither is.
 */
function keyCharacter(key: string): string | undefined {
  const lower = key.toLowerCase();
  if (KEY_CHARACTER.test(lower)) {
    return lower;
  }
  return KEY_CHARACTER.test(key) ? key : undefined;
}

/**
 * ev.key's keyCharacter as a key code ("İ" reports itself: its US position makes
 * Turkish-F Caps Lock+Ctrl+i ctrl+s). Undefined where the physical key answers: a key with
 * no keyCharacter (Windows Hebrew Ctrl+[ types U+200E, https://kbdlayout.info/kbdheb),
 * and a non-Latin letter or mark on a letter key, folded to its Latin letter so Russian
 * Ctrl+С is ctrl+c for apps that never ask for the base-layout key (flag 0x4, masked).
 */
function layoutKeyCodepoint(key: string, code: string): number | undefined {
  const ch = keyCharacter(key);
  if (ch === undefined) {
    return undefined;
  }
  if (/^Key[A-Z]$/.test(code) && /^[\p{L}\p{M}]$/u.test(ch) && !/\p{Script=Latin}/u.test(ch)) {
    return undefined;
  }
  return ch.codePointAt(0);
}

/**
 * The unshifted US-layout codepoint of a Key*, Digit* or KITTY_CODE_BASE code. Any
 * other code (Space, Intl*, empty) reports its ev.key's keyCharacter, which may be
 * the shifted or composed one; with none, Space reports 32 and any other code 0
 * (the chord is ignored).
 */
function physicalKeyCodepoint(ev: KeyboardEvent): number {
  const code = ev.code;
  if (code.length === 4 && code.startsWith("Key")) {
    return code.charCodeAt(3) + 32; // "KeyA".."KeyZ" -> 'a'..'z'
  }
  if (code.length === 6 && code.startsWith("Digit")) {
    return code.charCodeAt(5); // "Digit0".."Digit9" -> '0'..'9'
  }
  const base = KITTY_CODE_BASE[code];
  if (base !== undefined) {
    return base;
  }
  return keyCharacter(ev.key)?.codePointAt(0) ?? (code === "Space" ? 32 : 0);
}

/**
 * Encode a keydown under the kitty disambiguate flag (0x1). Returns "ignore"
 * for text-producing keys so the hidden textarea still handles typing/IME.
 * Called only when the flag is active; the modifier-only and local-scroll cases
 * are handled by the shared preamble in mapKeyboardEvent (which skips its
 * application-keypad SS3 branch under this flag, so keypad keys reach here).
 */
function encodeKittyDisambiguate(ev: KeyboardEvent): KeyboardResult {
  const key = ev.key;

  // Keypad navigation keys and NumpadEnter get KP_* codes. NumLock-on digits and
  // operators fall through to the printable path: KP_ digit codes belong to the
  // report-all flag (0x8), which is not honored.
  if (ev.code.startsWith("Numpad")) {
    const kp = KITTY_KEYPAD[key];
    if (kp !== undefined) {
      return { kind: "send", bytes: kittyUSeq(kp, ev) };
    }
  }

  const letter = KITTY_LETTER[key];
  if (letter !== undefined) {
    return { kind: "send", bytes: kittyLetterSeq(letter, ev) };
  }
  const tnum = KITTY_TILDE[key];
  if (tnum !== undefined) {
    return { kind: "send", bytes: kittyTildeSeq(tnum, ev) };
  }
  const fnHigh = KITTY_FN_HIGH[key];
  if (fnHigh !== undefined) {
    return { kind: "send", bytes: kittyUSeq(fnHigh, ev) };
  }

  // Enter / Tab / Backspace keep their legacy bytes when UNMODIFIED (so a user
  // can still type `reset` if a program leaves the mode on after crashing);
  // with modifiers they take the disambiguated CSI-u form.
  if (key === "Enter") {
    return { kind: "send", bytes: anyModifier(ev) ? kittyUSeq(13, ev) : "\r" };
  }
  if (key === "Tab") {
    return { kind: "send", bytes: anyModifier(ev) ? kittyUSeq(9, ev) : "\t" };
  }
  if (key === "Backspace") {
    return { kind: "send", bytes: anyModifier(ev) ? kittyUSeq(127, ev) : DEL };
  }
  if (key === "Escape") {
    // Unmodified Escape shares the logical-Escape home with the toolbar's ESC
    // button; a modified Escape carries its modifier digit (no toolbar analog).
    // Kitty is definitionally active on this path (mapKeyboardEvent routes
    // here only when the injected modes carry the disambiguate flag).
    return { kind: "send", bytes: anyModifier(ev) ? kittyUSeq(27, ev) : plainEscapeSeq(true) };
  }

  // A single character stays text unless Ctrl, Alt or Meta is held, where the
  // legacy encoding is ambiguous (Ctrl+I == Tab, Alt+x == ESC x): CSI u then.
  if (isOneCodePoint(key)) {
    // Meta/Cmd-ONLY combos stay with the browser even under kitty disambiguate,
    // as in legacy mode: Cmd+C, Cmd+V and Cmd+R are browser and OS chrome.
    if (ev.metaKey && !ev.ctrlKey && !ev.altKey) {
      return { kind: "ignore" };
    }
    // A desktop AltGr character arrives with ctrlKey and altKey clear, so it stays
    // text: on Windows, Chromium and Firefox swap Ctrl+Alt for AltGraph whenever
    // the chord types a character (https://chromestatus.com/feature/5059438092222464,
    // https://searchfox.org/mozilla-central/source/widget/windows/KeyboardLayout.cpp).
    // AltGraph must not exempt a key here: Firefox sets it on every macOS Option
    // chord (see altComposes), and Ctrl+Option+a is 97;7u.
    if (ev.ctrlKey || ev.altKey || ev.metaKey) {
      const cp = kittyKeyCodepoint(ev);
      if (cp > 0) {
        return { kind: "send", bytes: kittyUSeq(cp, ev) };
      }
    }
    return { kind: "ignore" };
  }
  return { kind: "ignore" };
}

/**
 * mapKeyboardEvent converts a KeyboardEvent into the terminal action to take,
 * read against `modes`, the mode state of the terminal the key belongs to.
 * Returns "ignore" for a bare modifier press or a key the browser should keep
 * (Cmd+R). The caller calls `ev.preventDefault()` on "send" and "scroll-*";
 * this function stays pure.
 */
export function mapKeyboardEvent(ev: KeyboardEvent, modes: KeyboardModes): KeyboardResult {
  // Modifier-only presses (Shift, Ctrl, Alt, Meta) — no-op.
  if (ev.key === "Shift" || ev.key === "Control" || ev.key === "Alt" || ev.key === "Meta") {
    return { kind: "ignore" };
  }

  // Composition-in-progress: caller ignores keydowns while
  // CompositionHelper.isComposing is true. We don't see that state
  // here. The caller filters at its layer.

  // Application keypad mode (DECKPAM): when active, numpad keys send
  // SS3 sequences (ESC O <letter>) instead of their normal characters.
  // We detect numpad keys via ev.code (Numpad0-9, NumpadDecimal,
  // NumpadSubtract, NumpadAdd, NumpadMultiply, NumpadDivide, NumpadEnter).
  if (
    modes.isApplicationKeypad() &&
    (modes.getKeyboardFlags() & KITTY_DISAMBIGUATE) === 0 &&
    !ev.ctrlKey &&
    !ev.altKey &&
    !ev.metaKey &&
    ev.code.startsWith("Numpad")
  ) {
    const ss3Key = numpadCodeToKey(ev.code);
    if (ss3Key !== undefined) {
      const ss3Char = KEYPAD_SS3[ss3Key];
      if (ss3Char !== undefined) {
        return { kind: "send", bytes: `${ESC}O${ss3Char}` };
      }
    }
  }

  // Shift+PageUp / Shift+PageDown — local scrollback navigation
  // (matches xterm.js KeyboardResultType.PAGE_UP/PAGE_DOWN).
  if (ev.key === "PageUp" && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
    return { kind: "scroll-up" };
  }
  if (ev.key === "PageDown" && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
    return { kind: "scroll-down" };
  }

  // Kitty keyboard protocol: once the app enables the disambiguate flag, encode
  // non-text keys as CSI-u sequences (Esc, ctrl/alt combos, functional keys);
  // text keys still defer to the textarea. Placed after the shared preamble
  // (modifier-only, application keypad, local scroll) so those behave
  // identically in both modes.
  if ((modes.getKeyboardFlags() & KITTY_DISAMBIGUATE) !== 0) {
    return encodeKittyDisambiguate(ev);
  }

  // Cursor keys — CSI form with optional modifiers (CSI 1;{m}{letter}).
  const arrow = ARROW_LETTER[ev.key];
  if (arrow !== undefined) {
    return { kind: "send", bytes: csiLetter(arrow, ev, modes) };
  }

  // Home / End — CSI {H,F} with optional modifiers.
  if (ev.key === "Home") {
    return { kind: "send", bytes: csiLetter("H", ev, modes) };
  }
  if (ev.key === "End") {
    return { kind: "send", bytes: csiLetter("F", ev, modes) };
  }

  // Insert / Delete / PageUp / PageDown (no Shift) — CSI tilde forms.
  if (ev.key === "Insert") {
    return { kind: "send", bytes: csiTilde(2, ev) };
  }
  if (ev.key === "Delete") {
    return { kind: "send", bytes: csiTilde(3, ev) };
  }
  if (ev.key === "PageUp") {
    return { kind: "send", bytes: csiTilde(5, ev) };
  }
  if (ev.key === "PageDown") {
    return { kind: "send", bytes: csiTilde(6, ev) };
  }

  // F1-F4 — SS3 with optional modifier-CSI, like xterm.
  const fnLetter = FN_LETTER[ev.key];
  if (fnLetter !== undefined) {
    const m = modifiersDigit(ev);
    return {
      kind: "send",
      bytes: m === 1 ? `${ESC}O${fnLetter}` : `${ESC}[1;${m}${fnLetter}`,
    };
  }
  // F5-F12 — CSI tilde form with modifiers.
  const fnTilde = FN_TILDE[ev.key];
  if (fnTilde !== undefined) {
    return { kind: "send", bytes: csiTilde(fnTilde, ev) };
  }

  // Tab / Shift+Tab.
  if (ev.key === "Tab") {
    return { kind: "send", bytes: ev.shiftKey ? `${ESC}[Z` : "\t" };
  }

  // Enter — \r. Alt+Enter prefixes ESC.
  if (ev.key === "Enter") {
    return { kind: "send", bytes: ev.altKey ? `${ESC}\r` : "\r" };
  }

  // Backspace — \x7f (DEL). Alt+Backspace = ESC + DEL (delete-prev-word
  // in readline). Ctrl+Backspace = \b (^H).
  if (ev.key === "Backspace") {
    if (ev.altKey) {
      return { kind: "send", bytes: ESC + DEL };
    }
    if (ev.ctrlKey) {
      return { kind: "send", bytes: "\b" };
    }
    return { kind: "send", bytes: DEL };
  }

  // Escape — ESC (via the shared logical-Escape home; kitty is definitionally
  // inactive on this legacy path, so it yields the bare ESC byte).
  // Alt+Escape = ESC ESC (xterm.js convention).
  if (ev.key === "Escape") {
    return { kind: "send", bytes: ev.altKey ? `${ESC}${ESC}` : plainEscapeSeq(false) };
  }

  // Space — \x00 with Ctrl (per xterm). Alt+Space = ESC ' '.
  if (ev.key === " ") {
    if (ev.ctrlKey) {
      return { kind: "send", bytes: "\x00" };
    }
    if (ev.altKey) {
      return { kind: "send", bytes: ESC + " " };
    }
    return { kind: "ignore" }; // let `input` event handle plain space
  }

  // Single printable character with modifiers. Neither branch takes Meta, so
  // the browser keeps Cmd+R/T/W/C/V; Ctrl+R/T/W go to the PTY, where readline
  // binds them.
  if (ev.key.length === 1) {
    const ch = ev.key;

    // Ctrl+printable → C0 control byte (a-z → \x01..\x1a, plus the
    // C0 set @[\]^_? → \x00..\x1f, \x7f). Single branch via
    // `ctrlByteFor`; same table also drives bindMobileToolbar's
    // sticky-Ctrl applier.
    if (ev.ctrlKey && !ev.altKey && !ev.metaKey) {
      const c0 = ctrlByteFor(ch);
      if (c0 !== null) {
        return { kind: "send", bytes: c0 };
      }
    }
    // Alt+printable → ESC + char (meta prefix). Plain `input` event
    // would still fire with the char, so we'd duplicate. Caller must
    // preventDefault when we return "send" here to suppress `input`.
    if (ev.altKey && !ev.ctrlKey && !ev.metaKey) {
      return { kind: "send", bytes: ESC + ch };
    }
  }

  // Everything else: defer to the `input` event. The browser will
  // produce the printable character (including IME / dead-key
  // composition output) via input, where we send it.
  return { kind: "ignore" };
}

/**
 * ctrlByteFor returns the C0 control byte produced by Ctrl+`ch`, or `null`
 * when the character has no Ctrl mapping: a-z (case-folded) give \x01..\x1a,
 * space and '@' give NUL, '[' '\\' ']' '^' '_' give \x1b..\x1f, '?' gives DEL.
 * A multi-character string or an unmapped character returns `null`.
 */
export function ctrlByteFor(ch: string): string | null {
  if (ch.length !== 1) {
    return null;
  }
  // Letters a-z (case-folded) → \x01..\x1a.
  const code = ch.toLowerCase().charCodeAt(0);
  if (code >= 97 && code <= 122) {
    return String.fromCharCode(code - 96);
  }
  switch (ch) {
    case " ":
    case "@":
      return "\x00";
    case "[":
      return "\x1b";
    case "\\":
      return "\x1c";
    case "]":
      return "\x1d";
    case "^":
      return "\x1e";
    case "_":
      return "\x1f";
    case "?":
      return "\x7f";
    default:
      return null;
  }
}

// -- Application keypad helpers -----------------------------------------------

/** Map a KeyboardEvent.code starting with "Numpad" to the KEYPAD_SS3 lookup key. */
function numpadCodeToKey(code: string): string | undefined {
  switch (code) {
    case "Numpad0":
      return "0";
    case "Numpad1":
      return "1";
    case "Numpad2":
      return "2";
    case "Numpad3":
      return "3";
    case "Numpad4":
      return "4";
    case "Numpad5":
      return "5";
    case "Numpad6":
      return "6";
    case "Numpad7":
      return "7";
    case "Numpad8":
      return "8";
    case "Numpad9":
      return "9";
    case "NumpadDecimal":
      return ".";
    case "NumpadSubtract":
      return "-";
    case "NumpadAdd":
      return "+";
    case "NumpadMultiply":
      return "*";
    case "NumpadDivide":
      return "/";
    case "NumpadEnter":
      return "Enter";
    default:
      return undefined;
  }
}

// -- Bracketed paste --------------------------------------------------------

/** The mode bit `bracketTextForPaste` reads; a `ModeState` satisfies it. */
export interface PasteModes {
  isBracketedPaste(): boolean;
}

/**
 * bracketTextForPaste wraps text in DEC 2004 bracketed-paste sentinels when
 * `modes` reports bracketed-paste mode (CSI ?2004h) enabled, and returns it
 * unchanged otherwise. Embedded ESC bytes are replaced with U+241B first: an
 * attacker-controlled paste containing \x1b[201~ would otherwise close the
 * paste region early and have the rest interpreted as a command.
 */
export function bracketTextForPaste(text: string, modes: PasteModes): string {
  if (!modes.isBracketedPaste()) {
    return text;
  }
  // eslint-disable-next-line no-control-regex -- intentional: sanitising ESC bytes in pasted text
  const sanitised = text.replace(/\x1b/g, "\u241B");
  return `\x1b[200~${sanitised}\x1b[201~`;
}

/**
 * Normalise CR/LF to a single CR and strip NUL bytes before bracketing.
 * xterm.js convention on both counts: NUL in pasted text is never meaningful
 * terminal input, and under the v3 wire framing a NUL-leading paste would
 * otherwise be split or (on old servers) misread as a control frame.
 */
export function prepareTextForTerminal(text: string): string {
  // eslint-disable-next-line no-control-regex -- intentional: stripping NUL from pasted text
  return text.replace(/\r?\n/g, "\r").replace(/\x00/g, "");
}

// -- Mobile toolbar ---------------------------------------------------------
