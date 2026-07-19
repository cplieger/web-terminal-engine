// Tier 3 — wire v4 typed framing across the REAL stack: real chromium
// WebSocket opcodes -> real Go server (web/e2e/testserver, /bin/cat PTY) ->
// real latch -> real byte accounting. This is the integration proof the
// implementation review required (impl-review finding 1): unit mocks pin
// JavaScript call order, but only this path proves browser opcode selection,
// server latching, PTY delivery, and the ack ledger as ONE flow.
//
// The oracle for full-alphabet delivery is the F1 collision payload —
// 0x00 + {"type":"ping"} — sent as binary input AFTER the upgrade:
//   - if the latch worked, all 16 bytes are PTY input: the ledger (observed
//     via the inputAck riding echo frames) advances by exactly 16, and cat
//     echoes the JSON back into the screen;
//   - if the sentinel path had consumed it as a control ping, the ledger
//     would not advance and a pong frame would appear instead.
//
// Run with `npm run test:e2e` (needs `npx playwright install chromium` once
// and a Go toolchain for the test server).
import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { bundleEngine } from "./e2e-harness.js";

interface SendRec {
  kind: "text" | "binary";
  len: number;
  text?: string;
}

declare global {
  interface Window {
    __wte?: {
      acks: number[];
      bounds: number;
      sends: SendRec[];
      text: () => string;
    };
  }
}

// WTE is the esbuild IIFE global injected via addScriptTag.
declare const WTE: {
  connection: {
    init: (cb: unknown) => void;
    connect: () => void;
    sendBinary: (b: Uint8Array) => boolean;
  };
  render: { init: (out: HTMLElement, wrap: HTMLElement) => void };
  decodeWireBinary: (b: ArrayBuffer) => unknown;
};

// The test server lives in the ROOT Go module (internal/e2etestserver), so
// `go run ./internal/e2etestserver` executes from the repo root — web/ itself
// stays carved out of the module by the web-ignore stub go.mod.
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");

let server: ChildProcess | null = null;
let baseURL = "";
let bundle = "";

test.beforeAll(async () => {
  bundle = await bundleEngine();
  server = spawn("go", ["run", "./internal/e2etestserver"], {
    cwd: repoRoot,
    stdio: ["pipe", "pipe", "inherit"],
  });
  baseURL = await new Promise<string>((resolve, reject) => {
    const to = setTimeout(() => reject(new Error("testserver did not report LISTEN")), 60_000);
    let buf = "";
    server!.stdout!.on("data", (d: Buffer) => {
      buf += d.toString();
      const m = /LISTEN (\S+)/.exec(buf);
      if (m) {
        clearTimeout(to);
        resolve(m[1]!);
      }
    });
    server!.on("exit", (code) => reject(new Error(`testserver exited early (${code})`)));
  });
});

test.afterAll(() => {
  // Closing stdin triggers the server's watchdog exit; kill is the backstop.
  server?.stdin?.end();
  server?.kill();
});

test("v4 upgrade delivers the 0x00-leading collision payload as full-alphabet PTY input", async ({
  page,
}) => {
  await page.goto(baseURL);
  await page.addScriptTag({ content: bundle });

  // Wire the REAL connection module: record every inputAck the server sends
  // (screen/scroll/title frames riding cat's echo) and the resume bounds
  // callback, which fires on the resumeAck AFTER the upgrade transition has
  // been sent — the deterministic "socket is upgraded" signal.
  await page.evaluate(() => {
    const rec: NonNullable<Window["__wte"]> = {
      acks: [],
      bounds: 0,
      sends: [],
      text: () => document.body.innerText,
    };
    window.__wte = rec;
    // Send-spy (impl-review confirmation, finding 1): the ledger alone cannot
    // distinguish the latched path from a v3 split (both count 16 bytes), so
    // record every outbound frame's TYPE and SIZE. Latched delivery is the
    // unique combination: one text `upgrade` frame + the payload as a SINGLE
    // 16-byte binary frame (a v3 split would send 1 + 15).
    const RealWS = window.WebSocket;
    window.WebSocket = class extends RealWS {
      override send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
        if (typeof data === "string") {
          rec.sends.push({ kind: "text", len: data.length, text: data });
        } else {
          const len =
            data instanceof Blob
              ? data.size
              : ArrayBuffer.isView(data)
                ? data.byteLength
                : data.byteLength;
          rec.sends.push({ kind: "binary", len });
        }
        super.send(data as never);
      }
    } as typeof WebSocket;
    WTE.connection.init({
      onMessage: (msg: { inputAck?: number }) => {
        if (typeof msg.inputAck === "number") {
          rec.acks.push(msg.inputAck);
        }
      },
      onOpen: () => undefined,
      onClose: () => undefined,
      onResumeBounds: () => {
        rec.bounds += 1;
      },
      computeSize: () => ({ cols: 80, rows: 24 }),
    });
    WTE.connection.connect();
  });

  // Proof + upgrade have happened once the bounds callback fires (it is
  // ordered after the upgrade send in the resumeAck branch).
  await page.waitForFunction(() => (window.__wte?.bounds ?? 0) > 0);

  // The F1 collision payload: a byte-exact v3 control frame sent as input.
  const payload = [0x00, ...Array.from(new TextEncoder().encode('{"type":"ping"}'))];
  expect(payload.length).toBe(16);
  await page.evaluate((bytes: number[]) => {
    WTE.connection.sendBinary(new Uint8Array(bytes));
  }, payload);

  // The ledger must advance by EXACTLY the payload length: 16 counts every
  // byte including the leading NUL as PTY input. (A sentinel mis-parse would
  // leave it at 0 — the ping would have been consumed as control.)
  await page.waitForFunction(() => Math.max(0, ...(window.__wte?.acks ?? [])) === 16, undefined, {
    timeout: 10_000,
  });
  const maxAck = await page.evaluate(() => Math.max(0, ...window.__wte!.acks));
  expect(maxAck).toBe(16);

  // Latch discrimination (the send-spy half of the oracle): the upgrade went
  // out as exactly one text frame, and the collision payload as exactly ONE
  // 16-byte binary frame — no solitary-NUL split (1 + 15) anywhere. Combined
  // with ack === 16 this is unique to the latched path: an unsplit 16-byte
  // 0x00-leading frame on a NON-latched server would have been consumed as a
  // control ping (ack 0), and a v3-mode client would have split it.
  const sends = await page.evaluate(() => window.__wte!.sends);
  const texts = sends.filter((s) => s.kind === "text");
  expect(texts).toHaveLength(1);
  expect(JSON.parse(texts[0]!.text!)).toEqual({ type: "upgrade" });
  const binLens = sends.filter((s) => s.kind === "binary").map((s) => s.len);
  expect(binLens.filter((l) => l === 16)).toHaveLength(1);
  expect(binLens).not.toContain(1); // no solitary-NUL split frame
  expect(binLens).not.toContain(15); // no split JSON tail
});
