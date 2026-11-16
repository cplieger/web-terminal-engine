package vt

// Color represents a terminal color (default, 8-color, 256-color, or RGB).
type Color struct {
	// Type selects the color space: 0=default, 1=basic(0-7), 2=256, 3=rgb.
	Type uint8
	// Val is the palette index for basic (0-7) or 256-color modes.
	Val uint8
	R   uint8 // red component, used when Type == 3
	G   uint8 // green component, used when Type == 3
	B   uint8 // blue component, used when Type == 3
}

// Style holds SGR attributes for a cell.
type Style struct {
	FG Color
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

// Theme holds the default foreground, background, and cursor colors a terminal
// reports via OSC 10/11/12 queries and restores on OSC 110/111/112 reset — the
// engine's analogue of xterm's -fg/-bg/-cr resources. Each color is packed
// 0xRRGGBB (use RGB to build one). A consumer sets these to its real rendering
// colors via WithTheme so color-probing apps (light/dark detection, "reset to
// default") see the terminal's actual appearance rather than a hardcoded guess.
type Theme struct {
	Foreground int32
	Background int32
	Cursor     int32
}

// RGB packs 8-bit red, green, and blue channels into a 0xRRGGBB value.
func RGB(r, g, b uint8) int32 {
	return int32(r)<<16 | int32(g)<<8 | int32(b)
}

// titleEntry is one saved (icon, window) title pair on the XTWINOPS title
// stack (CSI 22/23 t). A push snapshots both current titles; a pop restores the
// fields the Ps parameter selects.
type titleEntry struct {
	icon   string
	window string
}

// Cell is a single character cell in the screen grid.
type Cell struct {
	Hyperlink    string
	Ch           rune
	Style        Style
	Protected    bool
	IsoProtected bool
	// drawn marks a cell a character was written to, as xterm's CHARDRAWN:
	// a re-wrap carries a line up to its last drawn cell, so a space the
	// program wrote is text and the blanks an erase or a new row leave are not.
	drawn bool
	// widePad marks the blank autowrap leaves in a row's last column when a
	// wide character does not fit there: layout, not text, so a re-wrap drops
	// it unless the cursor is on it.
	widePad bool
	// wideCut marks a wide character written in a row's last column, or laid
	// on a one-column row, with no room for its right half: a re-wrap or an
	// alternate-screen widening gives the half back. A wide glyph DECFRA
	// fills into one cell lacks the mark and stays one cell.
	wideCut bool
}

// ParserState holds the VT500-style state machine state used by the
// screen's byte-at-a-time parser. Embedded in Screen.
type ParserState struct {
	oscBuf  []byte // buffered OSC payload
	dcsBuf  []byte // buffered DCS data
	utf8Buf [4]byte
	// Param accumulation: fixed flat array of numeric values.
	// Groups (semicolon-separated) contain subparams (colon-separated).
	pParams   [maxParams]uint16
	pGroupLen [maxParams]uint8 // group length at each group's start index
	numParams uint8            // total slots used in pParams
	numGroups uint8            // number of semicolon-separated groups
	curParam  uint16           // current param being accumulated

	pIntermed [maxIntermed]byte
	numInterm uint8
	utf8Len   uint8
	utf8Got   uint8
	pState    parserState
	dcsFunc   dcsFunction
	ignoring  bool // set when params overflow
	// Private marker for CSI (?, >, <, !)
	privateMarker byte
	oscOverflow   bool // the OSC payload ran past its cap
	oscClipboard  bool // the OSC payload is an OSC 52 grown past maxOSCLen
	dcsOverflow   bool // the DCS data ran past maxDCSLen
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
	dcsNone      dcsFunction = iota
	dcsDecrqss               // DCS $ q ... ST
	dcsXTGetTcap             // DCS + q ... ST (XTGETTCAP terminfo query)
	dcsIgnored               // unknown/unhandled DCS
)

// Buffer size limits.
const (
	maxOSCLen   = 4096 // max bytes buffered for an OSC payload other than OSC 52
	maxParams   = 32   // max param slots (params + subparams)
	maxIntermed = 2    // max intermediate bytes
	maxDCSLen   = 256  // max DCS data for DECRQSS and XTGETTCAP
)

// MaxOSC52Len is the most bytes an OSC 52 payload ("52;<targets>;<base64>",
// between OSC and its terminator) may hold; a longer one is refused whole
// (TakeClipboardRefused). 1 MiB is tmux's default input-buffer-size
// (https://github.com/tmux/tmux/blob/75fac4a77bb40dc655f8b217071c224c0c819c7f/options-table.c#L540-L547)
// and carries about 768 KiB of text, past the 256 KiB kitty buffers for other
// escapes (https://github.com/kovidgoyal/kitty/blob/8e5487e9d4a1dd79c3a769e0ba82326b8ad7c37f/kitty/vt-parser.c#L18-L21);
// xterm.js allows 10 MB and foot, WezTerm and Windows Terminal set no bound.
const MaxOSC52Len = 1 << 20
