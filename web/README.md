# @cplieger/vterm
> Browser-side terminal renderer, keyboard mapper, and binary wire decoder.

The TypeScript half of the vterm cross-language library. Provides a DOM-based terminal renderer with scrollback, an xterm.js-compatible keyboard event mapper, and a binary wire protocol decoder matching the Go server's format.

## Install
<!-- TODO: registry/pull link -->
`npx jsr add @cplieger/vterm` or `npm i @cplieger/vterm`

## Usage
```typescript
import { render, keyboard, scroll, modes, decodeWireBinary } from "@cplieger/vterm";

render.init({ output: outputEl, termWrap: wrapEl });
scroll.init({ scrollEl: wrapEl });

// On WebSocket binary message:
const msg = decodeWireBinary(event.data);
if (msg?.type === "screen") render.handleScreen(msg);
if (msg?.type === "scroll") render.handleScroll(msg);
if (msg?.type === "modes") modes.setModes(msg.bracketedPaste, msg.applicationCursor);
```

## License
GPL-3.0 — see [LICENSE](../LICENSE).
