// Mouse event encoding for terminal input (SGR 1006 protocol).
//
// Encodes mouse events as SGR sequences: ESC[<code;col;rowM (press/move)
// or ESC[<code;col;rowm (release). Coordinates are 1-based.
//
// Button encoding (matches xterm/xterm.js):
//   0=left, 1=middle, 2=right, 64=wheel-up, 65=wheel-down
//   +4=shift, +8=alt, +16=ctrl, +32=motion
//
// Focus events: ESC[I (focus in), ESC[O (focus out).
//
// Out-of-scope (consumed but not implemented):
//   - X10 mouse mode (mode 9): not supported.
//   - urxvt encoding (mode 1015): not supported.
//   - DEFAULT encoding (raw bytes): not supported; only SGR 1006.

import { getMouseMode, isMouseSGR, isFocusReporting } from "./modes.js";

const ESC = "\x1b";

/** Encode a mouse button code for SGR protocol. */
function buttonCode(
  button: number,
  motion: boolean,
  shift: boolean,
  alt: boolean,
  ctrl: boolean,
): number {
  let code = button;
  if (shift) {
    code |= 4;
  }
  if (alt) {
    code |= 8;
  }
  if (ctrl) {
    code |= 16;
  }
  if (motion) {
    code |= 32;
  }
  return code;
}

/** Encode an SGR mouse sequence. */
export function encodeSGR(
  code: number,
  col: number,
  row: number,
  release: boolean,
): string {
  const final = release ? "m" : "M";
  return `${ESC}[<${code};${col};${row}${final}`;
}

/** Map DOM button index to xterm button number. */
function domButtonToXterm(button: number): number {
  // DOM: 0=left, 1=middle, 2=right
  // xterm: 0=left, 1=middle, 2=right
  if (button <= 2) {
    return button;
  }
  return 0; // fallback
}

export interface MouseInputHandler {
  send: (data: string) => void;
  cellSize: () => { width: number; height: number };
  termElement: () => HTMLElement;
}

let handler: MouseInputHandler | null = null;
let lastButton = -1;

export function init(h: MouseInputHandler): void {
  handler = h;
  const el = h.termElement();
  el.addEventListener("mousedown", onMouseDown);
  el.addEventListener("mouseup", onMouseUp);
  el.addEventListener("mousemove", onMouseMove);
  el.addEventListener("wheel", onWheel, { passive: false });
  el.addEventListener("focusin", onFocusIn);
  el.addEventListener("focusout", onFocusOut);
}

function pixelToCell(e: MouseEvent): { col: number; row: number } | null {
  if (!handler) {
    return null;
  }
  const el = handler.termElement();
  const rect = el.getBoundingClientRect();
  const { width, height } = handler.cellSize();
  if (width <= 0 || height <= 0) {
    return null;
  }
  const col = Math.floor((e.clientX - rect.left) / width) + 1; // 1-based
  const row = Math.floor((e.clientY - rect.top) / height) + 1; // 1-based
  if (col < 1 || row < 1) {
    return null;
  }
  return { col, row };
}

function onMouseDown(e: MouseEvent): void {
  const mode = getMouseMode();
  if (mode === 0 || !isMouseSGR() || !handler) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  lastButton = domButtonToXterm(e.button);
  const code = buttonCode(lastButton, false, e.shiftKey, e.altKey, e.ctrlKey);
  handler.send(encodeSGR(code, pos.col, pos.row, false));
  e.preventDefault();
}

function onMouseUp(e: MouseEvent): void {
  const mode = getMouseMode();
  if (mode === 0 || !isMouseSGR() || !handler) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  const btn = domButtonToXterm(e.button);
  const code = buttonCode(btn, false, e.shiftKey, e.altKey, e.ctrlKey);
  handler.send(encodeSGR(code, pos.col, pos.row, true));
  lastButton = -1;
  e.preventDefault();
}

function onMouseMove(e: MouseEvent): void {
  const mode = getMouseMode();
  if (mode === 0 || !isMouseSGR() || !handler) {
    return;
  }
  // mode 1000: no motion events
  // mode 1002: motion only while button held (drag)
  // mode 1003: all motion events
  if (mode === 1000) {
    return;
  }
  if (mode === 1002 && !e.buttons) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  const btn = e.buttons
    ? (e.buttons & 1 ? 0 : e.buttons & 4 ? 1 : e.buttons & 2 ? 2 : 0)
    : 0;
  const code = buttonCode(btn, true, e.shiftKey, e.altKey, e.ctrlKey);
  handler.send(encodeSGR(code, pos.col, pos.row, false));
}

function onWheel(e: WheelEvent): void {
  const mode = getMouseMode();
  if (mode === 0 || !isMouseSGR() || !handler) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  // Wheel up = button 64, wheel down = button 65
  const btn = e.deltaY < 0 ? 64 : 65;
  const code = buttonCode(btn, false, e.shiftKey, e.altKey, e.ctrlKey);
  handler.send(encodeSGR(code, pos.col, pos.row, false));
  e.preventDefault();
}

function onFocusIn(): void {
  if (!isFocusReporting() || !handler) {
    return;
  }
  handler.send(`${ESC}[I`);
}

function onFocusOut(): void {
  if (!isFocusReporting() || !handler) {
    return;
  }
  handler.send(`${ESC}[O`);
}
