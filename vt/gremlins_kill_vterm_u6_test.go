package vt

// Mutant-killing tests for unit vterm-u6 (package vt).
// Targets surviving gremlins mutants in screen.go, sgr.go, table.go, tabstops.go.
// Internal test package so unexported methods/fields/consts are reachable.
// Every identifier is prefixed gk_vterm_u6_ / Test_gk_vterm_u6_ to avoid
// collisions with sibling units that share this package dir.

import "testing"

// gk_vterm_u6_didPanic reports whether fn panicked. Used for boundary mutants
// whose only observable effect is an out-of-range slice write (the mutant
// proceeds past a `col < len` guard and indexes out of range).
func gk_vterm_u6_didPanic(fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	fn()
	return false
}

// gk_vterm_u6_groups builds n single-param semicolon groups directly on the
// parser state (mirrors what "\x1b[v0;v1;...m" produces): numGroups=len(vals),
// each group length 1 with pParams[i]=vals[i].
func gk_vterm_u6_groups(s *Screen, vals ...uint16) {
	s.numGroups = uint8(len(vals))
	s.numParams = uint8(len(vals))
	for i, v := range vals {
		s.pParams[i] = v
		s.pGroupLen[i] = 1
	}
}

// gk_vterm_u6_exitAlt sets up an alt-screen with the given saved main-screen
// scrollBottom, exits alt-screen, and returns the resulting Screen so the
// post-restore scrollBottom clamp (screen.go:465-466) can be inspected.
func gk_vterm_u6_exitAlt(t *testing.T, height, width, savedScrollBottom int) *Screen {
	t.Helper()
	s := New(height, width)
	s.InAltScreen = true
	s.savedMainCells = make([][]Cell, height)
	for i := range s.savedMainCells {
		s.savedMainCells[i] = make([]Cell, width)
	}
	s.savedMainCurY = 0
	s.savedMainCurX = 0
	s.savedMainScrollTop = 0
	s.savedMainScrollBottom = savedScrollBottom
	s.exitAltScreen()
	return s
}

// --- screen.go:465-466 (exitAltScreen scrollBottom clamp) ---

func Test_gk_vterm_u6_ExitAltScreenClampsScrollBottom(t *testing.T) {
	// 465:20 (>= -> < negation, >= -> > boundary) and 466:29 (Height-1:
	// - -> + arithmetic / invert-negatives): the boundary savedScrollBottom ==
	// Height must clamp to exactly Height-1.
	s := gk_vterm_u6_exitAlt(t, 5, 10, 5)
	if s.scrollBottom != 4 {
		t.Errorf("exitAltScreen(savedScrollBottom=5, height=5): scrollBottom = %d, want 4", s.scrollBottom)
	}
	// savedScrollBottom below Height must be preserved (clamp not taken) — pins
	// the negation mutant, which would clamp this case to 4.
	s2 := gk_vterm_u6_exitAlt(t, 5, 10, 2)
	if s2.scrollBottom != 2 {
		t.Errorf("exitAltScreen(savedScrollBottom=2, height=5): scrollBottom = %d, want 2", s2.scrollBottom)
	}
}

// --- sgr.go:119/129/137 (parseExtColorGroup semicolon-form bounds; return value) ---

func Test_gk_vterm_u6_ParseExtColor38LastGroupReturn(t *testing.T) {
	// 119:6 (i+1 -> i-1) and 119:9 (>= -> >): with "38" as the only/last group,
	// the original bails returning i (=0); both mutations make it proceed and
	// return i+1 (=1) via the empty mode group.
	s := &Screen{}
	gk_vterm_u6_groups(s, 38)
	var c Color
	if got := s.parseExtColorGroup(0, &c); got != 0 {
		t.Errorf("parseExtColorGroup(0) with [38]: ret = %d, want 0 (bail, no following group)", got)
	}
	if c.Type != 0 {
		t.Errorf("parseExtColorGroup(0) with [38]: c.Type = %d, want 0 (no color set)", c.Type)
	}
}

func Test_gk_vterm_u6_ParseExtColor256NoValueReturn(t *testing.T) {
	// 129:7 (i+2 -> i-2) and 129:10 (< -> <=): mode 5 (256-color) with no value
	// group present. The original returns i+1 (=1); both mutations enter the
	// branch and return i+2 (=2).
	s := &Screen{}
	gk_vterm_u6_groups(s, 38, 5)
	var c Color
	if got := s.parseExtColorGroup(0, &c); got != 1 {
		t.Errorf("parseExtColorGroup(0) with [38,5]: ret = %d, want 1 (missing value group)", got)
	}
	if c.Type != 0 {
		t.Errorf("parseExtColorGroup(0) with [38,5]: c.Type = %d, want 0 (no color set)", c.Type)
	}
}

func Test_gk_vterm_u6_ParseExtColorRGBNoBlueReturn(t *testing.T) {
	// 137:7 (i+4 -> i-4) and 137:10 (< -> <=): mode 2 (RGB) with the blue group
	// absent. The original returns i+1 (=1); both mutations enter the branch and
	// return i+4 (=4).
	s := &Screen{}
	gk_vterm_u6_groups(s, 38, 2, 10, 20)
	var c Color
	if got := s.parseExtColorGroup(0, &c); got != 1 {
		t.Errorf("parseExtColorGroup(0) with [38,2,10,20]: ret = %d, want 1 (incomplete RGB)", got)
	}
	if c.Type != 0 {
		t.Errorf("parseExtColorGroup(0) with [38,2,10,20]: c.Type = %d, want 0 (no color set)", c.Type)
	}
}

// --- sgr.go:131 / 141:42 (Len>0 guards reject empty value groups) ---

func Test_gk_vterm_u6_ParseExtColor256EmptyValueGroup(t *testing.T) {
	// 131:14 (vg.Len > 0 -> >= 0): a counted-but-empty value group (Len 0) must
	// NOT set a color. Mutating > to >= sets a bogus Type-2 color from the empty
	// group. State built directly (the guard defends against this exact shape).
	s := &Screen{}
	s.numGroups = 3
	s.numParams = 2
	s.pGroupLen[0] = 1
	s.pGroupLen[1] = 1
	s.pGroupLen[2] = 0 // empty value group
	s.pParams[0] = 38
	s.pParams[1] = 5
	var c Color
	if got := s.parseExtColorGroup(0, &c); got != 2 {
		t.Errorf("parseExtColorGroup(0) [38,5,<empty>]: ret = %d, want 2", got)
	}
	if c.Type != 0 {
		t.Errorf("parseExtColorGroup(0) [38,5,<empty>]: c.Type = %d, want 0 (empty value group must not set color)", c.Type)
	}
}

func Test_gk_vterm_u6_ParseExtColorRGBEmptyBlueGroup(t *testing.T) {
	// 141:42 (bg.Len > 0 -> >= 0): a counted-but-empty blue group (Len 0) must
	// NOT set a color. Mutating > to >= sets a bogus Type-3 color.
	s := &Screen{}
	s.numGroups = 5
	s.numParams = 4
	s.pGroupLen[0] = 1
	s.pGroupLen[1] = 1
	s.pGroupLen[2] = 1
	s.pGroupLen[3] = 1
	s.pGroupLen[4] = 0 // empty blue group
	s.pParams[0] = 38
	s.pParams[1] = 2
	s.pParams[2] = 10
	s.pParams[3] = 20
	var c Color
	if got := s.parseExtColorGroup(0, &c); got != 4 {
		t.Errorf("parseExtColorGroup(0) RGB empty-blue: ret = %d, want 4", got)
	}
	if c.Type != 0 {
		t.Errorf("parseExtColorGroup(0) RGB empty-blue: c.Type = %d, want 0 (empty blue group must not set color)", c.Type)
	}
}

// --- sgr.go:269 (sgrParamsString underline-color emission) ---

func Test_gk_vterm_u6_SgrParamsStringUnderlineColor(t *testing.T) {
	// 269:28 (st.UnderlineColor.Type != 0 -> == 0): a set underline color must
	// emit its "58;..." params; the negation drops them.
	if got := sgrParamsString(Style{UnderlineColor: Color{Type: 2, Val: 5}}); got != "0;58;5;5" {
		t.Errorf("sgrParamsString(underline 256-color 5) = %q, want %q", got, "0;58;5;5")
	}
	// false branch: no underline color -> no "58" params.
	if got := sgrParamsString(Style{Bold: true}); got != "0;1" {
		t.Errorf("sgrParamsString(bold only) = %q, want %q", got, "0;1")
	}
}

// --- table.go:418 (SosPmApcString ST -> Ground transition) ---

func Test_gk_vterm_u6_StateTableSosPmApcST(t *testing.T) {
	// 418:8 (s == stSosPmApcString -> !=): ST (0x9C) in the SOS/PM/APC string
	// state must transition to Ground. buildSosPmApcString leaves [0x9C] as
	// stay-in-state; applyAnywhere's `if s == stSosPmApcString` overwrites it to
	// Ground. Negating that condition skips the overwrite, leaving stay-in-state.
	if got := stateTable[stSosPmApcString][0x9C].next(); got != stGround {
		t.Errorf("stateTable[SosPmApcString][0x9C].next() = %d, want stGround (%d)", got, stGround)
	}
	// Contrast: OscString's 0x9C->Ground is set by a different branch (line 415),
	// unaffected by the line-418 mutant.
	if got := stateTable[stOscString][0x9C].next(); got != stGround {
		t.Errorf("stateTable[OscString][0x9C].next() = %d, want stGround (%d)", got, stGround)
	}
}

// --- tabstops.go:18 (setTabStop bounds) ---

func Test_gk_vterm_u6_SetTabStopBoundaries(t *testing.T) {
	// 18:9 (col >= 0 -> col > 0): column 0 must be settable.
	s := New(5, 24)
	s.setTabStop(0)
	if !s.tabStops[0] {
		t.Errorf("setTabStop(0): tabStops[0] = false, want true")
	}
	// 18:21 (col < len -> col <= len): col == len is a safe no-op; the mutant
	// indexes one past the end and panics.
	s2 := New(5, 24)
	if gk_vterm_u6_didPanic(func() { s2.setTabStop(24) }) {
		t.Errorf("setTabStop(24): panicked, want safe no-op")
	}
	if len(s2.tabStops) != 24 {
		t.Errorf("setTabStop(24): len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}

// --- tabstops.go:25 (clearTabStop nil-reinit guard) ---

func Test_gk_vterm_u6_ClearTabStopReinitGuard(t *testing.T) {
	// 25:16 (s.tabStops == nil -> !=): with a populated custom table the original
	// skips re-init and preserves it; the negation re-initializes, wiping the
	// custom stop and restoring the default every-8 stops.
	s := New(5, 24)
	s.tabStops = make([]bool, 24)
	s.tabStops[3] = true
	s.clearTabStop(5)
	if !s.tabStops[3] {
		t.Errorf("clearTabStop(5): tabStops[3] = false, want true (custom stop wiped by spurious re-init)")
	}
	if s.tabStops[8] {
		t.Errorf("clearTabStop(5): tabStops[8] = true, want false (default stop restored by spurious re-init)")
	}
	// nil branch: a nil table must be initialized (len == Width).
	s2 := New(5, 24)
	s2.clearTabStop(8)
	if len(s2.tabStops) != 24 {
		t.Errorf("clearTabStop(8) on nil table: len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}

// --- tabstops.go:28 (clearTabStop bounds) ---

func Test_gk_vterm_u6_ClearTabStopBoundaries(t *testing.T) {
	// 28:9 (col >= 0 -> col > 0): column 0 must be clearable.
	s := New(5, 24)
	s.tabStops = make([]bool, 24)
	s.tabStops[0] = true
	s.clearTabStop(0)
	if s.tabStops[0] {
		t.Errorf("clearTabStop(0): tabStops[0] = true, want false")
	}
	// 28:21 (col < len -> col <= len): col == len is a safe no-op; the mutant
	// indexes one past the end and panics.
	s2 := New(5, 24)
	s2.tabStops = make([]bool, 24)
	if gk_vterm_u6_didPanic(func() { s2.clearTabStop(24) }) {
		t.Errorf("clearTabStop(24): panicked, want safe no-op")
	}
	if len(s2.tabStops) != 24 {
		t.Errorf("clearTabStop(24): len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}
