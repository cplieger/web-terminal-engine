import { isOneCodePoint } from "./code-point.js";
import {
  ctrlByteFor,
  type KeyboardModes,
  kittyCtrlCharSeq,
  kittyDisambiguateActive,
  plainCursorKeySeq,
  plainEscapeSeq,
} from "./keyboard.js";

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
   * Called once per arrow, Tab, Enter or Esc press with that press's encoder,
   * which answers the press's bytes under the `modes` it is given (the arrows
   * follow kitty and DECCKM, Escape kitty). Sticky-Ctrl input goes through the
   * controller's `applyStickyCtrl` instead.
   */
  readonly send: (encode: KeyEncoder) => void;
  /** Optional id overrides. Missing keys fall back to defaults. */
  readonly ids?: MobileToolbarIds;
  /**
   * Fired whenever the sticky-Ctrl state changes (toolbar press,
   * `setCtrlArmed`, or the auto-disarm on armed input).
   */
  readonly onCtrlChange?: (armed: boolean) => void;
}

/** A key's bytes under the mode state of the terminal it is sent to; pure, so it may be called more than once. */
export type KeyEncoder = (modes: KeyboardModes) => string;

/** Returned from `bindMobileToolbar`; manages sticky-Ctrl state and tear-down. */
export interface MobileToolbarController {
  /**
   * Apply sticky-Ctrl to `text` as it is typed: every armed call disarms, at
   * once. Armed and one character (one code point): the encoder of its Ctrl
   * chord, its CSI u form under kitty disambiguate (`kittyCtrlCharSeq`), else
   * its Ctrl byte (`ctrlByteFor`), or the character when none exists. Not armed,
   * or longer (paste, IME commit, which Ctrl would garble): null, `text` unchanged.
   */
  applyStickyCtrl(text: string): KeyEncoder | null;
  /** Programmatically arm/disarm Ctrl. Updates the toolbar button visuals (`.armed` class + `aria-pressed`) + fires `onCtrlChange`. */
  setCtrlArmed(on: boolean): void;
  /** Whether sticky-Ctrl is currently armed. */
  isCtrlArmed(): boolean;
  /** Detach all event listeners and reset the toolbar to a non-armed state. Idempotent. */
  dispose(): void;
}

/**
 * Wire a mobile / touch toolbar of on-screen keyboard buttons (Ctrl, arrows,
 * Tab, Enter, Esc, plus a collapse toggle) to a vterm send sink. Each button's
 * `pointerdown` is intercepted with `preventDefault()` so the press never
 * fires a focus change or scroll on the host page. Arrow keys and Escape emit
 * exactly what an unmodified physical key press emits, through the shared
 * encodings in keyboard.ts.
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
    ctrlBtn.setAttribute("aria-pressed", on ? "true" : "false");
  }

  function setCtrlArmed(on: boolean): void {
    if (armed === on) {
      // Repaint without notifying, so onCtrlChange stays edge-triggered.
      paintCtrlBtn(on);
      return;
    }
    armed = on;
    paintCtrlBtn(on);
    opts.onCtrlChange?.(on);
  }

  // Arrow buttons emit exactly what an unmodified physical arrow press emits
  // (the toolbar.test.ts equivalence fixtures pin the two paths together).
  function arrow(letter: "A" | "B" | "C" | "D"): KeyEncoder {
    return (modes) =>
      plainCursorKeySeq(letter, kittyDisambiguateActive(modes), modes.isApplicationCursor());
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
    opts.send(arrow("A"));
  });
  on(downBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrow("B"));
  });
  on(rightBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrow("C"));
  });
  on(leftBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(arrow("D"));
  });

  on(tabBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(() => "\t");
  });
  on(enterBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    opts.send(() => "\r");
  });
  on(escBtn, (e) => {
    e.preventDefault();
    setCtrlArmed(false);
    // The shared logical-Escape encoding (CSI 27 u under kitty disambiguate,
    // bare ESC otherwise) — same single home the physical path uses.
    opts.send((modes) => plainEscapeSeq(kittyDisambiguateActive(modes)));
  });

  function applyStickyCtrl(text: string): KeyEncoder | null {
    if (!armed) {
      return null;
    }
    setCtrlArmed(false);
    if (!isOneCodePoint(text)) {
      return null;
    }
    // Matches a physical Ctrl chord for an unshifted Latin character; a shifted
    // or non-Latin one can differ off a US layout, since toolbar input has no
    // KeyboardEvent.code and kittyCtrlCharSeq reads the character alone.
    return (modes) =>
      (kittyDisambiguateActive(modes) ? kittyCtrlCharSeq(text) : null) ?? ctrlByteFor(text) ?? text;
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
