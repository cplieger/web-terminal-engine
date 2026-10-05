# Contributing to web-terminal-engine

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

- A new fixture in `wire-golden/` needs its own decode test in `web/src/wire-golden.node.test.ts` in the same change. That test reads fixtures by name, so without one a layout change fails in Go and passes in the browser.
- After a socket upgrades, send every control through `sendControl` in `web/src/connection.ts`, which sends it as a text frame. The Go server reads a binary frame as terminal input, so a control built with `controlFrame` reaches the user's program.
- Change the wire compatibly by adding a server-to-client opcode or a length-gated frame tail. Older clients skip both, but they misread a field or opcode whose meaning changed.
- A wire revision bump changes `WireProtocolVersion` in `terminal/wire_binary.go` and `WIRE_PROTOCOL_VERSION` in `web/src/wire-compatibility.ts` in one change. The same change updates both codecs, the golden fixtures and the revision stated in [README.md](README.md#wire-protocol) and [docs/wire-protocol.md](docs/wire-protocol.md).
- `wire-golden/v3-published.json` holds the fixtures released at tag `v2.8.0`, and the `@cplieger/web-terminal-engine-v3` devDependency is that release's decoder. The Go and TypeScript tests hold its revision equal to both floors.
- Raise both floors in one change with their tests, the frozen fixtures, the pinned decoder and the floor stated in `README.md` and `docs/wire-protocol.md`. The review then shows which peers stop working.
- An importable file outside `src/`, such as `wire-compatibility.json`, goes in `files` and `exports` in `web/package.json` and in `publish.include` in `web/jsr.json`. JSR `exports` lists modules only.

## Checks

- Run `bash scripts/esctest.sh` after a change to `vt/`. It clones the esctest2 VT conformance suite at a pinned commit into the gitignored `.esctest2/` folder and needs `git`, `python3` and network access. CI skips this gate.
- Run `npm run test:e2e` in `web/` after a change to rendering, scrolling, keyboard encoding or client-to-server framing, and after you regenerate a `render-golden/` fixture. CI does not run this Playwright suite.
- The suite needs Chromium, installed once with `npx playwright install chromium`, and a Go toolchain, because the framing test starts `go run ./internal/e2etestserver` from the repository root.

## Releases

- One tag releases the Go module and the npm and JSR package. A breaking change in either half, TypeScript included, needs the next `/vN` module path in `go.mod` and every internal import, or the release stops before it tags.
- Raising a floor, or any wire change a released peer cannot read, is a breaking change even when no API changes.
- When the wire protocol changes, web-terminal-engine and [web-terminal-ui](https://github.com/cplieger/web-terminal-ui) release together.
