package terminal

import (
	"testing"

	"github.com/cplieger/web-terminal-engine/v3/vt"
)

func makeLine(text string) []vt.WireRun {
	return []vt.WireRun{{T: text, F: -1, B: -1, Uc: -1}}
}

func TestScrollbackRing_Basic(t *testing.T) {
	r := newScrollbackRing(5)
	if r.Len() != 0 {
		t.Fatalf("expected empty ring, got %d", r.Len())
	}

	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b"), makeLine("c")})
	if r.Len() != 3 {
		t.Fatalf("expected 3, got %d", r.Len())
	}

	lines := r.Lines()
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0][0].T != "a" || lines[1][0].T != "b" || lines[2][0].T != "c" {
		t.Fatalf("unexpected content: %v", lines)
	}
}

func TestScrollbackRing_Eviction(t *testing.T) {
	r := newScrollbackRing(3)
	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b"), makeLine("c")})
	r.Append([][]vt.WireRun{makeLine("d"), makeLine("e")})

	if r.Len() != 3 {
		t.Fatalf("expected 3 (capped), got %d", r.Len())
	}
	lines := r.Lines()
	if lines[0][0].T != "c" || lines[1][0].T != "d" || lines[2][0].T != "e" {
		t.Fatalf("expected [c,d,e], got %v", []string{lines[0][0].T, lines[1][0].T, lines[2][0].T})
	}
}

func TestScrollbackRing_Clear(t *testing.T) {
	r := newScrollbackRing(5)
	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b")})
	r.Clear()
	if r.Len() != 0 {
		t.Fatalf("expected 0 after clear, got %d", r.Len())
	}
	if lines := r.Lines(); lines != nil {
		t.Fatalf("expected nil lines after clear, got %v", lines)
	}
}

func TestScrollbackRing_WrapAround(t *testing.T) {
	r := newScrollbackRing(4)
	// Fill completely
	r.Append([][]vt.WireRun{makeLine("1"), makeLine("2"), makeLine("3"), makeLine("4")})
	// Overwrite oldest two
	r.Append([][]vt.WireRun{makeLine("5"), makeLine("6")})

	lines := r.Lines()
	if len(lines) != 4 {
		t.Fatalf("expected 4, got %d", len(lines))
	}
	expected := []string{"3", "4", "5", "6"}
	for i, exp := range expected {
		if lines[i][0].T != exp {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i][0].T, exp)
		}
	}
}

// TestScrollbackRing_AbsoluteIndices verifies the absolute-index accounting
// that the resume protocol depends on: Committed advances monotonically,
// OldestIndex tracks eviction, and indices never repeat.
func TestScrollbackRing_AbsoluteIndices(t *testing.T) {
	r := newScrollbackRing(5)
	if r.Committed() != 0 || r.OldestIndex() != 0 {
		t.Fatalf("fresh ring: committed=%d oldest=%d, want 0/0", r.Committed(), r.OldestIndex())
	}
	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b"), makeLine("c")})
	if r.Committed() != 3 {
		t.Fatalf("after 3 appends: committed=%d, want 3", r.Committed())
	}
	if r.OldestIndex() != 0 {
		t.Fatalf("no eviction yet: oldest=%d, want 0", r.OldestIndex())
	}
	// Overflow capacity: committed keeps growing, oldest advances.
	r.Append([][]vt.WireRun{makeLine("d"), makeLine("e"), makeLine("f"), makeLine("g")})
	if r.Committed() != 7 {
		t.Fatalf("after 7 appends: committed=%d, want 7", r.Committed())
	}
	if r.OldestIndex() != 2 {
		t.Fatalf("after evicting 2: oldest=%d, want 2 (committed 7 - count 5)", r.OldestIndex())
	}
}

// TestScrollbackRing_LinesFrom verifies index-aligned replay: LinesFrom returns
// the retained tail at and after a given absolute index, with the true first
// index so the caller can detect an eviction gap.
func TestScrollbackRing_LinesFrom(t *testing.T) {
	r := newScrollbackRing(5)
	r.Append([][]vt.WireRun{makeLine("0"), makeLine("1"), makeLine("2"), makeLine("3"), makeLine("4")})

	// Exact alignment: ask from index 2, get [2,3,4] starting at 2.
	first, lines := r.LinesFrom(2)
	if first != 2 || len(lines) != 3 || lines[0][0].T != "2" || lines[2][0].T != "4" {
		t.Fatalf("LinesFrom(2) = first %d lines %v, want first 2 [2 3 4]", first, lineTexts(lines))
	}
	// At committed: nothing to replay.
	if first, lines := r.LinesFrom(5); first != 5 || lines != nil {
		t.Fatalf("LinesFrom(5) = first %d lines %v, want first 5 nil", first, lineTexts(lines))
	}

	// Force eviction: indices 0..1 drop out (cap 5, now 8 committed).
	r.Append([][]vt.WireRun{makeLine("5"), makeLine("6"), makeLine("7")})
	if r.OldestIndex() != 3 {
		t.Fatalf("oldest=%d, want 3", r.OldestIndex())
	}
	// Request from an evicted index 0: clamp up to oldest (3) and signal
	// the gap by returning first > requested.
	first, lines = r.LinesFrom(0)
	if first != 3 {
		t.Fatalf("LinesFrom(0) after eviction: first=%d, want 3 (gap signal)", first)
	}
	if len(lines) != 5 || lines[0][0].T != "3" || lines[4][0].T != "7" {
		t.Fatalf("LinesFrom(0) lines = %v, want [3 4 5 6 7]", lineTexts(lines))
	}
}

// TestScrollbackRing_ClearPreservesCommitted verifies Clear drops retained
// lines but keeps the committed counter, so absolute indices never repeat
// within a session even after a clear.
func TestScrollbackRing_ClearPreservesCommitted(t *testing.T) {
	r := newScrollbackRing(5)
	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b")})
	r.Clear()
	if r.Committed() != 2 {
		t.Fatalf("after clear: committed=%d, want 2 (preserved)", r.Committed())
	}
	r.Append([][]vt.WireRun{makeLine("c")})
	if r.Committed() != 3 {
		t.Fatalf("append after clear: committed=%d, want 3 (index 2 not reused)", r.Committed())
	}
}

// TestScrollbackRing_ZeroCapacityAdvancesCommitted verifies a disabled
// scrollback still advances the absolute base so the live window's base stays
// correct; nothing is retained for replay.
func TestScrollbackRing_ZeroCapacityAdvancesCommitted(t *testing.T) {
	r := newScrollbackRing(0)
	r.Append([][]vt.WireRun{makeLine("a"), makeLine("b"), makeLine("c")})
	if r.Committed() != 3 {
		t.Fatalf("zero-cap committed=%d, want 3", r.Committed())
	}
	if r.Len() != 0 {
		t.Fatalf("zero-cap retains %d lines, want 0", r.Len())
	}
	if _, lines := r.LinesFrom(0); lines != nil {
		t.Fatalf("zero-cap LinesFrom(0) = %v, want nil", lineTexts(lines))
	}
}

func lineTexts(lines [][]vt.WireRun) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) > 0 {
			out[i] = l[0].T
		}
	}
	return out
}
