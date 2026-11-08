# Wire protocol and compatibility

This page covers the frames the Go server and the TypeScript client exchange, and the rules that decide whether two releases can talk to each other. It is for a developer who upgrades one half without the other, or who gates a release on the pair.

The two halves share a protocol over WebSocket, not code. The code is the authoritative byte-level definition. It lives in the Go encoder in `terminal/wire_binary.go`, the Go `WireRun` types in `vt/wire.go`, and the TypeScript decoder in `web/src/wire-binary.ts`. Round-trip fuzz tests and the `wire-golden/*.bin` fixtures keep them aligned.

## Frames

- Server-to-client frames are binary messages with little-endian integers.
- Client-to-server control messages are text frames that carry bare JSON. They cover resize, [resume](#the-resume-control), ping, the protocol upgrade and the `history` page request.
- Binary client frames carry raw terminal input with the full byte range.
- A client message is at most 64 KiB, and the server closes the socket with 1009 above that, so the client splits input into messages of at most that size. `wire-golden/limits.json` holds this limit and the others both halves share: the resize floor, the replay ceiling and the history page size. A test on each side reads it.

A control sent in the wrong encoding is not a control. After the upgrade, the server reads a binary frame as terminal input, so it reaches the program's standard input. Every socket first sends a resume that an older revision 3 server understands, a binary frame starting with `0x00`. It upgrades only after the server's resume reply proves revision 4.

The TypeScript keyboard and mouse code encodes input for DEC modes, such as SGR 1006 mouse reports and the application keypad. The server writes those bytes to the PTY. The `vt` screen reads the program output that turns those modes on, so the server owns the modes: the client knows them only from the modes frame on its current socket, sent on every resume. Until that frame arrives (or the socket's first screen frame, from a server that sends none) the client's `ModeState.isKnown()` is false and the connection holds every input whose bytes depend on the modes (`DeferredInput`). `POWER_ON_MODES` equals a fresh screen's announcement, pinned by `wire-golden/modes-power-on.bin`. The DEC 1004 focus answer is the exception. The server derives it from the focus each client reports through `connection.setClientFocus`.

## The resume control

A socket's first control is the resume, which binds the socket to a session's input ledger and asks for the history the client lacks. When the client knows its size, it sends one resize before the resume, so the snapshot comes back at that size. The client sends no input on a socket before that socket's first resize, so the PTY has the client's size before it reads a byte from it. Its fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `type` | string | `"resume"`. |
| `sessionId` | string | The input ledger's key. The TypeScript client sends the session id and an id per connection instance, so two connections to one session keep separate ledgers. |
| `sentBytes` | integer | Input bytes the client has put on the wire for this ledger. The resumeAck's `inputAck` answers how many the server received, and the client retransmits the rest. |
| `haveThrough` | integer | The client's replay boundary: the highest line index it holds as committed history, or -1 for none. The TypeScript client sends `renderer.getReplayBoundary()`, never `getHighestIndex()`, which also counts rows the application drew that the server has not committed. Claiming those rows leaves a stale copy of the last screen in scrollback. |
| `replayMax` | integer, optional | The most replay lines the client wants. The server clamps it to 2000 (`maxReplayLines`, in `wire-golden/limits.json`) and reads an absent or malformed value as 2000. |
| `protocolVersion` | integer, optional | The client's wire revision; absent is version-silent ([Revisions and floors](#revisions-and-floors)). |
| `gridFrames` | boolean, optional | The client decodes grid frames ([grid (8)](#grid-8)). |

The replay starts at the largest of `haveThrough + 1`, the oldest retained line, and `committed` minus the clamped `replayMax`. The client predicts that start to tell a clamped replay from missing history. `wire-golden/replay-start.json` holds worked cases that both halves test against.

The server answers with a resumeAck frame ([resumeAck (2)](#resumeack-2)), then, when declared, the grid, then the modes and the title. Then it sends the screen frame and the replay as scroll frames. On the main screen the screen frame goes first, and on the alternate screen the replay does.

## Byte layout

Every server-to-client frame starts with the same header. Integers are fixed-width, and frames carry no length-prefixed dictionary keys and no repeated string identifiers, so they stay small over slow links. This section describes what the encoder and decoder do. Where it disagrees with them, this section is wrong.

### Common header

```text
[1B] msg_type   0=screen, 1=scroll, 2=resumeAck, 3=modes, 4=title, 5=pong, 6=clipboard, 7=ackOnly, 8=grid
[8B] inputAck   uint64  server-confirmed received count on the socket's input ledger
```

### screen (0)

```text
[8B] base          uint64  absolute index of the top screen row; changed[y] -> base+y
[2B] cursor_row    uint16
[2B] cursor_col    uint16
[2B] screen_height uint16  full terminal height; the row list below is sparse
[2B] num_changed   uint16
[1B] cursor_style  uint8   DECSCUSR style 0-6
[1B] cursor_flags  uint8   bit0=hidden, bit1=bell, bit2=blink, bit3=altActive, bit4=scrollbackCleared
For each changed row:
  [2B] row_idx     uint16
  [row payload]
```

### scroll (1)

```text
[8B] first_index  uint64  absolute index of lines[0]; line i applies at first_index+i
[2B] num_lines    uint16
For each line:
  [row payload]
```

### resumeAck (2)

The header's inputAck carries the ack value. The body:

```text
[8B] serverEpoch        uint64  process-start nanoseconds since the Unix epoch
[8B] committed          uint64  absolute index of the next line to commit
[8B] oldestIndex        uint64  absolute index of the oldest retained line
[1B] serverWireVersion  uint8   the server's wire protocol revision
[1B] ackFlags           uint8   capability and condition bits
```

`serverEpoch` identifies one server process, and 0 means the server reports none. The client compares it twice. The bound `LineStore` compares it with the epoch its content came from and discards content from another process. The connection drops the session's queued input when it was meant for another process: the one the session's last answer named or, before the first answer, the one whose content the store showed, so input typed at discarded content goes with it. Either loss is reported instead of hidden.

`serverWireVersion` and `ackFlags` form a length-gated optional tail: a frame from a server that predates them is shorter, and the client reads a short frame as version-silent with every flag clear. `serverWireVersion` lets the client report a stale-bundle skew.

| Bit | Name | Meaning |
| --- | --- | --- |
| 0 | `ledgerLost` | The resume key missed the registry while the client claimed `sentBytes > 0`, so the client drops its in-flight input and notifies instead of replaying. |
| 1 | `historyPaging` | Demand-paged scrollback is served ([Scrollback and reconnect](scrollback.md)). |
| 2 | `serverFocus` | The server derives the DEC 1004 answer from the `focus` control. |
| 3 | `ephemeralInput` | The `ephemeralInput` control is served. |

An older client ignores the tail. A client that reads it masks only the bits it knows.

### ackOnly (7)

The header's inputAck carries the value and there is no body. The flush tick sends it when input was applied but no content frame carried the advanced ack (input into a read that echoes nothing), so acks never depend on output. Older clients ignore the unknown opcode.

### grid (8)

```text
[2B] cols  uint16  the server screen's columns
[2B] rows  uint16  the server screen's rows
```

Sent only to a socket whose resume carried `gridFrames: true`: before the modes and the window on every resume, and before the next modes and screen frame after each size change. The client releases input typed while the modes were unknown as soon as the modes arrive, so the grid it encodes that input against must already be this socket's. A client that never declares it is never sent one, so the frame needs no revision bump. The client predicts and hit-tests against this grid, not its own measured box, which can differ when several clients share a session.

### Row payload

```text
[2B] num_runs    uint16
For each run:
  [2B] text_byte_len  uint16
  [N B] text          UTF-8 bytes
  [4B] fg             int32   -1 = default foreground
  [4B] bg             int32   -1 = default background
  [2B] attrs          uint16  bit flags, see vt.WireRun.A
  [4B] uc             int32   -1 = default underline color
  [2B] url_len        uint16  UTF-8 byte length of the OSC 8 URL; 0 = no link
  [N B] url           UTF-8 bytes of the OSC 8 hyperlink URI
```

The modes (3), title (4), pong (5) and clipboard (6) bodies are defined by their encoders in `terminal/wire_binary.go`.

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
