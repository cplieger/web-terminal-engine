# Contributing to vterm

vterm is a cross-language terminal library: a Go VT100/VT500 emulator plus
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
  ping, and the resume/inputAck reliability layer.
- **`web/`** (TypeScript, published as `@cplieger/vterm`) — the browser
  renderer (`render`), keyboard mapper (`keyboard`), mouse/focus encoder
  (`mouse`), DEC-mode state (`modes`), scroll tracker (`scroll`), socket
  lifecycle (`connection`), and the binary frame decoder
  (`decodeWireBinary`). Zero runtime dependencies.

### The wire contract is load-bearing

The Go server and TS client do **not** import shared types — they agree on a
byte-level binary WebSocket frame format documented in
[WIRE_PROTOCOL.md](WIRE_PROTOCOL.md). The canonical implementations are:

- Encoder (Go): `terminal/wire_binary.go`
- Decoder (Go): `vt/wire.go`
- Decoder (TS): `web/src/wire-binary.ts`
- Client to server control/input (TS): `web/src/connection.ts`,
  `web/src/wire.ts`, `web/src/wsurl.ts`

There is no version byte in the frame header. The Go module and the npm/JSR
package are released in lockstep, so a breaking wire change **must land in
both the Go encoder/decoder and the TS decoder in a single PR/release**. If
you touch one side of the protocol, update the other side, WIRE_PROTOCOL.md,
and the round-trip fuzz tests together.

### Intentional non-features

The README and WIRE_PROTOCOL.md both carry an "Unsupported by Design" table
(selective erase, double-width lines, Sixel/Kitty graphics, NRCS charsets,
exotic SGR, ZWJ clustering, DCS device control). These are deliberate scope
decisions, not gaps — input for these sequences is consumed but produces no
effect. Don't file them as bugs or implement them without first proposing a
scope change.

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
(gosec, gocritic, revive, gocyclo at complexity 18, sloglint kv-only, and
the gofumpt/gci formatters with extra rules).

### TypeScript (`web/`)

```sh
cd web
npm install
npm run typecheck        # tsgo -p tsconfig.json
npm run typecheck:tests  # tsgo -p tsconfig.test.json
npm test                 # vitest --run
npx eslint .             # strict typed linting
npx prettier --check .   # format check (printWidth 100)
```

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
  path. Wire round-trip fuzzing (`terminal/fuzz_wire_roundtrip_test.go`,
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
ships as `github.com/cplieger/vterm`; the TS package ships to both npm and
JSR as `@cplieger/vterm` from `web/`. Both are versioned in lockstep — the
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
