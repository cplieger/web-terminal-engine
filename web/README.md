# @cplieger/vterm

[![CI](https://github.com/cplieger/vterm/actions/workflows/ci.yaml/badge.svg)](https://github.com/cplieger/vterm/actions/workflows/ci.yaml)
[![npm](https://img.shields.io/npm/v/@cplieger/vterm)](https://www.npmjs.com/package/@cplieger/vterm)
[![JSR](https://jsr.io/badges/@cplieger/vterm)](https://jsr.io/@cplieger/vterm)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPL--3.0-blue.svg)](../LICENSE)

> The browser half of the vterm cross-language terminal library: DOM renderer, keyboard mapper, mouse encoder, and binary wire decoder.

Provides a DOM-based terminal renderer with scrollback, an xterm.js-compatible keyboard event mapper, SGR 1006 mouse encoding, and a binary wire protocol decoder matching the Go server's format.

## Install

`npx jsr add @cplieger/vterm` or `npm i @cplieger/vterm`

## Usage

```typescript
import { render, keyboard, mouse, scroll, modes, decodeWireBinary } from "@cplieger/vterm";

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
