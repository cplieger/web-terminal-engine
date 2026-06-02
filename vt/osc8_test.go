package vt

import "testing"

func TestOSC8SetsHyperlinkBEL(t *testing.T) {
	s := New(24, 80)
	// OSC 8 ; ; http://example.com BEL then print text
	s.Write([]byte("\x1b]8;;http://example.com\x07"))
	s.Write([]byte("link"))
	for i := range 4 {
		if s.Cells[0][i].Hyperlink != "http://example.com" {
			t.Fatalf("cell[0][%d] hyperlink = %q, want %q", i, s.Cells[0][i].Hyperlink, "http://example.com")
		}
	}
}

func TestOSC8SetsHyperlinkST(t *testing.T) {
	s := New(24, 80)
	// OSC 8 ; ; http://example.com ST
	s.Write([]byte("\x1b]8;;http://example.com\x1b\\"))
	s.Write([]byte("hi"))
	for i := range 2 {
		if s.Cells[0][i].Hyperlink != "http://example.com" {
			t.Fatalf("cell[0][%d] hyperlink = %q, want %q", i, s.Cells[0][i].Hyperlink, "http://example.com")
		}
	}
}

func TestOSC8ClearsOnEmptyURI(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]8;;http://example.com\x07"))
	s.Write([]byte("AB"))
	// Close hyperlink
	s.Write([]byte("\x1b]8;;\x07"))
	s.Write([]byte("CD"))
	// AB should have the link
	for i := range 2 {
		if s.Cells[0][i].Hyperlink != "http://example.com" {
			t.Fatalf("cell[0][%d] hyperlink = %q, want link", i, s.Cells[0][i].Hyperlink)
		}
	}
	// CD should not
	for i := 2; i < 4; i++ {
		if s.Cells[0][i].Hyperlink != "" {
			t.Fatalf("cell[0][%d] hyperlink = %q, want empty", i, s.Cells[0][i].Hyperlink)
		}
	}
}

func TestOSC8WithIdParam(t *testing.T) {
	s := New(24, 80)
	// id= param is parsed but not used; URI still attaches
	s.Write([]byte("\x1b]8;id=foo;http://example.com\x07"))
	s.Write([]byte("X"))
	if s.Cells[0][0].Hyperlink != "http://example.com" {
		t.Fatalf("cell hyperlink = %q, want %q", s.Cells[0][0].Hyperlink, "http://example.com")
	}
}

func TestOSC8RunsSplitOnURLBoundary(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]8;;http://a.com\x07"))
	s.Write([]byte("AA"))
	s.Write([]byte("\x1b]8;;http://b.com\x07"))
	s.Write([]byte("BB"))
	s.Write([]byte("\x1b]8;;\x07"))
	s.Write([]byte("CC"))

	runs := s.RenderRowWire(0)
	// Should have at least 3 runs: AA(url=a), BB(url=b), CC+rest(no url)
	if len(runs) < 3 {
		t.Fatalf("expected at least 3 runs, got %d", len(runs))
	}
	if runs[0].U != "http://a.com" {
		t.Fatalf("run[0].U = %q, want %q", runs[0].U, "http://a.com")
	}
	if runs[1].U != "http://b.com" {
		t.Fatalf("run[1].U = %q, want %q", runs[1].U, "http://b.com")
	}
	// The last run(s) should have no URL
	lastRun := runs[len(runs)-1]
	if lastRun.U != "" {
		t.Fatalf("last run U = %q, want empty", lastRun.U)
	}
}

func TestOSC8MalformedNoSecondSemicolon(t *testing.T) {
	s := New(24, 80)
	// Malformed: no second semicolon — should be ignored
	s.Write([]byte("\x1b]8;http://example.com\x07"))
	s.Write([]byte("X"))
	// No hyperlink should be set (malformed ignored)
	if s.Cells[0][0].Hyperlink != "" {
		t.Fatalf("cell hyperlink = %q, want empty (malformed OSC 8)", s.Cells[0][0].Hyperlink)
	}
}
