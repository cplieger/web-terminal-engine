# Wire Protocol v2

This document specifies the binary WebSocket frame format used between the Go server (`terminal/wire_binary.go`) and the browser client (`web/src/wire-binary.ts`). The Go and TypeScript packages are versioned in lockstep; this wire protocol version is the compatibility contract between them.

## Absolute line indexing (v2)

Every line the server produces is assigned a monotonic **absolute index** (0, 1, 2, … growing for the life of the session). The screen's top row has absolute index `base`; window row `y` is absolute `base + y`. When the screen scrolls, a line keeps its absolute index (it only moves to a lower window row), so the client stores every line in one buffer keyed by absolute index. Applying a line is therefore idempotent: re-delivering a line the client already holds overwrites the same slot and never duplicates. Resume aligns by absolute index rather than a fragile count, which also makes an eviction gap detectable.

The screen frame carries `base`; the scroll frame carries `first_index`; the resumeAck carries `committed` and `oldest_index`. The client's resume control message carries `haveThrough` (the highest absolute index it holds).

## Transport

- WebSocket binary frames (not text).
- All multi-byte integers are **little-endian**.
- Client → server: raw terminal input bytes (no framing).
- Client → server control messages: `0x00` prefix byte + JSON body.
- Server → client: binary frames as described below.

## Frame Header (common to all message types)

| Offset | Size | Field    | Description                                         |
| ------ | ---- | -------- | --------------------------------------------------- |
| 0      | 1    | msg_type | 0=screen, 1=scroll, 2=resumeAck, 3=modes, 4=title   |
| 1      | 8    | inputAck | uint64 — server-confirmed bytesReceived for session |

## MSG_SCREEN (type 0)

Carries the current terminal viewport (sparse — only changed rows).

| Offset | Size | Field         | Description                                    |
| ------ | ---- | ------------- | ---------------------------------------------- |
| 9      | 8    | base          | uint64 — absolute index of the top screen row  |
| 17     | 2    | cursor_row    | uint16                                         |
| 19     | 2    | cursor_col    | uint16                                         |
| 21     | 2    | screen_height | uint16 — full terminal height                  |
| 23     | 2    | num_changed   | uint16 — number of changed rows following      |
| 25     | 1    | cursor_style  | uint8 (DECSCUSR 0-6)                           |
| 26     | 1    | cursor_flags  | bits: 0 hidden, 1 bell, 2 blink, 3 alt-screen  |

Followed by `num_changed` changed-row entries:

| Size | Field    | Description                                         |
| ---- | -------- | --------------------------------------------------- |
| 2    | row_idx  | uint16 — window row index `y` (absolute = base + y) |
| var  | row_data | row payload (below)                                 |

## MSG_SCROLL (type 1)

Carries committed history lines, addressed by absolute index. Used both for lines that scrolled off the live window and for resume replay.

| Offset | Size | Field       | Description                                |
| ------ | ---- | ----------- | ------------------------------------------ |
| 9      | 8    | first_index | uint64 — absolute index of the first line  |
| 17     | 2    | num_lines   | uint16 — number of lines                   |

Followed by `num_lines` row payloads; line `i` has absolute index `first_index + i`.

## MSG_RESUME_ACK (type 2)

Sent in response to a client's `resume` control message.

| Offset | Size | Field        | Description                                          |
| ------ | ---- | ------------ | ---------------------------------------------------- |
| 1      | 8    | inputAck     | uint64 — bytesReceived (in common header)            |
| 9      | 8    | serverEpoch  | uint64 — server boot time (nanoseconds)              |
| 17     | 8    | committed    | uint64 — absolute index one past the newest line     |
| 25     | 8    | oldest_index | uint64 — absolute index of the oldest retained line  |

The client compares `serverEpoch` against its last-seen value to detect server restarts. It compares `oldest_index` against its highest-held index + 1: if `oldest_index` is greater, the history between them was evicted while the client was away, and the client shows a "history trimmed" marker rather than stitching misaligned lines.

## MSG_MODES (type 3)

Announces DEC private mode state changes.

| Offset | Size | Field     | Description                                                                                                                                                                                    |
| ------ | ---- | --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 9      | 1    | flags     | bit 0: bracketed paste (?2004h), bit 1: app cursor (?1h), bit 2: SGR mouse encoding (?1006h), bit 3: focus reporting (?1004h), bit 4: app keypad (DECKPAM), bit 5: reverse video (DECSCNM ?5h) |
| 10     | 2    | mouseMode | uint16: 0=off, 1000=normal (press+release), 1002=button-event (drag), 1003=any-event (move)                                                                                                    |

### Mouse Tracking (client → server input)

When `mouseMode > 0` and SGR encoding is active (bit 2 of flags), the client
encodes mouse events as SGR 1006 sequences and sends them as terminal input:

- Press/move: `ESC[<code;col;rowM` (1-based coords)
- Release: `ESC[<code;col;rowm`
- Button code: 0=left, 1=middle, 2=right, 64=wheel-up, 65=wheel-down
- Modifiers: +4=shift, +8=alt, +16=ctrl, +32=motion

Mode semantics:

- 1000: press + release only (no motion)
- 1002: press + release + drag (motion while button held)
- 1003: press + release + all motion (including no-button moves)

### Focus Reporting (client → server input)

When focus reporting is active (bit 3 of flags), the client sends:

- Focus in: `ESC[I`
- Focus out: `ESC[O`

### Application Keypad Mode (client → server input)

When application keypad mode is active (bit 4 of flags), the client maps
numeric keypad keys to SS3 sequences (`ESC O <letter>`) per VT100 Table 3-8:

- Numpad 0-9: `ESC O p` through `ESC O y`
- Numpad `.`: `ESC O n`
- Numpad `-`: `ESC O m`
- Numpad `+`: `ESC O k`
- Numpad `*`: `ESC O j`
- Numpad `/`: `ESC O o`
- Numpad Enter: `ESC O M`

When inactive, numpad keys send their normal ASCII characters.

### Out-of-scope

- X10 mouse mode (mode 9): not implemented.
- urxvt encoding (mode 1015): not implemented.
- DEFAULT encoding (raw bytes): not implemented; only SGR 1006 is supported.

## MSG_TITLE (type 4)

Carries the window/icon title set by OSC 0, 1, or 2.

| Offset | Size | Field          | Description                         |
| ------ | ---- | -------------- | ----------------------------------- |
| 9      | 2    | title_byte_len | uint16 — UTF-8 byte length of title |
| 11     | var  | title          | UTF-8 bytes                         |

## Row Payload

Used by MSG_SCREEN and MSG_SCROLL:

| Size | Field    | Description                   |
| ---- | -------- | ----------------------------- |
| 2    | num_runs | uint16 — number of style runs |

For each run:

| Size | Field         | Description                                     |
| ---- | ------------- | ----------------------------------------------- |
| 2    | text_byte_len | uint16 — UTF-8 byte length of text              |
| var  | text          | UTF-8 bytes                                     |
| 4    | fg            | int32 — foreground 0xRRGGBB or -1 for default   |
| 4    | bg            | int32 — background 0xRRGGBB or -1 for default   |
| 2    | attrs         | uint16 — attribute bit flags (see below)        |
| 4    | uc            | int32 — underline color 0xRRGGBB or -1 default  |
| 2    | url_byte_len  | uint16 — UTF-8 byte length of URL (0 = no link) |
| var  | url           | UTF-8 bytes (OSC 8 hyperlink URI)               |

## Attribute Flags

| Bit | Attribute        |
| --- | ---------------- |
| 0   | Bold             |
| 1   | Italic           |
| 2   | Underline        |
| 3   | Inverse          |
| 4   | Strikethrough    |
| 5   | Dim              |
| 6   | Hidden           |
| 7   | Blink            |
| 8   | Overline         |
| 9   | Double-underline |

## Client → Server Control Messages

JSON prefixed with `0x00`:

```json
{"type":"resize","cols":120,"rows":30}
{"type":"resume","sessionId":"...","sentBytes":1234,"haveThrough":1233}
```

`haveThrough` is the highest absolute line index the client already holds (`-1` if it holds nothing and wants the full retained history). The server replays lines with absolute index greater than `haveThrough`, clamped into the retained range, and reports any eviction gap via the resumeAck's `oldest_index`.

## Versioning

No version byte is included in the frame header. The Go and TypeScript packages are released in lockstep from the same repository. A version byte will be added if the protocol ever needs breaking changes that cannot be coordinated via a single release.

## Unsupported by Design

Several VT/DEC features are intentionally not implemented in the emulator and therefore never appear on the wire; input bytes for these sequences are consumed but produce no effect. See the [Unsupported by Design](README.md#unsupported-by-design) table in the README for the full list and per-feature rationale.

## Canonical Implementations

- **Encoder (Go):** `terminal/wire_binary.go`
- **Decoder (Go):** `vt/wire.go` (WireRun type definitions)
- **Decoder (TypeScript):** `web/src/wire-binary.ts`
- **Client → server (TypeScript):** `web/src/connection.ts` (socket lifecycle + resume/inputAck), `web/src/wire.ts` (`0x00`-prefixed control frames), `web/src/wsurl.ts` (WebSocket URL building)
