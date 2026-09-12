// Mouse event encoding for terminal input (SGR 1006 protocol).
//
// Encodes mouse events as SGR sequences: ESC[<code;col;rowM (press/move)
// or ESC[<code;col;rowm (release). Coordinates are 1-based.
//
// Button encoding (matches xterm/xterm.js):
//   0=left, 1=middle, 2=right, 64=wheel-up, 65=wheel-down
//   +4=shift, +8=alt, +16=ctrl, +32=motion
//
// Mouse reports are BEST-EFFORT: a report carries no sequence number and
// describes a screen a resume may since have repainted, so `sendReport` may
// refuse one and a refused report is forgotten rather than retried. Focus is not
// mouse input at all — it is transport state the server derives — and lives in
// `connection.setClientFocus`.
//
// Out-of-scope (consumed but not implemented):
//   - X10 mouse mode (mode 9): not supported.
//   - urxvt encoding (mode 1015): not supported.
//   - DEFAULT encoding (raw bytes): not supported; only SGR 1006.

import { getMouseMode, isMouseSGR, isMousePixels } from "./modes.js";

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

/**
 * Encode a mouse event into an SGR 1006 escape sequence ready to send to the
 * terminal as input. The returned string starts with ESC[< and ends with `M`
 * for press/move or `m` for release.
 *
 * @param code  Button code returned from `buttonCode` (button + modifier bits).
 * @param col   1-based column.
 * @param row   1-based row.
 * @param release `true` for release events (terminator `m`), `false` otherwise.
 */
export function encodeSGR(code: number, col: number, row: number, release: boolean): string {
  const final = release ? "m" : "M";
  return `${ESC}[<${code};${col};${row}${final}`;
}

/** Map DOM button index to xterm button number. */
function domButtonToXterm(button: number): number {
  // DOM MouseEvent.button: 0=left, 1=middle, 2=right, 3=back, 4=forward.
  // xterm: 0/1/2 for left/middle/right; the "additional buttons" (X11 8-11,
  // reached via DOM back/forward) use the +128 extended-button encoding, so
  // back (DOM 3 = X11 8) → 128 and forward (DOM 4 = X11 9) → 129.
  if (button <= 2) {
    return button;
  }
  return 128 + (button - 3);
}

/**
 * Adapter the consumer wires into `init` so the mouse module can: send encoded
 * input bytes to the server, query current cell pixel size for hit-testing,
 * and access the terminal DOM element to attach listeners to.
 */
export interface MouseInputHandler {
  /**
   * Sends one encoded report as best-effort input. Returns whether it reached a
   * live socket; `false` means it was NOT sent and must not be retried (see
   * `connection.sendEphemeral`).
   */
  sendReport: (data: string) => boolean;
  /** Returns the current cell pixel size for hit-testing pointer coordinates. */
  cellSize: () => { width: number; height: number };
  /** Returns the terminal DOM element to attach mouse listeners to. */
  termElement: () => HTMLElement;
  /**
   * Returns the element whose box IS the grid: its top-left is cell (1,1)'s
   * origin and its bottom edge is the last row's bottom. Defaults to
   * `termElement()`, which is only the same box when that element carries no
   * padding and no reserved scrollbar gutter.
   */
  gridElement?: () => HTMLElement;
  /**
   * Returns the dimensions of the grid currently on screen (the engine
   * renderer's `gridSize`). Supplying it clamps every report into the grid and
   * anchors the row arithmetic to the grid's bottom edge; omitting it keeps the
   * top-anchored, unclamped hit test. Rows must be the RENDERED height, not the
   * height this client would like: the hit test anchors on it, so a disagreement
   * shifts every reported row rather than loosening a bound.
   */
  gridSize?: () => { cols: number; rows: number };
}

let handler: MouseInputHandler | null = null;
// True while a Shift-initiated press is in flight. Shift+click/drag is the
// xterm convention for "bypass application mouse tracking": the gesture is
// reserved for the browser's native text selection instead of being reported
// to (and preventDefault-ed away from) the TUI. Tracked from press to release
// so the move/up of a bypassed drag stay bypassed even if Shift is lifted
// mid-drag.
let shiftBypass = false;
// Last motion report sent, for same-cell dedup: a drag within one cell fires
// many DOM mousemove events that would all encode to the identical SGR
// sequence and flood the PTY (xterm.js applies the same suppression). Cleared
// on press/release so the first motion of a new gesture always reports.
let lastMotion = "";
// The element the current init() attached to, so a re-init or dispose can
// detach the exact listener set (addEventListener dedups identical
// registrations on the SAME element, but a re-init on a NEW element would
// otherwise leak the old one's listeners).
let attachedEl: HTMLElement | null = null;
// One warning per wiring, not per event: a non-positive cell size makes every
// report vanish, which is indistinguishable from mouse-tracking being off, and
// that silence is why a consumer wiring cellSize to a guess went unnoticed.
let warnedDegenerateCell = false;
// The same silence for the other divisor, and it is worse: an empty grid clamps
// every coordinate to 0, so instead of vanishing the report becomes a confident
// (1,1) for every pointer position.
let warnedDegenerateGrid = false;
// The buttons whose PRESS was actually delivered, with the coordinates that
// press reported. This is what the APPLICATION believes is held, so it also
// decides which reports are truthful: a release is emitted only for a recorded
// button, and a motion report claims a held button only for a recorded one.
const pressed = new Map<number, { col: number; row: number }>();
// DOM MouseEvent.button index -> its bit in MouseEvent.buttons. Middle and right
// are SWAPPED between the two (button 1 = middle = bit 4; button 2 = right =
// bit 2), so `1 << button` is wrong.
const BUTTONS_BIT = [1, 4, 2, 8, 16] as const;
// The freshest `buttons` bitmask any mouse event gave this element, which is the
// only reading of present button state the DOM offers outside an event.
let lastButtons = 0;
// The motion report waiting for the next animation frame, and the frame handle
// that will flush it. Motion coalesces to one report per frame, latest wins.
let pendingMotion: string | null = null;
let motionFrame: number | null = null;

// The one predicate every read and every write of `pressed` is gated on: the
// application is asking for SGR-encoded reports right now. A record written
// under tracking outlives the mode that created it, and an SGR release written
// once tracking is off lands in the PTY as ordinary keystrokes.
function trackingActive(): boolean {
  return getMouseMode() !== 0 && (isMouseSGR() || isMousePixels());
}

// Forget the held-gesture record and the button reading it is judged against.
function clearGesture(): void {
  pressed.clear();
  lastButtons = 0;
}

// True when the application is not tracking, and the shared gate for every
// handler and for resyncGesture. Forgetting the record here is what closes the
// stranded-gesture sequence: an SGR release written while nothing asked for one
// reaches the PTY as keystrokes, and carrying the record into a later re-enable
// would describe a press that program never saw. tmux answers the same way at
// attach, disabling every mouse mode and clearing mouse_drag_flag with it
// (tty.c, tty_start_tty).
function trackingOff(): boolean {
  if (trackingActive()) {
    return false;
  }
  clearGesture();
  return true;
}

// Drop the pending motion report and disarm its frame. A press, release or wheel
// CANCELS rather than flushes: the button event carries a newer position, so
// ordering is correct by construction and the superseded motion is exactly what
// coalescing is entitled to drop.
function cancelPendingMotion(): void {
  pendingMotion = null;
  if (motionFrame !== null) {
    cancelAnimationFrame(motionFrame);
    motionFrame = null;
  }
}

function flushMotion(): void {
  motionFrame = null;
  const seq = pendingMotion;
  pendingMotion = null;
  if (seq === null || !handler) {
    return;
  }
  handler.sendReport(seq);
}

function detach(): void {
  if (!attachedEl) {
    return;
  }
  attachedEl.removeEventListener("mousedown", onMouseDown);
  attachedEl.removeEventListener("mouseup", onMouseUp);
  attachedEl.removeEventListener("mousemove", onMouseMove);
  attachedEl.removeEventListener("wheel", onWheel);
  attachedEl = null;
}

/**
 * Initialize the mouse module by attaching pointer/wheel listeners to the
 * terminal element. Listeners gate on the active mouse mode + SGR 1006
 * encoding; they are no-ops when the server hasn't enabled mouse tracking.
 *
 * Returns an idempotent disposer that detaches the listeners and resets the
 * module state, so a consumer can tear the terminal down (or re-init on a
 * different element) without leaking listeners. Re-initializing without
 * disposing first detaches the previous element automatically.
 */
export function init(h: MouseInputHandler): () => void {
  detach(); // self-heal a re-init on a different element
  handler = h;
  shiftBypass = false;
  lastMotion = "";
  warnedDegenerateCell = false;
  warnedDegenerateGrid = false;
  clearGesture();
  cancelPendingMotion();
  const el = h.termElement();
  attachedEl = el;
  el.addEventListener("mousedown", onMouseDown);
  el.addEventListener("mouseup", onMouseUp);
  el.addEventListener("mousemove", onMouseMove);
  el.addEventListener("wheel", onWheel, { passive: false });
  return function dispose(): void {
    if (attachedEl !== el) {
      return; // superseded by a later init — nothing of ours left to detach
    }
    detach();
    handler = null;
    shiftBypass = false;
    lastMotion = "";
    clearGesture();
    cancelPendingMotion();
  };
}

/**
 * resyncGesture cancels a gesture left in flight, called once the resume has
 * declared the server's capabilities (the resumeAck, not the socket's open: the
 * ephemeral channel is not yet known at open, so a release sent there would take
 * the reliable fallback and become the one replayable mouse report). A button the
 * server believes is still held is a stuck drag for the rest of the session. For
 * each recorded button the last reading says is now UP it emits one release at
 * that press's coordinates; a button still down is left alone, because it
 * genuinely is.
 *
 * This reports PRESENT state, which is what RFB and Guacamole do implicitly with
 * their next message, not a replay of the missed release. The synthesis is OUR
 * JUDGEMENT — no reference addresses a producer-side resync (tmux resets its own
 * gesture model at attach, but tmux is the CONSUMER and can) — chosen over
 * emitting nothing and letting the next real click unstick the application. Its
 * bound: the reading is the freshest the browser gave THIS element, so a release
 * that happened off-element is invisible either way.
 */
export function resyncGesture(): void {
  if (!handler) {
    return;
  }
  cancelPendingMotion();
  if (trackingOff()) {
    return;
  }
  for (const [button, pos] of [...pressed]) {
    if ((lastButtons & (BUTTONS_BIT[button] ?? 0)) !== 0) {
      continue; // still held
    }
    const code = buttonCode(domButtonToXterm(button), false, false, false, false);
    if (handler.sendReport(encodeSGR(code, pos.col, pos.row, true))) {
      pressed.delete(button); // only a DELIVERED release ends the gesture, as in onMouseUp
    }
  }
}

/**
 * disarmGesture forgets the held gesture without reporting anything, for a
 * consumer pointing one terminal's socket at a different session.
 *
 * The record says what the OUTGOING session's application believes is held, and
 * this module is per terminal while sessions multiplex over it, so a release
 * surviving the switch would reach a session that never saw the press — the
 * desync `pressed` exists to prevent, arriving through resyncGesture. Nothing is
 * emitted instead: the same treatment the consumer gives every other latched
 * input class on a switch.
 */
export function disarmGesture(): void {
  cancelPendingMotion();
  clearGesture();
}

// pixelToCell returns the coordinate pair to report for a mouse event. Normally
// that's the 1-based cell column/row; under DEC 1016 (SGR-pixels) it's instead
// the 1-based pixel offset within the terminal element (same SGR grammar, so
// encodeSGR is reused unchanged). The {col,row} field names carry whichever the
// active mode reports.
function pixelToCell(e: MouseEvent): { col: number; row: number } | null {
  const h = handler;
  if (!h) {
    return null;
  }
  const el = (h.gridElement ?? h.termElement)();
  const rect = el.getBoundingClientRect();
  const grid = h.gridSize?.() ?? null;
  if (grid !== null && (grid.cols <= 0 || grid.rows <= 0)) {
    // rows 0 is the documented pre-first-frame answer (render.gridSize): the
    // server has not described the screen yet, so refuse without spending the
    // warning latch that exists to make a genuine mis-wiring loud.
    if (grid.cols > 0 && grid.rows === 0) {
      return null;
    }
    warnUnmeasuredGrid(grid.cols, grid.rows);
    return null;
  }
  if (isMousePixels()) {
    const box = grid === null ? null : pixelBox(h, grid);
    if (box === null) {
      const px = Math.round(e.clientX - rect.left);
      const py = Math.round(e.clientY - rect.top);
      if (px < 0 || py < 0) {
        return null;
      }
      return { col: px + 1, row: py + 1 }; // 1-based pixel offsets
    }
    // Same origin as the cell branch below — the grid's BOTTOM edge — so one
    // press resolves to the same row under either encoding. Measured from the
    // box's top edge instead, a partial top row shifts every pixel report by up
    // to a cell.
    return {
      col: clampToGrid(Math.round(e.clientX - rect.left), box.width) + 1,
      row: clampToGrid(Math.round(e.clientY - (rect.bottom - box.height)), box.height) + 1,
    };
  }
  const { width, height } = h.cellSize();
  if (width <= 0 || height <= 0) {
    warnUnmeasuredCell(width, height);
    return null;
  }
  const col0 = Math.floor((e.clientX - rect.left) / width);
  if (grid === null) {
    const row0 = Math.floor((e.clientY - rect.top) / height);
    if (col0 < 0 || row0 < 0) {
      return null;
    }
    return { col: col0 + 1, row: row0 + 1 };
  }
  // The screen window is the TAIL of the content, so the grid is bottom-anchored:
  // on the primary screen a bottom-pinned view shows a PARTIAL row at the top and
  // row 0 begins below the box's top edge, which top-anchored arithmetic reports
  // one row out for.
  const row0 = grid.rows - 1 - Math.floor((rect.bottom - e.clientY) / height);
  return { col: clampToGrid(col0, grid.cols) + 1, row: clampToGrid(row0, grid.rows) + 1 };
}

/** Clamp a 0-based grid coordinate into `0..limit-1`, and never below 0. */
function clampToGrid(value: number, limit: number): number {
  return Math.max(0, Math.min(value, limit - 1));
}

// The grid's own box in pixels, or null while the cell is unmeasured — in which
// case a pixel report goes out UNCLAMPED rather than being withheld: the offset
// is still the right offset, only its bound is unknown.
function pixelBox(
  h: MouseInputHandler,
  grid: { cols: number; rows: number },
): { width: number; height: number } | null {
  const { width, height } = h.cellSize();
  if (width <= 0 || height <= 0) {
    return null;
  }
  return { width: grid.cols * width, height: grid.rows * height };
}

function warnUnmeasuredCell(width: number, height: number): void {
  if (warnedDegenerateCell) {
    return;
  }
  warnedDegenerateCell = true;
  console.warn(
    `vterm: mouse reports suppressed, cellSize() answered ${String(width)}x${String(height)}`,
  );
}

function warnUnmeasuredGrid(cols: number, rows: number): void {
  if (warnedDegenerateGrid) {
    return;
  }
  warnedDegenerateGrid = true;
  console.warn(
    `vterm: mouse reports suppressed, gridSize() answered ${String(cols)}x${String(rows)}`,
  );
}

function onMouseDown(e: MouseEvent): void {
  lastButtons = e.buttons;
  if (trackingOff() || !handler) {
    return;
  }
  if (e.shiftKey) {
    // Shift+press: leave the whole gesture to the browser (native selection).
    shiftBypass = true;
    return;
  }
  shiftBypass = false;
  lastMotion = ""; // new gesture: next motion always reports
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  cancelPendingMotion();
  const code = buttonCode(domButtonToXterm(e.button), false, e.shiftKey, e.altKey, e.ctrlKey);
  if (handler.sendReport(encodeSGR(code, pos.col, pos.row, false))) {
    // Only a DELIVERED press starts a gesture, so onMouseUp cannot report a
    // release for a press the application never saw.
    pressed.set(e.button, pos);
  }
  e.preventDefault();
}

function onMouseUp(e: MouseEvent): void {
  lastButtons = e.buttons;
  if (trackingOff() || !handler) {
    return;
  }
  const bypassed = shiftBypass;
  shiftBypass = false;
  cancelPendingMotion();
  const pos = pixelToCell(e);
  if (!pos) {
    // The record SURVIVES a release that could not be reported. `pressed` is what
    // the application believes is held, and it was told nothing here, so
    // resyncGesture must still find the button and cancel it on the next open.
    return;
  }
  if (pressed.has(e.button)) {
    const btn = domButtonToXterm(e.button);
    const code = buttonCode(btn, false, e.shiftKey, e.altKey, e.ctrlKey);
    if (handler.sendReport(encodeSGR(code, pos.col, pos.row, true))) {
      pressed.delete(e.button); // only a DELIVERED release ends the gesture
    }
  }
  if (bypassed) {
    // The Shift-initiated gesture is the browser's (native selection), so its
    // own default is left alone; a button recorded BEFORE the bypass began
    // belongs to a delivered gesture and is still owed the release above.
    return;
  }
  lastMotion = ""; // gesture over: next motion always reports
  e.preventDefault();
}

// The button to report as held: the highest-priority DOM button that is both
// down now and recorded as delivered, or null when the application believes none
// is. Priority matches the DOM's own left/middle/right order.
function heldButton(buttons: number): number | null {
  for (const button of [0, 1, 2]) {
    if ((buttons & (BUTTONS_BIT[button] ?? 0)) !== 0 && pressed.has(button)) {
      return button;
    }
  }
  return null;
}

function onMouseMove(e: MouseEvent): void {
  lastButtons = e.buttons;
  if (trackingOff() || !handler) {
    return;
  }
  const mode = getMouseMode();
  if (shiftBypass && e.buttons) {
    return; // a Shift-initiated drag is a native selection; don't report it
  }
  // mode 1000: no motion events
  // mode 1002: motion only while button held (drag)
  // mode 1003: all motion events
  if (mode === 1000) {
    return;
  }
  // A button the application never saw pressed is not held for reporting
  // purposes: a drag whose press was refused (or began off the element) would
  // otherwise start a drag the pairing rule above then refuses to end.
  const held = heldButton(e.buttons);
  if (mode === 1002 && held === null) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  // No button held (bare motion in mode 1003) reports xterm's "no button"
  // code 3, not 0 — code 0 would be read as a left-button drag during hover.
  const code = buttonCode(held ?? 3, true, e.shiftKey, e.altKey, e.ctrlKey);
  const seq = encodeSGR(code, pos.col, pos.row, false);
  if (seq === lastMotion) {
    return; // same cell, same buttons/modifiers: suppress the duplicate report
  }
  lastMotion = seq;
  // Coalesce MOTION to one report per animation frame, latest wins. Under DEC
  // 1016 the cell dedup above does nothing, so pixel mode would otherwise emit
  // one report per DOM mousemove.
  pendingMotion = seq;
  motionFrame ??= requestAnimationFrame(flushMotion);
}

function onWheel(e: WheelEvent): void {
  if (trackingOff() || !handler) {
    return;
  }
  const pos = pixelToCell(e);
  if (!pos) {
    return;
  }
  cancelPendingMotion();
  // Wheel up = button 64, wheel down = button 65. A gesture with NO vertical
  // component — a horizontal trackpad swipe, or shift-wheel where the browser
  // routes the delta to deltaX — has no wheel button to report: xterm.js's
  // reference encoder returns before encoding when the vertical delta is zero,
  // while its wheel handler still calls preventDefault unconditionally. So
  // parity is "report nothing, still swallow the gesture": a terminal owns the
  // wheel, and letting a sideways swipe through would scroll the page instead.
  // Reporting 65 here (the pre-fix behavior, `deltaY < 0 ? 64 : 65` with no
  // zero case) scrolled the TUI DOWN on a sideways gesture.
  //
  // A wheel report is never coalesced: it is a NOTCH COUNT against a screen, so
  // dropping one loses scroll distance — which is why xterm.js expands a single
  // WheelEvent into N discrete reports.
  if (e.deltaY !== 0) {
    const btn = e.deltaY < 0 ? 64 : 65;
    const code = buttonCode(btn, false, e.shiftKey, e.altKey, e.ctrlKey);
    handler.sendReport(encodeSGR(code, pos.col, pos.row, false));
  }
  e.preventDefault();
}
