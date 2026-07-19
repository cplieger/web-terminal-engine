package vt

import (
	"strings"
	"testing"
)

// TestED3RaisesScrollbackClearedNotScreen verifies CSI 3 J ("erase saved
// lines") raises the ScrollbackCleared signal — which the terminal layer uses
// to clear its scrollback ring — WITHOUT erasing the visible screen. xterm's
// ED3 clears only the scrollback buffer. Inline TUIs (kiro-cli) emit ED3 on
// every resize redraw to discard the previous frame; honoring it is exactly
// what stops a real terminal from accumulating stale frames on resize.
func TestED3RaisesScrollbackClearedNotScreen(t *testing.T) {
	s := New(5, 20)
	s.Write([]byte("hello"))
	if s.scrollbackCleared {
		t.Fatal("ScrollbackCleared set before ED3")
	}
	s.Write([]byte("\x1b[3J"))
	if !s.scrollbackCleared {
		t.Fatal("CSI 3 J must set ScrollbackCleared")
	}
	if got := strings.TrimRight(s.RowString(0), " "); got != "hello" {
		t.Fatalf("ED3 must not erase the visible screen; row 0 = %q, want %q", got, "hello")
	}
}

// TestED2ErasesScreenNoScrollbackSignal verifies CSI 2 J still erases the whole
// screen and does NOT raise the scrollback-clear signal (that is ED3's job).
func TestED2ErasesScreenNoScrollbackSignal(t *testing.T) {
	s := New(5, 20)
	s.Write([]byte("hello"))
	s.Write([]byte("\x1b[2J"))
	if s.scrollbackCleared {
		t.Fatal("CSI 2 J must not set ScrollbackCleared")
	}
	if got := strings.TrimRight(s.RowString(0), " "); got != "" {
		t.Fatalf("ED2 must erase the visible screen; row 0 = %q, want empty", got)
	}
}
