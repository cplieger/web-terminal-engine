// Keyboard event → terminal byte sequence mapping.
//
// Mirrors xterm.js's evaluateKeyboardEvent (src/common/input/Keyboard.ts):
// every browser KeyboardEvent maps to either a sequence to send to the
// PTY or a local action (page-up/down for local scrollback nav). The
// mapping is exhaustive over xterm.js's coverage so vim/readline/Ink/
// the host application get the keys they expect.
//
// Modifier encoding follows xterm.js / VT520 convention:
//   1=Shift, 2=Alt, 4=Ctrl, 8=Meta. Sum then +1 for CSI 1;{n}{letter}.
//
// Application cursor mode (DECCKM, CSI ?1h/l) is tracked client-side
// via modes.ts (announced by server in wireMsgModes). Server-side, the
// vt screen tracks both bracketed paste (?2004) and DECCKM (?1) and
// emits a modes frame whenever they change.

import { isApplicationCursor, isApplicationKeypad, isBracketedPaste } from "./modes.js";

/** Result of mapping a keyboard event. */
export type KeyboardResult =
  | { kind: "send"; bytes: string }
  | { kind: "scroll-up" } // Shift+PageUp — handled locally
  | { kind: "scroll-down" } // Shift+PageDown — handled locally
  | { kind: "ignore" }; // Modifier-only press, etc.

const ESC = "\x1b";
const DEL = "\x7f";

/** Compute the xterm modifier digit (used in CSI 1;{n}letter sequences). */
function modifiersDigit(ev: KeyboardEvent): number {
  return (
    1 + (ev.shiftKey ? 1 : 0) + (ev.altKey ? 2 : 0) + (ev.ctrlKey ? 4 : 0) + (ev.metaKey ? 8 : 0)
  );
}

// -- Cursor / navigation keys -----------------------------------------------
// Letter is the xterm trailing letter (ABCDEFGHPQRS for arrows / Home /
// End / F1-F4 etc.). Without modifiers we send the bare CSI form; with
// modifiers we send CSI 1;{mod}{letter}. xterm.js Keyboard.ts pattern.
//
// Application cursor mode (DECCKM, CSI ?1) is plumbed via modes.ts.
// When the application has set DECCKM, the modifier-less form switches
// from CSI to SS3 (ESC O letter); modifier-bearing forms stay on CSI
// because they have no SS3 equivalent.
function csiLetter(letter: string, ev: KeyboardEvent): string {
  const m = modifiersDigit(ev);
  if (m === 1) {
    if (
      isApplicationCursor() &&
      (letter === "A" ||
        letter === "B" ||
        letter === "C" ||
        letter === "D" ||
        letter === "H" ||
        letter === "F")
    ) {
      return `${ESC}O${letter}`;
    }
    return `${ESC}[${letter}`;
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
};

const ARROW_LETTER: Record<string, string | undefined> = {
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

/**
 * mapKeyboardEvent converts a KeyboardEvent into the terminal action
 * to take. Returns "ignore" when the event is purely a modifier press
 * or when the browser should be allowed to handle it (e.g. browser
 * shortcuts like Cmd+R).
 *
 * Caller is responsible for ev.preventDefault() when the result is
 * "send" or "scroll-*"; we don't call it here so the function stays
 * pure and testable.
 */
export function mapKeyboardEvent(ev: KeyboardEvent): KeyboardResult {
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
  if (isApplicationKeypad() && !ev.ctrlKey && !ev.altKey && !ev.metaKey && ev.code.startsWith("Numpad")) {
    const ss3Key = numpadCodeToKey(ev.code);
    if (ss3Key !== undefined) {
      const ss3Char = KEYPAD_SS3[ss3Key];
      if (ss3Char !== undefined) {
        return { kind: "send", bytes: `${ESC}O${ss3Char}` };
      }
    }
  }

  // Browser-reserved meta combos (Cmd+R, Cmd+T, Cmd+Q, Cmd+W on Mac;
  // Ctrl+R, Ctrl+T, Ctrl+W with no Shift on others). Let the browser
  // handle them. Heuristic: only take Ctrl+letter when Shift is NOT
  // held AND the letter is one of the ones xterm consumes (a-z),
  // skipping the few that browsers commonly reserve.
  // (We deliberately pass Ctrl+R / Ctrl+T / Ctrl+W through to the PTY
  // — the host application's Ctrl+T transcript view depends on it. Users on macOS
  // who want browser-reserved combos can use Cmd instead.)

  // Shift+PageUp / Shift+PageDown — local scrollback navigation
  // (matches xterm.js KeyboardResultType.PAGE_UP/PAGE_DOWN).
  if (ev.key === "PageUp" && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
    return { kind: "scroll-up" };
  }
  if (ev.key === "PageDown" && ev.shiftKey && !ev.ctrlKey && !ev.altKey && !ev.metaKey) {
    return { kind: "scroll-down" };
  }

  // Cursor keys — CSI form with optional modifiers (CSI 1;{m}{letter}).
  const arrow = ARROW_LETTER[ev.key];
  if (arrow !== undefined) {
    return { kind: "send", bytes: csiLetter(arrow, ev) };
  }

  // Home / End — CSI {H,F} with optional modifiers.
  if (ev.key === "Home") {
    return { kind: "send", bytes: csiLetter("H", ev) };
  }
  if (ev.key === "End") {
    return { kind: "send", bytes: csiLetter("F", ev) };
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

  // Escape — ESC. Alt+Escape = ESC ESC (xterm.js convention).
  if (ev.key === "Escape") {
    return { kind: "send", bytes: ev.altKey ? `${ESC}${ESC}` : ESC };
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

  // Single printable character with modifiers.
  if (ev.key.length === 1) {
    const ch = ev.key;
    const code = ch.toLowerCase().charCodeAt(0);

    // Ctrl+letter (a-z) → ASCII 1-26.
    if (ev.ctrlKey && !ev.altKey && !ev.metaKey && code >= 97 && code <= 122) {
      return { kind: "send", bytes: String.fromCharCode(code - 96) };
    }
    // Ctrl+key for the C0 set: @[\]^_? produce \x00..\x1f, \x7f.
    if (ev.ctrlKey && !ev.altKey && !ev.metaKey) {
      const c0 = ctrlSymbolByte(ch);
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
 * ctrlSymbolByte handles Ctrl+symbol combos that map to C0 controls:
 *   Ctrl+@   → \x00 (NUL)
 *   Ctrl+[   → \x1b (ESC)
 *   Ctrl+\   → \x1c (FS)
 *   Ctrl+]   → \x1d (GS)
 *   Ctrl+^   → \x1e (RS)
 *   Ctrl+_   → \x1f (US)
 *   Ctrl+?   → \x7f (DEL)  (also Ctrl+8 on US layouts via Shift+/)
 *
 * Letters (Ctrl+a..z) are handled separately by the caller.
 */
function ctrlSymbolByte(ch: string): string | null {
  switch (ch) {
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
    case "Numpad0": return "0";
    case "Numpad1": return "1";
    case "Numpad2": return "2";
    case "Numpad3": return "3";
    case "Numpad4": return "4";
    case "Numpad5": return "5";
    case "Numpad6": return "6";
    case "Numpad7": return "7";
    case "Numpad8": return "8";
    case "Numpad9": return "9";
    case "NumpadDecimal": return ".";
    case "NumpadSubtract": return "-";
    case "NumpadAdd": return "+";
    case "NumpadMultiply": return "*";
    case "NumpadDivide": return "/";
    case "NumpadEnter": return "Enter";
    default: return undefined;
  }
}

// -- Bracketed paste --------------------------------------------------------

/**
 * bracketTextForPaste wraps text in DEC 2004 bracketed-paste sentinels
 * after sanitising any embedded ESC bytes to U+241B (visible escape
 * symbol), but only when the application has currently enabled
 * bracketed-paste mode (CSI ?2004h). When disabled, returns the text
 * unchanged. The current mode state is owned by modes.ts, kept in
 * sync by the server's wireMsgModes wire frame.
 *
 * The ESC sanitisation defends against an attacker-controlled paste
 * containing \x1b[201~ that would prematurely close the paste region
 * and let the rest be interpreted as a command — only relevant when
 * we are bracketing.
 */
export function bracketTextForPaste(text: string): string {
  if (!isBracketedPaste()) {
    return text;
  }
  // eslint-disable-next-line no-control-regex -- intentional: sanitising ESC bytes in pasted text
  const sanitised = text.replace(/\x1b/g, "\u241B");
  return `\x1b[200~${sanitised}\x1b[201~`;
}

/** Normalise CR/LF to a single CR before bracketing. xterm.js convention. */
export function prepareTextForTerminal(text: string): string {
  return text.replace(/\r?\n/g, "\r");
}
