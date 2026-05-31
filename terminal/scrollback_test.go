package terminal

import (
	"testing"

	"github.com/cplieger/vterm/vt"
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
