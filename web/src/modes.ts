/** One session's complete DEC-mode mirror, as a typed value. */
export interface ModeSnapshot {
  bracketedPaste: boolean;
  applicationCursor: boolean;
  mouseSGR: boolean;
  focusReporting: boolean;
  mouseMode: number;
  applicationKeypad: boolean;
  reverseVideo: boolean;
  mousePixels: boolean;
  keyboardFlags: number;
}

/**
 * The VT power-on mode state, frozen: copy it (`{ ...POWER_ON_MODES }`) before
 * changing a field. Equal to a fresh server screen's (wire-golden/modes-power-on.bin).
 */
export const POWER_ON_MODES: Readonly<ModeSnapshot> = Object.freeze({
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

/**
 * The DEC private-mode state of one terminal instance. The server's screen owns
 * the modes; a connection marks them known only once its current socket's
 * server has announced them, and input must not be encoded while they are not.
 */
export interface ModeState {
  /** True when the server has DEC 2004 (bracketed paste) enabled. */
  isBracketedPaste(): boolean;
  /** True when the server has DECCKM (application cursor keys) enabled. */
  isApplicationCursor(): boolean;
  /** Active mouse tracking mode (xterm DECSET): 0 = off, 1000 = normal, 1002 = button-event, 1003 = any-event. */
  getMouseMode(): number;
  /** True when the server has DEC 1006 (SGR mouse encoding) enabled. */
  isMouseSGR(): boolean;
  /** True when the server has DEC 1016 (SGR-pixels mouse) enabled: reports carry pixel coordinates. */
  isMousePixels(): boolean;
  /** True when the server has DEC 1004 (focus event reporting) enabled. */
  isFocusReporting(): boolean;
  /** True when the server has DECKPAM (application keypad) enabled. */
  isApplicationKeypad(): boolean;
  /** True when the server has DEC 5 (reverse video / DECSCNM) enabled. */
  isReverseVideo(): boolean;
  /**
   * Kitty keyboard progressive-enhancement flags in effect (bit0 disambiguate,
   * bit1 report-event-types, bit2 report-alternate-keys); 0 means legacy encoding.
   */
  getKeyboardFlags(): number;
  /** Replaces every mode bit with the snapshot's; no field is left at its previous value. */
  applySnapshot(s: Readonly<ModeSnapshot>): void;
  /** Whether the values are the current socket's server's (true until a connection says otherwise). */
  isKnown(): boolean;
  setKnown(known: boolean): void;
  /**
   * Calls `cb` after every `applySnapshot` and every change of `isKnown()`;
   * returns the unsubscribe. A throwing subscriber does not stop the others.
   */
  subscribe(cb: () => void): () => void;
  /** The current mode state as a snapshot value (a copy; safe to retain). */
  snapshot(): ModeSnapshot;
  /** Resets every field to `POWER_ON_MODES`, drops the subscribers and makes every write a no-op. */
  dispose(): void;
}

/** Creates independent mode state initialized from a copy of `initial` (default `POWER_ON_MODES`). */
export function createModeState(initial: Readonly<ModeSnapshot> = POWER_ON_MODES): ModeState {
  let bracketedPaste = initial.bracketedPaste;
  let applicationCursor = initial.applicationCursor;
  let mouseSGR = initial.mouseSGR;
  let focusReporting = initial.focusReporting;
  let mouseMode = initial.mouseMode;
  let applicationKeypad = initial.applicationKeypad;
  let reverseVideo = initial.reverseVideo;
  let mousePixels = initial.mousePixels;
  let keyboardFlags = initial.keyboardFlags;
  let known = true;
  let disposed = false;
  const subscribers = new Set<() => void>();

  function notify(): void {
    for (const cb of [...subscribers]) {
      try {
        cb();
      } catch (err) {
        console.error("vterm: mode subscriber threw", err);
      }
    }
  }

  function assign(s: Readonly<ModeSnapshot>): void {
    bracketedPaste = s.bracketedPaste;
    applicationCursor = s.applicationCursor;
    mouseSGR = s.mouseSGR;
    focusReporting = s.focusReporting;
    mouseMode = s.mouseMode;
    applicationKeypad = s.applicationKeypad;
    reverseVideo = s.reverseVideo;
    mousePixels = s.mousePixels;
    keyboardFlags = s.keyboardFlags;
  }

  return {
    isBracketedPaste: () => bracketedPaste,
    isApplicationCursor: () => applicationCursor,
    getMouseMode: () => mouseMode,
    isMouseSGR: () => mouseSGR,
    isMousePixels: () => mousePixels,
    isFocusReporting: () => focusReporting,
    isApplicationKeypad: () => applicationKeypad,
    isReverseVideo: () => reverseVideo,
    getKeyboardFlags: () => keyboardFlags,
    applySnapshot(s: Readonly<ModeSnapshot>): void {
      if (disposed) {
        return;
      }
      assign(s);
      notify();
    },
    isKnown: () => known,
    setKnown(next: boolean): void {
      if (disposed || next === known) {
        return;
      }
      known = next;
      notify();
    },
    subscribe(cb: () => void): () => void {
      if (disposed) {
        return () => undefined;
      }
      subscribers.add(cb);
      return () => {
        subscribers.delete(cb);
      };
    },
    snapshot(): ModeSnapshot {
      return {
        bracketedPaste,
        applicationCursor,
        mouseSGR,
        focusReporting,
        mouseMode,
        applicationKeypad,
        reverseVideo,
        mousePixels,
        keyboardFlags,
      };
    },
    dispose(): void {
      disposed = true;
      subscribers.clear();
      assign(POWER_ON_MODES);
    },
  };
}
