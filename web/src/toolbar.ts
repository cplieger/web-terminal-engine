// On-screen mobile keyboard toolbar: a DOM widget (button lookup, ARIA
// painting, a listener registry, sticky-Ctrl state) wiring touch buttons to a
// terminal send sink. Split out of keyboard.ts (2026-07): that module is the
// pure key-ENCODING layer (KeyboardEvent -> wire bytes), and a DOM widget
// buried in it made both harder to navigate. The wire encodings themselves
// stay in keyboard.ts — this module consumes its exported logical-key homes
// (plainCursorKeySeq / plainEscapeSeq / kittyCtrlCharSeq / ctrlByteFor), so
// the toolbar cannot drift from the physical-key path (the equivalence
// fixtures in toolbar.test.ts pin the two byte-for-byte).
//
// This was duplicated across vibekit and web-terminal-kiro — same wire
// sequences, same sticky-Ctrl semantics, same DECCKM nuance — so it lives in
// the engine.

import {
  ctrlByteFor,
  kittyCtrlCharSeq,
  kittyDisambiguateActive,
  plainCursorKeySeq,
  plainEscapeSeq,
} from "./keyboard.js";
import { isApplicationCursor } from "./modes.js";

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
 * sink.
 *
 * Each button's `pointerdown` is intercepted with `preventDefault()`
 * so the press never fires a focus change or scroll on the host page.
 *
 * Arrow keys and Escape emit exactly what an unmodified physical key press
 * emits: the shared logical-key encodings live in keyboard.ts
 * (plainCursorKeySeq honors kitty disambiguate over DECCKM, then DECCKM's
 * SS3 form; plainEscapeSeq is CSI 27 u under kitty, bare ESC otherwise).
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

  // Arrow buttons emit exactly what an unmodified physical arrow press emits
  // (the toolbar.test.ts equivalence fixtures pin the two paths together).
  // Mode bits come from the module-global state — the toolbar's source until
  // P3 threads per-session modes through it.
  function arrowSeq(letter: "A" | "B" | "C" | "D"): string {
    return plainCursorKeySeq(letter, kittyDisambiguateActive(), isApplicationCursor());
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
    // The shared logical-Escape encoding (CSI 27 u under kitty disambiguate,
    // bare ESC otherwise) — same single home the physical path uses.
    opts.send(plainEscapeSeq(kittyDisambiguateActive()));
  });

  function applyStickyCtrl(text: string): string {
    if (!armed) {
      return text;
    }
    if (text.length === 1) {
      setCtrlArmed(false);
      // Under kitty disambiguate, Ctrl+char is the CSI-u form with the
      // spec's UNSHIFTED codepoint (shift folded into the modifier),
      // byte-identical with the physical-keyboard path: Ctrl+':' is 59;6u
      // from both, never 58;5u.
      if (kittyDisambiguateActive()) {
        const seq = kittyCtrlCharSeq(text);
        if (seq !== null) {
          return seq;
        }
      }
      const ctrl = ctrlByteFor(text);
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
