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

```typescript
import {
  render,
  keyboard,
  mouse,
  scroll,
  modes,
  decodeWireBinary,
} from "@cplieger/web-terminal-engine";

const wrap = document.getElementById("term") as HTMLElement;
const out = document.getElementById("term-output") as HTMLElement;

render.init({ output: out, termWrap: wrap }); // optional: maxLines (retained-line cap, default 5000)
scroll.init({ scrollEl: wrap });
mouse.init({
  // Best-effort input: return whether the report reached a live socket, so a
  // refused one is forgotten instead of retried. With the connection module this
  // is `connection.sendEphemeral`.
  sendReport: (data) => {
    ws.send(data);
    return true;
  },
  cellSize: render.cellSize,
  termElement: () => wrap,
  gridElement: () => out, // the rows' own box: no padding, no scrollbar gutter
  gridSize: render.gridSize, // the RENDERED grid, not the size this client would like
});

ws.binaryType = "arraybuffer";
ws.addEventListener("message", (ev) => {
  const msg = decodeWireBinary(ev.data);
  if (!msg) return;
  switch (msg.type) {
    case "screen":
      render.handleScreen(msg);
      break;
    case "scroll":
      render.handleScroll(msg);
      break;
    case "modes":
      modes.setModes(
        msg.bracketedPaste,
        msg.applicationCursor,
        msg.mouseSGR,
        msg.focusReporting,
        msg.mouseMode,
        msg.applicationKeypad,
        msg.reverseVideo,
      );
      break;
    case "title":
      document.title = msg.title;
      break;
  }
});

document.addEventListener("keydown", (ev) => {
  const r = keyboard.mapKeyboardEvent(ev);
  if (r.kind === "send") {
    ws.send(r.bytes);
    ev.preventDefault();
  }
  if (r.kind === "scroll-up" || r.kind === "scroll-down") ev.preventDefault();
});
```

## API

- **`render`** — DOM renderer driven by `ScreenMessage` / `ScrollMessage` frames. `init` (accepts `maxLines`, the retained-line cap — memory-constrained consumers pass a smaller budget; history above the cap is evicted from the top in batches, and the live screen is never evicted, so a cap at or below the terminal height keeps the full screen with no scrollback), `handleScreen`, `handleScroll`, `updateFontMetrics`, `computeSize`, `cellSize`, `gridSize`, `getCursorPx`, `setPredictedCursor`, `resetScreen`, `resetScrollback`, `getHighestIndex`, `noteResumeBounds`, `updateReverseVideo`.
- **`keyboard`** — Translates `KeyboardEvent` to terminal byte sequences. `mapKeyboardEvent`, `bracketTextForPaste`, `prepareTextForTerminal`, `ctrlByteFor`, plus the shared logical-key encodings (`plainCursorKeySeq`, `plainEscapeSeq`) the toolbar module reuses. Honors `applicationCursor`, `applicationKeypad`, `bracketedPaste`.
- **`toolbar`** — On-screen mobile toolbar widget (moved out of `keyboard` in v3). `bindMobileToolbar({toolbar, send, ids?})` wires `pointerdown` handlers for an on-screen Ctrl/arrows/Tab/Enter/Esc toolbar (with sticky-Ctrl semantics and kitty/DECCKM-aware arrows byte-identical to the physical-key path), returning a `MobileToolbarController` exposing `applyStickyCtrl`, `setCtrlArmed`, `isCtrlArmed`, and `dispose`.
- **`mouse`** — SGR 1006 mouse encoder. `init`, `encodeSGR`, `resyncGesture`, `disarmGesture`, `MouseInputHandler`. Auto-gates on `mouseMode > 0`. `MouseInputHandler` carries two optional members, both defaulting to today's behaviour: `gridElement` names the element whose box IS the grid (the scroll container's own box includes its padding and scrollbar gutter, so it is not that box), and `gridSize` supplies the dimensions of the grid ON SCREEN — `render.gridSize`, not the size this client measured — so every report is clamped into it and rows are measured up from the bottom edge (the screen window is the tail of the content). Its required `sendReport(data)` returns whether the report reached a live socket; wire it to `connection.sendEphemeral`. Four consequences of mouse input being best-effort: a release is emitted only for a press that was DELIVERED (a release whose press never arrived is its own desync, because the application never learned the button went down); a drag reports a held button only while the application believes that button is down, so motion for an undelivered gesture is dropped under mode 1002 and reports as no-button under 1003; `resyncGesture()`, called once the resumeAck has declared the server's capabilities (not from `onOpen`, where the ephemeral channel is not yet known, so the release would take the reliable fallback), emits one release for any button the last reading says is now up, so a drag is not left pressed across a reconnect; and motion is coalesced to one report per animation frame, latest wins, while press, release and wheel are never coalesced (a wheel report is a notch count against a screen, so coalescing loses scroll distance). `resyncGesture` shares one gate with the handlers, and each of them enforces it: a gesture stranded by the application turning tracking off is FORGOTTEN by whichever one next observes tracking off, because a release nothing asked for reaches the PTY as keystrokes. tmux answers the same way at attach, clearing `mouse_drag_flag` as it disables the modes. `disarmGesture()` forgets a held gesture without reporting anything — call it when pointing one terminal's socket at another session, since the record belongs to the session that saw the press. Focus reporting is NOT here: it is transport state, and it lives in `connection.setClientFocus`.
- **`scroll`** — Auto-follow tracker for the scroll container. `init`, `stickToBottom`, `scrollToBottom`, `isUserScrolledUp`, `currentScrollTop`, `restoreView`, `adjustForContentShift`, plus two renderer seams: `noteContentShrink(scrollTopBefore)` says a row removal rather than a gesture caused the scroll event about to arrive, and `reconcileScrollRange()` moves the offset back inside the container's range when the container did not reconcile the shrink itself. The second exists because clamping an offset the content shrank out from under is an implementation behaviour and not a specified one: Blink and Gecko reconcile during layout, WebKit does not, and without the correction an iOS viewport is left parked past the end of the content after an application clears the screen, showing background with the content above it until the reader scrolls.
- **`modes`** — DEC private mode state (synced from server's `ModesMessage`). `setModes`, `isBracketedPaste`, `isApplicationCursor`, `getMouseMode`, `isMouseSGR`, `isFocusReporting`, `isApplicationKeypad`, `isReverseVideo`.
- **`decodeWireBinary(buf)`**: Top-level decoder for the binary WebSocket frames. Returns a `ServerMessage` or `null` for invalid/truncated frames.
- **Wire compatibility metadata**: `WIRE_PROTOCOL_VERSION`, `MIN_SUPPORTED_SERVER_WIRE_VERSION`, `WIRE_INCOMPATIBLE_CLOSE_CODE`, and `WIRE_COMPATIBILITY` publish this client release's directional contract. The same values ship as a language-neutral JSON artifact for non-TypeScript consumers — see [Wire compatibility manifest](#wire-compatibility-manifest).
- **`connection`**: Client → server WebSocket lifecycle: owns the socket, exponential-backoff reconnect, and the resume/inputAck reliability layer (outbox + server-restart detection). `init(callbacks)`, `connect`, `sendBinary(bytes)`, `sendEphemeral(text)`, `setClientFocus(focused)`, `sendResize`, `reconnectNow`. `sendEphemeral` carries BEST-EFFORT input (mouse reports): it sends only when a socket is live right now, returns whether it did, and never touches the outbox or the byte counters, so a report is forgotten rather than replayed against a screen the resume has since repainted. Against a server that declares neither capability it falls back to `sendBinary`, because an unrecognized control is dropped post-latch and total silence is a worse failure than a stale report — on that path the report rides the reliable outbox and can be retransmitted after a reconnect. `setClientFocus` reports the terminal widget's focus: it sends the `focus` control when the server declared it derives the DEC 1004 answer (from attachment state, every attached client's report, and its own `terminal.WithKeepUnfocused`), and otherwise writes `CSI I` / `CSI O` itself while the application has 1004 enabled — including on the enable edge, so an application that turns focus reporting on while the tab is already focused is told so immediately. The callbacks expose `onMessage(ServerMessage)`, `onOpen`/`onClose`/`onConnecting`/`onOutboxFull`/`onServerRestart`, `onWireVersionMismatch`, and the definitive `onWireIncompatible`; a `computeSize()` provider; and an optional `wsPath` (defaults to `"/ws"`). An explicit below-floor server revision or close code 4002 stops automatic reconnects until `disconnect()` or a page reload. Version-silent and future-revision servers remain tolerated. The module decodes frames internally and applies `modes.setModes`, so a consumer only needs to dispatch screen/scroll to `render`. Prefer this over wiring `WebSocket` + `decodeWireBinary` by hand unless you need full control.
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
