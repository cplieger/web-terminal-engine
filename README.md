# web-terminal-engine

[![Go Reference](https://pkg.go.dev/badge/github.com/cplieger/web-terminal-engine/v6.svg)](https://pkg.go.dev/github.com/cplieger/web-terminal-engine/v6) [![npm](https://img.shields.io/npm/v/@cplieger/web-terminal-engine)](https://www.npmjs.com/package/@cplieger/web-terminal-engine) [![JSR](https://jsr.io/badges/@cplieger/web-terminal-engine)](https://jsr.io/@cplieger/web-terminal-engine) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/web-terminal-engine/badges/mutation.json)](https://github.com/cplieger/web-terminal-engine/issues?q=label%3Agremlins-tracker) [![Mutation (TS)](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/web-terminal-engine/badges/mutation-ts.json)](https://github.com/cplieger/web-terminal-engine/issues?q=label%3Astryker-tracker)

web-terminal-engine puts a real terminal in a web page, with a Go server that runs each program in a PTY and a TypeScript renderer that draws it in the browser.

The server keeps each terminal's screen and scrollback, so a browser that reconnects, reloads or opens a second tab gets the same screen back. Sessions end when the server restarts. The Go module needs Go 1.27.1 or later and a Unix-like system such as Linux or macOS, and depends on `coder/websocket`, `creack/pty`, `golang.org/x/sys` and `cplieger/runesafe/v2`. The TypeScript package has no runtime dependencies. web-terminal-engine is licensed under MPL-2.0.

## Why use it

web-terminal-engine is built for Go servers that host terminals people come back to, such as a web shell or an agent console. It checks the page's origin but authenticates no one, so add a login.

- A reconnect resends only the lines the browser missed.
- One server runs many sessions, with a REST API, a status stream and a shared tab order and pane layout.
- On Linux, a closed session's processes are stopped, even ones that called `setsid()`.
- The DOM renderer holds your place in scrollback, and a helper turns your own toolbar buttons into keys.
- You can upgrade the Go and TypeScript halves separately.

Consider [xterm.js](https://github.com/xtermjs/xterm.js) if you want a browser terminal you connect to a PTY yourself. It has an optional GPU-accelerated renderer and a screen reader mode. Consider [ttyd](https://github.com/tsl0922/ttyd) if you want a command-line tool that shares a terminal over the web, with basic authentication and SSL.

## Install

```sh
go get github.com/cplieger/web-terminal-engine/v6@latest
npx jsr add @cplieger/web-terminal-engine
npm i @cplieger/web-terminal-engine
```

## Usage

On the server, a handler runs one command per session and serves its WebSocket at `/ws`:

```go
import (
    "log/slog"
    "net/http"

    "github.com/cplieger/web-terminal-engine/v6/terminal"
)

h := terminal.NewHandler(
    []string{"/bin/bash"},
    terminal.WithWorkDir("/home/user"),
    terminal.WithLogger(slog.Default()),
)
mux := http.NewServeMux()
h.RegisterRoutes(mux)
```

In the page, `createTerminalEngine` builds one terminal over two elements and connects it to `/ws`:

```typescript
import { createTerminalEngine, keyboard } from "@cplieger/web-terminal-engine";

const engine = createTerminalEngine({
  termWrap: document.getElementById("term") as HTMLElement, // the scroll container
  output: document.getElementById("term-output") as HTMLElement, // receives the rows
  callbacks: {
    onMessage: (msg) => {
      if (msg.type === "title") document.title = msg.title;
    },
    onOpen: () => {},
    onClose: () => {},
    computeSize: () => engine.renderer.computeSize(),
  },
});

document.addEventListener("keydown", (ev) => {
  const r = keyboard.mapKeyboardEvent(ev, engine.modes);
  if (r.kind === "send") {
    engine.connection.sendBinary(new TextEncoder().encode(r.bytes));
    ev.preventDefault();
  }
});

document.fonts.ready.then(() => {
  engine.renderer.updateFontMetrics();
  engine.connection.connect();
});
```

The engine routes screen frames to the renderer for you, and `engine.dispose()` removes everything it added. `NewSessionManager` runs many sessions behind one set of routes, as [Sessions and processes](docs/sessions.md) shows. To build the terminal from its five browser parts yourself, see the [package README](web/README.md).

## API

- `vt` is the VT100/VT500 screen buffer: `New`, `Write`, `Resize`, row rendering to the wire format, and one-shot reads of replies, clipboard writes, the bell and palette changes.
- `terminal` serves sessions: `NewHandler` and its options, `NewSessionManager`, `MountSessionRoutes`, `NewOriginPolicy`, `StartZombieReaper`, `LogID`, and the wire compatibility constants with `WirePairIncompatibility`.
- The TypeScript package exports `createTerminalEngine` and the five factories it composes, the `keyboard` and `toolbar` namespaces, `decodeWireBinary`, `connectStatusStream`, `LineStore` and the wire types.

The full reference is on [pkg.go.dev](https://pkg.go.dev/github.com/cplieger/web-terminal-engine/v6) and [JSR](https://jsr.io/@cplieger/web-terminal-engine/doc).

## Wire protocol

The Go server and the TypeScript client share a protocol over WebSocket, not code. Server frames are binary. Each connection opens with a resume in the older binary form. After the upgrade to revision 4, client controls are JSON text frames and client input is raw bytes. Every line carries an absolute index, so applying a line twice changes nothing and a resume lines up by index.

Each half states the wire revision it speaks and the oldest revision it accepts from the other half. Today both speak revision 4 and accept revision 3 or later. A peer that states no revision still connects. A peer older than the oldest accepted revision is refused with WebSocket close code 4002. A newer peer gets a warning, and the connection continues.

`WirePairIncompatibility` checks a declared Go and TypeScript pair at build time, so a release gate can refuse a mismatched pair before it ships. Every npm and JSR release also carries the TypeScript values as `wire-compatibility.json`, for a gate that cannot read TypeScript. [Wire protocol and compatibility](docs/wire-protocol.md) has the frame rules and the manifest contract.

## Cross-origin access

A terminal socket is an interactive shell, and the engine allows same-origin pages only by default. That check is the only one on the socket. Go's `http.CrossOriginProtection` middleware passes every `GET` request, and a WebSocket connection opens with a `GET`.

To embed a terminal in another origin, build one policy with `NewOriginPolicy` and pass it to both `WithOriginPolicy` and `WithManagerOriginPolicy`. Entries are exact origins. The policy has no wildcards and refuses `Origin: null`.

Anyone who has a session id can attach to that session, so treat it as a secret. Log it only through `LogID`, which keeps the first 8 bytes and drops the rest. The engine sets `Cache-Control: no-store` on every session REST response and on the status stream. [Security for embedders](docs/security.md) covers the details.

## Unsupported by design

The engine reads these sequences and does nothing with them:

- Double-width and double-height lines.
- Resizing or moving the window from the program.
- tmux control-mode passthrough.
- Sixel, ReGIS, kitty and iTerm images.
- National character sets other than UK.
- Rare SGR attributes such as fonts 10 to 20.
- X11 Xcms color specifications.
- Combining zero-width-joiner emoji into one cell.

[Unsupported VT features](docs/non-goals.md) gives the reasons and lists the device queries the engine answers.

## Related projects

- [web-terminal-ui](https://github.com/cplieger/web-terminal-ui) is the reference touch-first browser UI built on the TypeScript renderer, with tabs, a key bar, IME input and soft-keyboard resizing.
- [web-terminal-server](https://github.com/cplieger/web-terminal-server) is a ready-to-run container that serves a PTY command in the browser over HTTP and WebSocket.

## Documentation

- [Sessions and processes](docs/sessions.md) covers the session manager, its routes, statuses and process cleanup.
- [Security for embedders](docs/security.md) covers origins, session ids, caching and logs.
- [Scrollback and reconnect](docs/scrollback.md) covers history depth, paging and restoring after a reload.
- [Colors, links and input](docs/rendering.md) covers the palette, contrast, character width, links, keyboard and mouse.
- [Wire protocol and compatibility](docs/wire-protocol.md) covers frames, revisions and the manifest.
- [Unsupported VT features](docs/non-goals.md) lists what the engine leaves out and why.
- [Environment variable names](docs/environment-variables.md) lists the setting names servers share.

## Credits

- `vt/screen.go` derives from [tonistiigi/vt100](https://github.com/tonistiigi/vt100), and the DEC Special Graphics table comes from [xterm.js](https://github.com/xtermjs/xterm.js).
- The 16 base colors follow [kitty's default palette](https://github.com/kovidgoyal/kitty/blob/master/kitty/options/definition.py).
- The server runs on [creack/pty](https://github.com/creack/pty) and [coder/websocket](https://github.com/coder/websocket).

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the conventions and how to run the checks.

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI-assisted tooling using [Claude](https://claude.com), [GPT](https://openai.com), and [Kiro](https://kiro.dev). The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

MPL-2.0. See [LICENSE](LICENSE).

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
