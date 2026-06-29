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
| Engine (Go) | `github.com/cplieger/web-terminal` — pkgs `vt`, `terminal` | VT500 screen buffer; PTY + WebSocket + wire encode; scrollback; ping. |
| Engine (TS) | `@cplieger/web-terminal` (same repo, `web/`) | render, scroll, connection, store, wire-binary, wire, wsurl, keyboard, mouse, modes, types, reconnect. Decode + render rows into a container; input/scroll/reconnect. No app UI, no CSS. |
| Wire contract | (in the engine repo) | `WIRE_PROTOCOL.md` + `wire-golden/` byte fixtures tested by BOTH the Go encoder and TS decoder. The drift guard. |
| Reference UI | `@cplieger/web-terminal-ui` (new repo) | The full default UI built on the engine: textarea input model, mobile key toolbar, context menu, IME (composition), predictive echo, viewport/keyboard-inset handling, CSS, optional HTML scaffold + font loading. Optional — consumers can skip it and build on the engine. |
| Generic server + image | `cplieger/web-terminal-server` → `ghcr.io/cplieger/web-terminal` (new) | Thin Go server: mount `terminal.Handler` + serve `web-terminal-ui`, env-configured (addr, command, workdir, scrollback, auth). The standalone "native-touch web terminal for any command." |
| kiro-cli integration | `cplieger/vibecli` (thin) | kiro-cli install model + deployment; consumes engine (Go) + `web-terminal-ui`. |
| Chat app | `cplieger/vibekit` | Consumes engine (Go + TS) directly; its own UI chrome. |

Dependency flow: `web-terminal` (engine) ← `web-terminal-ui` ← {`web-terminal-server`, `vibecli`}; and `web-terminal` (engine) ← `vibekit` (engine directly). No cycles.

## 2. File moves (from the current `rebuild/terminal-viewer` branches)

Engine repo (rename `vterm` → `web-terminal`):
- `vt/**` → unchanged (Go pkg `vt`).
- `terminal/**` → unchanged (Go pkg `terminal`).
- `web/src/**` (render, scroll, connection, store, wire-binary, wire, wsurl, keyboard, mouse,
  modes, types, reconnect, index) → unchanged content; published as `@cplieger/web-terminal`.
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

TS engine `@cplieger/web-terminal` (current `web/src/index.ts`, unchanged):
- `render`: init, handleScreen, handleScroll, updateReverseVideo, updateFontMetrics,
  computeSize, getCursorPx, setPredictedCursor, getHighestIndex, noteResumeBounds,
  resetScreen, resetScrollback.
- `scroll`: init, stickToBottom, scrollToBottom, isUserScrolledUp.
- `connection`: init (Callbacks incl. getHaveThrough, onResumeBounds), connect, reconnectNow,
  sendBinary, sendResize.
- `keyboard`: mapKeyboardEvent, bracketTextForPaste, prepareTextForTerminal. Plus `modes`,
  `mouse`, `types`, `decodeWireBinary`, `controlFrame`, `wsURL`.

Go engine `github.com/cplieger/web-terminal`:
- `terminal.NewHandler(cmd []string, opts ...Option)` with `WithWorkDir`,
  `WithScrollbackCapacity`, `WithLogger`, `WithEnv`, `WithAcceptOptions`; `Handler.RegisterRoutes`,
  `Handler.Shutdown`. `vt.New`, `vt.Screen`.

Reference UI `@cplieger/web-terminal-ui`:
- `mount(opts: { container, wsPath?, ... }): TerminalUI` — wires the engine to the DOM, owns
  the textarea input model + toolbar + context menu + IME + predictive echo + viewport. Ships
  CSS and an optional HTML scaffold. This is the surface a consumer customizes or replaces.

## 4. Consumer updates

- **vibecli**: `go.mod` require `github.com/cplieger/web-terminal`; `static-src/package.json`
  deps `@cplieger/web-terminal` + `@cplieger/web-terminal-ui`. `routes.go` unchanged
  (`terminal.NewHandler(..., WithScrollbackCapacity(5000))`). `app.ts` shrinks to: import
  `web-terminal-ui` `mount()` + kiro-cli-specific config. `index.html`/CSS come from the UI pkg.
- **vibekit**: bump `go.mod` + `package.json` from `vterm`/`@cplieger/vterm` to
  `web-terminal`/`@cplieger/web-terminal`. `shell.ts` import path changes only (it uses the
  engine directly; brick-7 already added getHaveThrough/onResumeBounds).

## 5. Sequencing (pre-publish, all cheap)

1. Rename engine repo `vterm` → `web-terminal`: Go module path, npm package name, `index.ts`
   unchanged, `WIRE_PROTOCOL.md` stays. Update the dev-overlay import paths in vibecli/vibekit.
2. Add the `wire-golden/` contract test (Go + TS) + a `protocolVersion` constant in the resume
   handshake (fail-fast on a future engine/UI version skew).
3. Extract `web-terminal-ui` from vibecli `static-src` (move files, define `mount()`,
   make it publish-ready and consumer-agnostic via opts).
4. Create `web-terminal-server` (thin server + bundled UI) → the `ghcr.io/cplieger/web-terminal`
   image, with auth/exposure guidance baked into its README and default config.
5. Point vibecli at the engine + UI packages; slim `app.ts` to kiro-cli glue.
6. Point vibekit at the engine package.
7. First lockstep publish: Go module tag + npm `@cplieger/web-terminal` + `@cplieger/web-terminal-ui`
   + the container image. After this point, wire changes follow the cross-language pairing rule
   within the engine repo (one PR, golden fixtures updated).

## 6. What does NOT change

The rebuild itself (bricks 1-7 + the budgeted-render burst fix) is untouched — same code, same
behavior, same tests. This restructure only moves files, renames the module/package, extracts
the UI, and adds the server/container. The device-validation checklist (REBUILD.md §11) is
still the gate before relying on it on a real iPhone.
