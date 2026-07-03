// @vitest-environment happy-dom
//
// mapKeyboardEvent takes its mode state explicitly (design section 8), so a
// tabbed shell can map a keystroke against the active tab's modes rather than a
// process-global singleton. These tests pass ad-hoc KeyboardModes objects and
// assert the mapping follows the argument, independent of any module state.

import { describe, it, expect } from "vitest";
import { mapKeyboardEvent, type KeyboardModes, type KeyboardResult } from "./keyboard.js";

function ev(init: KeyboardEventInit & { key: string; code?: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}
function sent(result: KeyboardResult): string {
  if (result.kind !== "send") {
    throw new Error(`expected send, got ${result.kind}`);
  }
  return result.bytes;
}
const normal: KeyboardModes = {
  isApplicationCursor: () => false,
  isApplicationKeypad: () => false,
};
const appCursor: KeyboardModes = {
  isApplicationCursor: () => true,
  isApplicationKeypad: () => false,
};
const appKeypad: KeyboardModes = {
  isApplicationCursor: () => false,
  isApplicationKeypad: () => true,
};

describe("mapKeyboardEvent honors the passed modes", () => {
  it("arrow keys use CSI form under the normal-modes argument", () => {
    expect(sent(mapKeyboardEvent(ev({ key: "ArrowUp" }), normal))).toBe("\x1b[A");
  });

  it("arrow keys switch to SS3 form under the application-cursor argument", () => {
    // Same event, different modes object -> different encoding. This is the
    // per-tab behavior: the argument decides, not any shared state.
    expect(sent(mapKeyboardEvent(ev({ key: "ArrowUp" }), appCursor))).toBe("\x1bOA");
  });

  it("numpad keys stay bare under normal modes but emit SS3 under application keypad", () => {
    expect(mapKeyboardEvent(ev({ key: "1", code: "Numpad1" }), normal).kind).toBe("ignore");
    expect(sent(mapKeyboardEvent(ev({ key: "1", code: "Numpad1" }), appKeypad))).toBe("\x1bOq");
  });

  it("two modes objects passed in sequence do not bleed into each other", () => {
    // A tabbed shell alternates modes objects on switch; each call is isolated.
    expect(sent(mapKeyboardEvent(ev({ key: "ArrowLeft" }), appCursor))).toBe("\x1bOD");
    expect(sent(mapKeyboardEvent(ev({ key: "ArrowLeft" }), normal))).toBe("\x1b[D");
    expect(sent(mapKeyboardEvent(ev({ key: "ArrowLeft" }), appCursor))).toBe("\x1bOD");
  });
});
