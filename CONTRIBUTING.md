# Contributing to web-terminal-engine

web-terminal-engine is a cross-language terminal library: a Go VT100/VT500 emulator plus
WebSocket session handler, and a browser-side TypeScript renderer. The two
halves never share code — they communicate over a versioned binary wire
protocol. That split is the one thing to internalize before changing
anything here.

## Architecture

Three packages, two languages, one wire contract:

- **`vt/`** (Go) — the VT100/VT500 screen buffer. Parses terminal byte
  streams (CSI/OSC/DCS, SGR, DEC modes, charsets, mouse) into a cell grid
  and renders rows to the wire format. No I/O, no networking.
- **`terminal/`** (Go) — the WebSocket session handler. Bridges a PTY
  (`github.com/creack/pty`) to a browser over `github.com/coder/websocket`,
  driving a `vt` screen and adding reconnect, scrollback replay, adaptive
  ping, and the resume/inputAck reliability layer. `terminal/` also provides
  `SessionManager` (`session_manager.go`, `events.go`) for the multi-session
  `/ws?session=`, `/api/sessions`, and status-SSE surface.
- **`web/`** (TypeScript, published as `@cplieger/web-terminal-engine`) — the browser
  renderer (`render`), keyboard mapper (`keyboard`), mouse/focus encoder
  (`mouse`), DEC-mode state (`modes`), scroll tracker (`scroll`), socket
  lifecycle (`connection`), and the binary frame decoder
  (`decodeWireBinary`). Zero runtime dependencies.

### The wire contract is load-bearing

The Go server and TS client do **not** import shared types — they agree on a
byte-level binary WebSocket frame format. The code is the authoritative
definition (guarded by the round-trip fuzz tests and the `wire-golden/*.bin`
fixtures); the README's "Wire Protocol" section carries the design rationale.
The canonical implementations are:

- Encoder (Go): `terminal/wire_binary.go`
- Decoder (Go): `vt/wire.go`
- Decoder (TS): `web/src/wire-binary.ts`
- Client to server control/input (TS): `web/src/connection.ts`,
  `web/src/wire.ts`, `web/src/wsurl.ts`

There is no version byte in the frame header. The Go module and the npm/JSR
package are released in lockstep, so a breaking wire change **must land in
both the Go encoder/decoder and the TS decoder in a single PR/release**. If
you touch one side of the protocol, update the other side and the round-trip
fuzz tests together (and the README "Wire Protocol" rationale if the model changes).

### Intentional non-features

The README's [Unsupported by Design](README.md#unsupported-by-design) table
lists the VT/DEC features that are deliberate scope decisions, not gaps: input
for those sequences is consumed but produces no effect. Don't file them as bugs
or implement them without first proposing a scope change.

## Local development

The Go packages live at the repo root; the TypeScript package lives in
`web/`. The two toolchains are independent.

### Go (`vt/`, `terminal/`)

```sh
go build ./...
go test ./...
go test -race ./...
golangci-lint run
golangci-lint fmt   # apply gofumpt + gci formatting
```

`go.mod` targets Go 1.26+. Linting is golangci-lint v2 (`.golangci.yaml`):
`golangci-lint run` reports unformatted files as issues, so run
`golangci-lint fmt` before pushing. The config enables a strict linter set
(gosec, gocritic, revive, gocyclo and gocognit at complexity 15, sloglint kv-only, and
the gofumpt/gci formatters with extra rules).

### TypeScript (`web/`)

```sh
cd web
npm install
npm run typecheck        # tsgo -p tsconfig.json
npm run typecheck:tests  # tsgo -p tsconfig.test.json
npm test                 # vitest --run
npm run test:e2e         # Playwright (real-browser render + keyboard); needs `npx playwright install chromium` once
npx eslint .             # strict typed linting
npx prettier --check .   # format check (printWidth 100)
```

The `e2e/` suite (Playwright, `*.e2e.test.ts`) runs the real modules in headless
chromium: display conformance against the Go-generated `render-golden` fixtures,
and the keyboard encoder against real browser key events. It is separate from the
happy-dom unit tests under `src/`.

ESLint uses the strictest typed-linting preset
(`strictTypeChecked` + `stylisticTypeChecked`); `tsconfig.json` is equally
strict (`exactOptionalPropertyTypes`, `noUncheckedIndexedAccess`,
`noPropertyAccessFromIndexSignature`, and more). Vitest runs in a `node`
environment with `requireAssertions` on, so every test must assert; the
`fc-strict-setup.ts` setup file wires fast-check for property tests.

CI (`.github/workflows/ci.yaml`) auto-detects both surfaces and runs the Go
and TS jobs in parallel, so run the checks for whichever half you touched —
both if you changed the wire format.

## Conventions and gotchas

- **Tests live beside the code** (`*_test.go`, `*.test.ts`). The suites lean
  heavily on fuzz and adversarial/red-team tests; when you change the parser
  or the wire codec, extend the fuzz corpus rather than only adding a happy
  path. Wire round-trip fuzzing (`terminal/wire_binary_fuzz_test.go`,
  `web/src/wire-binary.fuzz.test.ts`) guards the cross-language contract.
- **Public API is a surface.** The exported Go symbols (`vt.New`,
  `terminal.NewHandler`, the `WithX` options) and the TS package exports
  (re-exported from `web/src/index.ts`) are documented in the READMEs. Keep
  them in sync when you add or rename anything public.
- **Keep the dependency set tiny.** The whole point is "no app-specific
  dependencies": only the Go standard library, `coder/websocket`, and
  `creack/pty` on the Go side, and zero runtime deps on the TS side.

## Publishing model

Releases are automated (`.github/workflows/release.yaml`). The Go module
ships as `github.com/cplieger/web-terminal-engine/v2`; the TS package ships to both npm and
JSR as `@cplieger/web-terminal-engine` from `web/`. Both are versioned in lockstep — the
wire protocol depends on it. You don't publish manually.

## Commits and PRs

Branch from `main`, keep changes focused, and open a PR. Commits follow
[Conventional Commits](https://www.conventionalcommits.org/) (parsed by
git-cliff for release notes): `feat:`, `fix:`, `sec:`, and the non-releasing
`chore:`/`ci:`/`docs:`/`refactor:`/`test:` types. Wire-format changes that
break compatibility are breaking changes (`feat!:` / `BREAKING CHANGE:`).

## Conduct & security

By participating you agree to the
[Code of Conduct](https://github.com/cplieger/.github/blob/main/CODE_OF_CONDUCT.md).
Report security vulnerabilities through the
[security policy](https://github.com/cplieger/.github/blob/main/SECURITY.md) —
never in a public issue.
