// Current DEC private mode state, as last announced by the server via
// a wireMsgModes frame. Used by:
//   - keyboard.ts arrow-key encoder: emits SS3 (ESC O letter) when
//     applicationCursor is true, CSI (ESC [ letter) otherwise.
//   - paste handling: wraps in \e[200~..\e[201~ only when
//     bracketedPaste is on; otherwise sends raw text.

let bracketedPaste = true;
let applicationCursor = false;

export function setModes(bracketed: boolean, appCursor: boolean): void {
  bracketedPaste = bracketed;
  applicationCursor = appCursor;
}

export function isBracketedPaste(): boolean {
  return bracketedPaste;
}

export function isApplicationCursor(): boolean {
  return applicationCursor;
}
