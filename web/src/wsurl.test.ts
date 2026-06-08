// Property-based test for wsURL.
//
// Invariants tested:
// 1. The `wss://` scheme is selected exactly when the page protocol is
//    "https:"; for any other protocol value, `ws://` is selected.
// 2. The host is preserved verbatim in the result (no encoding,
//    truncation, or rewriting).
// 3. The default path suffix `/ws` is present when no path is given,
//    and a supplied path is honored verbatim.
// 4. The result is a parseable URL.

import { describe, it, expect } from "vitest";
import fc from "fast-check";

import { wsURL } from "./wsurl.js";

describe("wsURL property", () => {
  it("selects wss: only when page protocol is https:, ws: otherwise", () => {
    fc.assert(
      fc.property(
        fc.constantFrom("http:", "https:", "file:", "ftp:", ""),
        fc.string({ minLength: 1, maxLength: 100 }),
        (proto, host) => {
          const url = wsURL(proto, host);
          if (proto === "https:") {
            expect(url.startsWith("wss://")).toBe(true);
          } else {
            expect(url.startsWith("ws://")).toBe(true);
          }
        },
      ),
    );
  });

  it("preserves the host verbatim in the URL", () => {
    fc.assert(
      fc.property(fc.constantFrom("http:", "https:"), fc.domain(), (proto, host) => {
        const url = wsURL(proto, host);
        expect(url).toContain(host);
      }),
    );
  });

  it("defaults to /ws and honors a supplied path", () => {
    fc.assert(
      fc.property(
        fc.constantFrom("http:", "https:"),
        fc.domain(),
        fc.constantFrom("/ws", "/api/shell/ws", "/terminal/socket"),
        (proto, host, path) => {
          expect(wsURL(proto, host).endsWith("/ws")).toBe(true);
          expect(wsURL(proto, host, path).endsWith(path)).toBe(true);
        },
      ),
    );
  });

  it("normalizes a path without a leading slash", () => {
    expect(wsURL("http:", "example.com", "ws")).toBe("ws://example.com/ws");
    expect(wsURL("https:", "example.com", "api/shell/ws")).toBe("wss://example.com/api/shell/ws");
  });

  it("produces a parseable URL with the expected scheme and path", () => {
    fc.assert(
      fc.property(
        fc.constantFrom("http:", "https:"),
        fc.domain(),
        fc.constantFrom("/ws", "/api/shell/ws"),
        (proto, host, path) => {
          const url = wsURL(proto, host, path);
          const parsed = new URL(url);
          expect(parsed.protocol).toBe(proto === "https:" ? "wss:" : "ws:");
          expect(parsed.host).toBe(host);
          expect(parsed.pathname).toBe(path);
        },
      ),
    );
  });
});
