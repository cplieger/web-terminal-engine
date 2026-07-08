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
// Single-codepoint emoji with default emoji presentation (Unicode
// Emoji_Presentation=Yes, e.g. 🟢 U+1F7E2) are Wide (width 2). This matches the
// modern terminal-emulator consensus (iTerm2, kitty, WezTerm, VTE, Windows
// Terminal) and, decisively, the wcwidth model the programs driving the PTY use
// (go-runewidth): a width-1 mismatch is exactly what clips such glyphs into the
// next cell. The set is emojiRanges below.
//
// OUT OF SCOPE: multi-codepoint grapheme clusters — ZWJ sequences (family
// emoji), skin-tone modifiers, variation selectors that change width, and
// regional-indicator flags. Combining marks are consumed (width 0) but no
// grapheme joining is performed; see the README "Unsupported by Design" table.

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
	if r < 0xA0 { // DEL (0x7F) + C1 controls (0x80-0x9F); r >= 0x7F guaranteed above
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
	return inTable(r, wideRanges) || inTable(r, emojiRanges)
}

// wideRanges lists East Asian Wide (W) and Fullwidth (F) ranges (the CJK
// blocks, Hangul syllables, fullwidth forms, and the CJK Extension catch-alls),
// which cover current Unicode CJK via the coarse blocks. Emoji-presentation
// codepoints are Wide too but are listed separately in emojiRanges. Source:
// UAX#11 East Asian Width.
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

// emojiRanges lists single-codepoint emoji with default emoji presentation
// (Unicode Emoji_Presentation=Yes), which render two cells wide in modern
// terminals and in the wcwidth model the PTY-side programs use (go-runewidth),
// so the engine reserves two cells to stay in sync (a width-1 mismatch is what
// clips e.g. 🟢 U+1F7E2). Ascending + non-overlapping for inTable's binary
// search (guarded by TestEmojiRangesSortedNonOverlapping). Regional-indicator
// flags (U+1F1E6–1F1FF) and ZWJ / skin-tone / variation-selector sequences are
// intentionally excluded: they are multi-codepoint grapheme clusters, out of
// scope (see the width scope note above and the README).
var emojiRanges = []interval{
	{0x231A, 0x231B},
	{0x23E9, 0x23EC},
	{0x23F0, 0x23F0},
	{0x23F3, 0x23F3},
	{0x25FD, 0x25FE},
	{0x2614, 0x2615},
	{0x2648, 0x2653},
	{0x267F, 0x267F},
	{0x2693, 0x2693},
	{0x26A1, 0x26A1},
	{0x26AA, 0x26AB},
	{0x26BD, 0x26BE},
	{0x26C4, 0x26C5},
	{0x26CE, 0x26CE},
	{0x26D4, 0x26D4},
	{0x26EA, 0x26EA},
	{0x26F2, 0x26F3},
	{0x26F5, 0x26F5},
	{0x26FA, 0x26FA},
	{0x26FD, 0x26FD},
	{0x2705, 0x2705},
	{0x270A, 0x270B},
	{0x2728, 0x2728},
	{0x274C, 0x274C},
	{0x274E, 0x274E},
	{0x2753, 0x2755},
	{0x2757, 0x2757},
	{0x2795, 0x2797},
	{0x27B0, 0x27B0},
	{0x27BF, 0x27BF},
	{0x2B1B, 0x2B1C},
	{0x2B50, 0x2B50},
	{0x2B55, 0x2B55},
	{0x1F004, 0x1F004},
	{0x1F0CF, 0x1F0CF},
	{0x1F18E, 0x1F18E},
	{0x1F191, 0x1F19A},
	{0x1F201, 0x1F201},
	{0x1F21A, 0x1F21A},
	{0x1F22F, 0x1F22F},
	{0x1F232, 0x1F236},
	{0x1F238, 0x1F23A},
	{0x1F250, 0x1F251},
	{0x1F300, 0x1F320},
	{0x1F32D, 0x1F335},
	{0x1F337, 0x1F37C},
	{0x1F37E, 0x1F393},
	{0x1F3A0, 0x1F3CA},
	{0x1F3CF, 0x1F3D3},
	{0x1F3E0, 0x1F3F0},
	{0x1F3F4, 0x1F3F4},
	{0x1F3F8, 0x1F43E},
	{0x1F440, 0x1F440},
	{0x1F442, 0x1F4FC},
	{0x1F4FF, 0x1F53D},
	{0x1F54B, 0x1F54E},
	{0x1F550, 0x1F567},
	{0x1F57A, 0x1F57A},
	{0x1F595, 0x1F596},
	{0x1F5A4, 0x1F5A4},
	{0x1F5FB, 0x1F64F},
	{0x1F680, 0x1F6C5},
	{0x1F6CC, 0x1F6CC},
	{0x1F6D0, 0x1F6D2},
	{0x1F6D5, 0x1F6D7},
	{0x1F6DC, 0x1F6DF},
	{0x1F6EB, 0x1F6EC},
	{0x1F6F4, 0x1F6FC},
	{0x1F7E0, 0x1F7EB},
	{0x1F7F0, 0x1F7F0},
	{0x1F90C, 0x1F93A},
	{0x1F93C, 0x1F945},
	{0x1F947, 0x1F9FF},
	{0x1FA70, 0x1FA7C},
	{0x1FA80, 0x1FA88},
	{0x1FA90, 0x1FABD},
	{0x1FABF, 0x1FAC5},
	{0x1FACE, 0x1FADB},
	{0x1FAE0, 0x1FAE8},
	{0x1FAF0, 0x1FAF8},
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
