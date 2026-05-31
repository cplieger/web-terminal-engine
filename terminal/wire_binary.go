// Binary wire format for server → client messages.
//
// Replaces the JSON encoding for screen/scroll/resumeAck so frames stay
// small over slow links (notably iPad on a Korea↔France relay where JSON
// payloads of >100KB caused the browser to choke). The format is little-
// endian fixed-width fields; no length-prefixed dictionary keys, no
// repeated string identifiers.
//
//	[1B] msg_type:    0=screen, 1=scroll, 2=resumeAck
//	[8B] inputAck:    uint64  (server-confirmed bytesReceived for this session)
//
//	If msg_type == screen:
//	  [2B] cursor_row    uint16
//	  [2B] cursor_col    uint16
//	  [2B] screen_height uint16  (full terminal height; rows below is sparse)
//	  [2B] num_changed   uint16
//	  For each changed row:
//	    [2B] row_idx     uint16
//	    [row payload]
//
//	If msg_type == scroll:
//	  [2B] num_lines    uint16
//	  For each line:
//	    [row payload]
//
//	If msg_type == resumeAck:
//	  inputAck above carries the value;
//	  [8B] serverEpoch  uint64 (process-start nanoseconds since epoch).
//	                    Client compares against last seen epoch to detect
//	                    server restart; on mismatch the resume protocol's
//	                    silent-data-loss case (server has no record of
//	                    bytes the client thinks are acked) is surfaced
//	                    instead of being papered over.
//
//	row payload:
//	  [2B] num_runs    uint16
//	  For each run:
//	    [2B] text_byte_len uint16
//	    [N B] text         utf-8 bytes
//	    [4B] fg            int32   (-1 = default fg)
//	    [4B] bg            int32   (-1 = default bg)
//	    [2B] attrs         uint16  (bit flags, see WireRun.A)
//	    [4B] uc            int32   (-1 = default underline color)
//
// Per-client ack patching: encodeScreenMsg / encodeScrollMsg accept a
// placeholder ack (typically 0) and return a template that flushLoop
// then clones and patches with the real per-client ack via
// withClientAck. This keeps the encode work O(frame_size) instead of
// O(clients × frame_size).

package terminal

import (
	"encoding/binary"

	"github.com/cplieger/vterm/vt"
)

const (
	wireMsgScreen    byte = 0
	wireMsgScroll    byte = 1
	wireMsgResumeAck byte = 2
	wireMsgModes     byte = 3

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
)

// encodeScreenMsg builds a binary screen frame containing only the
// rows whose indices appear in `changed`. screenHeight is the full
// terminal height (rowEls count on the client) — needed because rows
// is sparse on the wire.
func encodeScreenMsg(screenHeight, curRow, curCol int, ack uint64, changed []int, rows [][]vt.WireRun, cursorStyle uint8, cursorHidden, cursorBlink, bell bool) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, wireMsgScreen)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(curRow))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(curCol))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(screenHeight))
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(changed)))
	// Cursor metadata: style (0-6) and flags (bit 0 = hidden, bit 1 = bell).
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

// encodeScrollMsg builds a binary scroll frame containing the given
// drained scrollback lines. flushLoop emits scroll frames alongside
// screen frames so iOS clients (no physical Ctrl+T) can swipe through
// recent terminal history. Per-flush line count is capped by the
// caller to keep frames small on slow links.
func encodeScrollMsg(ack uint64, lines [][]vt.WireRun) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, wireMsgScroll)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(lines)))
	for _, line := range lines {
		buf = appendRowRuns(buf, line)
	}
	return buf
}

// encodeResumeAck builds a resumeAck frame carrying the server's current
// per-session bytesReceived count and the server boot epoch. The client
// uses the epoch to detect server restarts: when the epoch changes, any
// bytesAcked the client had on the previous boot's session is stale,
// so the client resets bytesSent/bytesAcked and the user sees a
// `[server restarted]` banner instead of silent input loss.
func encodeResumeAck(ack uint64, epochNanos int64) []byte {
	buf := make([]byte, 0, 17)
	buf = append(buf, wireMsgResumeAck)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(epochNanos)) // #nosec G115 -- epochNanos is always positive
	return buf
}

// encodeModesMsg builds a frame announcing the current DEC private
// mode state. flushLoop emits this when it observes a change in
// screen.BracketedPaste or screen.AppCursorKeys so the client's input
// path can format paste and arrow keys correctly:
//
//	[1B] msg_type = 3 (modes)
//	[8B] inputAck (uint64)
//	[1B] flags
//	     bit 0: bracketed paste enabled (DEC ?2004h)
//	     bit 1: application cursor keys (DECCKM, CSI ?1h) enabled
func encodeModesMsg(ack uint64, bracketedPaste, appCursorKeys bool) []byte {
	buf := make([]byte, 0, 10)
	buf = append(buf, wireMsgModes)
	buf = binary.LittleEndian.AppendUint64(buf, ack)
	var flags byte
	if bracketedPaste {
		flags |= modeFlagBracketedPaste
	}
	if appCursorKeys {
		flags |= modeFlagAppCursorKeys
	}
	buf = append(buf, flags)
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

func appendRowRuns(buf []byte, runs []vt.WireRun) []byte {
	buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(runs)))
	for _, run := range runs {
		text := run.T
		buf = binary.LittleEndian.AppendUint16(buf, clampU16(len(text)))
		buf = append(buf, text...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(int32ToWire(run.F))) // #nosec G115 -- bit-cast
		buf = binary.LittleEndian.AppendUint32(buf, uint32(int32ToWire(run.B))) // #nosec G115 -- bit-cast
		buf = binary.LittleEndian.AppendUint16(buf, run.A)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(int32ToWire(run.Uc))) // #nosec G115 -- bit-cast
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

// int32ToWire returns the wire representation of a WireRun color (int32).
// Default-color value is -1; we encode as the bit pattern of int32(-1).
func int32ToWire(v int32) int32 { return v }
