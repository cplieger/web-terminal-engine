// DEC private mode state — THE ACTIVE SESSION'S VIEW (P3). This singleton
// mirrors the modes of whichever session the live socket serves: the
// connection module is the single writer (it applies every inbound modes
// frame via applySnapshot AND, in a tabbed shell, synchronously restores the
// target session's cached snapshot inside setSession — power-on defaults for
// a session never seen). Readers therefore never observe another session's
// modes after setSession returns; the only staleness left is the inherent
// one-frame lag against the session's own server state. Used by:
//   - keyboard.ts arrow-key encoder: emits SS3 (ESC O letter) when
//     applicationCursor is true, CSI (ESC [ letter) otherwise.
//   - paste handling: wraps in \e[200~..\e[201~ only when
//     bracketedPaste is on; otherwise sends raw text.
//   - mouse.ts: encodes mouse events only when mouseMode > 0.
//   - focus reporting: sends ESC[I / ESC[O when focusReporting is on.
//
// Defaults: bracketedPaste starts true because most modern shells
// (bash 4.4+, zsh, fish) enable it immediately on startup via
// CSI ?2004h. Starting false would cause the first paste before the
// server's modes frame arrives to be sent un-bracketed, which shells
// interpret as typed input (potentially executing pasted commands
// character-by-character). applicationCursor starts false (normal
// mode) because that's the VT100 power-on default.

/**
 * One session's complete DEC-mode mirror, as a typed value (P3). The
 * connection module caches one per session and applies it via applySnapshot;
 * the named-field shape (vs setModes' nine positional parameters) makes
 * field-order drift impossible for production writers.
 */
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
 * The VT power-on mode state — what a session that has never announced modes
 * is in. Matches this module's initial values (see the bracketedPaste
 * default rationale above).
 */
export const POWER_ON_MODES: Readonly<ModeSnapshot> = {
  bracketedPaste: true,
  applicationCursor: false,
  mouseSGR: false,
  focusReporting: false,
  mouseMode: 0,
  applicationKeypad: false,
  reverseVideo: false,
  mousePixels: false,
  keyboardFlags: 0,
};

let bracketedPaste = true;
let applicationCursor = false;
let applicationKeypad = false;
let mouseMode = 0; // 0=off, 1000=normal, 1002=button-event, 1003=any-event
let mouseSGR = false;
let mousePixels = false; // DEC 1016: report pixel coords instead of cell coords
let focusReporting = false;
let reverseVideo = false;
// Kitty keyboard progressive-enhancement flags (bit0 disambiguate, bit1
// event-types, bit2 alternate-keys). 0 = protocol disabled (legacy encoding).
let keyboardFlags = 0;

/**
 * Update the cached mode state. Called by the consumer whenever the server
 * sends a `ModesMessage`. Pass-through arguments are optional so older
 * server builds that don't include all fields don't reset them to defaults.
 */
export function setModes(
  bracketed: boolean,
  appCursor: boolean,
  mSGR?: boolean,
  focus?: boolean,
  mMode?: number,
  appKeypad?: boolean,
  revVideo?: boolean,
  mPixels?: boolean,
  kbdFlags?: number,
): void {
  bracketedPaste = bracketed;
  applicationCursor = appCursor;
  if (mSGR !== undefined) {
    mouseSGR = mSGR;
  }
  if (focus !== undefined) {
    focusReporting = focus;
  }
  if (mMode !== undefined) {
    mouseMode = mMode;
  }
  if (appKeypad !== undefined) {
    applicationKeypad = appKeypad;
  }
  if (revVideo !== undefined) {
    reverseVideo = revVideo;
  }
  if (mPixels !== undefined) {
    mousePixels = mPixels;
  }
  if (kbdFlags !== undefined) {
    keyboardFlags = kbdFlags;
  }
}

/**
 * Apply a complete mode snapshot — every field, no optional-leaves-unchanged
 * semantics (that is setModes' positional back-compat contract). The
 * connection module uses this for both inbound modes frames and the
 * synchronous per-session restore in setSession.
 */
export function applySnapshot(s: Readonly<ModeSnapshot>): void {
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

/** The current mode state as a snapshot value (a copy; safe to retain). */
export function snapshot(): ModeSnapshot {
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
}

/** True when the server has DEC 2004 (bracketed paste) enabled. */
export function isBracketedPaste(): boolean {
  return bracketedPaste;
}

/** True when the server has DECCKM (application cursor keys) enabled. */
export function isApplicationCursor(): boolean {
  return applicationCursor;
}

/**
 * Active mouse tracking mode (xterm DECSET): 0 = off, 1000 = normal,
 * 1002 = button-event, 1003 = any-event.
 */
export function getMouseMode(): number {
  return mouseMode;
}

/** True when the server has DEC 1006 (SGR mouse encoding) enabled. */
export function isMouseSGR(): boolean {
  return mouseSGR;
}

/** True when the server has DEC 1016 (SGR-pixels mouse) enabled: mouse reports
 *  carry pixel coordinates instead of cell coordinates. */
export function isMousePixels(): boolean {
  return mousePixels;
}

/** True when the server has DEC 1004 (focus event reporting) enabled. */
export function isFocusReporting(): boolean {
  return focusReporting;
}

/** True when the server has DECKPAM (application keypad) enabled. */
export function isApplicationKeypad(): boolean {
  return applicationKeypad;
}

/** True when the server has DEC 5 (reverse video / DECSCNM) enabled. */
export function isReverseVideo(): boolean {
  return reverseVideo;
}

/**
 * Kitty keyboard progressive-enhancement flags currently in effect (bit0
 * disambiguate, bit1 report-event-types, bit2 report-alternate-keys). 0 means
 * the protocol is disabled and keys use legacy encoding. Read by keyboard.ts to
 * choose between legacy and kitty CSI-u encoding.
 */
export function getKeyboardFlags(): number {
  return keyboardFlags;
}
