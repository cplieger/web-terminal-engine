# @cplieger/web-terminal-engine

[![npm](https://img.shields.io/npm/v/@cplieger/web-terminal-engine)](https://www.npmjs.com/package/@cplieger/web-terminal-engine)
[![JSR](https://jsr.io/badges/@cplieger/web-terminal-engine)](https://jsr.io/@cplieger/web-terminal-engine)

> Browser virtual terminal renderer for the [`cplieger/web-terminal-engine`](https://github.com/cplieger/web-terminal-engine) Go module: DOM-based VT500 screen with OSC 8 hyperlink support, scrollback, keyboard mapper, mouse encoder, and binary wire decoder. Zero runtime dependencies.

The browser half of the web-terminal-engine cross-language terminal library. Pairs with the Go server-side packages (`vt`, `terminal`) over a versioned WebSocket protocol; see the [project README](https://github.com/cplieger/web-terminal-engine#readme) for the full story.

## Install

```sh
npx jsr add @cplieger/web-terminal-engine   # JSR (preferred)
npm i @cplieger/web-terminal-engine          # NPM
```

## Usage

One engine per terminal. `createTerminalEngine` builds a renderer, a scroll controller, a mouse controller, a mode state and a connection over your two elements, routes `screen`, `scroll` and `modes` frames to the renderer for you, and returns the five parts plus one `dispose()`:

```typescript
import { createTerminalEngine, keyboard } from "@cplieger/web-terminal-engine";

const termWrap = document.getElementById("term") as HTMLElement; // the scroll container
const output = document.getElementById("term-output") as HTMLElement; // receives the rows

const engine = createTerminalEngine({
  output,
  termWrap,
  wsPath: "/ws",
  maxLines: 5000, // optional retained-line cap for the renderer's store
  callbacks: {
    onMessage: (msg) => {
      if (msg.type === "title") document.title = msg.title;
    },
    onOpen: () => {},
    onClose: () => {},
    onConnecting: () => {},
    onServerRestart: () => {},
    onProcessExit: () => {},
    computeSize: () => engine.renderer.computeSize(),
    initialSize: () => engine.renderer.computeSize(),
  },
});

document.fonts.ready.then(() => {
  engine.renderer.updateFontMetrics(); // measure the real font before announcing a size
  engine.connection.connect();
});

const onKeydown = (ev: KeyboardEvent): void => {
  const r = keyboard.mapKeyboardEvent(ev, engine.modes);
  if (r.kind === "send") {
    engine.connection.sendBinary(new TextEncoder().encode(r.bytes));
    ev.preventDefault();
  }
  if (r.kind === "scroll-up" || r.kind === "scroll-down") ev.preventDefault();
};
document.addEventListener("keydown", onKeydown);

// Called when the terminal leaves the page (a route change, a panel close):
function unmountTerminal(): void {
  document.removeEventListener("keydown", onKeydown);
  engine.dispose();
}
```

The five factories are also available on their own. Composing them by hand means passing every state dependency explicitly (there is no default instance to fall back to) and routing frames to the renderer yourself:

```typescript
import {
  createConnection,
  createModeState,
  createMouseController,
  createRenderer,
  createScrollController,
} from "@cplieger/web-terminal-engine";

const modes = createModeState();
const scroll = createScrollController({
  scrollEl: termWrap,
  onScrollPosition: () => renderer.handleScrollPosition(),
});
const renderer = createRenderer({
  output,
  termWrap,
  scroll,
  modes,
  requestHistory: (from, max) => connection.requestHistory(from, max),
  historyBudget: () => connection.historyBudget(),
});
const mouse = createMouseController({
  modes,
  termElement: () => termWrap,
  gridElement: () => output, // the rows' own box: no padding, no scrollbar gutter
  cellSize: () => renderer.cellSize(),
  gridSize: () => renderer.gridSize(), // the RENDERED grid, not the size this client would like
  sendReport: (data) => connection.sendEphemeral(data),
});
const connection = createConnection({
  renderer,
  modes,
  mouse,
  callbacks: {
    onMessage: (msg) => {
      if (msg.type === "screen") renderer.handleScreen(msg);
      else if (msg.type === "scroll") renderer.handleScroll(msg);
      else if (msg.type === "modes") renderer.updateReverseVideo();
    },
    onOpen: () => {},
    onClose: () => {},
    computeSize: () => renderer.computeSize(),
  },
});
connection.connect();
```

### Two engines on one page

Two engines share nothing: each holds its own DOM handles, timers, listeners, socket and mode state, and the `--char-w` custom property is written on each engine's own `termWrap`, not on `document.documentElement`. The one thing two engines can collide on is the unmanaged session id, which lives in `sessionStorage` per browser tab: two engines that both run without a session manager (`connect()` with no `setSession`) and share the default `sessionIdKey` attach to the same session. Pass a distinct `sessionIdKey` to each (`"vterm-session-id"` and `"vterm-session-id:2"`, say). A managed engine (`setSession(id)`) never mints that id and needs no key.

### `dispose()`

`engine.dispose()` disposes the five parts in reverse build order: the connection closes its socket with code 1000 (no `onClose` fires), stops its heartbeat, history and reconnect timers, drops every session's queued bytes and detaches your callbacks; the mouse controller detaches its four listeners and cancels its pending motion frame; the renderer cancels its pending frame and blink interval, removes its `visibilitychange` listener, empties `output`, removes its overlays and the `--char-w`, `cursor-blink-off` and `term-reverse-video` marks from `termWrap`, and drops every element reference; the scroll controller removes its `scroll` listener; the mode state resets to `POWER_ON_MODES`. A callback may call `dispose()` on the engine that invoked it; the connection stops after the callback returns. Every method stays callable afterwards and answers an empty value rather than throwing:

| Part         | Method                                                             | After `dispose()`                                       |
| ------------ | ------------------------------------------------------------------ | ------------------------------------------------------- |
| `renderer`   | `boundStore()`                                                     | a new, empty `LineStore` on every call                  |
| `renderer`   | `getHighestIndex()`, `getReplayBoundary()`                         | `-1`                                                    |
| `renderer`   | `lastBrowseActivityMs()`, `browseCacheSize()`, `pendingRowCount()` | `0`                                                     |
| `renderer`   | `replayMaxForResume()`                                             | `5000`                                                  |
| `renderer`   | `captureViewMemory()`, `pendingRestoreAbs()`                       | `null`                                                  |
| `renderer`   | `computeSize()`, `gridSize()`                                      | `{ cols: 0, rows: 0 }`                                  |
| `renderer`   | `cellSize()`                                                       | `{ width: 0, height: 0 }`                               |
| `renderer`   | `getCursorPx()`                                                    | `{ left: 0, top: 0, cellH: 0 }`                         |
| `scroll`     | `isUserScrolledUp()`                                               | `false`                                                 |
| `scroll`     | `currentScrollTop()`                                               | `0`                                                     |
| `connection` | `sendBinary()`, `sendEphemeral()`, `requestHistory()`              | `false`                                                 |
| `connection` | `historyBudget()`, `serverEpochOf()`                               | `0`                                                     |
| `connection` | `currentSessionId()`                                               | `""`                                                    |
| `modes`      | the nine readers, `snapshot()`                                     | the `POWER_ON_MODES` values; `applySnapshot` is a no-op |

`createTerminalEngine` and each factory throw when a listener or timer cannot be acquired, and a throw leaves nothing behind: the parts built before it are disposed in reverse order, so there is no partially built engine to dispose.

## API

- **`createTerminalEngine(options)`** — builds the five parts below over `output` and `termWrap`, wires them together, routes `screen`, `scroll` and `modes` frames to the renderer before your `onMessage` sees them, and returns a `TerminalEngine` (`renderer`, `scroll`, `connection`, `mouse`, `modes`, `dispose`). Options: `callbacks` (a `ConnectionCallbacks`), `wsPath`, `sessionIdKey`, `maxLines`, `onCursorMove`, `onUserScrollChange`, `initialModes`. It neither connects nor measures fonts; call `renderer.updateFontMetrics()` when your font is ready and `connection.connect()` to start.
- **`createRenderer(options)` → `Renderer`** — DOM renderer driven by `ScreenMessage` / `ScrollMessage` frames over `output` inside `termWrap`, with an injected `scroll` controller and `modes` state. `maxLines` is the retained-line cap of its store (memory-constrained consumers pass a smaller budget; history above the cap is evicted from the top in batches, and the live screen is never evicted, so a cap at or below the terminal height keeps the full screen with no scrollback). `handleScreen`, `handleScroll`, `handleScrollPosition`, `updateFontMetrics`, `computeSize`, `cellSize`, `gridSize`, `getCursorPx`, `setPredictedCursor`, `bind`, `boundStore`, `captureViewMemory`, `resetScreen`, `resetScrollback`, `getHighestIndex`, `getReplayBoundary`, `noteResumeBounds`, `updateReverseVideo`, `dispose`.
- **`keyboard`** — Translates `KeyboardEvent` to terminal byte sequences. `mapKeyboardEvent(ev, modes)`, `bracketTextForPaste(text, modes)`, `kittyDisambiguateActive(modes)`, `prepareTextForTerminal`, `ctrlByteFor`, plus the shared logical-key encodings (`plainCursorKeySeq`, `plainEscapeSeq`) the toolbar module reuses. Every mode-dependent function takes the mode state as an argument; an engine's `modes` satisfies it.
- **`toolbar`** — On-screen mobile toolbar widget. `bindMobileToolbar({toolbar, send, modes, ids?})` wires `pointerdown` handlers for an on-screen Ctrl/arrows/Tab/Enter/Esc toolbar (with sticky-Ctrl semantics and kitty/DECCKM-aware arrows byte-identical to the physical-key path, read from the required `modes`), returning a `MobileToolbarController` exposing `applyStickyCtrl`, `setCtrlArmed`, `isCtrlArmed`, and `dispose`.
- **`createMouseController(options)` → `MouseController`**, **`encodeSGR`** — SGR 1006 mouse encoder attached to `termElement()`. The options are a `MouseInputHandler` plus `modes`; the controller exposes `resyncGesture`, `disarmGesture` and `dispose`. Auto-gates on `mouseMode > 0`. `MouseInputHandler` carries two optional members, both defaulting to today's behaviour: `gridElement` names the element whose box IS the grid (the scroll container's own box includes its padding and scrollbar gutter, so it is not that box), and `gridSize` supplies the dimensions of the grid ON SCREEN — `renderer.gridSize`, not the size this client measured — so every report is clamped into it and rows are measured up from the bottom edge (the screen window is the tail of the content). Its required `sendReport(data)` returns whether the report reached a live socket; wire it to `connection.sendEphemeral`. Four consequences of mouse input being best-effort: a release is emitted only for a press that was DELIVERED (a release whose press never arrived is its own desync, because the application never learned the button went down); a drag reports a held button only while the application believes that button is down, so motion for an undelivered gesture is dropped under mode 1002 and reports as no-button under 1003; `resyncGesture()`, called by the connection once the resumeAck has declared the server's capabilities (not from `onOpen`, where the ephemeral channel is not yet known, so the release would take the reliable fallback), emits one release for any button the last reading says is now up, so a drag is not left pressed across a reconnect; and motion is coalesced to one report per animation frame, latest wins, while press, release and wheel are never coalesced (a wheel report is a notch count against a screen, so coalescing loses scroll distance). `resyncGesture` shares one gate with the handlers, and each of them enforces it: a gesture stranded by the application turning tracking off is FORGOTTEN by whichever one next observes tracking off, because a release nothing asked for reaches the PTY as keystrokes. tmux answers the same way at attach, clearing `mouse_drag_flag` as it disables the modes. `disarmGesture()` forgets a held gesture without reporting anything — call it when pointing one terminal's socket at another session, since the record belongs to the session that saw the press. Focus reporting is NOT here: it is transport state, and it lives in `connection.setClientFocus`.
- **`createScrollController(options)` → `ScrollController`** — Auto-follow tracker for the scroll container `scrollEl`, with optional `onUserScrollChange` and `onScrollPosition` callbacks. `stickToBottom`, `scrollToBottom`, `isUserScrolledUp`, `currentScrollTop`, `restoreView`, `adjustForContentShift`, `dispose`, plus two renderer seams: `noteContentShrink(scrollTopBefore)` says a row removal rather than a gesture caused the scroll event about to arrive, and `reconcileScrollRange()` moves the offset back inside the container's range when the container did not reconcile the shrink itself. The second exists because clamping an offset the content shrank out from under is an implementation behaviour and not a specified one: Blink and Gecko reconcile during layout, WebKit does not, and without the correction an iOS viewport is left parked past the end of the content after an application clears the screen, showing background with the content above it until the reader scrolls.
- **`createModeState(initial?)` → `ModeState`**, **`POWER_ON_MODES`** — DEC private mode state, one per terminal (the connection writes it from the server's `ModesMessage` and restores the target session's snapshot on `setSession`). `isBracketedPaste`, `isApplicationCursor`, `getMouseMode`, `isMouseSGR`, `isMousePixels`, `isFocusReporting`, `isApplicationKeypad`, `isReverseVideo`, `getKeyboardFlags`, `applySnapshot`, `snapshot`, `dispose`. `POWER_ON_MODES` is frozen; copy it (`{ ...POWER_ON_MODES, reverseVideo: true }`) before changing a field.
- **`decodeWireBinary(buf)`**: Top-level decoder for the binary WebSocket frames. Returns a `ServerMessage` or `null` for invalid/truncated frames.
- **Wire compatibility metadata**: `WIRE_PROTOCOL_VERSION`, `MIN_SUPPORTED_SERVER_WIRE_VERSION`, `WIRE_INCOMPATIBLE_CLOSE_CODE`, and `WIRE_COMPATIBILITY` publish this client release's directional contract. The same values ship as a language-neutral JSON artifact for non-TypeScript consumers — see [Wire compatibility manifest](#wire-compatibility-manifest).
- **`createConnection(options)` → `Connection`**, **`MAX_REPLAY_LINES`**, **`MAX_OUTBOX_BYTES`**: Client → server WebSocket lifecycle: owns the socket, exponential-backoff reconnect, and the resume/inputAck reliability layer (outbox + server-restart detection). The options are the `renderer` it feeds, the `modes` it writes, the `mouse` it resyncs, your `callbacks`, and the optional `wsPath` (default `"/ws"`) and `sessionIdKey`. `connect`, `sendBinary(bytes)`, `sendEphemeral(text)`, `setClientFocus(focused)`, `sendResize`, `reconnectNow`, `setSession`, `forgetSession`, `adoptPersistedEpoch`, `serverEpochOf`, `currentSessionId`, `historyBudget`, `requestHistory`, `disconnect`, `dispose`. `sendEphemeral` carries BEST-EFFORT input (mouse reports): it sends only when a socket is live right now, returns whether it did, and never touches the outbox or the byte counters, so a report is forgotten rather than replayed against a screen the resume has since repainted. Against a server that declares neither capability it falls back to `sendBinary`, because an unrecognized control is dropped post-latch and total silence is a worse failure than a stale report — on that path the report rides the reliable outbox and can be retransmitted after a reconnect. `setClientFocus` reports the terminal widget's focus: it sends the `focus` control when the server declared it derives the DEC 1004 answer (from attachment state, every attached client's report, and its own `terminal.WithKeepUnfocused`), and otherwise drops the report. The client writes no focus bytes in either case, because one client cannot answer for a session two devices may be attached to and a client-written `CSI I` would override a keep-unfocused policy it cannot see; the next resumeAck re-asserts the current value once the capabilities are known. `ConnectionCallbacks` exposes `onMessage(ServerMessage)`, `onOpen`/`onClose`/`onConnecting`/`onOutboxFull`/`onServerRestart`/`onProcessExit`, `onWireVersionMismatch`, the definitive `onWireIncompatible`, `onResumeBounds`, a `computeSize()` provider, and the optional `getReplayMax` and `initialSize` providers; a member may call `dispose()` on the connection that invoked it. An explicit below-floor server revision or close code 4002 stops automatic reconnects until `disconnect()` or a page reload. Version-silent and future-revision servers remain tolerated. The connection decodes frames internally, applies modes frames to its `modes`, and answers the resume and history questions from its `renderer`, so a consumer only needs to dispatch screen/scroll to the renderer (which `createTerminalEngine` does for you). Prefer this over wiring `WebSocket` + `decodeWireBinary` by hand unless you need full control.
- **`controlFrame(msg)` / `wsURL(proto, host, path?)`** — Low-level helpers for the client → server protocol (0x00-prefixed JSON control frames, WebSocket URL building). Used internally by `connection`; exported for advanced consumers.

Wire types (`WireRun`, `ScreenMessage`, `ScrollMessage`, `ModesMessage`, `TitleMessage`, `ResumeAckMessage`, `ServerMessage`, `ControlMessage`) are re-exported from the package root and match the Go server's wire format byte-for-byte.

## Wire compatibility manifest

The same compatibility numbers are also published as a language-neutral JSON artifact, so a consumer that cannot import TypeScript — a Dockerfile, a shell release gate, a CI script in any language — can read them without scraping source:

```sh
# npm consumer
jq -r .wireCompatibility.protocolVersion \
  node_modules/@cplieger/web-terminal-engine/wire-compatibility.json

# JSR consumer (published as an included file; JSR exports are modules only)
curl -fsSL https://jsr.io/@cplieger/web-terminal-engine/<version>/wire-compatibility.json
```

```json
{
  "schemaVersion": 1,
  "generatedBy": "web/src/test-helpers/wire-manifest.ts",
  "wireCompatibility": {
    "protocolVersion": 4,
    "minimumServerProtocolVersion": 3,
    "incompatibleCloseCode": 4002
  }
}
```

It is generated from `WIRE_COMPATIBILITY` (there is no second copy of the numbers), checked into the repo because the publish pipeline runs no build step, and guarded by a regenerate-and-diff test so it cannot go stale. Its values are pinned to both the TypeScript constants and the Go `terminal` constants, so the three surfaces cannot diverge.

**What you may rely on** (semver-governed public artifact, versioned by the package version):

- The file is present at the package root of every npm tarball and JSR publish, at path `wire-compatibility.json`, and is importable from npm as `@cplieger/web-terminal-engine/wire-compatibility.json`.
- `schemaVersion` is an integer describing this file's LAYOUT, not the wire protocol. Read it first and reject a value you do not understand.
- Within a `schemaVersion`, the fields under `wireCompatibility` keep their names, types and meanings, and equal the correspondingly named `WIRE_COMPATIBILITY` members of the same release.
- New fields may be added under either object in a MINOR release, so parse permissively (ignore unknown keys).

**What is not part of the contract**: `generatedBy` is an informational provenance note. The manifest deliberately carries no package version — the publish step injects that into `package.json` / `jsr.json` after checkout, so a version here would ship frozen at the repo placeholder; read the package version from those files.

**Breaking changes to the manifest** (MAJOR, and always release-noted): removing or renaming a field, changing a field's type or meaning, moving the file, or bumping `schemaVersion`. A change to the wire revision NUMBERS is not a manifest break — reporting them is what the file is for.

## Browser-only

This package depends on `document`, `HTMLElement`, `MessageChannel`, and other DOM APIs, so it only runs in browser-like environments. The companion Go server runs anywhere Go does.

## License

MPL-2.0. See [LICENSE](LICENSE).
