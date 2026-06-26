package vt

import "testing"

// TestSetTabStopBounds verifies setTabStop sets column 0 and treats an index
// equal to the width as a safe no-op (no out-of-range write).
func TestSetTabStopBounds(t *testing.T) {
	s := New(5, 24)
	s.setTabStop(0)
	if !s.tabStops[0] {
		t.Errorf("setTabStop(0): tabStops[0] = false, want true")
	}
	s2 := New(5, 24)
	if didPanic(func() { s2.setTabStop(24) }) {
		t.Errorf("setTabStop(24) panicked, want safe no-op")
	}
	if len(s2.tabStops) != 24 {
		t.Errorf("setTabStop(24): len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}

// TestClearTabStopReinitGuard verifies clearTabStop preserves a populated custom
// table (no spurious re-init) but initializes a nil table.
func TestClearTabStopReinitGuard(t *testing.T) {
	s := New(5, 24)
	s.tabStops = make([]bool, 24)
	s.tabStops[3] = true
	s.clearTabStop(5)
	if !s.tabStops[3] {
		t.Errorf("clearTabStop(5): tabStops[3] = false, want true (custom table preserved)")
	}
	if s.tabStops[8] {
		t.Errorf("clearTabStop(5): tabStops[8] = true, want false (no spurious re-init)")
	}
	s2 := New(5, 24)
	s2.clearTabStop(8)
	if len(s2.tabStops) != 24 {
		t.Errorf("clearTabStop(8) on nil table: len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}

// TestClearTabStopBounds verifies clearTabStop clears column 0 and treats an
// index equal to the width as a safe no-op.
func TestClearTabStopBounds(t *testing.T) {
	s := New(5, 24)
	s.tabStops = make([]bool, 24)
	s.tabStops[0] = true
	s.clearTabStop(0)
	if s.tabStops[0] {
		t.Errorf("clearTabStop(0): tabStops[0] = true, want false")
	}
	s2 := New(5, 24)
	s2.tabStops = make([]bool, 24)
	if didPanic(func() { s2.clearTabStop(24) }) {
		t.Errorf("clearTabStop(24) panicked, want safe no-op")
	}
	if len(s2.tabStops) != 24 {
		t.Errorf("clearTabStop(24): len(tabStops) = %d, want 24", len(s2.tabStops))
	}
}

// TestRestoreCursorClampsCurY verifies restoreCursor clamps a saved row at
// Height to Height-1.
func TestRestoreCursorClampsCurY(t *testing.T) {
	s := New(5, 10)
	s.cursorStateSaved = true
	s.savedY = s.Height
	s.savedX = 0
	s.restoreCursor()
	if s.curY != s.Height-1 {
		t.Errorf("restoreCursor with savedY=Height: curY = %d, want %d", s.curY, s.Height-1)
	}
}

// TestRestoreCursorClampsCurX verifies restoreCursor clamps a saved column at
// Width to Width-1.
func TestRestoreCursorClampsCurX(t *testing.T) {
	s := New(5, 10)
	s.cursorStateSaved = true
	s.savedY = 0
	s.savedX = s.Width
	s.restoreCursor()
	if s.curX != s.Width-1 {
		t.Errorf("restoreCursor with savedX=Width: curX = %d, want %d", s.curX, s.Width-1)
	}
}

// TestCSISaveRestoreCursor verifies CSI s / CSI u save and restore the cursor
// position.
func TestCSISaveRestoreCursor(t *testing.T) {
	s := New(10, 10)
	s.Write([]byte("\x1b[5;5H")) // cursor to (4,4)
	s.Write([]byte("\x1b[s"))    // save
	s.Write([]byte("\x1b[1;1H"))
	s.Write([]byte("\x1b[u")) // restore
	if row, col := s.CursorPos(); row != 4 || col != 4 {
		t.Errorf("CSI save/restore = %d,%d, want 4,4", row, col)
	}
}
