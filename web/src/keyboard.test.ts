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

import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  bindMobileToolbar,
  bracketTextForPaste,
  ctrlByteFor,
  mapKeyboardEvent,
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

describe("ctrlByteFor", () => {
  it("a-z (case-folded) → \\x01..\\x1a", () => {
    // Spot-check the boundaries + a few interior cases, both cases.
    expect(ctrlByteFor("a")).toBe("\x01");
    expect(ctrlByteFor("A")).toBe("\x01");
    expect(ctrlByteFor("c")).toBe("\x03");
    expect(ctrlByteFor("C")).toBe("\x03");
    expect(ctrlByteFor("z")).toBe("\x1a");
    expect(ctrlByteFor("Z")).toBe("\x1a");
    // Full sweep: every letter in the table.
    const letters = "abcdefghijklmnopqrstuvwxyz";
    for (let i = 0; i < letters.length; i++) {
      const ch = letters.charAt(i);
      expect(ctrlByteFor(ch)).toBe(String.fromCharCode(i + 1));
    }
  });

  it("space and @ → \\x00 (NUL)", () => {
    expect(ctrlByteFor(" ")).toBe("\x00");
    expect(ctrlByteFor("@")).toBe("\x00");
  });

  it("C0 symbol set → \\x1b..\\x1f, \\x7f", () => {
    expect(ctrlByteFor("[")).toBe("\x1b");
    expect(ctrlByteFor("\\")).toBe("\x1c");
    expect(ctrlByteFor("]")).toBe("\x1d");
    expect(ctrlByteFor("^")).toBe("\x1e");
    expect(ctrlByteFor("_")).toBe("\x1f");
    expect(ctrlByteFor("?")).toBe("\x7f");
  });

  it("returns null for unmapped single chars and non-1-length strings", () => {
    // Unmapped single chars.
    expect(ctrlByteFor("0")).toBeNull();
    expect(ctrlByteFor("!")).toBeNull();
    expect(ctrlByteFor("é")).toBeNull();
    // Multi-char and empty.
    expect(ctrlByteFor("")).toBeNull();
    expect(ctrlByteFor("ab")).toBeNull();
    expect(ctrlByteFor("hello")).toBeNull();
  });
});

// -- bindMobileToolbar -------------------------------------------------------
//
// Synthetic toolbar fixture: a parent <div class="kb-toolbar"> containing
// the nine standard buttons. Tests fire a `pointerdown` Event at each
// button (happy-dom doesn't ship `PointerEvent`, but `Event` works for
// this mapping; the binder casts to `PointerEvent` in handlers but only
// calls `e.preventDefault()` which `Event` supports). Verifies the wire
// sequence sent through the `send` spy.
//
// Why pointerdown not click: matches the ergonomics of mobile keyboards
// (immediate feedback + cancel native focus shift) and lets the binder
// preventDefault to keep the IME / virtual keyboard behaviour correct.

function makeToolbar(): {
  toolbar: HTMLElement;
  buttons: Record<string, HTMLElement>;
} {
  const toolbar = document.createElement("div");
  toolbar.className = "kb-toolbar";
  const buttons: Record<string, HTMLElement> = {};
  const ids = [
    "kb-toggle",
    "kb-ctrl",
    "kb-up",
    "kb-down",
    "kb-left",
    "kb-right",
    "kb-tab",
    "kb-enter",
    "kb-esc",
  ];
  for (const id of ids) {
    const b = document.createElement("button");
    b.id = id;
    toolbar.appendChild(b);
    buttons[id] = b;
  }
  document.body.appendChild(toolbar);
  return { toolbar, buttons };
}

function fireDown(el: HTMLElement): { defaultPrevented: boolean } {
  // happy-dom dispatches Event for pointerdown fine; the binder doesn't
  // depend on PointerEvent-only fields.
  const ev = new Event("pointerdown", { cancelable: true, bubbles: true });
  el.dispatchEvent(ev);
  return { defaultPrevented: ev.defaultPrevented };
}

describe("bindMobileToolbar: happy path", () => {
  let send: ReturnType<typeof vi.fn<(bytes: string) => void>>;
  let fixture: ReturnType<typeof makeToolbar>;

  beforeEach(() => {
    document.body.innerHTML = "";
    send = vi.fn<(bytes: string) => void>();
    fixture = makeToolbar();
  });

  it("kb-up/down/right/left send arrow CSI sequences (default DECCKM off)", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });

    fireDown(fixture.buttons["kb-up"]!);
    fireDown(fixture.buttons["kb-down"]!);
    fireDown(fixture.buttons["kb-right"]!);
    fireDown(fixture.buttons["kb-left"]!);

    expect(send).toHaveBeenNthCalledWith(1, "\x1b[A");
    expect(send).toHaveBeenNthCalledWith(2, "\x1b[B");
    expect(send).toHaveBeenNthCalledWith(3, "\x1b[C");
    expect(send).toHaveBeenNthCalledWith(4, "\x1b[D");

    ctrl.dispose();
  });

  it("kb-tab/enter/esc send the canonical bytes", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });

    fireDown(fixture.buttons["kb-tab"]!);
    fireDown(fixture.buttons["kb-enter"]!);
    fireDown(fixture.buttons["kb-esc"]!);

    expect(send).toHaveBeenNthCalledWith(1, "\t");
    expect(send).toHaveBeenNthCalledWith(2, "\r");
    expect(send).toHaveBeenNthCalledWith(3, "\x1b");

    ctrl.dispose();
  });

  it("preventDefault is called on every toolbar press", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    for (const id of [
      "kb-toggle",
      "kb-ctrl",
      "kb-up",
      "kb-down",
      "kb-left",
      "kb-right",
      "kb-tab",
      "kb-enter",
      "kb-esc",
    ]) {
      const r = fireDown(fixture.buttons[id]!);
      expect(r.defaultPrevented).toBe(true);
    }
    ctrl.dispose();
  });

  it("kb-toggle toggles .collapsed on the toolbar; does NOT clear armed Ctrl", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.setCtrlArmed(true);
    expect(fixture.toolbar.classList.contains("collapsed")).toBe(false);

    fireDown(fixture.buttons["kb-toggle"]!);
    expect(fixture.toolbar.classList.contains("collapsed")).toBe(true);
    expect(ctrl.isCtrlArmed()).toBe(true); // toggle doesn't disarm

    fireDown(fixture.buttons["kb-toggle"]!);
    expect(fixture.toolbar.classList.contains("collapsed")).toBe(false);
    expect(ctrl.isCtrlArmed()).toBe(true);

    ctrl.dispose();
  });

  it("kb-ctrl toggles armed state and does NOT call send", () => {
    const onChange = vi.fn();
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send, onCtrlChange: onChange });

    expect(ctrl.isCtrlArmed()).toBe(false);
    fireDown(fixture.buttons["kb-ctrl"]!);
    expect(ctrl.isCtrlArmed()).toBe(true);
    expect(fixture.buttons["kb-ctrl"]!.classList.contains("armed")).toBe(true);
    expect(onChange).toHaveBeenLastCalledWith(true);

    fireDown(fixture.buttons["kb-ctrl"]!);
    expect(ctrl.isCtrlArmed()).toBe(false);
    expect(fixture.buttons["kb-ctrl"]!.classList.contains("armed")).toBe(false);
    expect(onChange).toHaveBeenLastCalledWith(false);

    expect(send).not.toHaveBeenCalled();

    ctrl.dispose();
  });

  it("arrow / Tab / Enter / Esc presses clear armed Ctrl", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    for (const id of ["kb-up", "kb-down", "kb-left", "kb-right", "kb-tab", "kb-enter", "kb-esc"]) {
      ctrl.setCtrlArmed(true);
      fireDown(fixture.buttons[id]!);
      expect(ctrl.isCtrlArmed()).toBe(false);
    }
    ctrl.dispose();
  });

  it("missing toolbar buttons are skipped silently", () => {
    // Strip every button.
    const empty = document.createElement("div");
    document.body.appendChild(empty);
    const ctrl = bindMobileToolbar({ toolbar: empty, send });
    // No buttons → no listeners attached → no send + no throws.
    expect(() => ctrl.applyStickyCtrl("a")).not.toThrow();
    ctrl.dispose();
    expect(send).not.toHaveBeenCalled();
  });
});

describe("bindMobileToolbar: applyStickyCtrl", () => {
  let send: ReturnType<typeof vi.fn<(bytes: string) => void>>;
  let fixture: ReturnType<typeof makeToolbar>;

  beforeEach(() => {
    document.body.innerHTML = "";
    send = vi.fn<(bytes: string) => void>();
    fixture = makeToolbar();
  });

  it("not armed → returns text unchanged", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    expect(ctrl.applyStickyCtrl("a")).toBe("a");
    expect(ctrl.applyStickyCtrl("hello")).toBe("hello");
    expect(ctrl.isCtrlArmed()).toBe(false);
    ctrl.dispose();
  });

  it("armed + 1-char input → ctrl byte and disarms", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.setCtrlArmed(true);
    expect(ctrl.applyStickyCtrl("c")).toBe("\x03");
    expect(ctrl.isCtrlArmed()).toBe(false);
    ctrl.dispose();
  });

  it("armed + 1-char unmapped input → original char and disarms", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.setCtrlArmed(true);
    expect(ctrl.applyStickyCtrl("é")).toBe("é");
    expect(ctrl.isCtrlArmed()).toBe(false);
    ctrl.dispose();
  });

  it("armed + paste (multi-char) → text unchanged and disarms", () => {
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.setCtrlArmed(true);
    expect(ctrl.applyStickyCtrl("hello")).toBe("hello");
    expect(ctrl.isCtrlArmed()).toBe(false);
    ctrl.dispose();
  });
});

describe("bindMobileToolbar: dispose", () => {
  it("removes all listeners — pointerdown after dispose does NOT call send", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.dispose();

    for (const id of ["kb-up", "kb-down", "kb-left", "kb-right", "kb-tab", "kb-enter", "kb-esc"]) {
      fireDown(fixture.buttons[id]!);
    }
    expect(send).not.toHaveBeenCalled();
  });

  it("clears armed state and resets kb-ctrl visuals", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    ctrl.setCtrlArmed(true);
    expect(fixture.buttons["kb-ctrl"]!.classList.contains("armed")).toBe(true);

    ctrl.dispose();
    expect(ctrl.isCtrlArmed()).toBe(false);
    expect(fixture.buttons["kb-ctrl"]!.classList.contains("armed")).toBe(false);
  });

  it("is idempotent", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    expect(() => {
      ctrl.dispose();
      ctrl.dispose();
    }).not.toThrow();
  });
});

describe("bindMobileToolbar: DECCKM-aware arrows", () => {
  it("application-cursor mode → SS3 sequences (\\x1bOA..D)", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    modes.setModes(true /* bracketed */, true /* app cursor */);
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });

    fireDown(fixture.buttons["kb-up"]!);
    fireDown(fixture.buttons["kb-down"]!);
    fireDown(fixture.buttons["kb-right"]!);
    fireDown(fixture.buttons["kb-left"]!);

    expect(send).toHaveBeenNthCalledWith(1, "\x1bOA");
    expect(send).toHaveBeenNthCalledWith(2, "\x1bOB");
    expect(send).toHaveBeenNthCalledWith(3, "\x1bOC");
    expect(send).toHaveBeenNthCalledWith(4, "\x1bOD");

    ctrl.dispose();
  });

  it("default mode → CSI sequences (\\x1b[A..D])", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    // beforeEach restored defaults (bracketed=true, appCursor=false).
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });

    fireDown(fixture.buttons["kb-up"]!);
    expect(send).toHaveBeenLastCalledWith("\x1b[A");

    ctrl.dispose();
  });

  it("mode is consulted on each press, not at bind time", () => {
    document.body.innerHTML = "";
    const send = vi.fn<(bytes: string) => void>();
    const fixture = makeToolbar();

    // Start in default mode.
    const ctrl = bindMobileToolbar({ toolbar: fixture.toolbar, send });
    fireDown(fixture.buttons["kb-up"]!);
    expect(send).toHaveBeenLastCalledWith("\x1b[A");

    // Server flips DECCKM mid-session — same controller honours it.
    modes.setModes(true, true);
    fireDown(fixture.buttons["kb-up"]!);
    expect(send).toHaveBeenLastCalledWith("\x1bOA");

    ctrl.dispose();
  });
});
