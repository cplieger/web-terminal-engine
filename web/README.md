# @cplieger/web-terminal-engine

[![npm](https://img.shields.io/npm/v/@cplieger/web-terminal-engine)](https://www.npmjs.com/package/@cplieger/web-terminal-engine)
[![JSR](https://jsr.io/badges/@cplieger/web-terminal-engine)](https://jsr.io/@cplieger/web-terminal-engine)

> Browser virtual terminal renderer for the [`cplieger/web-terminal-engine`](https://github.com/cplieger/web-terminal-engine) Go module: DOM-based VT500 screen with OSC 8 hyperlink support, scrollback, keyboard mapper, mouse encoder, and binary wire decoder. Zero runtime dependencies.

The browser half of the web-terminal-engine cross-language terminal library. Pairs with the Go server-side packages (`vt`, `terminal`) over a binary WebSocket protocol; see the [project README](https://github.com/cplieger/web-terminal-engine#readme) for the full story.

## Install

```sh
npx jsr add @cplieger/web-terminal-engine   # JSR (preferred)
npm i @cplieger/web-terminal-engine          # NPM
```

## Usage

```typescript
import {
  render,
  keyboard,
  mouse,
  scroll,
  modes,
  decodeWireBinary,
} from "@cplieger/web-terminal-engine";

const wrap = document.getElementById("term") as HTMLElement;
const out = document.getElementById("term-output") as HTMLElement;

render.init({ output: out, termWrap: wrap });
scroll.init({ scrollEl: wrap });
mouse.init({
  send: (data) => ws.send(data),
  cellSize: () => ({ width: cellW, height: cellH }),
  termElement: () => wrap,
});

ws.binaryType = "arraybuffer";
ws.addEventListener("message", (ev) => {
  const msg = decodeWireBinary(ev.data);
  if (!msg) return;
  switch (msg.type) {
    case "screen":
      render.handleScreen(msg);
      break;
    case "scroll":
      render.handleScroll(msg);
      break;
    case "modes":
      modes.setModes(
        msg.bracketedPaste,
        msg.applicationCursor,
        msg.mouseSGR,
        msg.focusReporting,
        msg.mouseMode,
        msg.applicationKeypad,
        msg.reverseVideo,
      );
      break;
    case "title":
      document.title = msg.title;
      break;
  }
});

document.addEventListener("keydown", (ev) => {
  const r = keyboard.mapKeyboardEvent(ev);
  if (r.kind === "send") {
    ws.send(r.bytes);
    ev.preventDefault();
  }
  if (r.kind === "scroll-up" || r.kind === "scroll-down") ev.preventDefault();
});
```

## API

- **`render`** — DOM renderer driven by `ScreenMessage` / `ScrollMessage` frames. `init`, `handleScreen`, `handleScroll`, `updateFontMetrics`, `computeSize`, `getCursorPx`, `setPredictedCursor`, `resetScreen`, `resetScrollback`, `getScrollbackRowCount`, `updateReverseVideo`.
- **`keyboard`** — Translates `KeyboardEvent` to terminal byte sequences. `mapKeyboardEvent`, `bracketTextForPaste`, `prepareTextForTerminal`, `ctrlByteFor`. Honors `applicationCursor`, `applicationKeypad`, `bracketedPaste`. For touch / mobile UIs, `bindMobileToolbar({toolbar, send, ids?})` wires `pointerdown` handlers for an on-screen Ctrl/arrows/Tab/Enter/Esc toolbar (with sticky-Ctrl semantics and DECCKM-aware arrows), returning a `MobileToolbarController` exposing `applyStickyCtrl`, `setCtrlArmed`, `isCtrlArmed`, and `dispose`.
- **`mouse`** — SGR 1006 mouse + focus reporting encoder. `init`, `encodeSGR`, `MouseInputHandler`. Auto-gates on `mouseMode > 0`.
- **`scroll`** — Auto-follow tracker for the scroll container. `init`, `scrollToBottom`, `suppressScroll`, `isUserScrolledUp`, `isInUserScroll`.
- **`modes`** — DEC private mode state (synced from server's `ModesMessage`). `setModes`, `isBracketedPaste`, `isApplicationCursor`, `getMouseMode`, `isMouseSGR`, `isFocusReporting`, `isApplicationKeypad`, `isReverseVideo`.
- **`decodeWireBinary(buf)`** — Top-level decoder for the binary WebSocket frames. Returns a `ServerMessage` or `null` for invalid/truncated frames.
- **`connection`** — Client → server WebSocket lifecycle: owns the socket, exponential-backoff reconnect, and the resume/inputAck reliability layer (outbox + server-restart detection). `init(callbacks)`, `connect`, `sendBinary(bytes)`, `sendResize`, `reconnectNow`. The callbacks expose `onMessage(ServerMessage)`, `onOpen`/`onClose`/`onConnecting`/`onOutboxFull`/`onServerRestart`, a `computeSize()` provider, and an optional `wsPath` (defaults to `"/ws"`). It decodes frames internally and applies `modes.setModes` for you, so a consumer only needs to dispatch screen/scroll to `render`. Prefer this over wiring `WebSocket` + `decodeWireBinary` by hand unless you need full control.
- **`controlFrame(msg)` / `wsURL(proto, host, path?)`** — Low-level helpers for the client → server protocol (0x00-prefixed JSON control frames, WebSocket URL building). Used internally by `connection`; exported for advanced consumers.

Wire types (`WireRun`, `ScreenMessage`, `ScrollMessage`, `ModesMessage`, `TitleMessage`, `ResumeAckMessage`, `ServerMessage`, `ControlMessage`) are re-exported from the package root and match the Go server's wire format byte-for-byte.

## Browser-only

This package depends on `document`, `HTMLElement`, `MessageChannel`, and other DOM APIs, so it only runs in browser-like environments. The companion Go server runs anywhere Go does.

## License

GPL-3.0 — see [LICENSE](../LICENSE).
