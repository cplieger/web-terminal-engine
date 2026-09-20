// Application keypad mode (DECKPAM, ESC =). Expected sequences are transcribed
// from the "VT220-Style Function Keys" application-keypad table at
// https://invisible-island.net/xterm/ctlseqs/ctlseqs.html: each numeric-keypad
// key is SS3 (ESC O <letter>) in application mode and the bare character in
// numeric mode (DECKPNM); 0..9 -> SS3 p..y, . -> n, - -> m, + -> k, * -> j,
// / -> o, Enter -> M. Numpad keys are recognised by KeyboardEvent.code, so every
// event sets `code`. Content rephrased for licensing compliance.

import { describe, it, expect, beforeEach } from "vitest";
import { mapKeyboardEvent as mapKeyboardEventRaw, type KeyboardResult } from "./keyboard.js";
import { createModeState, POWER_ON_MODES } from "./modes.js";

const modes = createModeState();
const mapKeyboardEvent = (e: KeyboardEvent): KeyboardResult => mapKeyboardEventRaw(e, modes);

function ev(init: KeyboardEventInit & { key: string; code: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

function sent(result: KeyboardResult): string {
  if (result.kind !== "send") {
    throw new Error(`expected send, got ${result.kind}`);
  }
  return result.bytes;
}

/** Enable application keypad (DECKPAM): bracketed on, cursor normal, appKeypad on. */
function enableAppKeypad(): void {
  modes.applySnapshot({ ...POWER_ON_MODES, applicationKeypad: true });
}

beforeEach(() => {
  // Reset to defaults: application keypad OFF (numeric / DECKPNM).
  modes.applySnapshot(POWER_ON_MODES);
});

// The full VT220-style application-keypad mapping (spec transcription).
const KEYPAD: { key: string; code: string; ss3: string; label: string }[] = [
  { key: "0", code: "Numpad0", ss3: "p", label: "0" },
  { key: "1", code: "Numpad1", ss3: "q", label: "1" },
  { key: "2", code: "Numpad2", ss3: "r", label: "2" },
  { key: "3", code: "Numpad3", ss3: "s", label: "3" },
  { key: "4", code: "Numpad4", ss3: "t", label: "4" },
  { key: "5", code: "Numpad5", ss3: "u", label: "5" },
  { key: "6", code: "Numpad6", ss3: "v", label: "6" },
  { key: "7", code: "Numpad7", ss3: "w", label: "7" },
  { key: "8", code: "Numpad8", ss3: "x", label: "8" },
  { key: "9", code: "Numpad9", ss3: "y", label: "9" },
  { key: ".", code: "NumpadDecimal", ss3: "n", label: ". (decimal)" },
  { key: "-", code: "NumpadSubtract", ss3: "m", label: "- (subtract)" },
  { key: "+", code: "NumpadAdd", ss3: "k", label: "+ (add)" },
  { key: "*", code: "NumpadMultiply", ss3: "j", label: "* (multiply)" },
  { key: "/", code: "NumpadDivide", ss3: "o", label: "/ (divide)" },
];

describe("application keypad mode (DECKPAM) [spec: SS3 sequences]", () => {
  beforeEach(enableAppKeypad);

  for (const { code, key, ss3, label } of KEYPAD) {
    it(`numpad ${label} → SS3 ${ss3} (ESC O ${ss3})`, () => {
      expect(sent(mapKeyboardEvent(ev({ key, code })))).toBe(`\x1bO${ss3}`);
    });
  }

  it("Numpad Enter → SS3 M (ESC O M)", () => {
    expect(sent(mapKeyboardEvent(ev({ key: "Enter", code: "NumpadEnter" })))).toBe("\x1bOM");
  });
});

describe("numeric keypad mode (DECKPNM, default) [spec: bare character]", () => {
  // In numeric mode the digit/operator characters are produced by the DOM
  // input event, so mapKeyboardEvent defers them (kind='ignore'); only
  // Numpad Enter has a dedicated keydown byte (CR).
  for (const { code, key, label } of KEYPAD) {
    it(`numpad ${label} → ignore (character emitted by the input event)`, () => {
      expect(mapKeyboardEvent(ev({ key, code })).kind).toBe("ignore");
    });
  }

  it("Numpad Enter → CR (0x0d)", () => {
    expect(sent(mapKeyboardEvent(ev({ key: "Enter", code: "NumpadEnter" })))).toBe("\x0d");
  });
});

describe("application keypad — modifiers suppress SS3 [spec: modified keypad is not application-keypad]", () => {
  beforeEach(enableAppKeypad);

  it("Ctrl+Numpad5 does NOT emit the SS3 application-keypad sequence", () => {
    const result = mapKeyboardEvent(ev({ key: "5", code: "Numpad5", ctrlKey: true }));
    // Ctrl+numpad has no C0 mapping for '5', so it defers to the input event.
    expect(result.kind).not.toBe("send");
    expect(result.kind).toBe("ignore");
  });

  it("Meta+Numpad5 does NOT emit the SS3 application-keypad sequence", () => {
    const result = mapKeyboardEvent(ev({ key: "5", code: "Numpad5", metaKey: true }));
    if (result.kind === "send") {
      expect(result.bytes).not.toBe("\x1bOu");
    } else {
      expect(result.kind).toBe("ignore");
    }
  });
});
