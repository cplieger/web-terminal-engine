package terminal

// gremlins_kill_vterm_u10_test.go — added by mutant-killing unit vterm-u10.
// Tests ONLY; no production code is modified. Each test documents the exact
// surviving gremlins mutant(s) it kills (or why a mutant is equivalent) and why
// the asserted value depends on the precise operator at that source location.
// Targets live in terminal/wire_binary.go.

import (
	"bytes"
	"testing"

	"github.com/cplieger/vterm/vt"
)

// gk_vterm_u10_assertBytes compares a produced wire frame against a hand-laid
// expected byte slice with a clear failure message.
func gk_vterm_u10_assertBytes(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Errorf("%s = % x, want % x", label, got, want)
	}
}

// --- wire_binary.go:113:22 CONDITIONALS_BOUNDARY -----------------------------

// Kills wire_binary.go:113:22 CONDITIONALS_BOUNDARY.
// Line 113 inside encodeScreenMsg: `if idx >= 0 && idx < len(rows) {`. The
// boundary mutant changes `idx < len(rows)` to `idx <= len(rows)`. With rows of
// length 1 (only index 0 valid) and a changed index equal to len(rows) (==1):
//   - Original (`<`): `1 < 1` is FALSE → else branch writes num_runs=0 and the
//     frame is fully defined.
//   - Mutant (`<=`): `1 <= 1` is TRUE → appendRowRuns(rows[1]) → rows[1] is out
//     of range (len 1) → panic.
// Asserting the exact else-branch frame both pins the value and fails (panics)
// under the mutant.
func TestGkVtermU10_EncodeScreenMsg_outOfRangeIdxWritesZeroRuns(t *testing.T) {
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
		0x00, 0x00, // num_runs = 0 (else branch: idx >= len(rows))
	}
	gk_vterm_u10_assertBytes(t, "encodeScreenMsg(idx==len(rows))", got, want)
}

// --- wire_binary.go:202 (withClientAck) --------------------------------------

// Kills wire_binary.go:202:14 CONDITIONALS_BOUNDARY (`>=`→`>`) AND
// wire_binary.go:202:14 CONDITIONALS_NEGATION (`>=`→`<`).
// Line 202 inside withClientAck: `if len(out) >= wireAckOffset+wireAckSize {`,
// i.e. `len(out) >= 9`. A template of length exactly 9 is the minimum length at
// which the per-client ack is patched into bytes [1:9]:
//   - Original (`>=`): `9 >= 9` is TRUE → PutUint64 writes the little-endian ack.
//   - Boundary mutant (`>`): `9 > 9` is FALSE → no patch → placeholder zeros stay.
//   - Negation mutant (`<`): `9 < 9` is FALSE → no patch → placeholder zeros stay.
// Asserting the patched bytes therefore fails under both mutants.
func TestGkVtermU10_WithClientAck_patchesAtExactMinLength(t *testing.T) {
	// msg byte + 8-byte placeholder ack == 9 bytes (wireAckOffset+wireAckSize).
	template := []byte{0xAA, 0, 0, 0, 0, 0, 0, 0, 0}
	const ack = uint64(0x0102030405060708)

	got := withClientAck(template, ack)

	want := []byte{0xAA, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01} // ack, little-endian
	gk_vterm_u10_assertBytes(t, "withClientAck(len-9 template)", got, want)
}

// Kills wire_binary.go:202:30 ARITHMETIC_BASE (`wireAckOffset+wireAckSize`→
// `wireAckOffset-wireAckSize`). `wireAckOffset`==1 and `wireAckSize`==8, so the
// threshold is `1+8`==9; the mutant makes it `1-8`==-7. With a template one byte
// below threshold (length 8):
//   - Original: `8 >= 9` is FALSE → the frame is copied through untouched.
//   - Mutant: `8 >= -7` is TRUE → PutUint64 on out[1:] (length 7) needs 8 bytes
//     → index-out-of-range panic.
// A clean unchanged copy proves the operator is `+` (threshold 9), not `-`.
func TestGkVtermU10_WithClientAck_shortTemplateLeftUnpatched(t *testing.T) {
	template := []byte{0xAA, 1, 2, 3, 4, 5, 6, 7} // length 8 (< 9)
	const ack = uint64(0xFFFFFFFFFFFFFFFF)

	got := withClientAck(template, ack)

	want := []byte{0xAA, 1, 2, 3, 4, 5, 6, 7} // identical copy: no patch
	gk_vterm_u10_assertBytes(t, "withClientAck(len-8 template)", got, want)
}

// --- wire_binary.go:241:27 ARITHMETIC_BASE -----------------------------------

// Kills wire_binary.go:241:27 ARITHMETIC_BASE.
// Line 241 inside encodeTitleMsg: `buf := make([]byte, 0, 11+len(title))`. The
// mutant changes the capacity hint `11+len(title)` to `11-len(title)`. With a
// 16-byte title (> 11):
//   - Original: capacity 11+16==27 (non-negative) → make succeeds, full frame built.
//   - Mutant: capacity 11-16==-5 (negative) → make([]byte, 0, -5) panics
//     ("makeslice: cap out of range").
// Producing the exact frame proves the `+` and that long titles are handled.
func TestGkVtermU10_EncodeTitleMsg_longTitleBuildsFrame(t *testing.T) {
	const ack = uint64(0)            // ack value is irrelevant to the capacity arithmetic
	const title = "abcdefghijklmnop" // 16 bytes (> 11)

	got := encodeTitleMsg(ack, title)

	want := []byte{
		0x04,                   // wireMsgTitle
		0, 0, 0, 0, 0, 0, 0, 0, // ack = 0
		0x10, 0x00, // title_byte_len = 16, little-endian
		'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h',
		'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p',
	}
	gk_vterm_u10_assertBytes(t, "encodeTitleMsg(16-byte title)", got, want)
}

// --- wire_binary.go:225:7 and 228:7 — EQUIVALENT (characterization only) -----

// Characterizes clampU16's boundaries. The two surviving CONDITIONALS_BOUNDARY
// mutants in clampU16 are EQUIVALENT (no input changes the output), so this test
// is NOT a kill — it pins the boundary values and confirms the equivalence:
//   - 225:7 `n < 0` → `n <= 0`: the operators differ only at n==0, where the
//     original falls through to `return uint16(0)` and the mutant takes the
//     early `return 0`. Both yield 0.
//   - 228:7 `n > 0xFFFF` → `n >= 0xFFFF`: the operators differ only at n==0xFFFF,
//     where the original falls through to `return uint16(0xFFFF)` and the mutant
//     takes the early `return 0xFFFF`. Both yield 65535.
// Every case below passes identically under original and mutated source.
func TestGkVtermU10_ClampU16_boundaryValues(t *testing.T) {
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
