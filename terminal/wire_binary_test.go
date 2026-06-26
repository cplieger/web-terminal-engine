package terminal

import (
	"bytes"
	"testing"

	"github.com/cplieger/vterm/vt"
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
	buf := encodeScreenMsg(3, 0, 0, 0, []int{0}, rows, 0, false, false, false)

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

	got := encodeScreenMsg(1, 0, 0, 0, changed, rows, 0, false, false, false)

	want := []byte{
		0x00,                   // wireMsgScreen
		0, 0, 0, 0, 0, 0, 0, 0, // ack = 0
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
