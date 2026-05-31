package vt

// Color represents a terminal color (default, 8-color, 256-color, or RGB).
type Color struct {
	Type    uint8 // 0=default, 1=basic(0-7), 2=256, 3=rgb
	Val     uint8 // basic index or 256-color index
	R, G, B uint8
}

// Style holds SGR attributes for a cell.
type Style struct {
	FG              Color
	BG              Color
	UnderlineColor  Color
	Bold            bool
	Dim             bool
	Italic          bool
	Underline       bool
	DoubleUnderline bool
	Overline        bool
	Blink           bool
	Inverse         bool
	Strikethrough   bool
	Hidden          bool
}

// Cell is a single character with its style.
type Cell struct {
	Ch    rune
	Style Style
}

// ParserState holds the VT500-style state machine state used by the
// screen's byte-at-a-time parser. Embedded in Screen.
type ParserState struct {
	pParams   []byte
	pIntermed []byte
	utf8Buf   [4]byte
	utf8Len   uint8
	utf8Got   uint8
	pState    parserState
}

// parserState enumerates the VT500-style state machine states used by the
// screen's byte-at-a-time parser.
type parserState uint8

const (
	stateGround parserState = iota
	stateEscape
	stateEscapeIntermediate
	stateCsiEntry
	stateCsiParam
	stateCsiIntermediate
	stateOscString
	stateOscEsc // saw ESC inside OSC, waiting for '\'
)
