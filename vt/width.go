package vt

import "unicode"

// Unicode cell width determination for terminal emulation.
//
// Implements UAX#11 East Asian Width:
//   - Wide (W) and Fullwidth (F) characters → width 2
//   - Combining / zero-width characters → width 0
//   - Ambiguous (A) → width 1 (Western convention)
//   - Everything else → width 1
//
// The zero-width set comes from the Go standard library's Unicode tables
// (unicode.Mn / Me / Cf), which track the toolchain's Unicode version, so it
// stays current without a hand-maintained table. The East Asian Wide/Fullwidth
// set is a small local table (wideRanges) because the stdlib does not expose
// East Asian Width. Runtime stays stdlib-only.
//
// OUT OF SCOPE: ZWJ sequences, emoji grapheme-cluster merging, variation
// selectors changing width, and single-codepoint emoji as Wide. Combining
// marks are consumed (width 0) but no grapheme joining is performed; see the
// README "Unsupported by Design" table.

// runeWidth returns the terminal cell width of a rune:
//
//	0 for combining/zero-width characters
//	2 for East Asian Wide/Fullwidth
//	1 for everything else (including Ambiguous)
func runeWidth(r rune) int {
	if r < 0x20 {
		return 0
	}
	if r < 0x7F {
		return 1
	}
	if r >= 0x7F && r < 0xA0 {
		return 0
	}
	if isZeroWidth(r) {
		return 0
	}
	// East Asian Wide/Fullwidth → 2 cells.
	if isWide(r) {
		return 2
	}
	return 1
}

// isZeroWidth reports whether r is a combining or zero-width character that
// does not advance the cursor: the Unicode nonspacing/enclosing marks (Mn, Me)
// and format characters (Cf) from the stdlib's current Unicode tables, plus the
// Hangul conjoining jamo (U+1160–U+11FF, category Lo, which combine onto the
// leading consonant), minus SOFT HYPHEN (U+00AD) — the one Cf character
// terminals advance over (the wcwidth/glibc convention).
func isZeroWidth(r rune) bool {
	if r == 0x00AD { // SOFT HYPHEN — printable, width 1
		return false
	}
	if r >= 0x1160 && r <= 0x11FF { // Hangul Jamo medial/final (conjoining)
		return true
	}
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf)
}

func isWide(r rune) bool {
	return inTable(r, wideRanges)
}

// wideRanges lists East Asian Wide (W) and Fullwidth (F) ranges (the CJK
// blocks, Hangul syllables, fullwidth forms, and the CJK Extension catch-alls),
// which cover current Unicode CJK via the coarse blocks. Single-codepoint emoji
// are deliberately NOT treated as Wide here — consistent with the emoji
// "Unsupported by Design" scope (see the README); treating emoji as width-2 is
// a separate policy decision, not a data refresh. Source: UAX#11 East Asian Width.
var wideRanges = []interval{
	{0x1100, 0x115F},   // Hangul Jamo init. consonants
	{0x2329, 0x232A},   // LEFT/RIGHT-POINTING ANGLE BRACKET
	{0x2E80, 0x303E},   // CJK Radicals..CJK Symbols (excl. 0x303F)
	{0x3040, 0xA4CF},   // Hiragana..Yi
	{0xAC00, 0xD7A3},   // Hangul Syllables
	{0xF900, 0xFAFF},   // CJK Compatibility Ideographs
	{0xFE10, 0xFE19},   // Vertical forms
	{0xFE30, 0xFE6F},   // CJK Compatibility Forms
	{0xFF00, 0xFF60},   // Fullwidth Forms
	{0xFFE0, 0xFFE6},   // Fullwidth Signs
	{0x20000, 0x2FFFD}, // CJK Unified Ext B+
	{0x30000, 0x3FFFD}, // CJK Unified Ext G+
}

type interval struct {
	first, last rune
}

func inTable(r rune, table []interval) bool {
	lo, hi := 0, len(table)-1
	if r < table[0].first || r > table[hi].last {
		return false
	}
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case r > table[mid].last:
			lo = mid + 1
		case r < table[mid].first:
			hi = mid - 1
		default:
			return true
		}
	}
	return false
}
