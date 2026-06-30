package terminal

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf8"

	"github.com/cplieger/web-terminal-engine/vt"
)

// assertWireBytes compares a produced wire frame against a hand-laid expected
// byte slice with a clear failure message.
func assertWireBytes(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s = % x, want % x", label, got, want)
	}
}

// TestEncodeScreenMsg_zeroRowIndexEncodesRunPayload verifies a changed row at
// index 0 has its run payload encoded: the [0, len(rows)) bounds check must
// accept index 0. The header bytes for this call are all 0/1/3, so the run
// text can only appear via the row payload.
func TestEncodeScreenMsg_zeroRowIndexEncodesRunPayload(t *testing.T) {
	run := vt.WireRun{T: "runtext", F: -1, B: -1, Uc: -1}
	rows := [][]vt.WireRun{{run}}
	buf := encodeScreenMsg(0, 3, 0, 0, 0, []int{0}, rows, 0, false, false, false, false)

	if !bytes.Contains(buf, []byte("runtext")) {
		t.Errorf("encodeScreenMsg(changed=[0]): row-0 run text missing; index 0 must be in range so rows[0] is appended")
	}
}

// TestEncodeScreenMsg_outOfRangeIdxWritesZeroRuns verifies a changed index
// equal to len(rows) (out of range by one) is encoded as a zero-run row rather
// than indexing out of bounds.
func TestEncodeScreenMsg_outOfRangeIdxWritesZeroRuns(t *testing.T) {
	rows := [][]vt.WireRun{{}} // len 1; only index 0 is valid
	changed := []int{1}        // idx == len(rows): out of range by exactly one

	got := encodeScreenMsg(0, 1, 0, 0, 0, changed, rows, 0, false, false, false, false)

	want := []byte{
		0x00,                   // wireMsgScreen
		0, 0, 0, 0, 0, 0, 0, 0, // ack = 0
		0, 0, 0, 0, 0, 0, 0, 0, // base = 0
		0x00, 0x00, // curRow = 0
		0x00, 0x00, // curCol = 0
		0x01, 0x00, // screenHeight = 1
		0x01, 0x00, // num_changed = 1
		0x00,       // cursorStyle = 0
		0x00,       // cursorFlags = 0
		0x01, 0x00, // changed[0] idx = 1
		0x00, 0x00, // num_runs = 0 (else branch: idx out of range)
	}
	assertWireBytes(t, "encodeScreenMsg(idx==len(rows))", got, want)
}

// TestWithClientAck_patchesAtExactMinLength verifies withClientAck patches the
// 8-byte ack into bytes [1:9] for a template of exactly the minimum length
// (wireAckOffset+wireAckSize == 9).
func TestWithClientAck_patchesAtExactMinLength(t *testing.T) {
	// msg byte + 8-byte placeholder ack == 9 bytes (wireAckOffset+wireAckSize).
	template := []byte{0xAA, 0, 0, 0, 0, 0, 0, 0, 0}
	const ack = uint64(0x0102030405060708)

	got := withClientAck(template, ack)

	want := []byte{0xAA, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01} // ack, little-endian
	assertWireBytes(t, "withClientAck(len-9 template)", got, want)
}

// TestWithClientAck_shortTemplateLeftUnpatched verifies a template shorter than
// the ack field (length 8 < 9) is copied through untouched rather than patched
// (which would index out of range).
func TestWithClientAck_shortTemplateLeftUnpatched(t *testing.T) {
	template := []byte{0xAA, 1, 2, 3, 4, 5, 6, 7} // length 8 (< 9)
	const ack = uint64(0xFFFFFFFFFFFFFFFF)

	got := withClientAck(template, ack)

	want := []byte{0xAA, 1, 2, 3, 4, 5, 6, 7} // identical copy: no patch
	assertWireBytes(t, "withClientAck(len-8 template)", got, want)
}

// TestEncodeTitleMsg_longTitleBuildsFrame verifies a title longer than the
// fixed header size builds a complete, correct frame (the capacity hint must
// not underflow for long titles).
func TestEncodeTitleMsg_longTitleBuildsFrame(t *testing.T) {
	const ack = uint64(0)
	const title = "abcdefghijklmnop" // 16 bytes (> fixed header)

	got := encodeTitleMsg(ack, title)

	want := []byte{
		0x04,                   // wireMsgTitle
		0, 0, 0, 0, 0, 0, 0, 0, // ack = 0
		0x10, 0x00, // title_byte_len = 16, little-endian
		'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h',
		'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p',
	}
	assertWireBytes(t, "encodeTitleMsg(16-byte title)", got, want)
}

// TestClampU16_boundaryValues pins clampU16's clamp behavior at and around the
// [0, 0xFFFF] boundaries.
func TestClampU16_boundaryValues(t *testing.T) {
	cases := []struct {
		in   int
		want uint16
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{0xFFFE, 0xFFFE},
		{0xFFFF, 0xFFFF},
		{0x10000, 0xFFFF},
	}
	for _, c := range cases {
		if got := clampU16(c.in); got != c.want {
			t.Errorf("clampU16(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEncodeModesMsg_eachFlagSetsItsBit asserts that each DEC-mode flag sets
// exactly its own bit in the modes frame's flags byte (index 9), independently.
// The existing callers only ever set bracketed/mouseSGR/reverseVideo, so the
// appCursorKeys/focusReporting/appKeypad branches were unexercised and a mutant
// dropping any `flags |= modeFlagX` survived.
func TestEncodeModesMsg_eachFlagSetsItsBit(t *testing.T) {
	cases := []struct {
		name string
		args [6]bool // bracketedPaste, appCursorKeys, mouseSGR, focusReporting, appKeypad, reverseVideo
		want byte
	}{
		{"none", [6]bool{false, false, false, false, false, false}, 0},
		{"bracketedPaste", [6]bool{true, false, false, false, false, false}, modeFlagBracketedPaste},
		{"appCursorKeys", [6]bool{false, true, false, false, false, false}, modeFlagAppCursorKeys},
		{"mouseSGR", [6]bool{false, false, true, false, false, false}, modeFlagMouseSGR},
		{"focusReporting", [6]bool{false, false, false, true, false, false}, modeFlagFocusReporting},
		{"appKeypad", [6]bool{false, false, false, false, true, false}, modeFlagAppKeypad},
		{"reverseVideo", [6]bool{false, false, false, false, false, true}, modeFlagReverseVideo},
		{"all", [6]bool{true, true, true, true, true, true}, modeFlagBracketedPaste | modeFlagAppCursorKeys | modeFlagMouseSGR | modeFlagFocusReporting | modeFlagAppKeypad | modeFlagReverseVideo},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := c.args
			buf := encodeModesMsg(a[0], a[1], a[2], a[3], a[4], a[5], 0)
			if len(buf) < 12 {
				t.Fatalf("encodeModesMsg returned %d bytes, want >= 12", len(buf))
			}
			if buf[9] != c.want {
				t.Errorf("encodeModesMsg flags byte = %08b, want %08b", buf[9], c.want)
			}
		})
	}
}

// TestEncodeScreenMsg_eachCursorFlagSetsItsBit asserts that each cursor-state
// flag sets exactly its own bit in the screen frame's cursorFlags byte
// (index 26), independently. Existing tests only cover all-false and blink=true
// (golden), so the hidden/bell/altActive branches were unexercised and a mutant
// dropping `cursorFlags |= 1|2|8` survived.
func TestEncodeScreenMsg_eachCursorFlagSetsItsBit(t *testing.T) {
	cases := []struct {
		name                     string
		hidden, blink, bell, alt bool
		want                     byte
	}{
		{"none", false, false, false, false, 0},
		{"hidden", true, false, false, false, 1},
		{"bell", false, false, true, false, 2},
		{"blink", false, true, false, false, 4},
		{"alt", false, false, false, true, 8},
		{"all", true, true, true, true, 1 | 2 | 4 | 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := encodeScreenMsg(0, 1, 0, 0, 0, nil, nil, 0, c.hidden, c.blink, c.bell, c.alt)
			if len(buf) < 27 {
				t.Fatalf("encodeScreenMsg returned %d bytes, want >= 27", len(buf))
			}
			if buf[26] != c.want {
				t.Errorf("cursorFlags byte = %08b, want %08b (hidden=%v blink=%v bell=%v alt=%v)",
					buf[26], c.want, c.hidden, c.blink, c.bell, c.alt)
			}
		})
	}
}

// TestAppendRowRuns_encodesHyperlinkAndStyle pins the per-run wire encoding
// (the cross-language contract appendRowRuns writes): text, fg, bg, attrs, uc,
// and the OSC 8 url_len + url. No existing test encodes a run with a non-empty
// URL or non-default colors/attrs, so a mutant in any of those field writes
// survived. Asserts via encodeScrollMsg, whose body is exactly one
// appendRowRuns call after an 19-byte header.
func TestAppendRowRuns_encodesHyperlinkAndStyle(t *testing.T) {
	const url = "https://example.com/x"
	run := vt.WireRun{T: "go", U: url, F: 0x112233, B: 0x445566, A: 5, Uc: 0x778899}
	buf := encodeScrollMsg(0, 0, [][]vt.WireRun{{run}})

	if got := binary.LittleEndian.Uint16(buf[17:19]); got != 1 {
		t.Fatalf("num_lines = %d, want 1", got)
	}
	off := 19
	if got := binary.LittleEndian.Uint16(buf[off:]); got != 1 {
		t.Fatalf("num_runs = %d, want 1", got)
	}
	off += 2
	tlen := int(binary.LittleEndian.Uint16(buf[off:]))
	off += 2
	if got := string(buf[off : off+tlen]); got != "go" {
		t.Fatalf("run text = %q, want %q", got, "go")
	}
	off += tlen
	if got := int32(binary.LittleEndian.Uint32(buf[off:])); got != 0x112233 {
		t.Errorf("fg = %#x, want 0x112233", got)
	}
	off += 4
	if got := int32(binary.LittleEndian.Uint32(buf[off:])); got != 0x445566 {
		t.Errorf("bg = %#x, want 0x445566", got)
	}
	off += 4
	if got := binary.LittleEndian.Uint16(buf[off:]); got != 5 {
		t.Errorf("attrs = %d, want 5", got)
	}
	off += 2
	if got := int32(binary.LittleEndian.Uint32(buf[off:])); got != 0x778899 {
		t.Errorf("uc = %#x, want 0x778899", got)
	}
	off += 4
	ulen := int(binary.LittleEndian.Uint16(buf[off:]))
	off += 2
	if ulen != len(url) {
		t.Fatalf("url_len = %d, want %d", ulen, len(url))
	}
	if got := string(buf[off : off+ulen]); got != url {
		t.Errorf("url = %q, want %q", got, url)
	}
}

// TestTruncateUTF8_clampsAtRuneBoundary pins truncateUTF8's contract: a run
// longer than the wire cap is truncated WITHOUT splitting a multi-byte rune, so
// the encoded length field always matches valid UTF-8 payload bytes. Only the
// under-cap early return was exercised (33.3% coverage), leaving the
// rune-boundary backoff loop -- the cross-language wire-safety invariant --
// untested; a mutant dropping the loop emits a half rune, caught by ValidString.
func TestTruncateUTF8_clampsAtRuneBoundary(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		maxBytes int
		want     string
	}{
		{"under cap unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"ascii truncated", "hello", 3, "hel"},
		{"zero cap empties", "hello", 0, ""},
		// "é" is 2 bytes (0xC3 0xA9); a cap landing mid-rune backs off to "h".
		{"multibyte split backs off", "héllo", 2, "h"},
		{"multibyte kept when it fits", "héllo", 3, "hé"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateUTF8(c.s, c.maxBytes)
			if got != c.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", c.s, c.maxBytes, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateUTF8(%q, %d) = %q: not valid UTF-8 (wire length would mismatch payload)", c.s, c.maxBytes, got)
			}
		})
	}
}
