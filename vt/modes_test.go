package vt

import "testing"

// TestPrivateModes_BracketedPasteTracked: CSI ?2004h enables, CSI
// ?2004l disables. The handler emits a wireMsgModes frame on each
// transition; the screen field reflects the current state.
func TestPrivateModes_BracketedPasteTracked(t *testing.T) {
	s := New(5, 40)
	if s.BracketedPaste {
		t.Fatalf("default BracketedPaste should be false, got true")
	}
	s.Write([]byte("\x1b[?2004h"))
	if !s.BracketedPaste {
		t.Errorf("after ?2004h, BracketedPaste = false; want true")
	}
	s.Write([]byte("\x1b[?2004l"))
	if s.BracketedPaste {
		t.Errorf("after ?2004l, BracketedPaste = true; want false")
	}
}

// TestPrivateModes_AppCursorKeysTracked: CSI ?1h / ?1l toggle DECCKM.
func TestPrivateModes_AppCursorKeysTracked(t *testing.T) {
	s := New(5, 40)
	if s.AppCursorKeys {
		t.Fatalf("default AppCursorKeys should be false, got true")
	}
	s.Write([]byte("\x1b[?1h"))
	if !s.AppCursorKeys {
		t.Errorf("after ?1h, AppCursorKeys = false; want true")
	}
	s.Write([]byte("\x1b[?1l"))
	if s.AppCursorKeys {
		t.Errorf("after ?1l, AppCursorKeys = true; want false")
	}
}

// TestPrivateModes_NoSubstringMatch: the previous strings.Contains
// implementation could match "?149" as containing the "1" mode and
// "?10491" as containing "1049". privateModes parses the
// semicolon-separated list, so neither false-positive should fire.
func TestPrivateModes_NoSubstringMatch(t *testing.T) {
	s := New(5, 40)
	// Mode 149 alone shouldn't toggle DECCKM (mode 1).
	s.Write([]byte("\x1b[?149h"))
	if s.AppCursorKeys {
		t.Errorf("?149h should not enable DECCKM (?1)")
	}
	if s.InAltScreen {
		t.Errorf("?149h should not enter alt-screen (?1049)")
	}
}

// TestPrivateModes_MultipleParams: a single h/l can list multiple
// modes; all should be applied.
func TestPrivateModes_MultipleParams(t *testing.T) {
	s := New(5, 40)
	s.Write([]byte("\x1b[?2004;1h"))
	if !s.BracketedPaste {
		t.Errorf("multi-param ?2004;1h: BracketedPaste = false")
	}
	if !s.AppCursorKeys {
		t.Errorf("multi-param ?2004;1h: AppCursorKeys = false")
	}
}
