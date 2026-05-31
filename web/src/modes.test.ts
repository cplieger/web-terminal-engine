// @vitest-environment happy-dom

// Tests for the PROTO-01 mode-aware behaviors of keyboard.ts +
// modes.ts: arrow keys switch between CSI and SS3 form based on
// DECCKM, and bracketed paste only wraps when ?2004h is on.

import { describe, it, expect, beforeEach } from "vitest";
import { mapKeyboardEvent, bracketTextForPaste } from "./keyboard.js";
import * as modes from "./modes.js";

function ev(init: KeyboardEventInit & { key: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

beforeEach(() => {
  // Reset to the conservative defaults each test (modes module is
  // module-singleton state). Tests opt into different modes as needed.
  modes.setModes(true /* bracketed */, false /* app cursor */);
});

describe("keyboard: arrow keys honor DECCKM", () => {
  it("default (CSI) cursor mode emits CSI", () => {
    modes.setModes(true, false);
    expect((mapKeyboardEvent(ev({ key: "ArrowUp" })) as { bytes: string }).bytes).toBe("\x1b[A");
  });
  it("application cursor mode emits SS3", () => {
    modes.setModes(true, true);
    expect((mapKeyboardEvent(ev({ key: "ArrowUp" })) as { bytes: string }).bytes).toBe("\x1bOA");
    expect((mapKeyboardEvent(ev({ key: "ArrowDown" })) as { bytes: string }).bytes).toBe("\x1bOB");
    expect((mapKeyboardEvent(ev({ key: "ArrowLeft" })) as { bytes: string }).bytes).toBe("\x1bOD");
    expect((mapKeyboardEvent(ev({ key: "ArrowRight" })) as { bytes: string }).bytes).toBe("\x1bOC");
    // Home/End in app cursor mode also switch to SS3.
    expect((mapKeyboardEvent(ev({ key: "Home" })) as { bytes: string }).bytes).toBe("\x1bOH");
    expect((mapKeyboardEvent(ev({ key: "End" })) as { bytes: string }).bytes).toBe("\x1bOF");
  });
  it("modifier-extended arrows always use CSI (no SS3 modifier form)", () => {
    modes.setModes(true, true);
    expect(
      (mapKeyboardEvent(ev({ key: "ArrowUp", ctrlKey: true })) as { bytes: string }).bytes,
    ).toBe("\x1b[1;5A");
  });
});

describe("keyboard: bracketTextForPaste honors DEC ?2004", () => {
  it("when bracketed paste is on, wraps with sentinels", () => {
    modes.setModes(true, false);
    expect(bracketTextForPaste("hello")).toBe("\x1b[200~hello\x1b[201~");
  });
  it("when bracketed paste is off, returns text unchanged", () => {
    modes.setModes(false, false);
    expect(bracketTextForPaste("hello")).toBe("hello");
  });
  it("ESC inside paste is sanitised only when bracketing", () => {
    modes.setModes(true, false);
    expect(bracketTextForPaste("a\x1b[201~b")).toBe(`\x1b[200~a\u241B[201~b\x1b[201~`);
    modes.setModes(false, false);
    expect(bracketTextForPaste("a\x1b[201~b")).toBe("a\x1b[201~b");
  });
});
