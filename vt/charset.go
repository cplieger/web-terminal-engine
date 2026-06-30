package vt

// SCS (Select Character Set) support: G0-G3 designation, SO/SI locking shifts,
// SS2/SS3 single shifts, and the DEC Special Graphics character set.
//
// Implements:
//   - ESC ( <final>  → designate G0
//   - ESC ) <final>  → designate G1
//   - ESC * <final>  → designate G2
//   - ESC + <final>  → designate G3
//   - SO (0x0E)      → GL = G1 (locking shift)
//   - SI (0x0F)      → GL = G0 (locking shift)
//   - ESC N          → SS2 (single shift G2 for next printable)
//   - ESC O          → SS3 (single shift G3 for next printable)
//
// Supported charsets: B = US ASCII (identity), 0 = DEC Special Graphics.
// All other final bytes (A, etc.) are treated as ASCII (consumed, no error).
//
// OUT OF SCOPE: NRCS national replacement character sets. The final byte is
// consumed but the charset is treated as ASCII.
//
// Reference: xterm.js Charsets.ts (MIT), VT220 Programmer Reference Manual
// Table 2-4, Wikipedia "DEC Special Graphics".

// charset identifies a character set designation.
type charset uint8

const (
	charsetASCII   charset = iota // US-ASCII (identity mapping)
	charsetGraphic                // DEC Special Graphics (line-drawing)
)

// charsetState holds G0-G3 designations and the active GL pointer.
// Embedded in Screen.
type charsetState struct {
	gsets      [4]charset // G0..G3 designations
	gl         uint8      // which Gn is mapped to GL (0-3)
	singleShft int8       // -1 = none, 2 = SS2, 3 = SS3
}

// decSpecialGraphics maps bytes 0x60-0x7E to Unicode code points.
// Index: byte - 0x60. Adapted from xterm.js Charsets.ts CHARSETS['0'].
var decSpecialGraphics = [31]rune{
	'\u25c6', // 0x60 '`' → ◆
	'\u2592', // 0x61 'a' → ▒
	'\u2409', // 0x62 'b' → ␉ (HT symbol)
	'\u240c', // 0x63 'c' → ␌ (FF symbol)
	'\u240d', // 0x64 'd' → ␍ (CR symbol)
	'\u240a', // 0x65 'e' → ␊ (LF symbol)
	'\u00b0', // 0x66 'f' → °
	'\u00b1', // 0x67 'g' → ±
	'\u2424', // 0x68 'h' → ␤ (NL symbol)
	'\u240b', // 0x69 'i' → ␋ (VT symbol)
	'\u2518', // 0x6A 'j' → ┘
	'\u2510', // 0x6B 'k' → ┐
	'\u250c', // 0x6C 'l' → ┌
	'\u2514', // 0x6D 'm' → └
	'\u253c', // 0x6E 'n' → ┼
	'\u23ba', // 0x6F 'o' → ⎺ (scan line 1)
	'\u23bb', // 0x70 'p' → ⎻ (scan line 3)
	'\u2500', // 0x71 'q' → ─ (scan line 5 / horizontal line)
	'\u23bc', // 0x72 'r' → ⎼ (scan line 7)
	'\u23bd', // 0x73 's' → ⎽ (scan line 9)
	'\u251c', // 0x74 't' → ├
	'\u2524', // 0x75 'u' → ┤
	'\u2534', // 0x76 'v' → ┴
	'\u252c', // 0x77 'w' → ┬
	'\u2502', // 0x78 'x' → │
	'\u2264', // 0x79 'y' → ≤
	'\u2265', // 0x7A 'z' → ≥
	'\u03c0', // 0x7B '{' → π
	'\u2260', // 0x7C '|' → ≠
	'\u00a3', // 0x7D '}' → £
	'\u00b7', // 0x7E '~' → ·
}

// translateChar applies the active character set mapping to a printable byte.
// Returns the (possibly translated) rune. Called from the print path.
func (s *Screen) translateChar(b byte) rune {
	// Determine which G-set to use: single-shift overrides GL for one char.
	var cs charset
	if s.singleShft >= 2 {
		cs = s.gsets[s.singleShft] // SS2→G2(index 2), SS3→G3(index 3)
		s.singleShft = -1
	} else {
		cs = s.gsets[s.gl]
	}
	if cs == charsetGraphic && b >= 0x60 && b <= 0x7E {
		return decSpecialGraphics[b-0x60]
	}
	return rune(b)
}

// designateCharset sets a G-set from an SCS escape sequence.
// intermediate: '(' = G0, ')' = G1, '*' = G2, '+' = G3.
// final: '0' = DEC Special Graphics, 'B' = ASCII, others = ASCII.
func (s *Screen) designateCharset(intermediate, final byte) {
	var idx int
	switch intermediate {
	case '(':
		idx = 0
	case ')':
		idx = 1
	case '*':
		idx = 2
	case '+':
		idx = 3
	default:
		return
	}
	switch final {
	case '0':
		s.gsets[idx] = charsetGraphic
	default:
		s.gsets[idx] = charsetASCII
	}
}

// resetCharsets resets all charset state to defaults (G0=ASCII in GL).
func (s *Screen) resetCharsets() {
	s.gsets = [4]charset{charsetASCII, charsetASCII, charsetASCII, charsetASCII}
	s.gl = 0
	s.singleShft = -1
}
