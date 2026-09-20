# Third-party notices

This package includes no third-party code. Two designs are followed without any of their code being included; each is named at the line of ours that follows it.

- The keyboard mapping in `src/keyboard.ts` (the whole module, stated at `src/keyboard.ts:3`) mirrors the coverage of `evaluateKeyboardEvent` in [xterm.js](https://github.com/xtermjs/xterm.js) (`src/common/input/Keyboard.ts`, MIT): every browser `KeyboardEvent` maps either to a byte sequence for the PTY or to a local scrollback action.
- `sendEphemeral` in `src/connection.ts` (`src/connection.ts:740`) refuses to send on a closing socket, as xterm.js's AttachAddon and [ttyd](https://github.com/tsl0922/ttyd) (MIT) both do.
