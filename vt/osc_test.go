package vt

import "testing"

func TestOSC2SetsTitleBEL(t *testing.T) {
	s := New(24, 80)
	// OSC 2 ; hello BEL
	s.Write([]byte("\x1b]2;hello\x07"))
	if s.Title != "hello" {
		t.Fatalf("expected title %q, got %q", "hello", s.Title)
	}
}

func TestOSC2SetsTitleST(t *testing.T) {
	s := New(24, 80)
	// OSC 2 ; world ST (ESC \)
	s.Write([]byte("\x1b]2;world\x1b\\"))
	if s.Title != "world" {
		t.Fatalf("expected title %q, got %q", "world", s.Title)
	}
}

func TestOSC0SetsTitleAndIcon(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]0;my title\x07"))
	if s.Title != "my title" {
		t.Fatalf("expected title %q, got %q", "my title", s.Title)
	}
}

func TestOSC1SetsTitle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]1;icon name\x07"))
	if s.Title != "icon name" {
		t.Fatalf("expected title %q, got %q", "icon name", s.Title)
	}
}

func TestUnknownOSCIgnored(t *testing.T) {
	s := New(24, 80)
	// Write some content first
	s.Write([]byte("ABC"))
	// Send an unknown OSC (e.g. OSC 52)
	s.Write([]byte("\x1b]52;some clipboard data\x07"))
	// Verify screen is not corrupted
	if s.RowString(0) != "ABC" {
		t.Fatalf("screen corrupted after unknown OSC: got %q", s.RowString(0))
	}
	// Title should remain empty
	if s.Title != "" {
		t.Fatalf("title should be empty after unknown OSC, got %q", s.Title)
	}
}

func TestOSCTitleUpdatesOnSubsequentSet(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;first\x07"))
	s.Write([]byte("\x1b]2;second\x1b\\"))
	if s.Title != "second" {
		t.Fatalf("expected title %q, got %q", "second", s.Title)
	}
}

func TestOSCEmptyTitle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;something\x07"))
	// Set empty title
	s.Write([]byte("\x1b]2;\x07"))
	if s.Title != "" {
		t.Fatalf("expected empty title, got %q", s.Title)
	}
}

func TestOSCAbortedByCANDoesNotCorrupt(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("AB"))
	// Start an OSC but abort with CAN (0x18) before terminator
	s.Write([]byte("\x1b]2;partial\x18"))
	// Title should NOT be set
	if s.Title != "" {
		t.Fatalf("title should be empty after CAN abort, got %q", s.Title)
	}
	// Screen should not be corrupted
	if s.RowString(0) != "AB" {
		t.Fatalf("screen corrupted after CAN abort: got %q", s.RowString(0))
	}
	// A subsequent valid OSC should work
	s.Write([]byte("\x1b]2;valid\x07"))
	if s.Title != "valid" {
		t.Fatalf("expected title %q after recovery, got %q", "valid", s.Title)
	}
}
