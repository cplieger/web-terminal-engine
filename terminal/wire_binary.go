// Server-to-client frames are little-endian and fixed-width, so they stay small
// on slow links. Each opens with a 1-byte msg_type and the 8-byte inputAck.
// docs/wire-protocol.md "Byte layout" describes them; the encoders below are
// authoritative.

package terminal

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

const (
	wireMsgScreen    byte = 0
	wireMsgScroll    byte = 1
	wireMsgResumeAck byte = 2
	wireMsgModes     byte = 3
	wireMsgTitle     byte = 4
	wireMsgPong      byte = 5
	// wireMsgClipboard carries text an app copied via OSC 52, for the client to
	// write to the system clipboard. New opcodes are back-compatible (older
	// clients ignore unknown message types), so no wireProtocolVersion bump.
	wireMsgClipboard byte = 6
	// wireMsgAckOnly carries a bare inputAck for input that produced no output
	// frame within a flush tick (e.g. typing into `read -s`), so the client's
	// outbox trims promptly even with a silent app. Back-compatible new opcode
	// (older clients ignore unknown message types), so no version bump.
	wireMsgAckOnly byte = 7
	// wireMsgGrid carries the screen's columns and rows, sent only to a socket
	// whose resume declared gridFrames, so it needs no version bump.
	wireMsgGrid byte = 8

	// WireProtocolVersion is the current binary-protocol revision, mirroring
	// WIRE_PROTOCOL_VERSION in web/src/wire-compatibility.ts. Bump it on any
	// change to a frame layout or control-message shape. v4 adds typed framing
	// (text controls, binary input), latched per connection by an `upgrade`
	// control; v3 sentinel framing stays accepted before the latch.
	WireProtocolVersion = 4

	// MinSupportedClientWireVersion is the oldest explicitly declared client
	// revision this server accepts. A version-silent client (0) remains
	// supported; a declared lower revision is refused with
	// WireIncompatibleCloseCode. Higher revisions warn but continue because a
	// future revision alone does not prove that its v4-compatible baseline is
	// unusable. Exported as directional release metadata for Go consumers.
	MinSupportedClientWireVersion = 3

	// Internal aliases keep the wire encoder and negotiation implementation
	// concise while the exported names above form the release metadata API.
	wireProtocolVersion           = WireProtocolVersion
	minSupportedClientWireVersion = MinSupportedClientWireVersion

	// typedFramingMinVersion is the first protocol revision with typed
	// client→server framing; a resume declaring at least this ARMS the
	// connection for the text-control latch.
	typedFramingMinVersion = 4

	// wireAckOffset is the byte offset of the inputAck field in
	// every server→client frame. Used by withClientAck to patch the
	// per-client ack into a pre-encoded template.
	wireAckOffset = 1
	wireAckSize   = 8

	// modeFlagBracketedPaste / modeFlagAppCursorKeys are the bit
	// positions in the modes message's flags byte. New flags MUST be
	// appended at higher bit positions to preserve back-compat with
	// older clients (unknown bits are ignored).
	modeFlagBracketedPaste byte = 1 << 0
	modeFlagAppCursorKeys  byte = 1 << 1
	modeFlagMouseSGR       byte = 1 << 2
	modeFlagFocusReporting byte = 1 << 3
	modeFlagAppKeypad      byte = 1 << 4
	modeFlagReverseVideo   byte = 1 << 5
	modeFlagMousePixels    byte = 1 << 6

	// resumeAckFlagLedgerLost is bit0 of the resumeAck ackFlags byte: set when
	// the resume key missed the registry while the client claimed sent bytes
	// (idle GC or cap eviction reclaimed the ledger). The client responds with
	// its designed loss semantic — drop the outbox and notify — instead of
	// guessing from an ambiguous received=0.
	resumeAckFlagLedgerLost byte = 1 << 0

	// resumeAckFlagHistoryPaging is bit1 of ackFlags: the server serves the
	// `history` control and its ring is deep enough to page
	// (historyPagingDeclared). It is declared in the ack, not probed, so the
	// client knows one RTT after attach. Unset (or an older server without the
	// tail) means unsupported: the client keeps its resident-tail cap.
	resumeAckFlagHistoryPaging byte = 1 << 1

	// resumeAckFlagServerFocus: the server derives the DEC 1004 answer and
	// honors the `focus` control. Without it the client drops its report and
	// still writes no focus bytes, which could override WithKeepUnfocused.
	resumeAckFlagServerFocus byte = 1 << 2

	// resumeAckFlagEphemeralInput: the server serves `ephemeralInput`, where the
	// client sends mouse reports. Without it the client uses reliable input,
	// since an unrecognized control is dropped after the latch.
	resumeAckFlagEphemeralInput byte = 1 << 3
)

// resumeAckFlags is the resumeAck ackFlags byte in labelled form. It is a struct
// rather than four bool parameters on encodeResumeAck because four adjacent
// same-typed arguments are transposable at every call site, and it is not a bare
// byte because that would move the bit constants out of this file and into the
// caller.
type resumeAckFlags struct {
	// LedgerLost sets resumeAckFlagLedgerLost.
	LedgerLost bool
	// HistoryPaging sets resumeAckFlagHistoryPaging.
	HistoryPaging bool
	// ServerFocus sets resumeAckFlagServerFocus.
	ServerFocus bool
	// EphemeralInput sets resumeAckFlagEphemeralInput.
	EphemeralInput bool
}

// bits packs the flags into the ackFlags byte.
func (f resumeAckFlags) bits() byte {
	var b byte
	if f.LedgerLost {
		b |= resumeAckFlagLedgerLost
	}
	if f.HistoryPaging {
		b |= resumeAckFlagHistoryPaging
	}
	if f.ServerFocus {
		b |= resumeAckFlagServerFocus
	}
	if f.EphemeralInput {
		b |= resumeAckFlagEphemeralInput
	}
	return b
}

// WireEnd is one half of a Go-server / TS-client pairing as WirePairIncompatibility
// judges it: the revision that half speaks, and the minimum peer revision it
// accepts: WireProtocolVersion and MinSupportedClientWireVersion for the Go
// half, WIRE_PROTOCOL_VERSION and MIN_SUPPORTED_SERVER_WIRE_VERSION for the TS
// half. Construct it with field names (WireEnd{Rev: ...}).
type WireEnd struct {
	// Rev is the wire-protocol revision this half speaks.
	Rev int
	// MinPeer is the oldest peer revision this half accepts.
	MinPeer int
}

// WirePair is the two halves of one Go-server / TS-client pairing, each
// labelled by its role so a transposed call cannot blame the wrong half.
type WirePair struct {
	// Server is the Go half, normally
	// {WireProtocolVersion, MinSupportedClientWireVersion}.
	Server WireEnd
	// Client is the TS half, read from its published constants.
	Client WireEnd
}

// WirePairIncompatibility is the build-time form of the runtime floor checks:
// it returns "" when both halves' floors admit the pairing, else a reason
// naming the violated floor and the half that is behind. A revision at the
// peer's floor is compatible, and so is a revision newer than any known. A
// non-positive value, or a half whose floor exceeds its own revision, is
// reported as unreadable input rather than judged. It rejects 0 although the
// runtime accepts a version-silent 0, because at build time 0 means the
// constant was not read.
func WirePairIncompatibility(p WirePair) string {
	server, client := p.Server, p.Client
	// Case order matters: garbage input must be reported before any cross-side
	// case can misdiagnose it as a skew.
	switch {
	case server.Rev <= 0 || server.MinPeer <= 0:
		return fmt.Sprintf(
			"server wire revisions must be positive (got rev %d, min client %d)",
			server.Rev, server.MinPeer,
		)
	case client.Rev <= 0 || client.MinPeer <= 0:
		return fmt.Sprintf(
			"client wire revisions must be positive (got rev %d, min server %d)",
			client.Rev, client.MinPeer,
		)
	case server.MinPeer > server.Rev:
		return fmt.Sprintf(
			"the Go server half is self-inconsistent: it demands client revision >= %d while itself speaking revision %d, so it could not talk to a client of its own build; these two numbers cannot both come from one released artifact, so treat this as corrupt or mis-extracted input (re-check how the constants were read) rather than a version skew to fix by bumping a pin",
			server.MinPeer, server.Rev,
		)
	case client.MinPeer > client.Rev:
		return fmt.Sprintf(
			"the TS client half is self-inconsistent: it demands server revision >= %d while itself speaking revision %d, so it could not talk to a server of its own build; these two numbers cannot both come from one released artifact, so treat this as corrupt or mis-extracted input (re-check how the constants were read) rather than a version skew to fix by bumping a pin",
			client.MinPeer, client.Rev,
		)
	case server.Rev < client.MinPeer:
		return fmt.Sprintf(
			"Go server wire revision %d is below the TS client's minimum supported server revision %d (the Go half is behind)",
			server.Rev, client.MinPeer,
		)
	case client.Rev < server.MinPeer:
		return fmt.Sprintf(
			"TS client wire revision %d is below the Go server's minimum supported client revision %d (the TS half is behind)",
			client.Rev, server.MinPeer,
		)
	default:
		return ""
	}
}

// encodeScreenMsg builds a screen frame holding only the rows in changed;
// screenHeight is sent because rows are sparse. ack is non-zero only on the
// resume window frame; dispatch stamps it later with withClientAck.
func encodeScreenMsg(base uint64, screenHeight, curRow, curCol int, ack uint64, changed []int, rows [][]vt.WireRun, cursorStyle uint8, cursorHidden, cursorBlink, bell, altActive, scrollbackCleared bool) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, wireMsgScreen)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint64(buf, base)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(curRow))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(curCol))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(screenHeight))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(changed)))
	// Cursor metadata: style (0-6) and flags.
	buf = append(buf, cursorStyle)
	var cursorFlags byte
	if cursorHidden {
		cursorFlags |= 1
	}
	if bell {
		cursorFlags |= 2
	}
	if cursorBlink {
		cursorFlags |= 4
	}
	if altActive {
		cursorFlags |= 8
	}
	if scrollbackCleared {
		cursorFlags |= 16
	}
	buf = append(buf, cursorFlags)
	for _, idx := range changed {
		buf = binary.LittleEndian.AppendUint16(buf, clampU16(idx))
		if idx >= 0 && idx < len(rows) {
			buf = appendRowRuns(buf, rows[idx])
		} else {
			buf = binary.LittleEndian.AppendUint16(buf, 0) // num_runs = 0
		}
	}
	return buf
}

// encodeScrollMsg builds a binary scroll frame carrying committed
// history lines. firstIndex is the absolute index of lines[0]; the
// client applies each line at firstIndex+i into its absolute-indexed
// store (idempotent, so re-delivery never duplicates). Used both for
// live committed lines and for resume replay.
func encodeScrollMsg(ack, firstIndex uint64, lines [][]vt.WireRun) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, wireMsgScroll)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint64(buf, firstIndex)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(lines)))
	for _, line := range lines {
		buf = appendRowRuns(buf, line)
	}
	return buf
}

// encodeResumeAck builds a resumeAck frame: the ledger's received count, the
// boot epoch, the retained-history bounds, and a length-gated
// [serverWireVersion, ackFlags] tail that a shorter frame reads as all clear.
func encodeResumeAck(ack uint64, epochNanos int64, committed, oldestIndex uint64, flags resumeAckFlags) []byte {
	buf := make([]byte, 0, 35)
	buf = append(buf, wireMsgResumeAck)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(epochNanos)) // #nosec G115 -- epochNanos is always positive
	buf = binary.LittleEndian.AppendUint64(buf, committed)
	buf = binary.LittleEndian.AppendUint64(buf, oldestIndex)
	buf = append(buf, wireProtocolVersion, flags.bits())
	return buf
}

// encodeAckOnly builds a bare-ack frame (type + inputAck, no body). Emitted by
// the flush tick's ack sweep for clients whose session ledger advanced without
// any content frame carrying the new value — typically input into a no-echo
// read — so outbox trimming (and therefore loss-free resume accounting) never
// depends on the app producing output.
func encodeAckOnly(ack uint64) []byte {
	buf := make([]byte, 0, 9)
	buf = append(buf, wireMsgAckOnly)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	return buf
}

// encodeModesMsg builds a frame announcing the DEC modes the client's input
// encoding depends on. After the header: [1B] modeFlag* bits, [2B] mouseMode
// (0, 1000, 1002 or 1003), [1B] kitty kbdFlags, which a pre-v3 decoder
// defaults to 0. docs/wire-protocol.md defers to this layout.
func encodeModesMsg(bracketedPaste, appCursorKeys, mouseSGR, focusReporting, appKeypad, reverseVideo, mousePixels bool, mouseMode uint16, kbdFlags uint8) []byte {
	buf := make([]byte, 0, 13)
	buf = append(buf, wireMsgModes)
	// inputAck placeholder (0); withClientAck patches the real per-client value at wireAckOffset.
	buf = binary.LittleEndian.AppendUint64(buf, 0)
	var flags byte
	if bracketedPaste {
		flags |= modeFlagBracketedPaste
	}
	if appCursorKeys {
		flags |= modeFlagAppCursorKeys
	}
	if mouseSGR {
		flags |= modeFlagMouseSGR
	}
	if focusReporting {
		flags |= modeFlagFocusReporting
	}
	if appKeypad {
		flags |= modeFlagAppKeypad
	}
	if reverseVideo {
		flags |= modeFlagReverseVideo
	}
	if mousePixels {
		flags |= modeFlagMousePixels
	}
	buf = append(buf, flags)
	buf = binary.LittleEndian.AppendUint16(buf, mouseMode)
	buf = append(buf, kbdFlags)
	return buf
}

// withClientAck returns a copy of template with the inputAck field
// patched to ack. Used by flushLoop to fan a single encoded frame out
// to multiple clients with their respective per-session ack values
// without re-encoding. The copy is mandatory: WebSocket libraries are
// allowed to retain or mutate (mask) the bytes through the duration
// of the write.
func withClientAck(template []byte, ack uint64) []byte {
	out := make([]byte, len(template))
	copy(out, template)
	if len(out) >= wireAckOffset+wireAckSize {
		binary.LittleEndian.PutUint64(out[wireAckOffset:], ack)
	}
	return out
}

// encodeGridMsg builds a frame announcing the screen's grid: after the header,
// [2B] cols, [2B] rows.
func encodeGridMsg(cols, rows int) []byte {
	buf := make([]byte, 0, 13)
	buf = append(buf, wireMsgGrid)
	buf = binary.LittleEndian.AppendUint64(buf, 0)
	// #nosec G115 -- the screen's size is clamped to maxResizeCols/Rows (1000).
	buf = binary.LittleEndian.AppendUint16(buf, uint16(cols))
	// #nosec G115 -- as above.
	buf = binary.LittleEndian.AppendUint16(buf, uint16(rows))
	return buf
}

// encodePongMsg builds a liveness pong: the header alone, with a zero ack.
func encodePongMsg() []byte {
	buf := make([]byte, 0, 9)
	buf = append(buf, wireMsgPong)
	buf = binary.LittleEndian.AppendUint64(buf, 0)
	return buf
}

// Per-row wire overhead, split out so the ceiling arithmetic below and the
// tests that pin it read the same numbers as the encoder: one helper drives
// stripping, page packing and the tests, so the three cannot drift.
const (
	// encodedRowCountSize is the row payload's leading num_runs field.
	encodedRowCountSize = 2
	// encodedRunFixedSize is the per-run fixed cost: 2 text_len + 4 fg +
	// 4 bg + 2 attrs + 4 uc + 2 url_len. Only text and url are variable.
	encodedRunFixedSize = 18
	// encodedScrollHeaderSize is encodeScrollMsg's header: 1 type + 8 ack +
	// 8 firstIndex + 2 numLines. A one-row reply is header + row payload, so
	// the largest row that can fit a budgeted reply is budget minus this.
	encodedScrollHeaderSize = 19
	// pageByteBudget is the hard per-reply bound for a history page. Browser
	// WebSockets expose no receive backpressure, so a delivered frame IS the
	// client's memory commitment.
	pageByteBudget = 256 * 1024
	// rowByteCeiling is the largest encoded row payload that still fits a
	// budgeted one-row reply (262125 at the shipped budget). A row above it is
	// re-encoded with hyperlink URIs stripped, which is arithmetically bounded
	// (~22 KB at the grid maximum) and therefore always fits.
	rowByteCeiling = pageByteBudget - encodedScrollHeaderSize
)

// encodedRowSize returns the exact number of bytes appendRowRuns will write
// for runs, including the leading num_runs field. It applies the same
// truncation the encoder applies, so the answer is the real wire size rather
// than an estimate.
func encodedRowSize(runs []vt.WireRun) int {
	size := encodedRowCountSize
	for _, run := range runs {
		size += encodedRunFixedSize
		size += len(truncateUTF8(run.T, 0xFFFF))
		size += len(truncateUTF8(run.U, 0xFFFF))
	}
	return size
}

// stripRowURIs returns a copy of runs with every hyperlink URI emptied and the
// autolink bit cleared, so a stripped run reads as never linked; text and
// styling are untouched. A row over rowByteCeiling therefore renders with no
// links, the accepted degradation (the client never scans text for URLs).
func stripRowURIs(runs []vt.WireRun) []vt.WireRun {
	out := make([]vt.WireRun, len(runs))
	copy(out, runs)
	for i := range out {
		out[i].U = ""
		out[i].A &^= vt.AttrAutolink
	}
	return out
}

// capRowRuns applies the per-row ceiling: a row whose encoding exceeds
// rowByteCeiling is re-encoded with its URIs stripped. The decision is a PURE
// FUNCTION of the canonical row — never of remaining frame budget or delivery
// path — so the same committed row encodes to identical bytes on every path
// (page, live flush, resume replay, screen) and repeated delivery of one index
// stays byte-identical, which is what makes the client's idempotence hold.
func capRowRuns(runs []vt.WireRun) []vt.WireRun {
	if encodedRowSize(runs) <= rowByteCeiling {
		return runs
	}
	return stripRowURIs(runs)
}

// appendRowRuns writes one row payload. The per-row ceiling lives here, in the
// encoder every row path shares, so an oversized row renders the same on
// screen as in scrollback.
func appendRowRuns(buf []byte, runs []vt.WireRun) []byte {
	runs = capRowRuns(runs)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(runs)))
	for _, run := range runs {
		text := truncateUTF8(run.T, 0xFFFF)
		buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(text)))
		buf = append(buf, text...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(run.F)) // #nosec G115 -- bit-cast
		buf = binary.LittleEndian.AppendUint32(buf, uint32(run.B)) // #nosec G115 -- bit-cast
		buf = binary.LittleEndian.AppendUint16(buf, run.A)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(run.Uc)) // #nosec G115 -- bit-cast
		url := truncateUTF8(run.U, 0xFFFF)
		buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(url)))
		buf = append(buf, url...)
	}
	return buf
}

func clampU16(n int) uint16 {
	if n < 0 {
		return 0
	}
	if n > 0xFFFF {
		return 0xFFFF
	}
	return uint16(n)
}

// truncateUTF8 returns s limited to at most maxBytes bytes without splitting a
// multi-byte rune, so the wire length field (clampU16(len)) always matches the
// appended payload. Only a pathological oversize run is clamped; every run
// <= maxBytes bytes (the normal case) is returned unchanged.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	t := s[:maxBytes]
	for t != "" && !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	return t
}

// encodeTitleMsg builds a title frame carrying the window title string.
//
//	[1B] msg_type = 4 (title)
//	[8B] inputAck (uint64) — 0 placeholder; withClientAck patches the real value at send.
//	[2B] title_byte_len (uint16)
//	[NB] title (UTF-8 bytes)
func encodeTitleMsg(title string) []byte {
	title = truncateUTF8(title, 0xFFFF)
	buf := make([]byte, 0, 11+len(title))
	buf = append(buf, wireMsgTitle)
	// inputAck placeholder (0); withClientAck patches the real per-client value at wireAckOffset.
	buf = binary.LittleEndian.AppendUint64(buf, 0)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(title)))
	buf = append(buf, title...)
	return buf
}

// encodeClipboardMsg builds a frame carrying text an app copied via OSC 52:
// after the header, [2B] text_byte_len and the UTF-8 text. maxOSCLen keeps it
// under the uint16 limit; the ack is zero until withClientAck stamps it.
func encodeClipboardMsg(text []byte) []byte {
	body := text
	if len(body) > 0xFFFF {
		body = body[:0xFFFF]
	}
	buf := make([]byte, 0, 11+len(body))
	buf = append(buf, wireMsgClipboard)
	buf = binary.LittleEndian.AppendUint64(buf, 0)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(body)))
	buf = append(buf, body...)
	return buf
}
