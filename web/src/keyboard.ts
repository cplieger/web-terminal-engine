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
  if (
    isApplicationKeypad() &&
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
 * ctrlByteFor returns the C0 control byte produced by Ctrl+`ch`, or
 * `null` when the character has no Ctrl mapping. The full table:
 *
 *   a-z (case-folded)  → \x01..\x1a (Ctrl+A=SOH .. Ctrl+Z=SUB)
 *   ' ' (space)        → \x00 (NUL — same as Ctrl+@)
 *   '@'                → \x00 (NUL)
 *   '['                → \x1b (ESC)
 *   '\\'               → \x1c (FS)
 *   ']'                → \x1d (GS)
 *   '^'                → \x1e (RS)
 *   '_'                → \x1f (US)
 *   '?'                → \x7f (DEL — also Ctrl+8 on US layouts via Shift+/)
 *
 * Anything else (multi-char strings, unmapped single chars) returns
 * `null`. Used by `mapKeyboardEvent` for Ctrl+printable handling and
 * by `bindMobileToolbar`'s sticky-Ctrl applier.
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

// -- Mobile toolbar ---------------------------------------------------------

/**
 * Default DOM ids for the on-screen mobile keyboard toolbar buttons,
 * inside the toolbar container element passed to `bindMobileToolbar`.
 * Override individual ids via `BindMobileToolbarOptions.ids`.
 */
export const DEFAULT_TOOLBAR_IDS = {
  toggle: "kb-toggle",
  ctrl: "kb-ctrl",
  up: "kb-up",
  down: "kb-down",
  left: "kb-left",
  right: "kb-right",
  tab: "kb-tab",
  enter: "kb-enter",
  esc: "kb-esc",
} as const;

/** Per-button id overrides for `bindMobileToolbar`. */
export interface MobileToolbarIds {
  readonly toggle?: string;
  readonly ctrl?: string;
  readonly up?: string;
  readonly down?: string;
  readonly left?: string;
  readonly right?: string;
  readonly tab?: string;
  readonly enter?: string;
  readonly esc?: string;
}

/** Options for `bindMobileToolbar`. */
export interface BindMobileToolbarOptions {
  /**
   * Toolbar container element. Buttons are looked up by id within this
   * element via `querySelector('#<id>')`. The same element is the one
   * `kb-toggle` adds/removes the `.collapsed` class on.
   */
  readonly toolbar: HTMLElement;
  /**
   * Send sink for arrow keys, Tab, Enter, Esc — i.e. for everything
   * the toolbar emits. Sticky-Ctrl input goes through `applyStickyCtrl`
   * on the controller; the consumer is responsible for routing
   * keyboard / paste / IME text through it before handing the result
   * to the same `send`.
   */
  readonly send: (bytes: string) => void;
  /** Optional id overrides. Missing keys fall back to defaults. */
  readonly ids?: MobileToolbarIds;
  /**
   * Fired whenever the sticky-Ctrl state changes (toolbar press,
   * `setCtrlArmed`, or auto-disarm after applying a Ctrl byte).
   */
  readonly onCtrlChange?: (armed: boolean) => void;
}

/** Returned from `bindMobileToolbar`; manages sticky-Ctrl state and tear-down. */
export interface MobileToolbarController {
  /**
   * Apply sticky-Ctrl to a piece of input text.
   *   - When NOT armed: returns `text` unchanged.
   *   - Armed AND `text.length === 1`: returns the matching Ctrl byte
   *     (`ctrlByteFor(text)`), or the original char when no mapping
   *     exists. Always disarms.
   *   - Armed AND longer (e.g. paste, IME commit): returns `text`
   *     unchanged and disarms — applying Ctrl to a multi-char string
   *     would garble it.
   */
  applyStickyCtrl(text: string): string;
  /** Programmatically arm/disarm Ctrl. Updates the toolbar button visuals (`.armed` class + `aria-pressed`) + fires `onCtrlChange`. */
  setCtrlArmed(on: boolean): void;
  /** Whether sticky-Ctrl is currently armed. */
  isCtrlArmed(): boolean;
  /** Detach all event listeners and reset the toolbar to a non-armed state. Idempotent. */
  dispose(): void;
}

/**
 * Wire a mobile / touch toolbar of on-screen keyboard buttons (Ctrl,
 * arrows, Tab, Enter, Esc, plus a collapse toggle) to a vterm send
 * sink. This was duplicated across vibekit and vibecli — same wire
 * sequences, same sticky-Ctrl semantics, same DECCKM nuance — so it
 * lives here.
 *
 * Each button's `pointerdown` is intercepted with `preventDefault()`
 * so the press never fires a focus change or scroll on the host page.
 *
 * Arrow keys consult `isApplicationCursor()` from `modes.ts` so apps
 * that have set DECCKM (vim, less, fzf, htop, …) get SS3 sequences
 * (`ESC O A..D`) and apps in the default mode get the bare CSI form
 * (`ESC [ A..D`). This matches what `mapKeyboardEvent` does for
 * physical-keyboard arrows.
 */
export function bindMobileToolbar(opts: BindMobileToolbarOptions): MobileToolbarController {
  const ids = { ...DEFAULT_TOOLBAR_IDS, ...opts.ids };
  let armed = false;

  function findBtn(id: string): HTMLElement | null {
    // IDs are kebab-case ASCII; no CSS escaping needed.
    return opts.toolbar.querySelector<HTMLElement>(`#${id}`);
  }

  const toggleBtn = findBtn(ids.toggle);
  const ctrlBtn = findBtn(ids.ctrl);
  const upBtn = findBtn(ids.up);
  const downBtn = findBtn(ids.down);
  const leftBtn = findBtn(ids.left);
  const rightBtn = findBtn(ids.right);
  const tabBtn = findBtn(ids.tab);
  const enterBtn = findBtn(ids.enter);
  const escBtn = findBtn(ids.esc);

  function paintCtrlBtn(on: boolean): void {
    if (!ctrlBtn) {
      return;
    }
    ctrlBtn.classList.toggle("armed", on);
    // Keep the ARIA toggle state in sync with the visual `.armed` class so
    // assistive tech announces the sticky-Ctrl button as pressed/unpressed.
    // The scaffold ships kb-ctrl as `aria-pressed="false"`; consumers used to
    // own this in their own setCtrlArmed before delegating the toolbar here.
    ctrlBtn.setAttribute("aria-pressed", on ? "true" : "false");
  }

  function setCtrlArmed(on: boolean): void {
    if (armed === on) {
      // Still update visuals defensively (e.g. after dispose was called
      // and someone re-armed via setCtrlArmed) — but skip the change
      // notification to keep onCtrlChange edge-triggered.
      paintCtrlBtn(on);
      return;
    }
    armed = on;
    paintCtrlBtn(on);
    opts.onCtrlChange?.(on);
  }

  function arrowSeq(letter: "A" | "B" | "C" | "D"): string {
    return isApplicationCursor() ? `${ESC}O${letter}` : `${ESC}[${letter}`;
  }

  // Listener registry so dispose() can detach them all.
  const cleanups: (() => void)[] = [];
  function on(el: HTMLElement | null, handler: (e: PointerEvent) => void): void {
    if (!el) {
      return;
    }
    const wrapped = (e: Event): void => {
      handler(e as PointerEvent);
    };
    el.addEventListener("pointerdown", wrapped);
    cleanups.push(() => {
      el.removeEventListener("pointerdown", wrapped);
    });
  }

  on(toggleBtn, (e) => {
    e.preventDefault();
    // Show/hide the toolbar. Deliberately does NOT clear armed Ctrl —
    // toggling visibility shouldn't lose state.
    opts.toolbar.classList.toggle("collapsed");
  });

  on(ctrlBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(!armed);
  });

  on(upBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrowSeq("A"));
  });
  on(downBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrowSeq("B"));
  });
  on(rightBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrowSeq("C"));
  });
  on(leftBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrowSeq("D"));
  });

  on(tabBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send("\t");
  });
  on(enterBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send("\r");
  });
  on(escBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(ESC);
  });

  function applyStickyCtrl(text: string): string {
    if (!armed) {
      return text;
    }
    if (text.length === 1) {
      const ctrl = ctrlByteFor(text);
      setCtrlArmed(false);
      return ctrl ?? text;
    }
    // Multi-char input (paste, IME commit) — applying Ctrl to a string
    // would garble it. Disarm and pass through verbatim.
    setCtrlArmed(false);
    return text;
  }

  function dispose(): void {
    setCtrlArmed(false);
    for (const c of cleanups) {
      c();
    }
    cleanups.length = 0;
  }

  return {
    applyStickyCtrl,
    setCtrlArmed,
    isCtrlArmed: () => armed,
    dispose,
  };
}
