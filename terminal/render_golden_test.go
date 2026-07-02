package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cplieger/web-terminal-engine/vt"
)

// render_golden_test.go generates the cross-language DISPLAY-conformance
// fixture: real escape sequences are fed through the actual vt.Screen, the
// resulting styled runs are encoded as a binary screen frame, and the bytes are
// written to ../render-golden/attributes.bin. The TypeScript tier
// (web/src/render-e2e-golden.test.ts) decodes that frame with the real decoder,
// renders it with the real render.ts, and asserts the SPEC — so this fixture is
// the engine's ACTUAL wire output, not a hand-authored "expected". If the
// engine's color resolution or attribute encoding drifts from the spec, the TS
// test fails.
//
// The row→feature layout is the contract shared with the TS test; keep the two
// in lockstep. Regenerate after an intentional change:
//
//	UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGolden
//
// renderGoldenRows is the ordered list of (feature, escape sequence) written one
// per screen row. Each sequence is self-contained (SGR reset appended) so an
// attribute never bleeds into the next row.
var renderGoldenRows = []struct {
	feature string
	seq     string
}{
	{"bold", "\x1b[1mB\x1b[0m"},
	{"italic", "\x1b[3mI\x1b[0m"},
	{"underline", "\x1b[4mU\x1b[0m"},
	{"strikethrough", "\x1b[9mS\x1b[0m"},
	{"overline", "\x1b[53mO\x1b[0m"},
	{"color256-cube-red", "\x1b[38;5;196mR\x1b[0m"},
	{"color256-cube-blue", "\x1b[38;5;21mB\x1b[0m"},
	{"color256-grayscale", "\x1b[38;5;244mG\x1b[0m"},
	{"truecolor-fg", "\x1b[38;2;255;128;0mT\x1b[0m"},
	{"truecolor-bg", "\x1b[48;2;0;0;255mK\x1b[0m"},
	{"underline-color", "\x1b[4;58;2;0;255;0mC\x1b[0m"},
	{"inverse-default", "\x1b[7mV\x1b[0m"},
	{"hyperlink", "\x1b]8;;https://example.com/x\x1b\\L\x1b]8;;\x1b\\"},
}

func TestRenderGolden(t *testing.T) {
	const cols = 40
	height := len(renderGoldenRows) + 2 // + 2 blank rows; cursor parked below.
	s := vt.New(height, cols)
	for i, r := range renderGoldenRows {
		// CUP to (row i+1, col 1), 1-indexed, then emit the feature sequence.
		s.Write(fmt.Appendf(nil, "\x1b[%d;1H", i+1))
		s.Write([]byte(r.seq))
	}

	rows := make([][]vt.WireRun, height)
	changed := make([]int, height)
	for y := range height {
		rows[y] = s.RenderRowWire(y)
		changed[y] = y
	}

	// base=0, cursor parked on the last (blank) row, hidden; no bell/alt/clear.
	frame := encodeScreenMsg(0, height, height-1, 0, 0, changed, rows, 0, true, false, false, false, false)

	dir := filepath.Join("..", "render-golden")
	path := filepath.Join(dir, "attributes.bin")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, frame, 0o600); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGolden): %v", path, err)
	}
	if !bytes.Equal(frame, want) {
		t.Errorf("render golden drifted (%d bytes now, %d in fixture). If intentional, regenerate with "+
			"UPDATE_GOLDEN=1 and re-run the TS render-e2e-golden test in the same change.", len(frame), len(want))
	}
}

// --- Comprehensive all-codes display fixture (tier-3 CDP dump) ---------------
//
// TestRenderGoldenAllCodes feeds EVERY SGR display code through the real
// vt.Screen — all attributes, all 16 basic fg/bg, the full 256-color palette
// (38;5;0..255), truecolor, and underline-color — one code per row, then writes
// the resulting wire frame (all-codes.bin) plus a layout MANIFEST
// (all-codes.manifest.json) that records which SGR each row carries. The
// Playwright tier (web/e2e/render-all-codes.e2e.test.ts) renders the frame in a
// real headless Chromium, dumps every cell's computed style over CDP, and
// asserts each against the SPEC derived independently from the manifest — the
// manifest is only "what was fed", never the expected output.
//
// Regenerate: UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGoldenAllCodes

// allCodesEntry records one row's SGR for the manifest (what was fed, not the
// expected render).
type allCodesEntry struct {
	Row  int    `json:"row"`
	Kind string `json:"kind"` // attr | basicFg | basicBg | color256Fg | truecolorFg | truecolorBg | ulColor
	Code int    `json:"code"` // SGR code, or the 256-color index
	R    int    `json:"r"`
	G    int    `json:"g"`
	B    int    `json:"b"`
}

func buildAllCodesMatrix() (seqs []string, manifest []allCodesEntry) {
	add := func(kind string, code, r, g, b int, seq string) {
		manifest = append(manifest, allCodesEntry{Row: len(seqs), Kind: kind, Code: code, R: r, G: g, B: b})
		seqs = append(seqs, seq)
	}
	// Text attributes.
	for _, code := range []int{1, 2, 3, 4, 5, 7, 8, 9, 21, 53} {
		add("attr", code, 0, 0, 0, fmt.Sprintf("\x1b[%dmX\x1b[0m", code))
	}
	// Basic 16 foreground (30-37 normal, 90-97 bright).
	for _, code := range []int{30, 31, 32, 33, 34, 35, 36, 37, 90, 91, 92, 93, 94, 95, 96, 97} {
		add("basicFg", code, 0, 0, 0, fmt.Sprintf("\x1b[%dmX\x1b[0m", code))
	}
	// Basic 16 background (40-47 normal, 100-107 bright).
	for _, code := range []int{40, 41, 42, 43, 44, 45, 46, 47, 100, 101, 102, 103, 104, 105, 106, 107} {
		add("basicBg", code, 0, 0, 0, fmt.Sprintf("\x1b[%dmX\x1b[0m", code))
	}
	// Full 256-color palette as foreground.
	for i := range 256 {
		add("color256Fg", i, 0, 0, 0, fmt.Sprintf("\x1b[38;5;%dmX\x1b[0m", i))
	}
	// Truecolor foreground samples.
	for _, c := range [][3]int{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 128, 0}, {18, 52, 86}} {
		add("truecolorFg", 0, c[0], c[1], c[2], fmt.Sprintf("\x1b[38;2;%d;%d;%dmX\x1b[0m", c[0], c[1], c[2]))
	}
	// Truecolor background samples.
	for _, c := range [][3]int{{0, 0, 255}, {200, 100, 50}} {
		add("truecolorBg", 0, c[0], c[1], c[2], fmt.Sprintf("\x1b[48;2;%d;%d;%dmX\x1b[0m", c[0], c[1], c[2]))
	}
	// Underline color (SGR 58) with an underline (SGR 4) so the decoration shows.
	for _, c := range [][3]int{{0, 255, 0}, {255, 0, 255}} {
		add("ulColor", 0, c[0], c[1], c[2], fmt.Sprintf("\x1b[4;58;2;%d;%d;%dmX\x1b[0m", c[0], c[1], c[2]))
	}
	return seqs, manifest
}

func TestRenderGoldenAllCodes(t *testing.T) {
	const cols = 8
	seqs, manifest := buildAllCodesMatrix()
	height := len(seqs) + 1 // + 1 blank row; cursor parked there.
	s := vt.New(height, cols)
	for i, seq := range seqs {
		s.Write(fmt.Appendf(nil, "\x1b[%d;1H", i+1))
		s.Write([]byte(seq))
	}
	rows := make([][]vt.WireRun, height)
	changed := make([]int, height)
	for y := range height {
		rows[y] = s.RenderRowWire(y)
		changed[y] = y
	}
	frame := encodeScreenMsg(0, height, height-1, 0, 0, changed, rows, 0, true, false, false, false, false)

	dir := filepath.Join("..", "render-golden")
	binPath := filepath.Join(dir, "all-codes.bin")
	manPath := filepath.Join(dir, "all-codes.manifest.json")
	manJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(binPath, frame, 0o600); err != nil {
			t.Fatalf("write bin: %v", err)
		}
		if err := os.WriteFile(manPath, manJSON, 0o600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		return
	}
	wantBin, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read %s (run UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGoldenAllCodes): %v", binPath, err)
	}
	if !bytes.Equal(frame, wantBin) {
		t.Errorf("all-codes fixture drifted (%d vs %d bytes); regenerate with UPDATE_GOLDEN=1 and re-run the TS all-codes e2e test.", len(frame), len(wantBin))
	}
	wantMan, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatalf("read manifest %s: %v", manPath, err)
	}
	if !bytes.Equal(manJSON, wantMan) {
		t.Errorf("all-codes manifest drifted; regenerate with UPDATE_GOLDEN=1.")
	}
}

// --- Behavioral display conformance (escape sequence -> engine -> wire -> DOM) ---
//
// esctest2 proves the engine's screen MODEL is correct after an operation
// (clear, erase, cursor move, insert/delete). It says nothing about whether the
// browser RENDERER turns that model into correct DOM. TestRenderGoldenBehavior
// closes that gap end to end: it runs a real escape SEQUENCE through the actual
// vt.Screen, asserts the engine's resulting grid equals a SPEC-AUTHORED expected
// grid (a spec-check of the engine, independent of esctest), then emits the real
// wire frame. The TS side (web/src/render-behavior.test.ts) decodes that frame
// with the real decoder, renders it with the real render.ts, and asserts the DOM
// text grid equals the SAME spec grid — so one hand-authored grid pins both the
// engine and the renderer, and they cannot silently disagree.
//
// Scope: in-place SCREEN operations (the grid the renderer paints). Scroll-to-
// history (which emits scroll frames into the scrollback ring) is covered by
// web/src/render-store.test.ts; this fixture asserts the on-screen grid only.
//
// Regenerate: UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGoldenBehavior

// behaviorScenarios is the ordered scenario table. `want` is the SPEC-EXPECTED
// screen (H=4 rows x W=8 cols), each row trailing-trimmed like vt RowString. The
// grid is authored from what the sequence must produce per the VT/ANSI spec, not
// copied from the engine or the renderer.
var behaviorScenarios = []struct {
	name  string
	input string
	want  []string
}{
	// The canonical case: write text, then clear the screen (ED2). The DOM must
	// end up blank — no leftover glyphs.
	{"clear_screen_ed2", "\x1b[1;1HABC\x1b[2;1HDEF\x1b[2J", []string{"", "", "", ""}},
	// ED0 erases from the cursor to the end of the screen.
	{"erase_below_ed0", "\x1b[1;1HAAAA\x1b[2;1HBBBB\x1b[3;1HCCCC\x1b[2;3H\x1b[0J", []string{"AAAA", "BB", "", ""}},
	// ED1 erases from the start of the screen to the cursor (inclusive).
	{"erase_above_ed1", "\x1b[1;1HAAAA\x1b[2;1HBBBB\x1b[3;1HCCCC\x1b[2;3H\x1b[1J", []string{"", "   B", "CCCC", ""}},
	// EL0 erases from the cursor to the end of the line.
	{"erase_line_to_end_el0", "HELLOWOR\x1b[1;3H\x1b[0K", []string{"HE", "", "", ""}},
	// EL1 erases from the start of the line to the cursor (inclusive).
	{"erase_line_to_start_el1", "HELLOWOR\x1b[1;4H\x1b[1K", []string{"    OWOR", "", "", ""}},
	// EL2 erases the whole line.
	{"erase_whole_line_el2", "HELLOWOR\x1b[2K", []string{"", "", "", ""}},
	// CUP positions the cursor; the next glyph must land at that cell.
	{"cursor_position_write", "\x1b[2;4HX", []string{"", "   X", "", ""}},
	// Repositioning mid-line and writing overwrites in place.
	{"overwrite_middle", "AAAAAAAA\x1b[1;3HBB", []string{"AABBAAAA", "", "", ""}},
	// CR returns to column 0; the following text overwrites from the start.
	{"carriage_return", "ABCDEF\rXY", []string{"XYCDEF", "", "", ""}},
	// BS moves left one cell; the next glyph overwrites the char under it.
	{"backspace_overwrite", "ABC\bX", []string{"ABX", "", "", ""}},
	// HT advances to the next 8-column tab stop, clamped to the right margin.
	{"tab_stop", "A\tB", []string{"A      B", "", "", ""}},
	// ICH inserts blanks at the cursor and shifts the rest of the line right.
	{"insert_char_ich", "ABCDEF\x1b[1;3H\x1b[2@", []string{"AB  CDEF", "", "", ""}},
	// DCH deletes chars at the cursor and shifts the rest of the line left.
	{"delete_char_dch", "ABCDEFGH\x1b[1;3H\x1b[2P", []string{"ABEFGH", "", "", ""}},
	// ECH erases n chars at the cursor in place (no shift).
	{"erase_char_ech", "ABCDEFGH\x1b[1;3H\x1b[2X", []string{"AB  EFGH", "", "", ""}},
	// IL inserts a blank line at the cursor row, pushing lines below down.
	{"insert_line_il", "\x1b[1;1HR0\x1b[2;1HR1\x1b[3;1HR2\x1b[4;1HR3\x1b[2;1H\x1b[L", []string{"R0", "", "R1", "R2"}},
	// DL deletes the cursor row, pulling lines below up.
	{"delete_line_dl", "\x1b[1;1HR0\x1b[2;1HR1\x1b[3;1HR2\x1b[4;1HR3\x1b[2;1H\x1b[M", []string{"R0", "R2", "R3", ""}},
	// Autowrap: writing past the right margin continues on the next row.
	{"autowrap_to_next_row", "ABCDEFGHIJKL", []string{"ABCDEFGH", "IJKL", "", ""}},
	// LF at the bottom row scrolls the screen up; the top line leaves the screen
	// (into scrollback) and a blank line appears at the bottom.
	{"newline_scroll_up", "\x1b[1;1HR0\x1b[2;1HR1\x1b[3;1HR2\x1b[4;1HR3\x1b[4;1H\n", []string{"R1", "R2", "R3", ""}},
	// RI (reverse index) at the top row scrolls the screen down; the bottom line
	// leaves the screen and a blank line appears at the top.
	{"reverse_index_scroll_down", "\x1b[1;1HR0\x1b[2;1HR1\x1b[3;1HR2\x1b[4;1HR3\x1b[1;1H\x1bM", []string{"", "R0", "R1", "R2"}},
}

// behaviorEntry is one scenario's manifest record. Frame marshals to base64
// (encoding/json encodes []byte that way), so the TS side decodes it directly.
type behaviorEntry struct {
	Name   string   `json:"name"`
	Input  string   `json:"input"` // the escape sequence fed to the engine (documentation)
	Want   []string `json:"want"`  // spec-expected screen grid, trailing-trimmed rows
	CurRow int      `json:"curRow"`
	CurCol int      `json:"curCol"`
	Frame  []byte   `json:"frame"` // the engine's real wire frame for this scenario
}

func TestRenderGoldenBehavior(t *testing.T) {
	const rows, cols = 4, 8
	manifest := make([]behaviorEntry, 0, len(behaviorScenarios))
	for _, sc := range behaviorScenarios {
		s := vt.New(rows, cols)
		s.Write([]byte(sc.input))

		// Spec-check the ENGINE: its resulting grid must equal the authored grid.
		if len(sc.want) != rows {
			t.Fatalf("%s: want grid has %d rows, screen has %d", sc.name, len(sc.want), rows)
		}
		for y := range rows {
			if got := s.RowString(y); got != sc.want[y] {
				t.Errorf("%s: engine RowString(%d) = %q, want %q (spec grid)", sc.name, y, got, sc.want[y])
			}
		}

		curRow, curCol := s.CursorPos()
		wire := make([][]vt.WireRun, rows)
		changed := make([]int, rows)
		for y := range rows {
			wire[y] = s.RenderRowWire(y)
			changed[y] = y
		}
		// Cursor hidden so the grid-text assertion is not disturbed by a cursor
		// span; the renderer's cursor placement is covered separately.
		frame := encodeScreenMsg(0, rows, curRow, curCol, 0, changed, wire, 0, true, false, false, false, false)
		manifest = append(manifest, behaviorEntry{
			Name: sc.name, Input: sc.input, Want: sc.want, CurRow: curRow, CurCol: curCol, Frame: frame,
		})
	}

	manJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal behavior manifest: %v", err)
	}
	dir := filepath.Join("..", "render-golden")
	manPath := filepath.Join(dir, "behavior.manifest.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(manPath, manJSON, 0o600); err != nil {
			t.Fatalf("write behavior manifest: %v", err)
		}
		return
	}
	want, err := os.ReadFile(manPath)
	if err != nil {
		t.Fatalf("read %s (run UPDATE_GOLDEN=1 go test ./terminal/ -run TestRenderGoldenBehavior): %v", manPath, err)
	}
	if !bytes.Equal(manJSON, want) {
		t.Errorf("behavior fixture drifted; regenerate with UPDATE_GOLDEN=1 and re-run the TS render-behavior test.")
	}
}
