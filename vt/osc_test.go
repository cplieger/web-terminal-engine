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

// TestOSC1DoesNotSetTitle verifies OSC 1 (set icon name only) does NOT change
// the window title — it must not clobber a title set via OSC 0/2.
func TestOSC1DoesNotSetTitle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;window\x07"))    // real window title
	s.Write([]byte("\x1b]1;icon name\x07")) // icon name only — must be ignored
	if s.Title != "window" {
		t.Fatalf("OSC 1 clobbered the window title: got %q, want %q", s.Title, "window")
	}
}

func TestUnknownOSCIgnored(t *testing.T) {
	s := New(24, 80)
	// Write some content first
	s.Write([]byte("ABC"))
	// Send an out-of-scope OSC (777 = urxvt notifications): it must be consumed
	// and ignored (dispatchOsc's default case), touching neither the screen nor
	// the title. OSC 52 is a real handler now, so it's covered in features_test.
	s.Write([]byte("\x1b]777;notify;title;body\x07"))
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

// TestOSCTerminatedBy8BitST verifies the 8-bit ST (0x9C) terminates an OSC and
// the title is set.
func TestOSCTerminatedBy8BitST(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b]2;Hello"))
	s.Write([]byte{0x9C}) // 8-bit ST
	if s.pState != stGround {
		t.Errorf("0x9C in OscString: state=%d, want Ground", s.pState)
	}
	if s.Title != "Hello" {
		t.Errorf("OSC title after 0x9C: got %q, want Hello", s.Title)
	}
}

// TestOSCAbortedBySUB verifies SUB aborts an OSC without dispatching the title.
func TestOSCAbortedBySUB(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;aborted"))
	s.Write([]byte{0x1A}) // SUB
	if s.pState != stGround {
		t.Fatalf("SUB did not abort OSC: state=%d", s.pState)
	}
	if s.Title == "aborted" {
		t.Fatal("SUB in OSC dispatched title (exit action fired)")
	}
}

// TestOSCNoTerminatorThenNewSequence verifies an unterminated OSC is abandoned
// when a fresh ESC sequence begins, which then takes effect.
func TestOSCNoTerminatorThenNewSequence(t *testing.T) {
	s := New(5, 80)
	s.Write([]byte("\x1b]8;params;http://example.com")) // no terminator
	s.Write([]byte("\x1b[1;1H"))                        // new sequence starts fresh
	if row, col := s.CursorPos(); row != 0 || col != 0 {
		t.Fatalf("expected cursor at 0,0 after OSC abort via ESC, got %d,%d", row, col)
	}
}

// TestOSCAllDigitPayloadNoSeparator verifies an all-digit OSC payload with no
// separator ("2", no ';') is parsed in-bounds, and OSC 2 sets the title to the
// empty (absent) data. Driven through the public parser (ESC ] 2 BEL) rather
// than by hand-seeding the internal OSC buffer.
func TestOSCAllDigitPayloadNoSeparator(t *testing.T) {
	s := New(1, 10)
	s.Title = "prev"
	s.Write([]byte("\x1b]2\x07")) // OSC 2, no ';' separator, terminated by BEL
	if s.Title != "" {
		t.Errorf("OSC 2 with no separator: Title = %q, want \"\" (empty data)", s.Title)
	}
}

// TestOSCUnhandledIdLeavesTitle verifies an unhandled OSC id (9) leaves the
// window title unchanged. Driven through the public parser (ESC ] 9 ; … BEL).
func TestOSCUnhandledIdLeavesTitle(t *testing.T) {
	s := New(1, 10)
	const keep = "keep"
	s.Title = keep
	s.Write([]byte("\x1b]9;iTerm-only notification\x07")) // OSC 9 is unhandled here
	if s.Title != keep {
		t.Errorf("unhandled OSC 9: Title = %q, want %q (unchanged)", s.Title, keep)
	}
}
