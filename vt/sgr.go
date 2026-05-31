package vt

import (
	"fmt"
	"strconv"
	"strings"
)

//nolint:gocyclo // SGR parameter parsing
func (s *Screen) applySGR(args string) {
	if args == "" || args == "0" {
		s.style = Style{}
		return
	}
	params := parseCSIParams(args)
	for i := 0; i < len(params); i++ {
		p := params[i]
		switch {
		case p == 0:
			s.style = Style{}
		case p == 1:
			s.style.Bold = true
		case p == 2:
			s.style.Dim = true
		case p == 3:
			s.style.Italic = true
		case p == 4:
			s.style.Underline = true
		case p == 5:
			s.style.Blink = true
		case p == 7:
			s.style.Inverse = true
		case p == 9:
			s.style.Strikethrough = true
		case p == 21:
			s.style.DoubleUnderline = true
			s.style.Underline = false
		case p == 22:
			s.style.Bold = false
			s.style.Dim = false
		case p == 23:
			s.style.Italic = false
		case p == 24:
			s.style.Underline = false
			s.style.DoubleUnderline = false
		case p == 25:
			s.style.Blink = false
		case p == 27:
			s.style.Inverse = false
		case p == 28:
			s.style.Hidden = false
		case p == 29:
			s.style.Strikethrough = false
		case p >= 30 && p <= 37:
			s.style.FG = Color{Type: 1, Val: uint8(p - 30)}
		case p == 38:
			i = parseExtColor(params, i, &s.style.FG)
		case p == 39:
			s.style.FG = Color{}
		case p >= 40 && p <= 47:
			s.style.BG = Color{Type: 1, Val: uint8(p - 40)}
		case p == 48:
			i = parseExtColor(params, i, &s.style.BG)
		case p == 49:
			s.style.BG = Color{}
		case p == 53:
			s.style.Overline = true
		case p == 55:
			s.style.Overline = false
		case p == 58:
			i = parseExtColor(params, i, &s.style.UnderlineColor)
		case p == 59:
			s.style.UnderlineColor = Color{}
		case p >= 90 && p <= 97:
			s.style.FG = Color{Type: 1, Val: uint8(p - 90 + 8)}
		case p >= 100 && p <= 107:
			s.style.BG = Color{Type: 1, Val: uint8(p - 100 + 8)}
		}
	}
}

// parseExtColor parses extended-color SGR forms (38;5;N or 38;2;R;G;B for fg,
// 48;... for bg). Returns the new param index after consuming.
func parseExtColor(params []int, i int, c *Color) int {
	if i+1 >= len(params) {
		return i
	}
	switch params[i+1] {
	case 5:
		if i+2 < len(params) {
			*c = Color{Type: 2, Val: clampByte(params[i+2])}
			return i + 2
		}
	case 2:
		if i+4 < len(params) {
			*c = Color{Type: 3, R: clampByte(params[i+2]), G: clampByte(params[i+3]), B: clampByte(params[i+4])}
			return i + 4
		}
	}
	return i + 1
}

// clampByte clamps an int from a parsed SGR parameter to the [0,255] byte
// range. Per ECMA-48 / ANSI X3.64 the SGR extended-color values (38;5;N
// and 38;2;R;G;B) MUST be 0-255; a malformed VT stream sending values
// outside that range is treated as the closest valid value rather than
// silently wrapping via uint8 truncation. Fixes CodeQL go/incorrect-integer-conversion.
func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// --- CSI parameter parsing helpers ---

// csiArg returns the first CSI parameter, or def if absent.
func csiArg(args string, def int) int {
	clean := strings.TrimLeftFunc(args, func(r rune) bool {
		return r == '?' || r == '>' || r == '!'
	})
	if clean == "" {
		return def
	}
	n, err := strconv.Atoi(strings.Split(clean, ";")[0])
	if err != nil {
		return def
	}
	return n
}

// csiArgs parses all semicolon-separated CSI parameters.
func csiArgs(args string) []int {
	clean := strings.TrimLeftFunc(args, func(r rune) bool {
		return r == '?' || r == '>' || r == '!'
	})
	if clean == "" {
		return nil
	}
	parts := strings.Split(clean, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

// parseCSIParams is like csiArgs but returns [0] for empty input (SGR default).
func parseCSIParams(args string) []int {
	clean := strings.TrimLeftFunc(args, func(r rune) bool {
		return r == '?' || r == '>' || r == '!'
	})
	if clean == "" {
		return []int{0}
	}
	parts := strings.Split(clean, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

// sgrSequence emits an ANSI SGR escape that reproduces the given Style.
func sgrSequence(st Style) string {
	var params []string
	if st == (Style{}) {
		return "\x1b[0m"
	}
	params = append(params, "0")
	if st.Bold {
		params = append(params, "1")
	}
	if st.Dim {
		params = append(params, "2")
	}
	if st.Italic {
		params = append(params, "3")
	}
	if st.Underline {
		params = append(params, "4")
	}
	if st.Inverse {
		params = append(params, "7")
	}
	if st.Strikethrough {
		params = append(params, "9")
	}
	params = appendColorParams(params, st.FG, 30)
	params = appendColorParams(params, st.BG, 40)
	return fmt.Sprintf("\x1b[%sm", strings.Join(params, ";"))
}

func appendColorParams(params []string, c Color, base int) []string {
	switch c.Type {
	case 1:
		params = append(params, strconv.Itoa(base+int(c.Val)))
	case 2:
		params = append(params, strconv.Itoa(base+8), "5", strconv.Itoa(int(c.Val)))
	case 3:
		params = append(params, strconv.Itoa(base+8), "2",
			strconv.Itoa(int(c.R)), strconv.Itoa(int(c.G)), strconv.Itoa(int(c.B)))
	}
	return params
}
