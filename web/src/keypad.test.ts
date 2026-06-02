// @vitest-environment happy-dom

// Tests for application keypad mode (DECKPAM/DECKPNM).
// When AppKeypad is active, numpad keys send SS3 (ESC O <letter>) sequences.

import { describe, it, expect, beforeEach } from "vitest";
import { mapKeyboardEvent, type KeyboardResult } from "./keyboard.js";
import * as modes from "./modes.js";

function ev(init: KeyboardEventInit & { key: string; code: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

function send(result: KeyboardResult): string {
  if (result.kind !== "send") {
    throw new Error(`expected send, got ${result.kind}`);
  }
  return result.bytes;
}

beforeEach(() => {
  modes.setModes(true, false, false, false, 0, false);
});

describe("application keypad mode (DECKPAM)", () => {
  it("numpad digits send SS3 codes when app keypad active", () => {
    modes.setModes(true, false, false, false, 0, true);
    expect(send(mapKeyboardEvent(ev({ key: "0", code: "Numpad0" })))).toBe("\x1bOp");
    expect(send(mapKeyboardEvent(ev({ key: "1", code: "Numpad1" })))).toBe("\x1bOq");
    expect(send(mapKeyboardEvent(ev({ key: "2", code: "Numpad2" })))).toBe("\x1bOr");
    expect(send(mapKeyboardEvent(ev({ key: "3", code: "Numpad3" })))).toBe("\x1bOs");
    expect(send(mapKeyboardEvent(ev({ key: "4", code: "Numpad4" })))).toBe("\x1bOt");
    expect(send(mapKeyboardEvent(ev({ key: "5", code: "Numpad5" })))).toBe("\x1bOu");
    expect(send(mapKeyboardEvent(ev({ key: "6", code: "Numpad6" })))).toBe("\x1bOv");
    expect(send(mapKeyboardEvent(ev({ key: "7", code: "Numpad7" })))).toBe("\x1bOw");
    expect(send(mapKeyboardEvent(ev({ key: "8", code: "Numpad8" })))).toBe("\x1bOx");
    expect(send(mapKeyboardEvent(ev({ key: "9", code: "Numpad9" })))).toBe("\x1bOy");
  });

  it("numpad operators send SS3 codes when app keypad active", () => {
    modes.setModes(true, false, false, false, 0, true);
    expect(send(mapKeyboardEvent(ev({ key: ".", code: "NumpadDecimal" })))).toBe("\x1bOn");
    expect(send(mapKeyboardEvent(ev({ key: "-", code: "NumpadSubtract" })))).toBe("\x1bOm");
    expect(send(mapKeyboardEvent(ev({ key: "+", code: "NumpadAdd" })))).toBe("\x1bOk");
    expect(send(mapKeyboardEvent(ev({ key: "*", code: "NumpadMultiply" })))).toBe("\x1bOj");
    expect(send(mapKeyboardEvent(ev({ key: "/", code: "NumpadDivide" })))).toBe("\x1bOo");
  });

  it("numpad Enter sends SS3 M when app keypad active", () => {
    modes.setModes(true, false, false, false, 0, true);
    expect(send(mapKeyboardEvent(ev({ key: "Enter", code: "NumpadEnter" })))).toBe("\x1bOM");
  });

  it("numpad keys send normal chars when app keypad inactive", () => {
    modes.setModes(true, false, false, false, 0, false);
    // Numpad digits should be ignored (deferred to input event)
    expect(mapKeyboardEvent(ev({ key: "5", code: "Numpad5" })).kind).toBe("ignore");
    // Numpad Enter should send normal CR
    expect(send(mapKeyboardEvent(ev({ key: "Enter", code: "NumpadEnter" })))).toBe("\r");
  });

  it("numpad keys with modifiers bypass app keypad", () => {
    modes.setModes(true, false, false, false, 0, true);
    // Ctrl+numpad should not send SS3 (modifiers suppress app keypad)
    const result = mapKeyboardEvent(ev({ key: "5", code: "Numpad5", ctrlKey: true }));
    expect(result.kind).not.toBe("send");
  });
});
