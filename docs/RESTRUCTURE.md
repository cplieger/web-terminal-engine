# web-terminal restructure (Option A)

Status: PLAN. Companion to `REBUILD.md` (which covers the engine rewrite, bricks 1-7).
This doc covers the repo/package reorganization that lands WITH the rebuild's first
publish. It is a reorg + rename + extract + publish — no behavior change to the bricks.

## 0. Decisions

- **Name: `web-terminal`** (generic, descriptive, namespaced). Chosen after an extensive
  search showed every distinctive single word in the terminal/aviation/CRT/historic space is
  taken in dev tooling (vterm→Emacs vterm + libvterm; domterm→PerBothner's DomTerm; wterm,
  webterm, waveterm, tarmac, concourse, squawk, glyph, phosphor, cathode, satin, slipstream,
  buttery, ghee — all taken). A scoped descriptive name sidesteps the collision problem
  entirely: it does not need global uniqueness because it lives under `@cplieger` /
  `github.com/cplieger`. Rejected `DOMterm` specifically because DomTerm is an existing
  DOM-based terminal (exact-niche confusion).
- **Per-artifact names.** `web-terminal` is the umbrella/family name, used by no single
  artifact. The three published artifacts carry role suffixes: the engine is
  `web-terminal-engine` (`github.com/cplieger/web-terminal-engine` +
  `@cplieger/web-terminal-engine`), the reference UI is `@cplieger/web-terminal-ui`, and the
  generic server is `web-terminal-server` (image `ghcr.io/cplieger/web-terminal-server` —
  matching its repo, like every other app image in the fleet). The engine took the `-engine`
  suffix rather than the bare name so all three read uniformly and the engine repo never
  collides with the server's image name.
- **Option A** (from REBUILD.md §6 discussion): the Go backend and TS frontend engine stay in
  ONE repo (the wire-protocol boundary; keeping both halves in one PR is the cheapest drift
  guard). Extract the UI into its own package; add a generic container. This was chosen over a
  full Go/TS repo split (Option B) to avoid per-wire-change cross-repo release coordination.
- This is the right moment to do it: nothing is published yet (all work is on
  `rebuild/terminal-viewer` branches), so the rename + reorg is a pre-publish find/replace with
  zero released versions to coordinate.

## 1. Target layout

| Tier | Artifact | Contents |
|---|---|---|
| Engine (Go) | `github.com/cplieger/web-terminal-engine` — pkgs `vt`, `terminal` | VT500 screen buffer; PTY + WebSocket + wire encode; scrollback; ping. |
| Engine (TS) | `@cplieger/web-terminal-engine` (same repo, `web/`) | render, scroll, connection, store, wire-binary, wire, wsurl, keyboard, mouse, modes, types, reconnect. Decode + render rows into a container; input/scroll/reconnect. No app UI, no CSS. |
| Wire contract | (in the engine repo) | `WIRE_PROTOCOL.md` + `wire-golden/` byte fixtures tested by BOTH the Go encoder and TS decoder. The drift guard. |
| Reference UI | `@cplieger/web-terminal-ui` (new repo) | The full default UI built on the engine: textarea input model, mobile key toolbar, context menu, IME (composition), predictive echo, viewport/keyboard-inset handling, CSS, optional HTML scaffold + font loading. Optional — consumers can skip it and build on the engine. |
| Generic server + image | `cplieger/web-terminal-server` → `ghcr.io/cplieger/web-terminal-server` (new) | Thin Go server: mount `terminal.Handler` + serve `web-terminal-ui`, env-configured (addr, command, workdir, scrollback, auth). The standalone "native-touch web terminal for any command." |
| kiro-cli integration | `cplieger/vibecli` (thin) | kiro-cli install model + deployment; consumes engine (Go) + `web-terminal-ui`. |
| Chat app | `cplieger/vibekit` | Consumes engine (Go + TS) directly; its own UI chrome. |

Dependency flow: `web-terminal` (engine) ← `web-terminal-ui` ← {`web-terminal-server`, `vibecli`}; and `web-terminal` (engine) ← `vibekit` (engine directly). No cycles.

## 2. File moves (from the current `rebuild/terminal-viewer` branches)

Engine repo (rename `vterm` → `web-terminal-engine`):
- `vt/**` → unchanged (Go pkg `vt`).
- `terminal/**` → unchanged (Go pkg `terminal`).
- `web/src/**` (render, scroll, connection, store, wire-binary, wire, wsurl, keyboard, mouse,
  modes, types, reconnect, index) → unchanged content; published as `@cplieger/web-terminal-engine`.
- Add `wire-golden/` + a Go test and a TS test that both assert against the same fixture bytes.

Reference UI repo (new `web-terminal-ui`, extracted from vibecli):
- `vibecli/static-src/app.ts` → split: the terminal-UI wiring (focus model, input/keydown,
  tap-to-focus, context menu, key toolbar, sticky-Ctrl) becomes `web-terminal-ui`'s `mount()`;
  only kiro-cli-specific glue stays in vibecli.
- `vibecli/static-src/composition.ts` (IME) → `web-terminal-ui`.
- `vibecli/static-src/predict.ts` (predictive echo) → `web-terminal-ui`.
- `vibecli/static-src/viewport.ts` (keyboard insets) → `web-terminal-ui`.
- `vibecli/static-src/status.ts` (connection banner/toast) → `web-terminal-ui`.
- `vibecli/static-src/css/*` + `vibecli/static/index.html` (the scaffold) → `web-terminal-ui`.

Generic server (new `web-terminal-server`):
- New `main.go`: `terminal.NewHandler(cmd, WithWorkDir, WithScrollbackCapacity, ...)` + serve
  the bundled `web-terminal-ui` assets. Env: `WT_ADDR`, `WT_CMD`, `WT_WORKDIR`,
  `WT_SCROLLBACK`, plus an auth toggle. **Flag at creation: a network-exposed terminal server
  MUST ship with auth guidance** (the container is a remote shell to `WT_CMD`); default to
  bind-localhost + document the reverse-proxy/auth story before any public exposure.

## 3. Public API surface (freeze before publish)

TS engine `@cplieger/web-terminal-engine` (current `web/src/index.ts`, unchanged):
- `render`: init, handleScreen, handleScroll, updateReverseVideo, updateFontMetrics,
  computeSize, getCursorPx, setPredictedCursor, getHighestIndex, noteResumeBounds,
  resetScreen, resetScrollback.
- `scroll`: init, stickToBottom, scrollToBottom, isUserScrolledUp.
- `connection`: init (Callbacks incl. getHaveThrough, onResumeBounds), connect, reconnectNow,
  sendBinary, sendResize.
- `keyboard`: mapKeyboardEvent, bracketTextForPaste, prepareTextForTerminal. Plus `modes`,
  `mouse`, `types`, `decodeWireBinary`, `controlFrame`, `wsURL`.

Go engine `github.com/cplieger/web-terminal-engine`:
- `terminal.NewHandler(cmd []string, opts ...Option)` with `WithWorkDir`,
  `WithScrollbackCapacity`, `WithLogger`, `WithEnv`, `WithAcceptOptions`; `Handler.RegisterRoutes`,
  `Handler.Shutdown`. `vt.New`, `vt.Screen`.

Reference UI `@cplieger/web-terminal-ui`:
- `mount(opts: { container, wsPath?, ... }): TerminalUI` — wires the engine to the DOM, owns
  the textarea input model + toolbar + context menu + IME + predictive echo + viewport. Ships
  CSS and an optional HTML scaffold. This is the surface a consumer customizes or replaces.

## 4. Consumer updates

- **vibecli**: `go.mod` require `github.com/cplieger/web-terminal-engine`; `static-src/package.json`
  deps `@cplieger/web-terminal-engine` + `@cplieger/web-terminal-ui`. `routes.go` unchanged
  (`terminal.NewHandler(..., WithScrollbackCapacity(5000))`). `app.ts` shrinks to: import
  `web-terminal-ui` `mount()` + kiro-cli-specific config. `index.html`/CSS come from the UI pkg.
- **vibekit**: bump `go.mod` + `package.json` from `vterm`/`@cplieger/web-terminal-engine` to
  `web-terminal`/`@cplieger/web-terminal-engine`. `shell.ts` import path changes only (it uses the
  engine directly; brick-7 already added getHaveThrough/onResumeBounds).

## 5. Sequencing (pre-publish, all cheap)

1. Rename engine repo `vterm` → `web-terminal-engine`: Go module path, npm package name, `index.ts`
   unchanged, `WIRE_PROTOCOL.md` stays. Update the dev-overlay import paths in vibecli/vibekit.
2. Add the `wire-golden/` contract test (Go + TS) + a `protocolVersion` constant in the resume
   handshake (fail-fast on a future engine/UI version skew).
3. Extract `web-terminal-ui` from vibecli `static-src` (move files, define `mount()`,
   make it publish-ready and consumer-agnostic via opts).
4. Create `web-terminal-server` (thin server + bundled UI) → the `ghcr.io/cplieger/web-terminal-server`
   image, with auth/exposure guidance baked into its README and default config.
5. Point vibecli at the engine + UI packages; slim `app.ts` to kiro-cli glue.
6. Point vibekit at the engine package.
7. First lockstep publish: Go module tag + npm `@cplieger/web-terminal-engine` + `@cplieger/web-terminal-ui`
   + the container image. After this point, wire changes follow the cross-language pairing rule
   within the engine repo (one PR, golden fixtures updated).

## 6. What does NOT change

The rebuild itself (bricks 1-7 + the budgeted-render burst fix) is untouched — same code, same
behavior, same tests. This restructure only moves files, renames the module/package, extracts
the UI, and adds the server/container. The device-validation checklist (REBUILD.md §11) is
still the gate before relying on it on a real iPhone.

## 7. CI/tooling is centrally synced — author only the repo-specific files

The two new repos (`web-terminal-ui`, `web-terminal-server`) must NOT hand-author
their CI workflows, lint/format configs, license, or repo-hygiene files. Those are
owned by `cplieger/ci` and pushed in automatically by its `sync.yaml` once the repo
exists on GitHub. `scripts/classify-repos.sh` auto-discovers every non-archived
`cplieger/*` repo (`gh repo list`), regenerates `.github/sync.yml`, and
`repo-file-sync-action` opens an auto-merging `chore(sync):` PR. No manual
`sync.yml` edit is needed — creating the GitHub repo is the only trigger (and that
is the user-gated step). Verified against `reactive`/`actions` (the pure-TS library
template) and the authoritative `ci/.github/sync.yml`.

**Synced in (DO NOT author):**

- Workflows: `.github/workflows/{ci.yaml,codeql.yml,coverage.yml,release.yaml,scorecard.yml,security.yml}`
- TS lint/format (TS repos): `eslint.config.base.mjs`, `.prettierrc.json`, `.stylelintrc.json`, `.htmlvalidate.json`
- Go tooling (Go repos): `.golangci.yaml`, `.gremlins.yaml`
- Hygiene/release: `.editorconfig`, `.gitattributes`, `LICENSE`, `renovate.json`, `cliff.toml`

**Authored per-repo (the only files we write):**

- TS (`web-terminal-ui`, mirror `reactive`/`actions`): `src/**`, `package.json`, `jsr.json`,
  `tsconfig.json`, `tsconfig.tests.json`, `vitest.config.ts`, `eslint.config.mjs`
  (thin — imports the synced `./eslint.config.base.mjs` per the `actions` pattern),
  `.gitignore`, `README.md`, repo-specific `CONTRIBUTING.md`.
- Go (`web-terminal-server`): `go.mod`/`go.sum`, `main.go` (+ support), `Dockerfile`,
  `.gitignore`, `README.md`, `CONTRIBUTING.md`.

**Bootstrap consequence:** until the GitHub repo is created and the first sync lands,
the synced files are absent locally. Local verification therefore relies on the
authored set only — `tsgo` (typecheck) + `vitest` (tests) for TS, `go build`/`go test`
for Go. `eslint`/`prettier` gate post-sync in CI (the base config arrives via sync);
a local lint run can borrow `ci/configs/eslint.config.base.mjs` without committing it.

## 8. Status — local execution complete (NOT pushed)

The §5 sequencing is done locally. Everything is committed on branches; nothing
is pushed (push + GitHub repo creation are user-gated, below).

| Repo | Location | Branch | Head | What landed |
|---|---|---|---|---|
| engine | dir still `vterm` | `rebuild/terminal-viewer` | `440ef26` | module/pkg rename → web-terminal-engine (`83212a2`); wire `protocolVersion` + golden fixtures (`440ef26`) |
| web-terminal-ui | new local repo | `main` | `32c8be8` | extracted reference UI + `mount()`; tsgo + vitest 17/17 + full lint battery green |
| web-terminal-server | new local repo | `main` | `1c42fd1` | thin generic Go server; full dev-build pipeline green; localhost-default + optional auth |
| vibecli | existing | `rebuild/terminal-viewer` | `bb92804` | client slimmed to `mount()`; UI modules + css moved out; dev-build green |
| vibekit | existing | `rebuild/terminal-viewer` | `e76f130` | shell repointed to the renamed engine; `go build` + tsgo green |

Local builds resolve the unpublished engine via a **gitignored** `go.work`
(`use .` + `replace github.com/cplieger/web-terminal-engine => ../vterm`) and a
node_modules overlay of the engine/UI TS. Both are dev-only and never committed
— a plain `go.work use ../vterm` is NOT enough (Go still tries to fetch the
pinned version's go.mod from the proxy; the `replace` reads `../vterm/go.mod`
directly). go.mod/package.json pin a placeholder `v0.1.0`/`0.1.0` until publish.

## 9. Remaining steps — user-gated (and gated on the REBUILD.md §11 iPhone validation)

**Do not proceed past the device-validation gate.** REBUILD.md §11 (touch
select/copy/paste, sleep/wake backfill, scroll-stick, IME, no duplicate appends)
must pass on a real iPhone first — publishing is what makes the rename
irreversible (released module/package versions can't be unpublished).

Then, in dependency order:

1. **Create the two new GitHub repos** (needs `PUBLISH_PAT`): `gh repo create
   cplieger/web-terminal-ui` + `cplieger/web-terminal-server` (public, GPL-3.0),
   push their `main`. Creating the repo is the ONLY trigger needed for
   `cplieger/ci`'s `classify-repos.sh` to discover them and sync in the
   CI/lint/license files (§7) — confirm the `chore(sync)` PRs land + merge before
   trusting CI.

2. **Rename the engine repo + local dir** `vterm` → `web-terminal-engine`: `gh repo
   rename` (GitHub keeps redirects), then rename the local checkout. Update the
   dev-only paths that hardcode `../vterm`: each consumer's gitignored `go.work`
   `replace … => ../vterm`, and the `ENGINE_DIR`/`UI_DIR` defaults in
   `web-terminal-server/scripts/dev-build.sh`, `web-terminal-ui/scripts/verify.sh`,
   and `vibecli/scripts/dev-build.sh`. (The Go module path and npm name are
   ALREADY `web-terminal-engine`; only the directory + GitHub repo name lag.)

3. **First lockstep publish, in order** (the engine has no unpublished deps;
   everything else depends on it):
   - **a. Engine** — merge `rebuild/terminal-viewer`; the hybrid release pipeline
     tags the Go module (`vX.Y.Z`) and publishes the `web/` subpackage to npm +
     JSR as `@cplieger/web-terminal-engine`.
   - **b. web-terminal-ui** — publish npm + JSR `@cplieger/web-terminal-ui` (its
     peer `@cplieger/web-terminal-engine` must be live first).
   - **c. Consumers** (`web-terminal-server`, `vibecli`, `vibekit`) — replace the
     placeholder `v0.1.0`/`0.1.0` pins with the real published versions, run
     `npm install` to **regenerate each `package-lock.json`** (vibecli + vibekit
     locks still reference `@cplieger/vterm`; this is unfixable until the package
     is published with a real integrity hash), bump the Dockerfile
     `ARG …_VERSION` pins, and confirm CI is green. Then `web-terminal-server`'s
     image builds → `ghcr.io/cplieger/web-terminal-server`.
   - **d.** Drop the dev-only `go.work` reliance once the real versions resolve
     from the module proxy.

### Deferred / follow-ups (not blocking)

- **`.kiro` steering docs** still describe `@cplieger/vterm` (`vterm.md`,
  `vibecli.md`, `vibekit*.md`, `shared-libraries.md`): rename the `#vterm` doc to
  `#web-terminal-engine`, fix package names + the "where it's used" inventory, and add
  `web-terminal-ui` / `web-terminal-server` entries. Update in the `.kiro` repo at
  publish.
- **web-terminal-server** has no Go tests (thin glue) → 0% coverage, OpenSSF
  `test` criterion unmet. Add a small `main_test.go` (env parsing,
  `isLoopbackHost`, the basic-auth middleware) when convenient.
- **vibecli static-src** is now glue-only (a single `mount()` call); its
  remaining TS lint deps (`stylelint`, `html-validate`) may be trimmable once the
  lint surface is confirmed post-sync.
