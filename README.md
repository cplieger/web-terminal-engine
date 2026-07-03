# web-terminal-engine

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/web-terminal-engine.svg)](https://pkg.go.dev/github.com/cplieger/web-terminal-engine)
[![npm](https://img.shields.io/npm/v/@cplieger/web-terminal-engine)](https://www.npmjs.com/package/@cplieger/web-terminal-engine)
[![JSR](https://jsr.io/badges/@cplieger/web-terminal-engine)](https://jsr.io/@cplieger/web-terminal-engine)
[![Go version](https://img.shields.io/github/go-mod/go-version/cplieger/web-terminal-engine)](https://github.com/cplieger/web-terminal-engine/blob/main/go.mod)
[![Test coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/web-terminal-engine/badges/coverage.json)](https://github.com/cplieger/web-terminal-engine/actions/workflows/coverage.yml)
[![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/web-terminal-engine/badges/mutation.json)](https://github.com/cplieger/web-terminal-engine/issues?q=label%3Agremlins-tracker)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/13225/badge)](https://www.bestpractices.dev/projects/13225)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/web-terminal-engine/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/web-terminal-engine)

> Cross-language terminal emulator and session engine (Go) with browser renderer (TypeScript).

A standalone library that bridges a PTY to a browser WebSocket. The Go packages provide a VT100/VT500 screen buffer with SGR support and a WebSocket-based terminal session handler with reconnect, scrollback replay, and adaptive ping. The TypeScript package provides the browser-side renderer, keyboard mapper, mouse encoder, and binary wire decoder. No app-specific dependencies — only the standard library, `github.com/coder/websocket`, and `github.com/creack/pty`.

## Install

Go: `go get github.com/cplieger/web-terminal-engine@latest` — TS: `npx jsr add @cplieger/web-terminal-engine` or `npm i @cplieger/web-terminal-engine`

## Usage

```go
import (
    "log/slog"
    "net/http"

    "github.com/cplieger/web-terminal-engine/terminal"
)

h := terminal.NewHandler(
    []string{"/bin/bash"},
    terminal.WithWorkDir("/home/user"),
    terminal.WithLogger(slog.Default()),
)
mux := http.NewServeMux()
h.RegisterRoutes(mux)
// or use h as an http.Handler directly:
// mux.Handle("/ws", h)
```

```typescript
import { render, keyboard, mouse, decodeWireBinary } from "@cplieger/web-terminal-engine";

render.init({
  output: document.getElementById("term-output")!,
  termWrap: document.getElementById("term")!,
});
// On WebSocket binary message:
const msg = decodeWireBinary(event.data);
if (msg?.type === "screen") render.handleScreen(msg);
```

## API

### Go packages

- **`vt`** — VT100/VT500 screen buffer: `New(rows, cols)`, `Write([]byte)`, `Resize(rows, cols)`, `RenderRowWire(y)`, `DrainScrollback()`, `CursorPos()`, `HoldFlush()`, `ReleaseFlush()`, `IsFlushHeld()`, `RenderViewport()`, `RowString(y)`. Public fields: `Cells`, `Width`, `Height`, `Title`, `MouseMode`, `InAltScreen`, cursor/mode state.
- **`terminal`** — WebSocket session handler: `NewHandler(command, ...Option)`, `RegisterRoutes(mux)`, `ServeHTTP(w, r)`, `Shutdown()`. Options: `WithWorkDir`, `WithLogger`, `WithEnv`, `WithScrollbackCapacity`, `WithAcceptOptions`, `WithOnProcessExit`. Handles PTY lifecycle, binary wire protocol, reconnect with scrollback replay, adaptive ping.

### TypeScript (`web/` — published as `@cplieger/web-terminal-engine` on NPM and JSR)

- **`render`** — DOM renderer driven by `ScreenMessage` / `ScrollMessage` frames: `init`, `handleScreen`, `handleScroll`, `updateFontMetrics`, `computeSize`, `getCursorPx`, `setPredictedCursor`, `resetScreen`, `resetScrollback`, `getHighestIndex`, `noteResumeBounds`, `updateReverseVideo`.
- **`keyboard`** — Translates `KeyboardEvent` to terminal byte sequences: `mapKeyboardEvent`, `bracketTextForPaste`, `prepareTextForTerminal`. Honors `applicationCursor`, `applicationKeypad`, `bracketedPaste`.
- **`mouse`** — SGR 1006 mouse + focus reporting encoder: `init`, `encodeSGR`, `MouseInputHandler`.
- **`scroll`** — Auto-follow tracker for the scroll container: `init`, `stickToBottom`, `scrollToBottom`, `isUserScrolledUp`.
- **`modes`** — DEC private mode state (synced from server's `ModesMessage`): `setModes`, `isBracketedPaste`, `isApplicationCursor`, `getMouseMode`, `isMouseSGR`, `isFocusReporting`, `isApplicationKeypad`, `isReverseVideo`.
- **`decodeWireBinary(buf)`** — Top-level decoder for binary WebSocket frames; returns a `ServerMessage` or `null` for invalid/truncated frames.
- **`connection`** — Client → server WebSocket lifecycle: socket ownership, exponential-backoff reconnect, and the resume/inputAck reliability layer (outbox + server-restart detection). `init(callbacks)`, `connect`, `sendBinary`, `sendResize`, `reconnectNow`; `wsPath` callback option defaults to `"/ws"`. Decodes frames and applies `modes.setModes` internally, so consumers only dispatch screen/scroll to `render`. Pairs with the Go `terminal` handler's resume protocol. (`controlFrame` / `wsURL` are also exported for advanced use.)
- **Wire types** — `WireRun`, `ScreenMessage`, `ScrollMessage`, `ModesMessage`, `TitleMessage`, `ResumeAckMessage`, `ServerMessage`, `ControlMessage` re-exported from the package root.

## Wire Protocol

The Go server and TypeScript client communicate over a binary WebSocket frame format rather than shared code. The **authoritative byte-level definition is the code itself** — the Go encoder (`terminal/wire_binary.go`), the Go `WireRun` types (`vt/wire.go`), and the TS decoder (`web/src/wire-binary.ts`), all guarded by the round-trip fuzz tests and the `wire-golden/*.bin` fixtures. The design rationale, which a prose byte-table cannot capture and tends to drift from, is:

- **Binary, not JSON.** Frames are WebSocket _binary_ messages with little-endian integers, and stay compact to keep frame size and latency low on a full repaint. Client → server, raw terminal input flows unframed, while control messages (resize, resume) are a `0x00` prefix byte + a JSON body — no valid terminal input starts with NUL, so the prefix is unambiguous.
- **Absolute line indexing.** Every line the server produces gets a monotonic absolute index that does not change as the screen scrolls, so the client keeps one buffer keyed by that index: applying a line is idempotent, resume aligns by absolute index rather than a fragile count, an eviction gap is detectable (surfaced as a "history trimmed" marker), and a server-epoch value detects restarts across reconnects. The exact index handling lives in the sources named above (`vt/wire.go`, `terminal/wire_binary.go`, `web/src/wire-binary.ts`).
- **Versioning by lockstep.** There is no version byte in the frame header: the Go module and the npm/JSR package release together from this one repository, so a breaking wire change must land in the Go encoder/decoder and the TS decoder in a single release (`feat!:` / `BREAKING CHANGE:`). A version byte is added only if a break ever cannot be coordinated in one release.

Client → server input for the DEC modes (SGR 1006 mouse, focus reporting, application keypad) is encoded by the TS `mouse` / `keyboard` modules and consumed server-side by `vt`; the sequences live in those sources. For VT/DEC features intentionally absent from the wire, see [Unsupported by Design](#unsupported-by-design).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude Opus](https://www.anthropic.com/claude) and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0 — see [LICENSE](LICENSE).

## Related projects

The web-terminal family builds on this engine:

- [`@cplieger/web-terminal-ui`](https://github.com/cplieger/web-terminal-ui) — the
  reference touch-first browser UI for the TypeScript renderer.
- [`web-terminal-server`](https://github.com/cplieger/web-terminal-server) — a
  ready-to-run container that bridges a PTY command to the browser over HTTP +
  WebSocket.

Apps built on the engine:

- [`vibekit`](https://github.com/cplieger/vibekit)
- [`vibecli`](https://github.com/cplieger/vibecli)

## Unsupported by Design

The following VT/DEC features are **intentionally not implemented**. Input bytes for these sequences are consumed (not echoed or half-rendered) and produce no visible effect, except where a row notes a performed side effect. This is a deliberate design choice — not a TODO.

| Category                       | Sequences                                                                                        | Rationale                                                                                                                                                                                                                                                      |
| ------------------------------ | ------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Double-width/height lines      | DECDWL, DECDHL                                                                                   | Requires line-level rendering attribute + renderer changes; purely legacy VT220 feature unused by modern apps.                                                                                                                                                 |
| Programmatic resize / geometry | DECCOLM (132-column) width change, XTWINOPS window resize/move/iconify/maximize, DECSLPP/DECSNLS | The browser viewport and PTY winsize own the terminal size, and a browser tab has no OS window to move. Only the size _change_ is declined — DECCOLM's clear/home side effects, the title stack (22/23), and the size/label reports (18/19/20/21) are honored. |
| DCS device control             | tmux control-mode passthrough                                                                    | Not modeled; consumed silently. DECRQSS and the XTGETTCAP color-count query share the same DCS parser and **are** supported (see the note below).                                                                                                              |
| Graphics protocols             | Sixel, ReGIS, Kitty image protocol, iTerm inline images                                          | Specialized rendering pipeline incompatible with the DOM-based renderer.                                                                                                                                                                                       |
| NRCS national charsets         | All national replacement character sets (only DEC Special Graphics + ASCII are supported)        | Legacy internationalization mechanism superseded by UTF-8. No modern app emits these.                                                                                                                                                                          |
| Exotic SGR attributes          | Fonts 10-20, framed/encircled (51/52/54), superscript/subscript (73-75), ideogram (60-65)        | No modern terminal or app uses these attributes; they have no visual representation in standard monospace fonts.                                                                                                                                               |
| X11 Xcms color specifications  | CIE Lab/Luv/XYZ/uvY/xyY, rgbi intensity, TekHVC in OSC 4/5/10-19                                 | libX11 device-colorimetry, not the VT/ANSI spec — no CLI tool emits them. The `rgb:` / `#hex` forms and the palette + dynamic-color set/query/reset are all supported.                                                                                         |
| ZWJ emoji grapheme clustering  | Zero-width joiner sequences are not clustered into single cells                                  | Requires ICU-level grapheme segmentation. Individual emoji codepoints render correctly; only multi-codepoint ZWJ sequences (family emoji, skin-tone modifiers) may misalign.                                                                                   |

> **Note on device queries:** several report/query sequences sharing the DCS or CSI parsers **are** supported for conformance. **DECRQSS** (`DCS $ q … ST`) answers SGR (`m`), scroll region (`r`), cursor style (`SP q`), protection (`" q`), conformance level (`" p`), left/right margins (`s`) and lines-per-page/screen (`t`, `* |`), replying `DCS 0 $ r ST` for anything else. **XTGETTCAP** (`DCS + q … ST`) answers the color-count capability (256). **DECRQCRA** (rectangular-area checksum) and **OSC 52** clipboard read-back are answered only when `Screen.AllowScreenReport` is enabled — both inject their reply into the PTY, so they default off. The VT model is validated against the [esctest2](https://github.com/ThomasDickey/esctest2) conformance suite (see CONTRIBUTING).
