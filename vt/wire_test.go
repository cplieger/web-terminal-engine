package vt

import (
	"fmt"
	"strings"
	"testing"
)

// TestWireRunEncoding locks the binary wire contract by asserting that
// WireRun field values and attribute bit positions match the canonical
// encoder in terminal/wire_binary.go (the authoritative byte layout).
func TestWireRunEncoding(t *testing.T) {
	s := New(3, 10)
	// Bold red FG (ANSI 1), default BG, with italic+underline.
	s.Write([]byte("\x1b[1;3;4;31mHi\x1b[0m rest"))
	runs := s.RenderRowWire(0)
	if len(runs) < 2 {
		t.Fatalf("expected >=2 runs, got %d", len(runs))
	}

	r := runs[0]
	if r.T != "Hi" {
		t.Errorf("run[0].T = %q, want %q", r.T, "Hi")
	}
	// Red (ANSI index 1) → 0xcc0403 per basic16RGB palette.
	if r.F != 0xcc0403 {
		t.Errorf("run[0].F = 0x%06x, want 0xcc0403", r.F)
	}
	// Default BG → -1.
	if r.B != -1 {
		t.Errorf("run[0].B = %d, want -1", r.B)
	}
	// Default underline color → -1.
	if r.Uc != -1 {
		t.Errorf("run[0].Uc = %d, want -1", r.Uc)
	}
	// Attrs: bold=1 | italic=2 | underline=4 = 7.
	if r.A != 7 {
		t.Errorf("run[0].A = %d, want 7 (bold|italic|underline)", r.A)
	}

	// Second run: plain text, default colors, no attrs.
	r2 := runs[1]
	if r2.F != -1 {
		t.Errorf("run[1].F = %d, want -1 (default)", r2.F)
	}
	if r2.A != 0 {
		t.Errorf("run[1].A = %d, want 0", r2.A)
	}
}

// TestWireRunRGBColor verifies true-color encoding in wire format.
func TestWireRunRGBColor(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("\x1b[38;2;255;128;0mX\x1b[0m"))
	runs := s.RenderRowWire(0)
	if len(runs) == 0 {
		t.Fatal("no runs")
	}
	// RGB(255,128,0) → 0xFF8000.
	if runs[0].F != 0xFF8000 {
		t.Errorf("F = 0x%06x, want 0xFF8000", runs[0].F)
	}
}

// TestWireRunAllAttributes verifies each attribute bit position per spec.
func TestWireRunAllAttributes(t *testing.T) {
	tests := []struct {
		seq  string
		name string
		bit  uint16
	}{
		{seq: "\x1b[1m", bit: 1, name: "bold"},
		{seq: "\x1b[3m", bit: 2, name: "italic"},
		{seq: "\x1b[4m", bit: 4, name: "underline"},
		{seq: "\x1b[7m", bit: 8, name: "inverse"},
		{seq: "\x1b[9m", bit: 16, name: "strikethrough"},
		{seq: "\x1b[2m", bit: 32, name: "dim"},
		{seq: "\x1b[8m", bit: 64, name: "hidden"},
		{seq: "\x1b[5m", bit: 128, name: "blink"},
		{seq: "\x1b[6m", bit: 128, name: "rapid-blink"},
		{seq: "\x1b[53m", bit: 256, name: "overline"},
		{seq: "\x1b[21m", bit: 512, name: "double-underline"},
	}
	for _, tc := range tests {
		s := New(1, 5)
		s.Write([]byte(tc.seq + "X\x1b[0m"))
		runs := s.RenderRowWire(0)
		if len(runs) == 0 {
			t.Errorf("%s: no runs", tc.name)
			continue
		}
		if runs[0].A&tc.bit == 0 {
			t.Errorf("%s: bit %d not set in A=%d", tc.name, tc.bit, runs[0].A)
		}
	}
}

// TestRenderRowWireRejectsOutOfRangeRow verifies the row-bounds guard:
// a row index equal to Height returns nil, while the last valid row is non-nil.
// The last row is given content first, because a row of nothing but padding
// legitimately encodes to zero runs now (trimTrailingBlanks) — so an empty
// screen could not tell the bounds guard apart from the trim.
func TestRenderRowWireRejectsOutOfRangeRow(t *testing.T) {
	s := New(3, 8) // valid rows 0..2
	s.Write([]byte("\x1b[3;1Hx"))
	if got := s.RenderRowWire(s.Height); got != nil {
		t.Errorf("RenderRowWire(Height=%d) = %v, want nil", s.Height, got)
	}
	if got := s.RenderRowWire(s.Height - 1); got == nil {
		t.Errorf("RenderRowWire(Height-1=%d) = nil, want non-nil", s.Height-1)
	}
}

// TestBasic16RGBPaletteBounds verifies the 16-entry palette lookup: in-range
// indices map to their colors, and an out-of-range index returns the plain-white
// fallback rather than panicking.
func TestBasic16RGBPaletteBounds(t *testing.T) {
	if got := basic16RGB(0); got != 0x000000 {
		t.Errorf("basic16RGB(0) = 0x%06x, want 0x000000", got)
	}
	if got := basic16RGB(15); got != 0xffffff {
		t.Errorf("basic16RGB(15) = 0x%06x, want 0xffffff", got)
	}
	if got := basic16RGB(16); got != 0xdddddd {
		t.Errorf("basic16RGB(16) = 0x%06x, want 0xdddddd (out-of-range fallback)", got)
	}
}

// wireRowText concatenates a wire row's run text, i.e. the row as the client
// sees it: one rune per cell column, U+FFFF for a wide glyph's second half.
func wireRowText(runs []WireRun) string {
	var text strings.Builder
	for _, run := range runs {
		text.WriteString(run.T)
	}
	return text.String()
}

// TestRenderRowWireWidePlaceholder verifies a wide character is followed by a
// U+FFFF spacer placeholder in the wire text, and that one rune still maps to
// one cell column.
func TestRenderRowWireWidePlaceholder(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("A漢B"))
	got := wireRowText(s.RenderRowWire(0))
	// Exact, not Contains: the row's six trailing padding cells no longer reach
	// the wire (trimTrailingBlanks), so this pins both the placeholder and the
	// trim in one comparison.
	if got != "A漢\uFFFFB" {
		t.Errorf("wire row = %q, want %q", got, "A漢\uFFFFB")
	}
	// Column alignment is the property the placeholder exists for: rune index
	// == cell column, which is what the client's caret positioner (glyphAt)
	// counts. Trailing padding no longer reaches the wire, so pin it on a row
	// whose LAST column is written — which also pins that MID-line blanks are
	// never trimmed.
	s2 := New(1, 10)
	s2.Write([]byte("A漢B\x1b[1;10Hz"))
	if got := wireRowText(s2.RenderRowWire(0)); got != "A漢\uFFFFB     z" {
		t.Errorf("wire row = %q, want %q (one rune per column, interior blanks kept)", got, "A漢\uFFFFB     z")
	}
}

// TestRenderRowWire256Color verifies the 256-color -> 0xRRGGBB wire conversion
// (color256RGB via colorToWire) for the <16 palette delegate, the 6x6x6 color
// cube, and the grayscale ramp, driven through the public SGR + RenderRowWire path.
func TestRenderRowWire256Color(t *testing.T) {
	cases := []struct {
		seq  string
		idx  int
		want int32
	}{
		{"\x1b[38;5;9mX\x1b[0m", 9, 0xf2201f},     // <16: delegates to basic-16 palette
		{"\x1b[38;5;16mX\x1b[0m", 16, 0x000000},   // cube: first entry, black (not the palette's slot 16)
		{"\x1b[38;5;21mX\x1b[0m", 21, 0x0000ff},   // cube: pure blue
		{"\x1b[38;5;46mX\x1b[0m", 46, 0x00ff00},   // cube: pure green
		{"\x1b[38;5;196mX\x1b[0m", 196, 0xff0000}, // cube: pure red
		{"\x1b[38;5;232mX\x1b[0m", 232, 0x080808}, // grayscale ramp: darkest
		{"\x1b[38;5;255mX\x1b[0m", 255, 0xeeeeee}, // grayscale ramp: lightest
	}
	for _, tc := range cases {
		s := New(1, 4)
		s.Write([]byte(tc.seq))
		runs := s.RenderRowWire(0)
		if len(runs) == 0 {
			t.Fatalf("256-color %d: no runs", tc.idx)
		}
		if runs[0].F != tc.want {
			t.Errorf("256-color %d -> F = 0x%06x, want 0x%06x", tc.idx, runs[0].F, tc.want)
		}
	}
}

// TestTrailingBlankTrim drives the trailing-blank trim through the public
// RenderRowWire path, one case per guard. The trim's whole value is that a grid
// pads every row to the full width, so a short line otherwise ships (and
// selects, and copies) dozens of spaces the application never printed; each
// guard below names the case where a trailing blank is CONTENT instead.
func TestTrailingBlankTrim(t *testing.T) {
	const width = 10
	cases := []struct {
		name  string
		write string
		want  string
	}{
		{
			// The app's OWN trailing space goes too: in a cell grid it is
			// indistinguishable from padding, which is the ambiguity xterm.js
			// and tmux already accept on their copy paths.
			name:  "padding is dropped",
			write: "$ ",
			want:  "$",
		},
		{
			name:  "interior blanks are kept",
			write: "a\x1b[1;5Hb",
			want:  "a   b",
		},
		{
			name:  "leading blanks are kept",
			write: "\x1b[1;4Hb",
			want:  "   b",
		},
		{
			name:  "a fully blank row encodes to nothing",
			write: "",
			want:  "",
		},
		{
			// An erase writes the application's current background into the
			// cells it clears, so this tail is a painted region: dropping it
			// would repaint it in the theme default.
			name:  "a colored tail is style-carrying",
			write: "\x1b[41mx\x1b[K",
			want:  "x         ",
		},
		{
			// Same erase, default background: nothing to preserve.
			name:  "an uncolored erase tail is padding",
			write: "x\x1b[K",
			want:  "x",
		},
		{
			// A blank inside the app's own OSC 8 anchor is part of the link.
			name:  "blanks inside an OSC 8 link are kept",
			write: "\x1b]8;;https://example.com\x07ab  \x1b]8;;\x07",
			want:  "ab  ",
		},
		{
			// U+FFFF is a wide glyph's second half, not a space: trimming it
			// would strand the glyph without its spacer cell.
			name:  "a wide glyph's continuation is not a blank",
			write: "\x1b[1;9H漢",
			want:  "        漢\uFFFF",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(2, width)
			s.Write([]byte(tc.write))
			runs := s.RenderRowWire(0)
			if got := wireRowText(runs); got != tc.want {
				t.Errorf("wire row = %q, want %q", got, tc.want)
			}
			if tc.want == "" && len(runs) != 0 {
				t.Errorf("blank row encoded to %d runs, want 0 (the client substitutes a filler span so the row keeps its line height)", len(runs))
			}
		})
	}
}

// TestTrailingBlankTrimSpareSoftWrappedRow is the xterm.js #1286 case, and the
// reason cellsToRuns needs the wrap flag at all: on a row that autowrapped onto
// the row below, the trailing blanks are mid-line content of ONE logical line —
// the application wrote them between "foo" and "bar" — so trimming them would
// join the two words on copy.
//
// The two cases write the same first row and differ only in whether the line
// continues, which is exactly the distinction the guard makes.
func TestTrailingBlankTrimSpareSoftWrappedRow(t *testing.T) {
	const width = 10
	full := "foo       " // "foo" + 7 spaces == exactly one row

	s := New(3, width)
	s.Write([]byte(full + "bar"))
	if !s.wrapped[1] {
		t.Fatalf("fixture: row 1 is not marked a soft-wrap continuation, so this test cannot exercise the guard")
	}
	if got := wireRowText(s.RenderRowWire(0)); got != full {
		t.Errorf("soft-wrapped row = %q, want %q (its trailing blanks are mid-line content)", got, full)
	}
	// The continuation row is itself the end of the logical line, so its own
	// padding IS padding.
	if got := wireRowText(s.RenderRowWire(1)); got != "bar" {
		t.Errorf("continuation row = %q, want %q", got, "bar")
	}

	plain := New(3, width)
	plain.Write([]byte(full))
	if plain.wrapped[1] {
		t.Fatalf("fixture: row 1 must not be a continuation when nothing was written after the full row")
	}
	if got := wireRowText(plain.RenderRowWire(0)); got != "foo" {
		t.Errorf("unwrapped row = %q, want %q (nothing follows, so the tail is padding)", got, "foo")
	}
}

// TestTrailingBlankTrimOnDrainedRows pins the trim on the OTHER cellsToRuns
// caller: rows drained into scrollback. A row is encoded once, when it scrolls
// off, so a missing trim there would ship padding for the whole history — the
// bulk of a paging client's transfer.
func TestTrailingBlankTrimOnDrainedRows(t *testing.T) {
	const width = 10
	s := New(2, width)
	// Four lines through a 2-row screen: "one" and "two" drain, the rest stay.
	s.Write([]byte("one\r\ntwo\r\nthree\r\nfour"))
	if len(s.Drained) != 2 {
		t.Fatalf("drained %d rows, want 2", len(s.Drained))
	}
	for i, want := range []string{"one", "two"} {
		if got := wireRowText(s.Drained[i]); got != want {
			t.Errorf("drained row %d = %q, want %q", i, got, want)
		}
	}

	// Same guard as the live screen: a row that soft-wrapped onto the row below
	// keeps its tail when it drains.
	w := New(2, width)
	w.Write([]byte("foo       bar\r\nnext\r\nlast"))
	if len(w.Drained) == 0 {
		t.Fatalf("nothing drained")
	}
	if got := wireRowText(w.Drained[0]); got != "foo       " {
		t.Errorf("drained soft-wrapped row = %q, want %q", got, "foo       ")
	}
}

// Allocation contracts: RenderRowWire runs per changed row per frame. Its count
// may track runs, the wire format's unit, but never columns, which a client
// sets. The autolink path's count varies with regexp's sync.Pool and -race, so
// its bounds carry headroom. It is a pure read, so no fixed point is needed;
// screen_test.go's header covers the rest.

// allocRowFill repeats seed until it yields exactly n runes, so a row fixture
// can be built to an exact column budget and nothing wraps onto the row below.
// Wrapping would matter: a soft-wrapped row joins its neighbour for the URL scan
// and suppresses the trailing-blank trim, so a fixture that overflowed would
// measure a different code path at some widths than at others.
func allocRowFill(seed string, n int) string {
	runes := []rune(seed)
	out := make([]rune, 0, n)
	for len(out) < n {
		out = append(out, runes[len(out)%len(runes)])
	}
	return string(out)
}

// allocBoundedRunRows builds row text whose RUN count stays fixed as the row
// widens, which is what makes each one a witness for width-independence: the
// padding continues the last run's style rather than starting new runs. Each
// builder fills exactly width columns (a wide rune counts two).
var allocBoundedRunRows = map[string]func(width int) string{
	"plain_ascii": func(width int) string {
		return allocRowFill("abcdefgh", width)
	},
	"wide_cjk": func(width int) string {
		return allocRowFill("日本語テスト", width/2)
	},
	"combining_marks": func(width int) string {
		return strings.Repeat("e\u0301", width)
	},
	"three_colour_runs": func(width int) string {
		return "\x1b[1;31mred \x1b[0;32mgreen \x1b[4;34m" + allocRowFill("blue text ", width-10)
	},
	"osc8_hyperlink": func(width int) string {
		return "\x1b]8;;https://example.com/x\x07anchor\x1b]8;;\x07" + allocRowFill("plain text ", width-6)
	},
	"one_bare_url": func(width int) string {
		return "see https://example.com/a/b/c " + allocRowFill("pad ", width-30)
	},
	"blank_row": func(int) string {
		return ""
	},
}

// allocRowScreen writes text into row 0 of a fresh screen of the given width and
// returns the screen, so every fixture is built outside the measured closure.
// Four rows rather than one because the autolink chain scan looks at the rows
// below the one being rendered.
func allocRowScreen(width int, text string) *Screen {
	s := New(4, width)
	s.Write([]byte(text))
	return s
}

// TestRenderRowWireAllocationCountDoesNotScaleWithRowWidth bounds the per-column
// allocation rate at 0.05: strings.Builder doubling adds a few, while per-column
// work would measure 1.0.
func TestRenderRowWireAllocationCountDoesNotScaleWithRowWidth(t *testing.T) {
	// A per-column allocation rate this far below 1 cannot be per-column work.
	// Measured worst case across these classes is 0.0083 (wide_cjk).
	const maxAllocsPerColumn = 0.05

	widths := []int{40, 80, 200, 400, 1000}

	for name, build := range allocBoundedRunRows {
		t.Run(name, func(t *testing.T) {
			counts := make([]float64, len(widths))
			runCounts := make([]int, len(widths))
			for i, width := range widths {
				s := allocRowScreen(width, build(width))
				runs := s.RenderRowWire(0)
				runCounts[i] = len(runs)
				if len(s.wrapped) > 1 && s.wrapped[1] {
					t.Fatalf("RenderRowWire(0) fixture %s at %d columns wrapped onto row 1: it must fit one row, or the widths measure different code paths",
						name, width)
				}
				counts[i] = testing.AllocsPerRun(200, func() {
					s.RenderRowWire(0)
				})
				if got := len(s.RenderRowWire(0)); got != runCounts[i] {
					t.Fatalf("RenderRowWire(0) on fixture %s at %d columns returned %d runs before the measurement and %d after: the function must be a pure read of the grid",
						name, width, runCounts[i], got)
				}
			}
			// The run count has to be flat for the width sweep to be about
			// width; a class whose runs grew with width is the next test's job.
			for i, got := range runCounts {
				if got != runCounts[0] {
					t.Fatalf("RenderRowWire(0) on fixture %s produced %d runs at %d columns and %d at %d: this fixture must hold its run count flat to witness width-independence",
						name, got, widths[i], runCounts[0], widths[0])
				}
			}

			narrow, wide := counts[0], counts[len(counts)-1]
			rate := (wide - narrow) / float64(widths[len(widths)-1]-widths[0])
			if rate > maxAllocsPerColumn {
				t.Errorf("RenderRowWire(0) on a %s row of %d runs allocated %v times per run at %d columns and %v at %d, a rate of %v per column, want at most %v: a cost that tracks the column count makes a wide client pay per cell on every frame",
					name, runCounts[0], narrow, widths[0], wide, widths[len(widths)-1], rate, maxAllocsPerColumn)
			}
			t.Logf("%s (%d runs): %v allocations at %d columns rising to %v at %d, %v per column",
				name, runCounts[0], narrow, widths[0], wide, widths[len(widths)-1], rate)
		})
	}
}

// TestRenderRowWireAllocationCountIsChargedPerRunNotPerColumn bounds the cost
// per run for rows that cannot coalesce, measured as a slope between two widths
// so the fixed cost cancels and two allocations per run would show.
func TestRenderRowWireAllocationCountIsChargedPerRunNotPerColumn(t *testing.T) {
	// One allocation per run is the wire format's own cost (each WireRun carries
	// its own string); 2 leaves room for a size-class step without tolerating a
	// second per-run allocation.
	const maxAllocsPerRun = 2.0

	const (
		narrowWidth = 40
		wideWidth   = 400
	)

	perCellRows := map[string]func(width int) string{
		"palette_colour_per_cell": func(width int) string {
			var b strings.Builder
			for i := range width {
				fmt.Fprintf(&b, "\x1b[%dm%c", 31+i%7, 'a'+i%26)
			}
			return b.String()
		},
		"truecolour_per_cell": func(width int) string {
			var b strings.Builder
			for i := range width {
				fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%c", i%256, (i*7)%256, (i*13)%256, 'a'+i%26)
			}
			return b.String()
		},
		"hyperlink_per_word": func(width int) string {
			var b strings.Builder
			for i := range width / 10 {
				fmt.Fprintf(&b, "\x1b]8;;https://example.com/%d\x07file%d\x1b]8;;\x07 ", i, i)
			}
			return b.String()
		},
	}

	for name, build := range perCellRows {
		t.Run(name, func(t *testing.T) {
			narrowScreen := allocRowScreen(narrowWidth, build(narrowWidth))
			wideScreen := allocRowScreen(wideWidth, build(wideWidth))
			narrowRuns := len(narrowScreen.RenderRowWire(0))
			wideRuns := len(wideScreen.RenderRowWire(0))
			if wideRuns <= narrowRuns {
				t.Fatalf("RenderRowWire(0) on fixture %s produced %d runs at %d columns and %d at %d: this fixture must NOT coalesce, or the per-run slope below divides by nothing",
					name, narrowRuns, narrowWidth, wideRuns, wideWidth)
			}

			narrow := testing.AllocsPerRun(200, func() {
				narrowScreen.RenderRowWire(0)
			})
			wide := testing.AllocsPerRun(200, func() {
				wideScreen.RenderRowWire(0)
			})
			rate := (wide - narrow) / float64(wideRuns-narrowRuns)
			if rate > maxAllocsPerRun {
				t.Errorf("RenderRowWire(0) on a %s row allocated %v times per run for %d runs at %d columns and %v for %d runs at %d, a rate of %v per additional run, want at most %v: a style-dense row must cost one allocation for each run the wire carries and no more",
					name, narrow, narrowRuns, narrowWidth, wide, wideRuns, wideWidth, rate, maxAllocsPerRun)
			}
			t.Logf("%s: %v allocations per additional run (%v for %d runs, %v for %d runs)", name, rate, narrow, narrowRuns, wide, wideRuns)
		})
	}
}

// TestRenderRowWireAllocationCountByContentClass bounds each content class at 80
// columns (blank 3, ASCII 11, per-cell color 90) with headroom, since autolink
// varies under -race; exact counts are logged.
func TestRenderRowWireAllocationCountByContentClass(t *testing.T) {
	const width = 80

	classes := map[string]struct {
		text string
		// max is the measured count plus 6 (plus 10 for the autolink class,
		// whose regexp pool makes it the only irreproducible one).
		max float64
	}{
		"blank_row":               {"", 9},
		"plain_ascii":             {allocBoundedRunRows["plain_ascii"](width), 15},
		"wide_cjk":                {allocBoundedRunRows["wide_cjk"](width), 18},
		"combining_marks":         {allocBoundedRunRows["combining_marks"](width), 15},
		"three_colour_runs":       {allocBoundedRunRows["three_colour_runs"](width), 19},
		"osc8_hyperlink":          {allocBoundedRunRows["osc8_hyperlink"](width), 17},
		"one_bare_url":            {allocBoundedRunRows["one_bare_url"](width), 30},
		"every_sgr_attribute":     {"\x1b[1;2;3;4;5;7;8;9;21;53;58;5;99m" + allocRowFill("A", width), 15},
		"palette_colour_per_cell": {allocRowScreenText(width, "palette"), 96},
	}

	for name, tc := range classes {
		t.Run(name, func(t *testing.T) {
			s := allocRowScreen(width, tc.text)
			runs := s.RenderRowWire(0)
			got := testing.AllocsPerRun(300, func() {
				s.RenderRowWire(0)
			})
			if got > tc.max {
				t.Errorf("RenderRowWire(0) on a %s row of %d columns and %d runs allocated %v times per run, want at most %v: this class costs more than it did when the bound was measured, and it is on the path of every rendered frame",
					name, width, len(runs), got, tc.max)
			}
			t.Logf("%s: %d runs, %v allocations", name, len(runs), got)
		})
	}
}

// allocRowScreenText builds the per-cell-styled row text the class table above
// shares with the per-run contract, so the two measure the same fixture.
func allocRowScreenText(width int, kind string) string {
	var b strings.Builder
	for i := range width {
		if kind == "palette" {
			fmt.Fprintf(&b, "\x1b[%dm%c", 31+i%7, 'a'+i%26)
			continue
		}
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%c", i%256, (i*7)%256, (i*13)%256, 'a'+i%26)
	}
	return b.String()
}
