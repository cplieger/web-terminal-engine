// Current DEC private mode state, as last announced by the server via
// a wireMsgModes frame. Used by:
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

let bracketedPaste = true;
let applicationCursor = false;
let applicationKeypad = false;
let mouseMode = 0; // 0=off, 1000=normal, 1002=button-event, 1003=any-event
let mouseSGR = false;
let mousePixels = false; // DEC 1016: report pixel coords instead of cell coords
let focusReporting = false;
let reverseVideo = false;

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
