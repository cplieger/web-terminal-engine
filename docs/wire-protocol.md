# Wire protocol and compatibility

This page covers the frames the Go server and the TypeScript client exchange, and the rules that decide whether two releases can talk to each other. It is for a developer who upgrades one half without the other, or who gates a release on the pair.

The two halves share a protocol over WebSocket, not code. The code is the authoritative byte-level definition. It lives in the Go encoder in `terminal/wire_binary.go`, the Go `WireRun` types in `vt/wire.go`, and the TypeScript decoder in `web/src/wire-binary.ts`. Round-trip fuzz tests and the `wire-golden/*.bin` fixtures keep them aligned.

## Frames

- Server-to-client frames are binary messages with little-endian integers.
- Client-to-server control messages are text frames that carry bare JSON. They cover resize, resume, ping, the protocol upgrade and the `history` page request.
- Binary client frames carry raw terminal input with the full byte range.

A control sent in the wrong encoding is not a control. After the upgrade, the server reads a binary frame as terminal input, so it reaches the program's standard input. Every socket first sends a resume that an older revision 3 server understands, a binary frame starting with `0x00`. It upgrades only after the server's resume reply proves revision 4.

The TypeScript keyboard and mouse code encodes input for DEC modes, such as SGR 1006 mouse reports and the application keypad. The server writes those bytes to the PTY. The `vt` screen reads the program output that turns those modes on. The DEC 1004 focus answer is the exception. The server derives it from the focus each client reports through `connection.setClientFocus`.

## Line indices

Every line gets an absolute index that only grows. Applying the same line twice changes nothing, a resume lines up by index, and a gap left by dropped history is visible. A server epoch tells the client when the server process restarted between two connections. [Scrollback and reconnect](scrollback.md) describes the resume.

## Revisions and floors

You can upgrade the Go and TypeScript artifacts independently, and their package versions need not match. Each side exports its current wire revision and the oldest peer revision it accepts:

| Side | Revision | Floor | Close code |
| --- | --- | --- | --- |
| Go `terminal` | `WireProtocolVersion` | `MinSupportedClientWireVersion` | `WireIncompatibleCloseCode` |
| TypeScript | `WIRE_PROTOCOL_VERSION` | `MIN_SUPPORTED_SERVER_WIRE_VERSION` | `WIRE_INCOMPATIBLE_CLOSE_CODE` |

Both sides currently send revision 4 and accept declared peers from revision 3. `WIRE_COMPATIBILITY` holds the TypeScript values as one object.

- A peer that declares no revision is still supported.
- A declared revision below the receiver's floor is refused with close code 4002.
- A higher revision produces a warning and the connection continues, because the peer may keep the compatible baseline.

In the browser, `onWireVersionMismatch` and `onWireIncompatible` on the connection callbacks report these outcomes. A definite incompatibility stops automatic reconnects. They stay stopped until `disconnect()` clears the terminal state, normally after the old half is updated and the page reloads.

Frozen fixtures from the previous revision, and the previous published decoder, test both directions of compatibility.

## Checking a pair before it ships

`WirePairIncompatibility(WirePair{Server, Client})` checks the compatibility of a declared pair without a running peer. A release gate can refuse a mismatched Go and TypeScript pair before the image ships, instead of meeting close code 4002 at the first connection.

Each `WireEnd` holds one half's revision and its minimum peer revision. The function returns `""` when the pair is compatible, and otherwise a reason that names the half that is behind. A revision exactly at the peer's floor is compatible, as it is at runtime. A value of 0 or less is a caller error here, because it usually means the gate failed to read the constant. At runtime, 0 is a peer that declares no revision.

The function also checks each half on its own. A half whose minimum peer revision is above its own revision could not talk to a copy of itself, so it is reported as corrupt input before any cross-side verdict. A bad pair of numbers then never produces a confident "update your pin" answer.

## The compatibility manifest

Every npm and JSR release carries the TypeScript values as a language-neutral file, `wire-compatibility.json`, at the package root. From npm it imports as `@cplieger/web-terminal-engine/wire-compatibility.json`. A Dockerfile or shell release gate can read it with `jq` instead of reading TypeScript source.

The file is generated from `WIRE_COMPATIBILITY`, carries a `schemaVersion` you must check, and is held to both the TypeScript and the Go constants by tests. On the Go side, `ReadWireManifest(path)` and `DecodeWireManifest(data)` read and validate it. The [package README](../web/README.md#wire-compatibility-manifest) states what you may rely on and what counts as a breaking change to the file.
