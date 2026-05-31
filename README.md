# vterm
> Cross-language terminal emulator and session engine (Go) with browser renderer (TypeScript).

A standalone library that bridges a PTY to a browser WebSocket. The Go packages provide a VT100/VT500 screen buffer with SGR support and a WebSocket-based terminal session handler with reconnect, scrollback replay, and adaptive ping. The TypeScript package provides the browser-side renderer, keyboard mapper, and binary wire decoder. No app-specific dependencies — only the standard library, `github.com/coder/websocket`, and `github.com/creack/pty`.

## Install
<!-- TODO: registry/pull link -->
Go: `go get github.com/cplieger/vterm@latest`  —  TS: `npx jsr add @cplieger/vterm` or `npm i @cplieger/vterm`

## Usage
```go
import (
    "github.com/cplieger/vterm/terminal"
)

h := terminal.NewHandler(terminal.Options{
    Command: []string{"/bin/bash"},
    WorkDir: "/home/user",
})
mux := http.NewServeMux()
h.RegisterRoutes(mux)
// or use h as an http.Handler directly:
// mux.Handle("/ws", h)
```

```typescript
import { render, keyboard, decodeWireBinary } from "@cplieger/vterm";

render.init({ output: document.getElementById("term-output")!, termWrap: document.getElementById("term")! });
// On WebSocket message:
const msg = decodeWireBinary(event.data);
if (msg?.type === "screen") render.handleScreen(msg);
```

## API

### Go packages

- **`vt`** — VT100/VT500 screen buffer: `New(rows, cols)`, `Write([]byte)`, `Resize(rows, cols)`, `RenderRowWire(y)`, `DrainScrollback()`, cursor/mode state.
- **`terminal`** — WebSocket session handler: `NewHandler(Options)`, `RegisterRoutes(mux)`, `ServeHTTP(w, r)`, `Shutdown()`. Handles PTY lifecycle, binary wire protocol, reconnect with scrollback replay, adaptive ping.

### TypeScript (`web/`)

- **`render`** — DOM renderer: `init()`, `handleScreen()`, `handleScroll()`, `updateFontMetrics()`, `computeSize()`.
- **`keyboard`** — Key event mapper: `mapKeyboardEvent()`, `bracketTextForPaste()`, `prepareTextForTerminal()`.
- **`scroll`** — Scroll state tracker: `init()`, `scrollToBottom()`, `isUserScrolledUp()`.
- **`modes`** — DEC private mode state: `setModes()`, `isBracketedPaste()`, `isApplicationCursor()`.
- **`wire-binary`** — Binary frame decoder: `decodeWireBinary()`.
- **`types`** — Shared TypeScript interfaces.

## License
GPL-3.0 — see [LICENSE](LICENSE).
