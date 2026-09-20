// Kitty keyboard (disambiguate 0x1) encoding through REAL Chromium key events:
// page.keyboard fills ev.code, ev.key and the modifier flags exactly as it does
// for a user, and the real keyboard.ts encoder runs over them. The Vitest
// siblings construct synthetic KeyboardEvents and cannot prove that half. The
// flag comes from a mode state built with keyboardFlags=1, the state a server's
// CSI >1u would sync through the modes frame.
import { test, expect, type Page } from "@playwright/test";
import { bundleEngine, HARNESS } from "./e2e-harness.js";

interface KbdRec {
  kind: string;
  bytes?: string;
}

declare global {
  interface Window {
    __kbd?: KbdRec[];
  }
}

/** The esbuild IIFE global `addScriptTag` injects; see `bundleEngine`. */
declare const WTE: Pick<
  typeof import("../src/index.js"),
  "createModeState" | "POWER_ON_MODES" | "keyboard"
>;

test.describe("kitty disambiguate encoding in a real browser (real key events -> encoder bytes)", () => {
  let bundle = "";
  test.beforeAll(async () => {
    bundle = await bundleEngine();
  });

  // setup builds the page, enables/disables the kitty flag on the client mode
  // state, and installs a keydown recorder on a focused textarea. mapKeyboardEvent
  // runs against the real event; send/scroll results are preventDefaulted so the
  // character is not also inserted (leaving "ignore" — the text path — untouched).
  async function setup(page: Page, kittyOn: boolean): Promise<void> {
    await page.setContent(HARNESS);
    await page.addScriptTag({ content: bundle });
    await page.evaluate((on: boolean) => {
      const modes = WTE.createModeState({ ...WTE.POWER_ON_MODES, keyboardFlags: on ? 1 : 0 });
      const input = document.createElement("textarea");
      input.id = "kbd-input";
      document.body.appendChild(input);
      window.__kbd = [];
      input.addEventListener("keydown", (ev) => {
        const r = WTE.keyboard.mapKeyboardEvent(ev, modes);
        window.__kbd?.push(r);
        if (r.kind !== "ignore") {
          ev.preventDefault();
        }
      });
      input.focus();
    }, kittyOn);
  }

  // pressAndRead clears the recorder, presses a Playwright key combo, and returns
  // the LAST recorded result (modifier-only keydowns record "ignore" first).
  async function pressAndRead(page: Page, combo: string): Promise<KbdRec | undefined> {
    await page.evaluate(() => {
      window.__kbd = [];
    });
    await page.keyboard.press(combo);
    return page.evaluate(() => window.__kbd?.at(-1));
  }

  test("core disambiguation with the flag ON", async ({ page }) => {
    await setup(page, true);
    const cases: [string, string][] = [
      ["Escape", "\x1b[27u"], // unambiguous Esc — the headline feature
      ["Control+i", "\x1b[105;5u"], // Ctrl+I disambiguated from Tab
      ["Alt+a", "\x1b[97;3u"],
      ["Control+Shift+A", "\x1b[97;6u"], // UNSHIFTED codepoint 97, not 65
      ["Control+3", "\x1b[51;5u"],
      ["Control+Meta+a", "\x1b[97;13u"], // meta reports when combined with ctrl/alt
    ];
    for (const [combo, want] of cases) {
      expect(await pressAndRead(page, combo), combo).toEqual({ kind: "send", bytes: want });
    }
    // DELIBERATE product deviation (mirrors the legacy carve-out and the kitty
    // terminal's own emulator-shortcut precedence): meta/Cmd-ONLY printables
    // are browser chrome (Cmd+C / Cmd+R) and are NOT reported to the app —
    // reporting them ate copy-on-macOS inside kitty apps.
    expect(await pressAndRead(page, "Meta+a"), "Meta+a stays with the browser").toEqual({
      kind: "ignore",
    });
  });

  test("functional keys and the Enter/Tab/Backspace exception with the flag ON", async ({
    page,
  }) => {
    await setup(page, true);
    const cases: [string, KbdRec][] = [
      ["F1", { kind: "send", bytes: "\x1b[P" }], // CSI form (legacy is SS3)
      ["F3", { kind: "send", bytes: "\x1b[13~" }], // tilde 13, NOT CSI R
      ["ArrowUp", { kind: "send", bytes: "\x1b[A" }],
      ["Control+ArrowUp", { kind: "send", bytes: "\x1b[1;5A" }],
      ["Delete", { kind: "send", bytes: "\x1b[3~" }],
      // Enter/Tab/Backspace stay legacy when unmodified, disambiguate when modified.
      ["Enter", { kind: "send", bytes: "\r" }],
      ["Control+Enter", { kind: "send", bytes: "\x1b[13;5u" }],
      ["Tab", { kind: "send", bytes: "\t" }],
      ["Shift+Tab", { kind: "send", bytes: "\x1b[9;2u" }],
      ["Backspace", { kind: "send", bytes: "\x7f" }],
      ["Control+Backspace", { kind: "send", bytes: "\x1b[127;5u" }],
    ];
    for (const [combo, want] of cases) {
      expect(await pressAndRead(page, combo), combo).toEqual(want);
    }
  });

  test("plain typing is NOT intercepted (text flows to the textarea) with the flag ON", async ({
    page,
  }) => {
    await setup(page, true);
    // Each plain key must be "ignore" (the encoder defers to the textarea/IME).
    expect(await pressAndRead(page, "a"), "a").toEqual({ kind: "ignore" });
    expect(await pressAndRead(page, "Z"), "Z").toEqual({ kind: "ignore" });
    // And the browser actually inserted the text into the textarea (not swallowed).
    const value = await page.evaluate(
      () => document.querySelector<HTMLTextAreaElement>("#kbd-input")?.value,
    );
    expect(value).toBe("aZ");
  });

  test("flag OFF => everything falls back to legacy encoding (regression)", async ({ page }) => {
    await setup(page, false);
    const cases: [string, string][] = [
      ["Escape", "\x1b"], // bare ESC, not CSI 27u
      ["Control+i", "\x09"], // C0 byte (== Tab), the legacy ambiguity
      ["Alt+a", "\x1ba"], // ESC-prefixed
      ["F1", "\x1bOP"], // SS3, not CSI
    ];
    for (const [combo, want] of cases) {
      expect(await pressAndRead(page, combo), combo).toEqual({ kind: "send", bytes: want });
    }
  });
});
