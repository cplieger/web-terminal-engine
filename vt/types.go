package vt

// Color represents a terminal color (default, 8-color, 256-color, or RGB).
type Color struct {
	// Type selects the color space: 0=default, 1=basic(0-7), 2=256, 3=rgb.
	Type uint8
	// Val is the palette index for basic (0-7) or 256-color modes.
	Val uint8
	// R is the red component for RGB colors.
	R uint8
	// G is the green component for RGB colors.
	G uint8
	// B is the blue component for RGB colors.
	B uint8
}

// Style holds SGR attributes for a cell.
type Style struct {
	// FG is the foreground color.
	FG Color
	// BG is the background color.
	BG Color
	// UnderlineColor is the color used for underline decorations (SGR 58).
	UnderlineColor Color
	// Bold indicates SGR bold (attribute 1).
	Bold bool
	// Dim indicates SGR dim/faint (attribute 2).
	Dim bool
	// Italic indicates SGR italic (attribute 3).
	Italic bool
	// Underline indicates SGR underline (attribute 4).
	Underline bool
	// DoubleUnderline indicates SGR double-underline (attribute 21).
	DoubleUnderline bool
	// Overline indicates SGR overline (attribute 53).
	Overline bool
	// Blink indicates SGR blink (attribute 5).
	Blink bool
	// Inverse indicates SGR inverse/reverse video (attribute 7).
	Inverse bool
	// Strikethrough indicates SGR strikethrough (attribute 9).
	Strikethrough bool
	// Hidden indicates SGR hidden/invisible (attribute 8).
	Hidden bool
}

// Cell is a single character cell in the screen grid.
type Cell struct {
	// Hyperlink is the OSC 8 hyperlink URI; empty means no link.
	Hyperlink string
	// Style holds the SGR attributes applied to this cell.
	Style Style
	// Ch is the Unicode codepoint displayed in this cell.
	Ch rune
}

// ParserState holds the VT500-style state machine state used by the
// screen's byte-at-a-time parser. Embedded in Screen.
type ParserState struct {
	oscBuf  []byte // buffered OSC payload (between ESC ] and BEL/ST)
	dcsBuf  []byte // buffered DCS data
	utf8Buf [4]byte
	// Param accumulation: fixed flat array of numeric values.
	// Groups (semicolon-separated) contain subparams (colon-separated).
	pParams   [maxParams]uint16
	pGroupLen [maxParams]uint8 // group length at each group's start index
	numParams uint8            // total slots used in pParams
	numGroups uint8            // number of semicolon-separated groups
	curParam  uint16           // current param being accumulated
	paramSeen bool             // whether any digit was seen for curParam

	pIntermed [maxIntermed]byte
	numInterm uint8
	utf8Len   uint8
	utf8Got   uint8
	pState    parserState
	dcsFunc   dcsFunction
	ignoring  bool // set when params overflow
	// Private marker for CSI (?, >, <, !)
	privateMarker byte
}

// parserState enumerates the VT500-style state machine states.
type parserState uint8

const (
	stGround parserState = iota
	stEscape
	stEscapeIntermediate
	stCsiEntry
	stCsiParam
	stCsiIntermediate
	stCsiIgnore
	stDcsEntry
	stDcsParam
	stDcsIntermediate
	stDcsPassthrough
	stDcsIgnore
	stOscString
	stSosPmApcString
	numStates // sentinel = 14
)

// dcsFunction identifies which DCS handler is active.
type dcsFunction uint8

const (
	dcsNone    dcsFunction = iota
	dcsDecrqss             // DCS $ q ... ST
	dcsIgnored             // unknown/unhandled DCS
)

// Buffer size limits.
const (
	maxOSCLen   = 4096 // max bytes buffered for an OSC payload
	maxParams   = 32   // max param slots (params + subparams)
	maxIntermed = 2    // max intermediate bytes
	maxDCSLen   = 256  // max DCS data for DECRQSS
)
