// SPEC-FIRST mouse input-encoding tests.
//
// Expected byte sequences here are derived from the xterm mouse-tracking
// SPECIFICATION, not from reading mouse.ts. A failing/skip'd case is a real
// deviation from xterm, not something to massage green.
//
// Spec source (verified 2026):
//   xterm "Control Sequences" — section "Mouse Tracking"
//   https://invisible-island.net/xterm/ctlseqs/ctlseqs.html
// Cross-checked against the xterm.js reference encoder
//   (src/common/services/MouseStateService.ts `eventCode`/`SGR`), which
//   mouse.ts's own doc-comment claims to match.
//
// Button byte (event code), per spec:
//   low 2 bits: 0=MB1(left) 1=MB2(middle) 2=MB3(right) 3=none/release
//   +4 Shift, +8 Meta, +16 Control  ("added together")
//   +32 motion (drag/move)
//   wheel: button 4/5 -> base 0/1 with +64  => up=64, down=65
// SGR (1006): press/move = CSI < Pb ; Px ; Py M ; release = ...m .
//   Px/Py are 1-based (upper-left cell = 1,1); the button value is NOT
//   offset by +32 (that was an X10-only printable-byte trick); a distinct
//   final char (m) disambiguates which button was released.
//
// DEVIATIONS found (see it.skip blocks at the bottom and the report). The
// [bug] ones have since been FIXED in mouse.ts; their cases are promoted to
// live tests above and state the spec value they now assert:
//   D1 [bug→fixed] any-event (1003) motion with no button -> spec 35, was 32
//   D2 [bug→fixed] aux button (DOM button >=3) -> spec 128/129 (the +128
//                extended-button bit for X11 8/9), was 0 (left)
//   D3 [choice]  DOM metaKey (Cmd/Win) -> spec Meta bit +8, got +0
//                (encoder derives +8 from altKey only; matches xterm.js)
//   D4 [scope]   normal tracking (1000) w/o SGR -> spec legacy CSI M report,
//                got nothing (encoder is SGR-1006-only by design)
//   D5 [bug→fixed] wheel with no vertical delta (horizontal gesture) -> spec
//                no report, was b=65 (wheel-down). preventDefault stays
//                unconditional, which is also what xterm.js's wheel handler
//                does: report nothing, still swallow the gesture.
//
// NOT COVERED HERE any more: DEC 1004 focus reporting. Focus is transport state
// the server derives (attachment + every client's reported focus + its own
// keep-unfocused declaration), so it moved to `connection.setClientFocus` and
// its tests to connection-ephemeral.test.ts.

import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  encodeSGR,
  init as initMouse,
  resyncGesture,
  disarmGesture,
  type MouseInputHandler,
} from "./mouse.js";
import * as modes from "./modes.js";

const ESC = "\x1b";

// Spec modifier/motion bit weights (xterm ctlseqs, "Mouse Tracking").
const SHIFT = 4;
const META = 8; // Meta/Alt
const CTRL = 16;
const MOTION = 32;

// Spec SGR-1006 wire grammar: CSI < Pb ; Px ; Py <final>, final = M press/move, m release.
// Independent oracle — mirrors the spec grammar, not encodeSGR's body.
const expectedSGR = (b: number, col: number, row: number, release: boolean): string =>
  `${ESC}[<${b};${col};${row}${release ? "m" : "M"}`;

beforeEach(() => {
  // modes is a module singleton (vitest isolate:false). Reset every flag —
  // notably the 8th arg (mousePixels) — so nothing leaks between tests.
  modes.setModes(true, false, false, false, 0, false, false, false);
});

// Motion is coalesced to one report per animation frame (press, release and
// wheel are not), so a motion assertion has to cross a frame boundary. Real
// headless Chromium, so requestAnimationFrame is real and needs no fake timer.
const nextFrame = (): Promise<void> =>
  new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });

// SGR 1006 on with the given tracking mode; focus + pixels off.
function enableSGR(mode: number): void {
  modes.setModes(true, false, true, false, mode, false, false, false);
}

// Tracking off with the SGR encoding still on: what an application leaves behind
// when it resets 1002 without resetting 1006.
function disableTracking(): void {
  modes.setModes(true, false, true, false, 0, false, false, false);
}

// The fixture element is never attached to the document, so its
// getBoundingClientRect is genuinely all-zero. With an 8x16 cell that makes
// hit-testing deterministic: col = floor(clientX/8)+1, row = floor(clientY/16)+1.
// So (clientX=16, clientY=32) -> cell (3,3). mouse-hit-testing.test.ts covers
// the non-zero-offset case with an explicit rect.
function setup(): { term: HTMLDivElement; sent: string[] } {
  const term = document.createElement("div");
  const sent: string[] = [];
  const handler: MouseInputHandler = {
    sendReport: (data) => {
      sent.push(data);
      return true;
    },
    cellSize: () => ({ width: 8, height: 16 }),
    termElement: () => term,
  };
  initMouse(handler);
  return { term, sent };
}

// Force event fields via defineProperty so the button-bit composition is
// exercised deterministically regardless of which MouseEventInit fields a given
// event constructor honours.
function patch(target: object, props: Record<string, unknown>): void {
  for (const [key, value] of Object.entries(props)) {
    Object.defineProperty(target, key, { value, configurable: true });
  }
}

interface MouseOpts {
  clientX?: number;
  clientY?: number;
  button?: number;
  buttons?: number;
  shift?: boolean | undefined;
  ctrl?: boolean | undefined;
  alt?: boolean | undefined;
  meta?: boolean | undefined;
}

// Shared it.each case shapes. Modifier flags are optional (absent = false); the
// explicit `| undefined` keeps them assignable to MouseOpts/WheelOpts under
// exactOptionalPropertyTypes and stops TS inferring a field-dropping union.
interface BtnCase {
  name: string;
  button: number;
  shift?: boolean | undefined;
  ctrl?: boolean | undefined;
  alt?: boolean | undefined;
  b: number;
}
interface DragCase {
  name: string;
  button: number;
  buttons: number;
  shift?: boolean | undefined;
  ctrl?: boolean | undefined;
  alt?: boolean | undefined;
  b: number;
}
interface WheelCase {
  name: string;
  deltaY: number;
  shift?: boolean | undefined;
  ctrl?: boolean | undefined;
  alt?: boolean | undefined;
  b: number;
}

function makeMouse(type: string, opts: MouseOpts = {}): MouseEvent {
  const {
    clientX = 16,
    clientY = 32,
    button = 0,
    buttons = 0,
    shift = false,
    ctrl = false,
    alt = false,
    meta = false,
  } = opts;
  const e = new MouseEvent(type);
  patch(e, {
    clientX,
    clientY,
    button,
    buttons,
    shiftKey: shift,
    ctrlKey: ctrl,
    altKey: alt,
    metaKey: meta,
  });
  return e;
}

interface WheelOpts {
  deltaY: number;
  clientX?: number;
  clientY?: number;
  shift?: boolean | undefined;
  ctrl?: boolean | undefined;
  alt?: boolean | undefined;
}

function makeWheel(opts: WheelOpts): WheelEvent {
  const { deltaY, clientX = 16, clientY = 32, shift = false, ctrl = false, alt = false } = opts;
  const e = new WheelEvent("wheel", { deltaY });
  patch(e, { clientX, clientY, shiftKey: shift, ctrlKey: ctrl, altKey: alt });
  return e;
}

describe("encodeSGR: SGR-1006 wire grammar (spec: CSI < Pb ; Px ; Py  M|m)", () => {
  it("uses final byte M for a press/move", () => {
    expect(encodeSGR(0, 1, 1, false)).toBe(`${ESC}[<0;1;1M`);
  });

  it("uses final byte m for a release", () => {
    expect(encodeSGR(0, 1, 1, true)).toBe(`${ESC}[<0;1;1m`);
  });

  it("passes coordinates through 1-based, column before row", () => {
    // spec: upper-left = 1,1; order is Px (col) then Py (row).
    expect(encodeSGR(0, 80, 24, false)).toBe(`${ESC}[<0;80;24M`);
  });

  it("renders the button byte as decimal with no +32 X10 offset", () => {
    // SGR drops the X10 printable-byte +32; the raw event code is emitted.
    expect(encodeSGR(35, 5, 7, false)).toBe(`${ESC}[<35;5;7M`);
  });

  it("imposes no coordinate ceiling (unlike the legacy 223 cap)", () => {
    // spec: "SGR (1006) ... No encoding limitation."
    expect(encodeSGR(0, 5000, 9999, false)).toBe(`${ESC}[<0;5000;9999M`);
  });

  it("preserves the button on release (m resolves which button went up)", () => {
    expect(encodeSGR(2, 10, 10, true)).toBe(`${ESC}[<2;10;10m`);
  });
});

describe("button-byte composition via init() event path — press (SGR 1006)", () => {
  // Base button + modifier bits, summed exactly as the spec says ("added
  // together"). Coordinates fixed at cell (3,3).
  const cases: BtnCase[] = [
    { name: "left, no modifiers", button: 0, b: 0 },
    { name: "left + Ctrl", button: 0, ctrl: true, b: 0 + CTRL },
    { name: "left + Meta/Alt", button: 0, alt: true, b: 0 + META },
    { name: "left + Ctrl + Meta/Alt", button: 0, ctrl: true, alt: true, b: 0 + CTRL + META },
    { name: "middle, no modifiers", button: 1, b: 1 },
    { name: "middle + Ctrl", button: 1, ctrl: true, b: 1 + CTRL },
    { name: "right, no modifiers", button: 2, b: 2 },
    { name: "right + Ctrl", button: 2, ctrl: true, b: 2 + CTRL },
  ];

  it.each(cases)("press: $name -> b=$b", ({ button, shift, ctrl, alt, b }) => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button, shift, ctrl, alt }));
    expect(sent).toEqual([expectedSGR(b, 3, 3, false)]);
  });
});

describe("button-byte composition via init() event path — release (SGR 1006)", () => {
  const cases: BtnCase[] = [
    { name: "left", button: 0, b: 0 },
    { name: "right", button: 2, b: 2 },
    { name: "middle + Ctrl", button: 1, ctrl: true, b: 1 + CTRL },
  ];

  it.each(cases)("release: $name -> b=$b, final=m", ({ button, shift, ctrl, alt, b }) => {
    enableSGR(1002);
    const { term, sent } = setup();
    // The press is part of the fixture: a release is reported only for a press
    // that was delivered, because a release whose press never arrived is its own
    // desync — the application never learned the button went down.
    term.dispatchEvent(makeMouse("mousedown", { button }));
    term.dispatchEvent(makeMouse("mouseup", { button, shift, ctrl, alt }));
    expect(sent).toEqual([expectedSGR(button, 3, 3, false), expectedSGR(b, 3, 3, true)]);
  });
});

describe("button-byte composition via init() event path — drag/motion (SGR 1006)", () => {
  // Held button is read from the DOM `buttons` bitmask (1=left, 4=middle, 2=right).
  // Motion adds +32 to the button code. The gesture's press is part of the
  // fixture: a drag reports a held button only while the application believes that
  // button is down, which is what a delivered press establishes.
  const cases: DragCase[] = [
    { name: "left held", button: 0, buttons: 1, b: 0 + MOTION },
    { name: "middle held", button: 1, buttons: 4, b: 1 + MOTION },
    { name: "right held", button: 2, buttons: 2, b: 2 + MOTION },
    { name: "left held + Shift", button: 0, buttons: 1, shift: true, b: 0 + SHIFT + MOTION },
  ];

  it.each(cases)(
    "drag (mode 1002): $name -> b=$b",
    async ({ button, buttons, shift, ctrl, alt, b }) => {
      enableSGR(1002);
      const { term, sent } = setup();
      term.dispatchEvent(makeMouse("mousedown", { button, buttons }));
      term.dispatchEvent(makeMouse("mousemove", { buttons, shift, ctrl, alt }));
      await nextFrame();
      expect(sent.at(-1)).toBe(expectedSGR(b, 3, 3, false));
    },
  );
});

describe("Shift bypass: Shift+press reserves the gesture for native selection (xterm convention)", () => {
  it("reports nothing for a Shift-initiated press/drag/release", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, shift: true }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 1, shift: true }));
    term.dispatchEvent(makeMouse("mouseup", { button: 0, shift: true }));
    expect(sent).toEqual([]);
  });

  it("keeps the bypass through a drag even if Shift is lifted mid-gesture", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, shift: true }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 1 })); // shift already lifted
    term.dispatchEvent(makeMouse("mouseup", { button: 0 }));
    expect(sent).toEqual([]);
  });

  it("resumes normal reporting on the next non-Shift press", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, shift: true }));
    term.dispatchEvent(makeMouse("mouseup", { button: 0, shift: true }));
    term.dispatchEvent(makeMouse("mousedown", { button: 0 }));
    expect(sent).toEqual([expectedSGR(0, 3, 3, false)]);
  });
});

describe("button-byte composition via init() event path — wheel (SGR 1006)", () => {
  // Wheel up = 64 (base 0 + 64), down = 65 (base 1 + 64); modifiers still add.
  const cases: WheelCase[] = [
    { name: "up", deltaY: -1, b: 64 },
    { name: "down", deltaY: 1, b: 65 },
    { name: "up + Ctrl", deltaY: -1, ctrl: true, b: 64 + CTRL },
    { name: "up + Shift", deltaY: -1, shift: true, b: 64 + SHIFT },
    { name: "down + Meta/Alt", deltaY: 1, alt: true, b: 65 + META },
  ];

  it.each(cases)("wheel: $name -> b=$b", ({ deltaY, shift, ctrl, alt, b }) => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeWheel({ deltaY, shift, ctrl, alt }));
    expect(sent).toEqual([expectedSGR(b, 3, 3, false)]);
  });

  it("reports nothing for a gesture with no vertical delta", () => {
    // A horizontal trackpad swipe (or shift-wheel, where some browsers route
    // the delta to deltaX) delivers deltaY 0. There is no wheel button for
    // that: the xterm.js reference encoder returns before encoding when the
    // vertical delta is zero. (Was a bug: `deltaY < 0 ? 64 : 65` had no zero
    // case, so a sideways gesture reported 65 and scrolled the TUI DOWN.)
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeWheel({ deltaY: 0 }));
    expect(sent).toEqual([]);
  });
});

describe("coordinates: 1-based cell mapping (spec: upper-left cell = 1,1)", () => {
  const cases = [
    { name: "pixel (0,0) -> cell (1,1)", clientX: 0, clientY: 0, col: 1, row: 1 },
    { name: "pixel (8,16) -> cell (2,2)", clientX: 8, clientY: 16, col: 2, row: 2 },
    { name: "pixel (16,32) -> cell (3,3)", clientX: 16, clientY: 32, col: 3, row: 3 },
    // >223 columns must survive: SGR has no coordinate ceiling.
    { name: "column beyond the legacy 223 cap", clientX: 2392, clientY: 0, col: 300, row: 1 },
  ];

  it.each(cases)("$name", ({ clientX, clientY, col, row }) => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { clientX, clientY, button: 0 }));
    expect(sent).toEqual([expectedSGR(0, col, row, false)]);
  });
});

describe("mode gating (DECSET 1000/1002/1003)", () => {
  it("mode 0 (tracking off) emits nothing on press", () => {
    modes.setModes(true, false, true, false, 0, false, false, false); // SGR on, tracking OFF
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0 }));
    expect(sent).toEqual([]);
  });

  it("normal tracking (1000) reports press/release but suppresses motion", () => {
    enableSGR(1000);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousemove", { buttons: 1 })); // held drag
    expect(sent).toEqual([]);
  });

  it("button-event (1002) suppresses motion with no button held", () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    expect(sent).toEqual([]);
  });

  it("button-event (1002) reports motion while a button is held", async () => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 1 }));
    await nextFrame();
    expect(sent.at(-1)).toBe(expectedSGR(0 + MOTION, 3, 3, false));
  });

  it("any-event (1003) motion with no button → b=35 (no-button 3 + motion 32)", async () => {
    // Spec: bare hover in any-event tracking reports the "no button" code 3
    // plus the motion bit 32. (Was a bug — code 0 read as a left-drag; fixed.)
    enableSGR(1003);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    await nextFrame();
    expect(sent).toEqual([expectedSGR(3 + MOTION, 3, 3, false)]);
  });
});

// ---------------------------------------------------------------------------
// DEVIATIONS from the xterm spec. Kept as it.skip so the file stays green;
// each body asserts the SPEC-CORRECT value, so un-skipping reproduces the
// finding verbatim. Do NOT edit these expectations to pass.
// ---------------------------------------------------------------------------
// Aux mouse buttons (DOM back/forward). Spec: xterm encodes the "additional
// buttons" (X11 8-11) with the +128 extended-button bit — DOM 3 (Back = X11 8)
// → 128, DOM 4 (Forward = X11 9) → 129. (Was a bug: collapsed to 0 = left.)
describe("aux buttons → xterm extended-button codes (spec)", () => {
  const cases: BtnCase[] = [
    { name: "Back (DOM 3 = X11 8)", button: 3, b: 128 },
    { name: "Forward (DOM 4 = X11 9)", button: 4, b: 129 },
  ];
  it.each(cases)("$name → b=$b", ({ button, b }) => {
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button }));
    expect(sent).toEqual([expectedSGR(b, 3, 3, false)]);
  });
});

describe("DEVIATIONS from xterm spec (skipped — see report)", () => {
  it.skip("DEVIATION: DOM metaKey (Cmd/Win) - spec Meta bit +8, got +0", () => {
    // The spec's +8 "Meta" bit; the encoder derives it from altKey only and
    // ignores the DOM metaKey. This matches the xterm.js reference (it also
    // reads altKey, not metaKey), so it is a defensible design choice, not a
    // wire bug. [choice]
    enableSGR(1002);
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, meta: true }));
    expect(sent).toEqual([expectedSGR(0 + META, 3, 3, false)]);
  });

  it.skip("DEVIATION: normal tracking (1000) without SGR - spec legacy 'CSI M CbCxCy', got nothing", () => {
    // With mode 1000 and no extended encoding, xterm sends the legacy report
    // CSI M Cb Cx Cy where each byte is value+32: Cb=0+32=0x20, Cx=3+32=0x23,
    // Cy=3+32=0x23. mouse.ts is SGR-1006-only and emits nothing. [scope —
    // legacy X10/1005/1015 encodings are intentionally unimplemented]
    modes.setModes(true, false, false, false, 1000, false, false, false); // SGR + pixels OFF
    const { term, sent } = setup();
    term.dispatchEvent(makeMouse("mousedown", { button: 0 }));
    expect(sent).toEqual([`${ESC}[M\x20\x23\x23`]);
  });
});

describe("motion dedup: identical same-cell reports are suppressed (matches xterm.js)", () => {
  it("a drag inside one cell reports once; crossing a cell boundary reports again", async () => {
    const { term, sent } = setup();
    enableSGR(1002);
    term.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    const afterPress = sent.length;
    // Three moves inside the same 8x16 cell (cell 1,1), left button held.
    for (const x of [1, 3, 5]) {
      term.dispatchEvent(new MouseEvent("mousemove", { buttons: 1, clientX: x, clientY: 8 }));
    }
    await nextFrame();
    expect(sent.length).toBe(afterPress + 1); // one motion report, two dupes suppressed
    // Crossing into the next cell reports again.
    term.dispatchEvent(new MouseEvent("mousemove", { buttons: 1, clientX: 12, clientY: 8 }));
    await nextFrame();
    expect(sent.length).toBe(afterPress + 2);
    expect(sent.at(-1)).toBe(expectedSGR(0 + MOTION, 2, 1, false));
  });

  it("a new press resets the dedup so the first motion of the next gesture reports", async () => {
    const { term, sent } = setup();
    enableSGR(1002);
    term.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    term.dispatchEvent(new MouseEvent("mousemove", { buttons: 1, clientX: 4, clientY: 8 }));
    // The frame boundary is required: a release CANCELS a motion still pending
    // for the frame, because the button event carries a newer position.
    await nextFrame();
    term.dispatchEvent(new MouseEvent("mouseup", { button: 0, clientX: 4, clientY: 8 }));
    const afterFirstGesture = sent.length;
    term.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    term.dispatchEvent(new MouseEvent("mousemove", { buttons: 1, clientX: 4, clientY: 8 }));
    await nextFrame();
    // Same cell as the previous gesture's motion — but a new gesture must report.
    expect(sent.length).toBe(afterFirstGesture + 2); // press + motion
  });
});

describe("init returns an idempotent disposer", () => {
  it("dispose detaches the listeners so no further events report", () => {
    const term = document.createElement("div");
    const sent: string[] = [];
    const dispose = initMouse({
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    });
    enableSGR(1000);
    term.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    expect(sent.length).toBe(1);
    dispose();
    term.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    expect(sent.length).toBe(1); // detached: nothing new
    dispose(); // idempotent: second call is a no-op
    expect(sent.length).toBe(1);
  });

  it("a stale disposer from a superseded init does not detach the new element", () => {
    const sent: string[] = [];
    const handlerFor = (el: HTMLElement): MouseInputHandler => ({
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => el,
    });
    const first = document.createElement("div");
    const staleDispose = initMouse(handlerFor(first));
    const second = document.createElement("div");
    initMouse(handlerFor(second)); // supersedes; auto-detaches `first`
    enableSGR(1000);
    staleDispose(); // must NOT touch the second element's listeners
    second.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    expect(sent.length).toBe(1);
    // The superseded element was auto-detached at re-init.
    first.dispatchEvent(new MouseEvent("mousedown", { button: 0, clientX: 4, clientY: 8 }));
    expect(sent.length).toBe(1);
  });
});

// A handler whose sendReport reports a given delivery outcome, so the pairing
// rule can be exercised from both sides. `false` is what the connection returns
// when there was no live socket to send on.
function setupDelivering(delivered: boolean): { term: HTMLDivElement; sent: string[] } {
  const term = document.createElement("div");
  const sent: string[] = [];
  initMouse({
    sendReport: (data) => {
      sent.push(data);
      return delivered;
    },
    cellSize: () => ({ width: 8, height: 16 }),
    termElement: () => term,
  });
  return { term, sent };
}

// A handler whose delivery outcome can change between events, which is the shape
// of an ordinary connection blip: the press reaches a live socket and the release
// does not.
function setupToggling(): {
  term: HTMLDivElement;
  sent: string[];
  deliver: (ok: boolean) => void;
} {
  const term = document.createElement("div");
  const sent: string[] = [];
  let delivered = true;
  initMouse({
    sendReport: (data) => {
      sent.push(data);
      return delivered;
    },
    cellSize: () => ({ width: 8, height: 16 }),
    termElement: () => term,
  });
  return {
    term,
    sent,
    deliver: (ok) => {
      delivered = ok;
    },
  };
}

describe("gesture pairing: a release belongs to a press that was delivered", () => {
  it("does not report the release of a press that was refused", () => {
    // A release whose press never arrived is its own desync: the application was
    // never told the button went down, so a bare release is a lie about a
    // gesture it did not see. The refused press is still ATTEMPTED (it is in
    // `sent`); what must not follow is the release.
    enableSGR(1002);
    const { term, sent } = setupDelivering(false);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    const afterPress = sent.length;

    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));

    expect(sent.length).toBe(afterPress);
  });

  it("reports the release of a press that was delivered", () => {
    // The control for the case above: the pairing rule must not swallow the
    // release of an ordinary gesture.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));

    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));

    expect(sent).toEqual([expectedSGR(0, 3, 3, false), expectedSGR(0, 3, 3, true)]);
  });

  it("keeps the gesture when the release itself was refused", () => {
    // The likeliest trigger of all: the socket dies mid-drag, the user lets go
    // while it is down, and the release is refused. Dropping the record here is
    // what leaves the application holding the button for the rest of the session,
    // because resyncGesture reads `pressed` and would find nothing.
    enableSGR(1002);
    const { term, sent, deliver } = setupToggling();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    deliver(false);
    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));
    const afterRefusedRelease = sent.length;

    deliver(true);
    resyncGesture();

    expect(sent.slice(afterRefusedRelease)).toEqual([expectedSGR(0, 3, 3, true)]);
  });

  it("keeps the gesture when the release could not be hit-tested", () => {
    // Same permanent desync through a different door: a cell size that goes
    // degenerate between the press and the release (an unmeasured re-layout)
    // reports nothing, so the record has to survive that too.
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    enableSGR(1002);
    const term = document.createElement("div");
    const sent: string[] = [];
    let cell = { width: 8, height: 16 };
    initMouse({
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => cell,
      termElement: () => term,
    });
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    cell = { width: 0, height: 0 };
    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));
    expect(sent).toEqual([expectedSGR(0, 3, 3, false)]);

    cell = { width: 8, height: 16 };
    resyncGesture();

    expect(sent.slice(1)).toEqual([expectedSGR(0, 3, 3, true)]);
  });

  it("still reports a delivered gesture's release once a Shift-press has set the bypass", () => {
    // A plain press on one button followed by a Shift-press on another sets the
    // bypass mid-gesture. The first button's release belongs to a gesture the
    // application DID see, so it is still owed — and leaving it recorded instead
    // is the stuck drag again.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mousedown", { button: 2, buttons: 3, shift: true }));

    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 2 }));

    expect(sent).toEqual([expectedSGR(0, 3, 3, false), expectedSGR(0, 3, 3, true)]);
    const afterRelease = sent.length;
    resyncGesture(); // nothing left recorded
    expect(sent.length).toBe(afterRelease);
  });

  it("does not report a drag for a press that was refused", async () => {
    // Mode 1002 motion means "a button is held", and the application never saw
    // this one go down. Reporting it starts a drag the application cannot end,
    // because the pairing rule above then suppresses its release.
    enableSGR(1002);
    const { term, sent } = setupDelivering(false);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    const afterPress = sent.length;

    term.dispatchEvent(makeMouse("mousemove", { clientX: 32, clientY: 32, buttons: 1 }));
    await nextFrame();

    expect(sent.length).toBe(afterPress);
  });

  it("reports bare motion, not a drag, for a refused press under any-event tracking", async () => {
    // 1003 reports motion whatever the buttons are doing, so the truthful report
    // is xterm's "no button" code 3 rather than a held-button drag.
    enableSGR(1003);
    const { term, sent } = setupDelivering(false);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    const afterPress = sent.length;

    term.dispatchEvent(makeMouse("mousemove", { clientX: 32, clientY: 32, buttons: 1 }));
    await nextFrame();

    expect(sent.slice(afterPress)).toEqual([expectedSGR(3 + MOTION, 5, 3, false)]);
  });
});

describe("resyncGesture: an in-flight gesture is cancelled, not replayed", () => {
  it("synthesizes exactly one release for a button the last reading says is up", () => {
    // Reporting PRESENT state, which is what RFB and Guacamole do implicitly with
    // their next message. The modifier bits are deliberately absent: this is not
    // a replay of the release that was missed.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1, ctrl: true }));
    // A later event is the only reading of button state the DOM offers.
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    const afterPress = sent.length;

    resyncGesture();

    expect(sent.slice(afterPress)).toEqual([expectedSGR(0, 3, 3, true)]);
    resyncGesture(); // the record is cleared, so a second call adds nothing
    expect(sent.slice(afterPress)).toEqual([expectedSGR(0, 3, 3, true)]);
  });

  it("leaves a button the last reading says is still down alone", () => {
    // It genuinely is down: synthesizing a release here would end a drag the
    // user is still making.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    const afterPress = sent.length;

    resyncGesture();

    expect(sent.length).toBe(afterPress);
  });

  it("synthesizes nothing for a press that was refused", () => {
    enableSGR(1002);
    const { term, sent } = setupDelivering(false);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    const afterPress = sent.length;

    resyncGesture();

    expect(sent.length).toBe(afterPress);
  });

  it("synthesizes nothing once the application has turned tracking off", () => {
    // The release lands after the application reset 1002, so onMouseUp cannot
    // discharge the record — and an SGR release written now is not mouse input at
    // all: nothing is asking for it, so the PTY reads it as ordinary keystrokes.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    disableTracking();
    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));
    const afterPress = sent.length;

    resyncGesture();

    expect(sent.length).toBe(afterPress);
  });

  it("forgets the gesture at the release, so a re-enable before the next resync is clean", () => {
    // The sequence with no resync while tracking was off: the application resets
    // 1002, the user lets go, the application enables 1002 again, and only then
    // does the socket come back. The release is where tracking-off is observed, so
    // the record dies there; carried to the resync it would describe a press this
    // application never saw. tmux does the same at attach, clearing
    // mouse_drag_flag as it disables the modes (tty.c, tty_start_tty).
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    disableTracking();
    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));
    const afterPress = sent.length;

    enableSGR(1002);
    resyncGesture();

    expect(sent.length).toBe(afterPress);
  });

  it("keeps the gesture when its own release is refused", () => {
    // Same answer onMouseUp gives to the same question: only a DELIVERED release
    // ends the gesture. Forgetting a refused one leaves the application holding a
    // button with nothing left that could cancel it.
    enableSGR(1002);
    const { term, sent, deliver } = setupToggling();
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    const afterPress = sent.length;

    deliver(false);
    resyncGesture();
    deliver(true);
    resyncGesture();

    // Both at the PRESS's cell, which is where makeMouse's default coordinates land.
    expect(sent.slice(afterPress)).toEqual([
      expectedSGR(0, 3, 3, true),
      expectedSGR(0, 3, 3, true),
    ]);
  });
});

describe("disarmGesture: a session switch forgets the gesture", () => {
  it("reports nothing itself and leaves nothing for the next resync", () => {
    // The record belongs to the session that saw the press, and this module is per
    // TERMINAL while sessions multiplex over it. Emitting the release on the way
    // out is not available either: the switch is what makes that session
    // unreachable from here.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mousemove", { buttons: 0 }));
    const afterPress = sent.length;

    disarmGesture();
    expect(sent.length).toBe(afterPress);

    resyncGesture();
    expect(sent.length).toBe(afterPress);
  });
});

describe("coalescing: motion is one report per frame, buttons and wheel are not", () => {
  it("coalesces three moves in one frame to the LATEST report", () => {
    enableSGR(1003);
    const { term, sent } = setupDelivering(true);
    for (const clientX of [0, 8, 16]) {
      term.dispatchEvent(makeMouse("mousemove", { clientX, clientY: 0, buttons: 0 }));
    }

    // Synchronously: nothing yet. This is the assertion the pre-coalescing
    // implementation fails, and under DEC 1016 it is three reports per frame
    // because the cell dedup cannot see a pixel change.
    expect(sent).toEqual([]);
  });

  it("emits the latest coalesced motion on the next frame", async () => {
    enableSGR(1003);
    const { term, sent } = setupDelivering(true);
    for (const clientX of [0, 8, 16]) {
      term.dispatchEvent(makeMouse("mousemove", { clientX, clientY: 0, buttons: 0 }));
    }

    await nextFrame();

    expect(sent).toEqual([expectedSGR(3 + MOTION, 3, 1, false)]);
  });

  it("reports a press synchronously, before any frame boundary", () => {
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    expect(sent).toEqual([expectedSGR(0, 3, 3, false)]);
  });

  it("reports a release synchronously, before any frame boundary", () => {
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));
    term.dispatchEvent(makeMouse("mouseup", { button: 0, buttons: 0 }));
    expect(sent.at(-1)).toBe(expectedSGR(0, 3, 3, true));
  });

  it("reports every wheel notch synchronously", () => {
    // A wheel report is a NOTCH COUNT against a screen, so coalescing wheel
    // loses scroll distance — xterm.js expands one WheelEvent into N discrete
    // reports for exactly that reason.
    enableSGR(1002);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeWheel({ deltaY: -1 }));
    term.dispatchEvent(makeWheel({ deltaY: -1 }));
    term.dispatchEvent(makeWheel({ deltaY: 1 }));
    expect(sent).toEqual([
      expectedSGR(64, 3, 3, false),
      expectedSGR(64, 3, 3, false),
      expectedSGR(65, 3, 3, false),
    ]);
  });

  it("drops a motion still pending when a press supersedes it", async () => {
    // A button event CANCELS rather than flushes: it carries a newer position,
    // so order is correct by construction and the superseded motion is exactly
    // what coalescing is entitled to drop.
    enableSGR(1003);
    const { term, sent } = setupDelivering(true);
    term.dispatchEvent(makeMouse("mousemove", { clientX: 0, clientY: 0, buttons: 0 }));
    term.dispatchEvent(makeMouse("mousedown", { button: 0, buttons: 1 }));

    await nextFrame();

    expect(sent).toEqual([expectedSGR(0, 3, 3, false)]);
  });
});
