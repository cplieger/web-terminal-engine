# vterm

[![CI](https://github.com/cplieger/vterm/actions/workflows/ci.yaml/badge.svg)](https://github.com/cplieger/vterm/actions/workflows/ci.yaml)
[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/vterm.svg)](https://pkg.go.dev/github.com/cplieger/vterm)
[![npm](https://img.shields.io/npm/v/@cplieger/vterm)](https://www.npmjs.com/package/@cplieger/vterm)
[![JSR](https://jsr.io/badges/@cplieger/vterm)](https://jsr.io/@cplieger/vterm)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cplieger/vterm/badge)](https://scorecard.dev/viewer/?uri=github.com/cplieger/vterm)

> Cross-language terminal emulator and session engine (Go) with browser renderer (TypeScript).

A standalone library that bridges a PTY to a browser WebSocket. The Go packages provide a VT100/VT500 screen buffer with SGR support and a WebSocket-based terminal session handler with reconnect, scrollback replay, and adaptive ping. The TypeScript package provides the browser-side renderer, keyboard mapper, mouse encoder, and binary wire decoder. No app-specific dependencies — only the standard library, `github.com/coder/websocket`, and `github.com/creack/pty`.

## Install

Go: `go get github.com/cplieger/vterm@latest` — TS: `npx jsr add @cplieger/vterm` or `npm i @cplieger/vterm`

## Usage

```go
import (
    "log/slog"
    "net/http"

    "github.com/cplieger/vterm/terminal"
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
import { render, keyboard, mouse, decodeWireBinary } from "@cplieger/vterm";

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

### TypeScript (`web/` — published as `@cplieger/vterm` on NPM and JSR)

- **`render`** — DOM renderer driven by `ScreenMessage` / `ScrollMessage` frames: `init`, `handleScreen`, `handleScroll`, `updateFontMetrics`, `computeSize`, `getCursorPx`, `setPredictedCursor`, `resetScreen`, `resetScrollback`, `getScrollbackRowCount`, `updateReverseVideo`.
- **`keyboard`** — Translates `KeyboardEvent` to terminal byte sequences: `mapKeyboardEvent`, `bracketTextForPaste`, `prepareTextForTerminal`. Honors `applicationCursor`, `applicationKeypad`, `bracketedPaste`.
- **`mouse`** — SGR 1006 mouse + focus reporting encoder: `init`, `encodeSGR`, `MouseInputHandler`.
- **`scroll`** — Auto-follow tracker for the scroll container: `init`, `scrollToBottom`, `suppressScroll`, `isUserScrolledUp`, `isInUserScroll`.
- **`modes`** — DEC private mode state (synced from server's `ModesMessage`): `setModes`, `isBracketedPaste`, `isApplicationCursor`, `getMouseMode`, `isMouseSGR`, `isFocusReporting`, `isApplicationKeypad`, `isReverseVideo`.
- **`decodeWireBinary(buf)`** — Top-level decoder for binary WebSocket frames; returns a `ServerMessage` or `null` for invalid/truncated frames.
- **`connection`** — Client → server WebSocket lifecycle: socket ownership, exponential-backoff reconnect, and the resume/inputAck reliability layer (outbox + server-restart detection). `init(callbacks)`, `connect`, `sendBinary`, `sendResize`, `reconnectNow`; `wsPath` callback option defaults to `"/ws"`. Decodes frames and applies `modes.setModes` internally, so consumers only dispatch screen/scroll to `render`. Pairs with the Go `terminal` handler's resume protocol. (`controlFrame` / `wsURL` are also exported for advanced use.)
- **Wire types** — `WireRun`, `ScreenMessage`, `ScrollMessage`, `ModesMessage`, `TitleMessage`, `ResumeAckMessage`, `ServerMessage`, `ControlMessage` re-exported from the package root.

## Wire Protocol

The Go server and TypeScript client communicate over a binary WebSocket frame format rather than shared code. The full byte-level specification — frame headers, all message types, row payloads, attribute flags, and client → server input encoding (mouse, focus, application keypad) — lives in [WIRE_PROTOCOL.md](WIRE_PROTOCOL.md). A breaking change to the wire format must land in both the Go encoder/decoder and the TS decoder in a single release.

## License

GPL-3.0 — see [LICENSE](LICENSE).

## Unsupported by Design

The following VT/DEC features are **intentionally not implemented**. Input bytes for these sequences are consumed (not echoed or half-rendered) but produce no effect. This is a deliberate design choice — not a TODO.

| Category                      | Sequences                                                                                 | Rationale                                                                                                                                                                                                        |
| ----------------------------- | ----------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Selective erase               | DECSCA, DECSED, DECSEL                                                                    | Requires per-cell "protected" attribute; no modern CLI tool uses this legacy VT feature.                                                                                                                         |
| Double-width/height lines     | DECDWL, DECDHL                                                                            | Requires line-level rendering attribute + renderer changes; purely legacy VT220 feature unused by modern apps.                                                                                                   |
| DCS device control            | DECRQSS, XTGETTCAP, tmux passthrough                                                      | Requires full DCS parser state + dispatch infrastructure (~150+ LOC). Apps that probe DECRQSS fall back gracefully when no response arrives.                                                                     |
| Graphics protocols            | Sixel, ReGIS, Kitty image protocol, iTerm inline images                                   | Massive feature (1000+ LOC each); specialized rendering pipeline incompatible with the DOM-based renderer.                                                                                                       |
| NRCS national charsets        | All national replacement character sets (only DEC Special Graphics + ASCII are supported) | Legacy internationalization mechanism superseded by UTF-8. No modern app emits these.                                                                                                                            |
| Exotic SGR attributes         | Fonts 10-20, framed/encircled (51/52/54), superscript/subscript (73-75), ideogram (60-65) | No modern terminal or app uses these attributes; they have no visual representation in standard monospace fonts.                                                                                                 |
| ZWJ emoji grapheme clustering | Zero-width joiner sequences are not clustered into single cells                           | Requires ICU-level grapheme segmentation (~500+ LOC or a runtime dependency). Individual emoji codepoints render correctly; only multi-codepoint ZWJ sequences (family emoji, skin-tone modifiers) may misalign. |
