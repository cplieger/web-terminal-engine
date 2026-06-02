package vt

import (
	"strings"
	"testing"
)

// Round 4 adversarial red-team tests: verify round-3 fix is sound and
// probe remaining attack surface.

// --- Width-2 char on width-1 screen (round-3 fix verification) ---

func TestWideCharWidth1Screen_Repeated(t *testing.T) {
	s := New(3, 1)
	// Many wide chars in succession on 1-col screen.
	for range 100 {
		s.Write([]byte("漢"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d OOB after wide char on 1-col", col)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d OOB", row)
		}
	}
}

func TestWideCharWidth1Screen_MixedASCII(t *testing.T) {
	s := New(3, 1)
	// Interleave wide and narrow
	for range 50 {
		s.Write([]byte("漢A字B"))
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("cursor col %d OOB", col)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("cursor row %d OOB", row)
		}
	}
}

// --- Width-2 char on width-2 screen (boundary) ---

func TestWideCharWidth2Screen(t *testing.T) {
	s := New(3, 2)
	// Wide char fits exactly
	s.Write([]byte("漢"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB on 2-col screen after wide char", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB on 2-col screen", row)
	}
	// Second wide char should wrap
	s.Write([]byte("字"))
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after 2nd wide on 2-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB after 2nd wide", row)
	}
}

// --- Combining char as first byte ---

func TestCombiningCharAsFirstInput(t *testing.T) {
	s := New(5, 10)
	// U+0301 COMBINING ACUTE ACCENT as the very first byte written
	s.Write([]byte("\xcc\x81")) // U+0301
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after combining as first char", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
	// Should not panic; combining with nothing before it is a no-op
}

func TestCombiningCharOnWidth1Screen(t *testing.T) {
	s := New(3, 1)
	// Combining char on 1-col screen
	s.Write([]byte("\xcc\x81"))
	s.Write([]byte("A"))
	s.Write([]byte("\xcc\x81"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- Wide char split across writes then resize to 1 col ---

func TestWideCharSplitWritesThenResize1Col(t *testing.T) {
	s := New(5, 80)
	// Write first byte of UTF-8 encoded 漢 (E6 BC A2) split across calls
	s.Write([]byte{0xE6})
	s.Resize(5, 1) // resize mid-codepoint
	s.Write([]byte{0xBC, 0xA2})
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after split-write + resize", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

func TestWideCharSplitWrite_ThenResize1Col_ThenMore(t *testing.T) {
	s := New(5, 80)
	// Start writing a wide char, resize mid-sequence, complete, then continue
	s.Write([]byte{0xE6, 0xBC}) // first 2 bytes of 漢
	s.Resize(3, 1)
	s.Write([]byte{0xA2}) // complete the char on 1-col screen
	// Write more
	for range 20 {
		s.Write([]byte("漢A"))
	}
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- REP (CSI b) of wide char near edge ---

func TestREPWideCharNearEdge(t *testing.T) {
	s := New(5, 10)
	// Place cursor near right edge, print wide char, then REP
	s.Write([]byte("\x1b[1;8H")) // col 8 (0-indexed col 7)
	s.Write([]byte("漢"))         // wide char at col 7-8, wraps
	// REP the wide char 100 times
	s.Write([]byte("\x1b[100b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after REP wide char near edge", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

func TestREPWideCharOnWidth1(t *testing.T) {
	s := New(5, 1)
	s.Write([]byte("漢"))
	// REP the wide char many times on 1-col screen
	s.Write([]byte("\x1b[500b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after REP on 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

func TestREPWideCharOnWidth2(t *testing.T) {
	s := New(5, 2)
	s.Write([]byte("漢"))
	// REP the wide char many times on 2-col screen
	s.Write([]byte("\x1b[500b"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after REP on 2-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- Scrollback drain with wide chars ---

func TestScrollbackDrainWideChars(t *testing.T) {
	s := New(3, 4)
	// Fill screen with wide chars, forcing scrolling
	for range 50 {
		s.Write([]byte("漢字\n"))
	}
	d := s.DrainScrollback()
	// Verify drained lines are well-formed
	for i, runs := range d {
		totalCols := 0
		for _, run := range runs {
			totalCols += len([]rune(run.T))
		}
		if totalCols != 4 {
			t.Errorf("drained line %d has %d cols, want 4", i, totalCols)
			break
		}
	}
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

func TestScrollbackDrainWideCharsWidth1(t *testing.T) {
	s := New(2, 1)
	for range 100 {
		s.Write([]byte("漢"))
	}
	d := s.DrainScrollback()
	// Each drained line should have exactly 1 col
	for i, runs := range d {
		totalCols := 0
		for _, run := range runs {
			totalCols += len([]rune(run.T))
		}
		if totalCols != 1 {
			t.Errorf("drained line %d has %d cols, want 1", i, totalCols)
			break
		}
	}
}

// --- Alt-screen + wide + resize ---

func TestAltScreenWideCharResize(t *testing.T) {
	s := New(10, 80)
	// Write content in main screen
	s.Write([]byte("Main漢字Content"))
	// Enter alt screen
	s.Write([]byte("\x1b[?1049h"))
	if !s.InAltScreen {
		t.Fatal("expected alt screen")
	}
	// Write wide chars in alt screen
	s.Write([]byte("漢字テスト"))
	// Resize to 1 col while in alt screen
	s.Resize(3, 1)
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after alt resize to 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
	// Exit alt screen at new size
	s.Write([]byte("\x1b[?1049l"))
	if s.InAltScreen {
		t.Fatal("expected main screen")
	}
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after exiting alt at 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB after exiting alt", row)
	}
}

func TestAltScreenResize_WideCharBoundary(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("漢字テスト長い文章"))
	// Enter alt
	s.Write([]byte("\x1b[?1049h"))
	s.Write([]byte("ALT漢字"))
	// Resize to 2 cols (wide-char boundary)
	s.Resize(3, 2)
	// Write more wide chars at the boundary
	s.Write([]byte("字"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
	// Exit alt
	s.Write([]byte("\x1b[?1049l"))
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after exit", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB after exit", row)
	}
}

// --- Mouse coords at boundary ---

func TestMouseCoordsAtBoundary(t *testing.T) {
	s := New(5, 5)
	// Enable SGR mouse mode
	s.Write([]byte("\x1b[?1000h\x1b[?1006h"))
	if s.MouseMode != 1000 {
		t.Fatalf("expected mouse mode 1000, got %d", s.MouseMode)
	}
	if !s.MouseSGR {
		t.Fatal("expected SGR mouse encoding")
	}
	// CPR at boundary
	s.Write([]byte("\x1b[5;5R")) // cursor at (5,5) -- 1-indexed
	// The screen is 5x5, so row 5 col 5 is valid (1-indexed)
	// but the response parser shouldn't panic
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

func TestMouseModeBoundaryResize(t *testing.T) {
	s := New(10, 80)
	s.Write([]byte("\x1b[?1003h\x1b[?1006h"))
	// Write wide chars, resize to minimum
	s.Write([]byte("漢字テスト"))
	s.Resize(1, 1)
	if s.MouseMode != 1003 {
		t.Fatalf("mouse mode lost on resize: %d", s.MouseMode)
	}
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after resize with mouse mode", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- Stress: many resizes interleaved with wide chars ---

func TestResizeStorm_WideChars(t *testing.T) {
	s := New(10, 80)
	for i := range 200 {
		s.Write([]byte("漢字"))
		// Alternate between narrow and wide screens
		w := 1 + (i % 3) // 1, 2, 3
		h := 1 + (i % 5)
		s.Resize(h, w)
		row, col := s.CursorPos()
		if col < 0 || col >= s.Width {
			t.Fatalf("iter %d: cursor col %d OOB [0,%d)", i, col, s.Width)
		}
		if row < 0 || row >= s.Height {
			t.Fatalf("iter %d: cursor row %d OOB [0,%d)", i, row, s.Height)
		}
	}
}

// --- Edge: width-2 char where width==3 (tests curX==Width-1 logic) ---

func TestWideCharWidth3Screen(t *testing.T) {
	s := New(3, 3)
	// Write at col 2 (last col, 0-indexed) — wide char should wrap
	s.Write([]byte("AB")) // cursor at col 2
	s.Write([]byte("漢"))  // at col 2, width-2 can't fit → wrap
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB on 3-col screen", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}

// --- RenderRowWire with wide chars for correct \uFFFF placeholders ---

func TestRenderRowWireWidePlaceholder(t *testing.T) {
	s := New(3, 10)
	s.Write([]byte("A漢B"))
	runs := s.RenderRowWire(0)
	// Reconstruct text
	var text strings.Builder
	for _, run := range runs {
		text.WriteString(run.T)
	}
	got := text.String()
	// Expected: 'A' + '漢' + '\uFFFF' + 'B' + spaces to fill 10 cols
	if !strings.Contains(got, "A漢\uFFFFB") {
		t.Errorf("wire row = %q, expected to contain 'A漢\\uFFFFB'", got)
	}
	// Total rune count must equal width
	if len([]rune(got)) != 10 {
		t.Errorf("wire row rune count = %d, want 10", len([]rune(got)))
	}
}

// --- Emoji (4-byte wide char) on 1-col ---

func TestEmojiWidth1Screen(t *testing.T) {
	s := New(3, 1)
	// 😀 is U+1F600, width 2
	s.Write([]byte("\xf0\x9f\x98\x80"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after emoji on 1-col", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
	// Multiple emoji
	for range 30 {
		s.Write([]byte("\xf0\x9f\x98\x80"))
	}
	row, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB after many emoji", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB after many emoji", row)
	}
}

// --- Interleaved: CSI cursor move + wide char on narrow ---

func TestCursorMoveWideNarrow(t *testing.T) {
	s := New(3, 1)
	// CUP to (1,1), write wide, move back, write again
	s.Write([]byte("\x1b[1;1H漢\x1b[1;1H漢\x1b[1;1H"))
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Fatalf("cursor col %d OOB", col)
	}
	if row < 0 || row >= s.Height {
		t.Fatalf("cursor row %d OOB", row)
	}
}
