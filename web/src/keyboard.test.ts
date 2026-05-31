// @vitest-environment happy-dom

// Unit tests for keyboard.ts. Locks down xterm.js-parity sequences so
// regressions surface here rather than in interactive use. Coverage:
//   - cursor keys, Home/End, Insert/Delete, PageUp/PageDown
//   - F1-F12 (SS3 form for F1-F4, CSI tilde for F5+)
//   - modifier-extended forms
//   - Ctrl+letter, Ctrl+symbol → C0
//   - Alt+printable → ESC + char (meta prefix)
//   - Backspace variants (plain, Alt, Ctrl)
//   - Shift+PageUp/PageDown → local scroll
//   - Bracketed paste and CR/LF normalisation

import { describe, it, expect, beforeEach } from "vitest";
import {
  mapKeyboardEvent,
  bracketTextForPaste,
  prepareTextForTerminal,
  type KeyboardResult,
} from "./keyboard.js";
import * as modes from "./modes.js";

function ev(init: KeyboardEventInit & { key: string; code?: string }): KeyboardEvent {
  return new KeyboardEvent("keydown", init);
}

function send(result: KeyboardResult): string {
  if (result.kind !== "send") {
    throw new Error(`expected send, got ${result.kind}`);
  }
  return result.bytes;
}

beforeEach(() => {
  // modes.ts is module-singleton state shared across test files
  // (vitest config has isolate:false). Reset to defaults so tests
  // don't depend on file ordering.
  modes.setModes(true /* bracketed */, false /* app cursor */);
});

describe("mapKeyboardEvent: cursor keys", () => {
  it("plain arrows send CSI form", () => {
    expect(send(mapKeyboardEvent(ev({ key: "ArrowUp" })))).toBe("\x1b[A");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowDown" })))).toBe("\x1b[B");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowRight" })))).toBe("\x1b[C");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowLeft" })))).toBe("\x1b[D");
  });

  it("modifier-extended arrows send CSI 1;mod;letter", () => {
    expect(send(mapKeyboardEvent(ev({ key: "ArrowRight", ctrlKey: true })))).toBe("\x1b[1;5C");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowLeft", shiftKey: true })))).toBe("\x1b[1;2D");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowUp", altKey: true })))).toBe("\x1b[1;3A");
    expect(send(mapKeyboardEvent(ev({ key: "ArrowDown", ctrlKey: true, shiftKey: true })))).toBe(
      "\x1b[1;6B",
    );
  });
});

describe("mapKeyboardEvent: Home/End/Insert/Delete/PageUp/PageDown", () => {
  it("send the canonical CSI sequences", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Home" })))).toBe("\x1b[H");
    expect(send(mapKeyboardEvent(ev({ key: "End" })))).toBe("\x1b[F");
    expect(send(mapKeyboardEvent(ev({ key: "Insert" })))).toBe("\x1b[2~");
    expect(send(mapKeyboardEvent(ev({ key: "Delete" })))).toBe("\x1b[3~");
    expect(send(mapKeyboardEvent(ev({ key: "PageUp" })))).toBe("\x1b[5~");
    expect(send(mapKeyboardEvent(ev({ key: "PageDown" })))).toBe("\x1b[6~");
  });

  it("Shift+PageUp/PageDown route to local scroll", () => {
    expect(mapKeyboardEvent(ev({ key: "PageUp", shiftKey: true })).kind).toBe("scroll-up");
    expect(mapKeyboardEvent(ev({ key: "PageDown", shiftKey: true })).kind).toBe("scroll-down");
  });

  it("Ctrl+Delete sends modifier-extended tilde", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Delete", ctrlKey: true })))).toBe("\x1b[3;5~");
  });
});

describe("mapKeyboardEvent: function keys", () => {
  it("F1-F4 use SS3 form", () => {
    expect(send(mapKeyboardEvent(ev({ key: "F1" })))).toBe("\x1bOP");
    expect(send(mapKeyboardEvent(ev({ key: "F2" })))).toBe("\x1bOQ");
    expect(send(mapKeyboardEvent(ev({ key: "F3" })))).toBe("\x1bOR");
    expect(send(mapKeyboardEvent(ev({ key: "F4" })))).toBe("\x1bOS");
  });

  it("F5-F12 use CSI tilde form", () => {
    expect(send(mapKeyboardEvent(ev({ key: "F5" })))).toBe("\x1b[15~");
    expect(send(mapKeyboardEvent(ev({ key: "F12" })))).toBe("\x1b[24~");
  });

  it("modifier-extended F-keys", () => {
    expect(send(mapKeyboardEvent(ev({ key: "F1", ctrlKey: true })))).toBe("\x1b[1;5P");
    expect(send(mapKeyboardEvent(ev({ key: "F5", shiftKey: true })))).toBe("\x1b[15;2~");
  });
});

describe("mapKeyboardEvent: control characters", () => {
  it("Ctrl+letter → ASCII 1-26", () => {
    expect(send(mapKeyboardEvent(ev({ key: "a", ctrlKey: true })))).toBe("\x01");
    expect(send(mapKeyboardEvent(ev({ key: "C", ctrlKey: true })))).toBe("\x03"); // capital still maps
    expect(send(mapKeyboardEvent(ev({ key: "z", ctrlKey: true })))).toBe("\x1a");
  });

  it("Ctrl+symbol → C0 controls", () => {
    expect(send(mapKeyboardEvent(ev({ key: "@", ctrlKey: true })))).toBe("\x00");
    expect(send(mapKeyboardEvent(ev({ key: "[", ctrlKey: true })))).toBe("\x1b");
    expect(send(mapKeyboardEvent(ev({ key: "\\", ctrlKey: true })))).toBe("\x1c");
    expect(send(mapKeyboardEvent(ev({ key: "_", ctrlKey: true })))).toBe("\x1f");
  });

  it("Ctrl+Space → NUL", () => {
    expect(send(mapKeyboardEvent(ev({ key: " ", ctrlKey: true })))).toBe("\x00");
  });
});

describe("mapKeyboardEvent: meta prefix (Alt)", () => {
  it("Alt+letter → ESC + letter", () => {
    expect(send(mapKeyboardEvent(ev({ key: "a", altKey: true })))).toBe("\x1ba");
    expect(send(mapKeyboardEvent(ev({ key: "f", altKey: true })))).toBe("\x1bf");
  });

  it("Alt+Backspace → ESC + DEL", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Backspace", altKey: true })))).toBe("\x1b\x7f");
  });

  it("Alt+Escape → ESC ESC", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Escape", altKey: true })))).toBe("\x1b\x1b");
  });

  it("Alt+Enter → ESC + CR", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Enter", altKey: true })))).toBe("\x1b\r");
  });
});

describe("mapKeyboardEvent: special keys", () => {
  it("Backspace plain sends DEL", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Backspace" })))).toBe("\x7f");
  });
  it("Ctrl+Backspace sends BS", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Backspace", ctrlKey: true })))).toBe("\b");
  });
  it("Tab sends \\t; Shift+Tab sends CSI Z", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Tab" })))).toBe("\t");
    expect(send(mapKeyboardEvent(ev({ key: "Tab", shiftKey: true })))).toBe("\x1b[Z");
  });
  it("Enter plain sends CR", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Enter" })))).toBe("\r");
  });
  it("Escape plain sends ESC", () => {
    expect(send(mapKeyboardEvent(ev({ key: "Escape" })))).toBe("\x1b");
  });
});

describe("mapKeyboardEvent: ignore paths", () => {
  it("modifier-only keys ignored", () => {
    expect(mapKeyboardEvent(ev({ key: "Shift" })).kind).toBe("ignore");
    expect(mapKeyboardEvent(ev({ key: "Control" })).kind).toBe("ignore");
    expect(mapKeyboardEvent(ev({ key: "Alt" })).kind).toBe("ignore");
    expect(mapKeyboardEvent(ev({ key: "Meta" })).kind).toBe("ignore");
  });

  it("plain printable defers to input event", () => {
    expect(mapKeyboardEvent(ev({ key: "a" })).kind).toBe("ignore");
    expect(mapKeyboardEvent(ev({ key: "1" })).kind).toBe("ignore");
    expect(mapKeyboardEvent(ev({ key: " " })).kind).toBe("ignore");
  });
});

describe("bracketed paste", () => {
  it("wraps with sentinels and sanitises ESC", () => {
    expect(bracketTextForPaste("hello")).toBe("\x1b[200~hello\x1b[201~");
    expect(bracketTextForPaste("a\x1b[201~b")).toBe(`\x1b[200~a\u241B[201~b\x1b[201~`);
  });

  it("normalises CR/LF to CR", () => {
    expect(prepareTextForTerminal("a\r\nb\nc\r")).toBe("a\rb\rc\r");
  });
});
