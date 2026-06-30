// @vitest-environment happy-dom

import { describe, it, expect, beforeEach } from "vitest";
import { encodeSGR, init as initMouse, type MouseInputHandler } from "./mouse.js";
import * as modes from "./modes.js";

beforeEach(() => {
  modes.setModes(true, false, true, true, 1003);
});

describe("mouse: SGR encoding", () => {
  it("encodes left press at (1,1)", () => {
    // button 0, col 1, row 1, press
    expect(encodeSGR(0, 1, 1, false)).toBe("\x1b[<0;1;1M");
  });
  it("encodes left release at (5,10)", () => {
    expect(encodeSGR(0, 5, 10, true)).toBe("\x1b[<0;5;10m");
  });
  it("encodes right press at (80,24)", () => {
    // button 2
    expect(encodeSGR(2, 80, 24, false)).toBe("\x1b[<2;80;24M");
  });
  it("encodes middle press with ctrl at (3,7)", () => {
    // button 1 + ctrl(16) = 17
    expect(encodeSGR(17, 3, 7, false)).toBe("\x1b[<17;3;7M");
  });
  it("encodes drag (motion) left button at (10,5)", () => {
    // button 0 + motion(32) = 32
    expect(encodeSGR(32, 10, 5, false)).toBe("\x1b[<32;10;5M");
  });
  it("encodes wheel up at (15,3)", () => {
    // wheel up = 64
    expect(encodeSGR(64, 15, 3, false)).toBe("\x1b[<64;15;3M");
  });
  it("encodes wheel down at (15,3)", () => {
    // wheel down = 65
    expect(encodeSGR(65, 15, 3, false)).toBe("\x1b[<65;15;3M");
  });
  it("encodes shift+left press", () => {
    // button 0 + shift(4) = 4
    expect(encodeSGR(4, 2, 2, false)).toBe("\x1b[<4;2;2M");
  });
});

describe("mouse: focus reporting (DEC 1004)", () => {
  function setup(): { term: HTMLDivElement; sent: string[] } {
    const term = document.createElement("div");
    const sent: string[] = [];
    const handler: MouseInputHandler = {
      send: (data) => sent.push(data),
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    };
    initMouse(handler);
    return { term, sent };
  }

  it("sends ESC[I on focus in when focus reporting is enabled", () => {
    modes.setModes(true, false, true, true, 1003); // focus reporting on
    const { term, sent } = setup();
    term.dispatchEvent(new Event("focusin"));
    expect(sent).toEqual(["\x1b[I"]);
  });

  it("sends ESC[O on focus out when focus reporting is enabled", () => {
    modes.setModes(true, false, true, true, 1003);
    const { term, sent } = setup();
    term.dispatchEvent(new Event("focusout"));
    expect(sent).toEqual(["\x1b[O"]);
  });

  it("sends nothing on focus changes when focus reporting is disabled", () => {
    modes.setModes(true, false, true, false, 1003); // focus reporting off
    const { term, sent } = setup();
    term.dispatchEvent(new Event("focusin"));
    term.dispatchEvent(new Event("focusout"));
    expect(sent).toEqual([]);
  });
});

describe("mouse: pointer events emit SGR input", () => {
  // cellSize 8x16; happy-dom does no layout so getBoundingClientRect is
  // all-zero, making cell math deterministic: col = floor(clientX/8)+1,
  // row = floor(clientY/16)+1. clientX=16,clientY=32 -> col 3, row 3.
  function setup(): { term: HTMLDivElement; sent: string[] } {
    const term = document.createElement("div");
    const sent: string[] = [];
    const handler: MouseInputHandler = {
      send: (data) => sent.push(data),
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    };
    initMouse(handler);
    return { term, sent };
  }

  it("emits an SGR press sequence on mousedown when tracking is active", () => {
    modes.setModes(true, false, true, true, 1003); // SGR on, mouse mode 1003
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0 }));
    expect(sent).toEqual(["\x1b[<0;3;3M"]);
  });

  it("emits an SGR release sequence on mouseup", () => {
    modes.setModes(true, false, true, true, 1003);
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mouseup", { clientX: 16, clientY: 32, button: 0 }));
    expect(sent).toEqual(["\x1b[<0;3;3m"]);
  });

  it("ignores pointer events when mouse tracking is off", () => {
    modes.setModes(true, false, true, true, 0); // mouseMode 0 = tracking off
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0 }));
    expect(sent).toEqual([]);
  });

  it("ignores pointer events when SGR encoding is off", () => {
    modes.setModes(true, false, false, true, 1003); // mouseSGR off
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousedown", { clientX: 16, clientY: 32, button: 0 }));
    expect(sent).toEqual([]);
  });
});

describe("mouse: motion and wheel events", () => {
  function setup(): { term: HTMLDivElement; sent: string[] } {
    const term = document.createElement("div");
    const sent: string[] = [];
    const handler: MouseInputHandler = {
      send: (d) => sent.push(d),
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    };
    initMouse(handler);
    return { term, sent };
  }

  it("mode 1003 (any-event) emits motion with no button held", () => {
    modes.setModes(true, false, true, true, 1003);
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 0 }));
    expect(sent).toEqual(["\x1b[<32;3;3M"]);
  });

  it("mode 1000 (normal) suppresses all motion events", () => {
    modes.setModes(true, false, true, true, 1000);
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    expect(sent).toEqual([]);
  });

  it("mode 1002 (button-event) emits motion only while a button is held", () => {
    modes.setModes(true, false, true, true, 1002);
    const { term, sent } = setup();
    term.dispatchEvent(new MouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 0 }));
    expect(sent).toEqual([]);
    term.dispatchEvent(new MouseEvent("mousemove", { clientX: 16, clientY: 32, buttons: 1 }));
    expect(sent).toEqual(["\x1b[<32;3;3M"]);
  });

  it("wheel up/down emit buttons 64/65", () => {
    modes.setModes(true, false, true, true, 1000);
    const { term, sent } = setup();
    // happy-dom's WheelEvent honours deltaY but not clientX/clientY from the
    // init dict; set the coordinates explicitly so pixel->cell hit-testing runs.
    const wheel = (deltaY: number): WheelEvent => {
      const w = new WheelEvent("wheel", { deltaY });
      Object.defineProperty(w, "clientX", { value: 16, configurable: true });
      Object.defineProperty(w, "clientY", { value: 32, configurable: true });
      return w;
    };
    term.dispatchEvent(wheel(-1));
    term.dispatchEvent(wheel(1));
    expect(sent).toEqual(["\x1b[<64;3;3M", "\x1b[<65;3;3M"]);
  });
});
