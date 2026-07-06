package vt

// VT500 flat state-transition table: [14 states][256 bytes] → packed action+nextState.
// Based on Paul Williams' canonical parser (vt100.net/emu/dec_ansi_parser)
// with modern extensions: colon=subparam, C1 suppressed in Ground under UTF-8.

// action enumerates parser actions.
type action uint8

const (
	actNone action = iota
	actPrint
	actExecute
	actClear
	actCollect
	actParam
	actSubparam
	actEscDispatch
	actCsiDispatch
	actHook
	actPut
	actUnhook
	actOscStart
	actOscPut
	actOscEnd
	actIgnore
	actMarker // collect private marker (?, >, <, !)
)

// transition packs action (high byte) + next state (low byte) into uint16.
type transition uint16

const noTransition transition = 0xFFFF

func mkT(act action, next parserState) transition {
	return transition(uint16(act)<<8 | uint16(next))
}

func (t transition) act() action       { return action(t >> 8) }
func (t transition) next() parserState { return parserState(t & 0xFF) }

// The tables.
var (
	stateTable  [numStates][256]transition
	entryAction [numStates]action
	exitAction  [numStates]action
)

func init() {
	// Fill with sentinel
	for s := range stateTable {
		for b := range stateTable[s] {
			stateTable[s][b] = noTransition
		}
	}

	// Entry/exit actions
	entryAction[stEscape] = actClear
	entryAction[stCsiEntry] = actClear
	entryAction[stDcsEntry] = actClear
	entryAction[stOscString] = actOscStart
	entryAction[stDcsPassthrough] = actHook
	exitAction[stOscString] = actOscEnd
	exitAction[stDcsPassthrough] = actUnhook

	// Build per-state transitions
	buildGround()
	buildEscape()
	buildEscapeIntermediate()
	buildCsiEntry()
	buildCsiParam()
	buildCsiIntermediate()
	buildCsiIgnore()
	buildDcsEntry()
	buildDcsParam()
	buildDcsIntermediate()
	buildDcsPassthrough()
	buildDcsIgnore()
	buildOscString()
	buildSosPmApcString()

	// Apply anywhere transitions to all states EXCEPT Ground for C1 (0x80-0x9F)
	applyAnywhere()

	// Verify completeness
	for si := range numStates {
		for b := range 256 {
			if stateTable[si][b] == noTransition {
				// Fill remaining with ignore-stay
				stateTable[si][b] = mkT(actNone, si)
			}
		}
	}
}

func setRange(state parserState, lo, hi byte, t transition) {
	for b := int(lo); b <= int(hi); b++ {
		stateTable[state][b] = t
	}
}

func buildGround() {
	s := stGround
	// C0 controls: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// CAN/SUB → execute+ground (handled specially, but table has them)
	stateTable[s][0x18] = mkT(actExecute, s)
	stateTable[s][0x1A] = mkT(actExecute, s)
	// ESC → Escape
	stateTable[s][0x1B] = mkT(actNone, stEscape)
	// Printable ASCII
	setRange(s, 0x20, 0x7F, mkT(actPrint, s))
	// C1 range (0x80-0x9F): in UTF-8 mode, suppress C1 → print (emit U+FFFD)
	setRange(s, 0x80, 0x9F, mkT(actPrint, s))
	// GR area (0xA0-0xFF): print (UTF-8 lead/continuation)
	setRange(s, 0xA0, 0xFF, mkT(actPrint, s))
}

func buildEscape() {
	s := stEscape
	// C0 controls: execute (stay in Escape)
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// Intermediates (0x20-0x2F): collect → EscapeIntermediate
	setRange(s, 0x20, 0x2F, mkT(actCollect, stEscapeIntermediate))
	// Final bytes (0x30-0x7E): dispatch
	setRange(s, 0x30, 0x4F, mkT(actEscDispatch, stGround))
	setRange(s, 0x51, 0x57, mkT(actEscDispatch, stGround))
	stateTable[s][0x59] = mkT(actEscDispatch, stGround)
	stateTable[s][0x5A] = mkT(actEscDispatch, stGround)
	stateTable[s][0x5C] = mkT(actEscDispatch, stGround)
	setRange(s, 0x60, 0x7E, mkT(actEscDispatch, stGround))
	// Special entries from Escape
	stateTable[s][0x5B] = mkT(actNone, stCsiEntry)       // [
	stateTable[s][0x5D] = mkT(actNone, stOscString)      // ]
	stateTable[s][0x50] = mkT(actNone, stDcsEntry)       // P (DCS)
	stateTable[s][0x58] = mkT(actNone, stSosPmApcString) // X (SOS)
	stateTable[s][0x5E] = mkT(actNone, stSosPmApcString) // ^ (PM)
	stateTable[s][0x5F] = mkT(actNone, stSosPmApcString) // _ (APC)
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes (0x80-0xFF): ignore (stay)
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildEscapeIntermediate() {
	s := stEscapeIntermediate
	// C0: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// More intermediates
	setRange(s, 0x20, 0x2F, mkT(actCollect, s))
	// Final bytes: dispatch
	setRange(s, 0x30, 0x7E, mkT(actEscDispatch, stGround))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: ignore
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildCsiEntry() {
	s := stCsiEntry
	// C0: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// Intermediates (0x20-0x2F): collect → CsiIntermediate
	setRange(s, 0x20, 0x2F, mkT(actCollect, stCsiIntermediate))
	// Params: digits (0x30-0x39) and ; (0x3B)
	setRange(s, 0x30, 0x39, mkT(actParam, stCsiParam))
	stateTable[s][0x3B] = mkT(actParam, stCsiParam)
	// Colon: subparam (modern extension, not CsiIgnore)
	stateTable[s][0x3A] = mkT(actSubparam, stCsiParam)
	// Private markers (0x3C-0x3F): ?, >, <, = → collect as marker
	setRange(s, 0x3C, 0x3F, mkT(actMarker, stCsiParam))
	// Final bytes (0x40-0x7E): dispatch
	setRange(s, 0x40, 0x7E, mkT(actCsiDispatch, stGround))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: ignore
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildCsiParam() {
	s := stCsiParam
	// C0: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// Intermediates: collect → CsiIntermediate
	setRange(s, 0x20, 0x2F, mkT(actCollect, stCsiIntermediate))
	// Params: digits and ;
	setRange(s, 0x30, 0x39, mkT(actParam, s))
	stateTable[s][0x3B] = mkT(actParam, s)
	// Colon: subparam
	stateTable[s][0x3A] = mkT(actSubparam, s)
	// 0x3C-0x3F in CsiParam → CsiIgnore (already saw params, invalid)
	setRange(s, 0x3C, 0x3F, mkT(actNone, stCsiIgnore))
	// Final bytes: dispatch
	setRange(s, 0x40, 0x7E, mkT(actCsiDispatch, stGround))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: ignore
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildCsiIntermediate() {
	s := stCsiIntermediate
	// C0: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// More intermediates
	setRange(s, 0x20, 0x2F, mkT(actCollect, s))
	// 0x30-0x3F → CsiIgnore
	setRange(s, 0x30, 0x3F, mkT(actNone, stCsiIgnore))
	// Final bytes: dispatch
	setRange(s, 0x40, 0x7E, mkT(actCsiDispatch, stGround))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: ignore
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildCsiIgnore() {
	s := stCsiIgnore
	// C0: execute
	setRange(s, 0x00, 0x17, mkT(actExecute, s))
	stateTable[s][0x19] = mkT(actExecute, s)
	setRange(s, 0x1C, 0x1F, mkT(actExecute, s))
	// 0x20-0x3F: stay
	setRange(s, 0x20, 0x3F, mkT(actNone, s))
	// Final bytes: → Ground (ignored dispatch)
	setRange(s, 0x40, 0x7E, mkT(actNone, stGround))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: ignore
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildDcsEntry() {
	s := stDcsEntry
	// C0: ignore
	setRange(s, 0x00, 0x17, mkT(actNone, s))
	stateTable[s][0x19] = mkT(actNone, s)
	setRange(s, 0x1C, 0x1F, mkT(actNone, s))
	// Intermediates: collect → DcsIntermediate
	setRange(s, 0x20, 0x2F, mkT(actCollect, stDcsIntermediate))
	// Params: digits, ;
	setRange(s, 0x30, 0x39, mkT(actParam, stDcsParam))
	stateTable[s][0x3B] = mkT(actParam, stDcsParam)
	// Colon: subparam
	stateTable[s][0x3A] = mkT(actSubparam, stDcsParam)
	// 0x3C-0x3F: marker → DcsParam
	setRange(s, 0x3C, 0x3F, mkT(actMarker, stDcsParam))
	// Final bytes (0x40-0x7E): → DcsPassthrough
	setRange(s, 0x40, 0x7E, mkT(actNone, stDcsPassthrough))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildDcsParam() {
	s := stDcsParam
	// C0: ignore
	setRange(s, 0x00, 0x17, mkT(actNone, s))
	stateTable[s][0x19] = mkT(actNone, s)
	setRange(s, 0x1C, 0x1F, mkT(actNone, s))
	// Intermediates: collect → DcsIntermediate
	setRange(s, 0x20, 0x2F, mkT(actCollect, stDcsIntermediate))
	// Params
	setRange(s, 0x30, 0x39, mkT(actParam, s))
	stateTable[s][0x3B] = mkT(actParam, s)
	stateTable[s][0x3A] = mkT(actSubparam, s)
	// 0x3C-0x3F → DcsIgnore
	setRange(s, 0x3C, 0x3F, mkT(actNone, stDcsIgnore))
	// Final bytes → DcsPassthrough
	setRange(s, 0x40, 0x7E, mkT(actNone, stDcsPassthrough))
	// DEL
	stateTable[s][0x7F] = mkT(actNone, s)
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildDcsIntermediate() {
	s := stDcsIntermediate
	// C0: ignore
	setRange(s, 0x00, 0x17, mkT(actNone, s))
	stateTable[s][0x19] = mkT(actNone, s)
	setRange(s, 0x1C, 0x1F, mkT(actNone, s))
	// More intermediates
	setRange(s, 0x20, 0x2F, mkT(actCollect, s))
	// 0x30-0x3F → DcsIgnore
	setRange(s, 0x30, 0x3F, mkT(actNone, stDcsIgnore))
	// Final bytes → DcsPassthrough
	setRange(s, 0x40, 0x7E, mkT(actNone, stDcsPassthrough))
	// DEL
	stateTable[s][0x7F] = mkT(actNone, s)
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildDcsPassthrough() {
	s := stDcsPassthrough
	// C0: put (pass through to handler)
	setRange(s, 0x00, 0x17, mkT(actPut, s))
	stateTable[s][0x19] = mkT(actPut, s)
	setRange(s, 0x1C, 0x1F, mkT(actPut, s))
	// Printable: put
	setRange(s, 0x20, 0x7E, mkT(actPut, s))
	// DEL: ignore
	stateTable[s][0x7F] = mkT(actNone, s)
	// High bytes: put
	setRange(s, 0x80, 0xFF, mkT(actPut, s))
}

func buildDcsIgnore() {
	s := stDcsIgnore
	// Everything stays in DcsIgnore
	setRange(s, 0x00, 0x17, mkT(actNone, s))
	stateTable[s][0x19] = mkT(actNone, s)
	setRange(s, 0x1C, 0x1F, mkT(actNone, s))
	setRange(s, 0x20, 0x7F, mkT(actNone, s))
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

func buildOscString() {
	s := stOscString
	// Most bytes: put
	setRange(s, 0x00, 0x06, mkT(actOscPut, s))
	// BEL (0x07): terminate OSC → Ground (via exit action osc_end)
	stateTable[s][0x07] = mkT(actNone, stGround)
	setRange(s, 0x08, 0x17, mkT(actOscPut, s))
	stateTable[s][0x19] = mkT(actOscPut, s)
	setRange(s, 0x1C, 0x1F, mkT(actOscPut, s))
	// 0x18 (CAN): → Ground
	stateTable[s][0x18] = mkT(actNone, stGround)
	// 0x1A (SUB): → Ground
	stateTable[s][0x1A] = mkT(actNone, stGround)
	// 0x1B (ESC): → Escape (exit action fires osc_end)
	stateTable[s][0x1B] = mkT(actNone, stEscape)
	// Printable
	setRange(s, 0x20, 0x7F, mkT(actOscPut, s))
	// High bytes: put (UTF-8 content in OSC payloads)
	setRange(s, 0x80, 0xFF, mkT(actOscPut, s))
}

func buildSosPmApcString() {
	s := stSosPmApcString
	// Consume everything silently until ST (ESC \) or CAN/SUB
	setRange(s, 0x00, 0x17, mkT(actNone, s))
	stateTable[s][0x19] = mkT(actNone, s)
	setRange(s, 0x1C, 0x1F, mkT(actNone, s))
	stateTable[s][0x18] = mkT(actNone, stGround)
	stateTable[s][0x1A] = mkT(actNone, stGround)
	stateTable[s][0x1B] = mkT(actNone, stEscape)
	setRange(s, 0x20, 0x7F, mkT(actNone, s))
	setRange(s, 0x80, 0xFF, mkT(actNone, s))
}

// applyAnywhere applies the "anywhere" transitions for CAN, SUB, ESC, and C1.
// C1 (0x80-0x9F) anywhere transitions are NOT applied to Ground (UTF-8 mode).
func applyAnywhere() {
	for si := range numStates {
		s := si
		// CAN (0x18) → execute + Ground
		stateTable[s][0x18] = mkT(actExecute, stGround)
		// SUB (0x1A) → execute + Ground
		stateTable[s][0x1A] = mkT(actExecute, stGround)
		// ESC (0x1B) → Escape (clear is entry action of Escape)
		if s != stEscape {
			stateTable[s][0x1B] = mkT(actNone, stEscape)
		}

		if s == stGround {
			continue // suppress C1 anywhere in Ground (UTF-8 mode)
		}

		// C1 controls in non-Ground states
		// 0x80-0x8F: execute + Ground
		if s != stDcsPassthrough && s != stOscString && s != stSosPmApcString {
			setRange(s, 0x80, 0x8F, mkT(actExecute, stGround))
			// 0x90 (DCS): → DcsEntry
			stateTable[s][0x90] = mkT(actNone, stDcsEntry)
			// 0x91-0x97: execute + Ground
			setRange(s, 0x91, 0x97, mkT(actExecute, stGround))
			// 0x98 (SOS): → SosPmApcString
			stateTable[s][0x98] = mkT(actNone, stSosPmApcString)
			// 0x99: execute + Ground
			stateTable[s][0x99] = mkT(actExecute, stGround)
			// 0x9A: execute + Ground
			stateTable[s][0x9A] = mkT(actExecute, stGround)
			// 0x9B (CSI): → CsiEntry
			stateTable[s][0x9B] = mkT(actNone, stCsiEntry)
			// 0x9C (ST): → Ground
			stateTable[s][0x9C] = mkT(actNone, stGround)
			// 0x9D (OSC): → OscString
			stateTable[s][0x9D] = mkT(actNone, stOscString)
			// 0x9E (PM): → SosPmApcString
			stateTable[s][0x9E] = mkT(actNone, stSosPmApcString)
			// 0x9F (APC): → SosPmApcString
			stateTable[s][0x9F] = mkT(actNone, stSosPmApcString)
		}

		// For DcsPassthrough: 0x9C (ST) causes unhook (exit action) + Ground
		if s == stDcsPassthrough {
			stateTable[s][0x9C] = mkT(actNone, stGround)
		}
		// For OscString: 0x9C (ST) causes osc_end (exit action) + Ground
		if s == stOscString {
			stateTable[s][0x9C] = mkT(actNone, stGround)
		}
		// For SosPmApcString: 0x9C (ST) → Ground
		if s == stSosPmApcString {
			stateTable[s][0x9C] = mkT(actNone, stGround)
		}
	}
}
