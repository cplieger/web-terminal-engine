// @vitest-environment happy-dom
//
// Behavioral display conformance — the RENDERER half of "does the operation
// actually look right on screen". esctest2 proves the Go engine's screen MODEL
// is correct after clear/erase/cursor/insert/delete; this proves render.ts
// turns that model into the correct DOM. Each case is a real escape SEQUENCE
// run through the actual engine (terminal/render_golden_test.go
// TestRenderGoldenBehavior), whose emitted wire frame is decoded here by the
// real decoder and rendered by the real render.ts. The expected screen grid is
// SPEC-AUTHORED in the Go generator and asserted there against the engine too,
// so the same grid pins both halves and they cannot silently disagree.
//
// Scope: in-place SCREEN-grid operations (what the renderer paints). Scroll-to-
// history is covered by render-store.test.ts.
import { describe, it, expect, beforeEach } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { decodeWireBinary } from "./wire-binary.js";
import { initHarness, renderScreen, rowSpans } from "./test-helpers/render-harness.js";
import type { ScreenMessage } from "./types.js";

interface BehaviorEntry {
  name: string;
  input: string;
  want: string[];
  curRow: number;
  curCol: number;
  frame: string; // base64 of the engine's real wire frame (Go marshals []byte so)
}

function loadManifest(): BehaviorEntry[] {
  const dir = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "render-golden");
  return JSON.parse(readFileSync(join(dir, "behavior.manifest.json"), "utf8")) as BehaviorEntry[];
}

function frameBuffer(b64: string): ArrayBuffer {
  const buf = Buffer.from(b64, "base64");
  return buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength);
}

// rowText returns a rendered row's visible text, normalizing the nbsp filler an
// empty row uses and trimming trailing blanks so it matches the vt RowString
// convention the spec grid is written in.
function rowText(output: HTMLElement, y: number): string {
  const text = rowSpans(output, y)
    .map((s) => s.textContent ?? "")
    .join("");
  return text.replace(/\u00a0/g, " ").replace(/[ ]+$/, "");
}

describe("behavioral display conformance (escape sequence -> engine -> wire -> DOM)", () => {
  beforeEach(() => {
    initHarness();
  });

  for (const entry of loadManifest()) {
    it(`renders the on-screen grid after ${entry.name}`, async () => {
      const decoded = decodeWireBinary(frameBuffer(entry.frame));
      expect(decoded?.type, `${entry.name}: fixture must decode as a screen frame`).toBe("screen");
      const output = await renderScreen(decoded as ScreenMessage);
      const got = entry.want.map((_, y) => rowText(output, y));
      // The rendered DOM grid must equal the spec-expected screen for the
      // sequence `entry.input` — e.g. after ED2 (clear) every row is blank.
      expect(got, `input ${JSON.stringify(entry.input)}`).toEqual(entry.want);
    });
  }
});
