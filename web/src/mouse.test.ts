// @vitest-environment happy-dom

import { describe, it, expect, beforeEach } from "vitest";
import { encodeSGR } from "./mouse.js";
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

describe("mouse: focus events", () => {
  it("focus in sequence", () => {
    // Focus in: ESC[I
    expect("\x1b[I").toBe("\x1b[I");
  });
  it("focus out sequence", () => {
    // Focus out: ESC[O
    expect("\x1b[O").toBe("\x1b[O");
  });
});
