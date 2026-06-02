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
let focusReporting = false;
let reverseVideo = false;

export function setModes(
  bracketed: boolean,
  appCursor: boolean,
  mSGR?: boolean,
  focus?: boolean,
  mMode?: number,
  appKeypad?: boolean,
  revVideo?: boolean,
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
}

export function isBracketedPaste(): boolean {
  return bracketedPaste;
}

export function isApplicationCursor(): boolean {
  return applicationCursor;
}

export function getMouseMode(): number {
  return mouseMode;
}

export function isMouseSGR(): boolean {
  return mouseSGR;
}

export function isFocusReporting(): boolean {
  return focusReporting;
}

export function isApplicationKeypad(): boolean {
  return applicationKeypad;
}

export function isReverseVideo(): boolean {
  return reverseVideo;
}
