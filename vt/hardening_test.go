package vt

import "testing"

// --- Unbounded buffer growth (P1 findings) ---

func TestOSCBufferBounded(t *testing.T) {
	s := New(24, 80)
	// Start an OSC without terminator, feed data exceeding the cap.
	s.Write([]byte("\x1b]2;"))
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = 'A'
	}
	for range 4 {
		s.Write(chunk)
	}
	if len(s.oscBuf) > maxOSCLen {
		t.Errorf("oscBuf grew to %d bytes, want <= %d", len(s.oscBuf), maxOSCLen)
	}
	// Terminate the OSC — title should be the capped content.
	s.Write([]byte("\x07"))
	if s.pState != stateGround {
		t.Errorf("parser not back to ground after BEL")
	}
}

func TestCSIParamsBounded(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b["))
	chunk := make([]byte, 4096)
	for i := range chunk {
		chunk[i] = '0' + byte(i%10)
	}
	for range 4 {
		s.Write(chunk)
	}
	if len(s.pParams) > maxCSIParams {
		t.Errorf("pParams grew to %d bytes, want <= %d", len(s.pParams), maxCSIParams)
	}
}

func TestCSIIntermedBounded(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b["))
	// Feed intermediate bytes (0x20-0x2F range)
	chunk := make([]byte, 256)
	for i := range chunk {
		chunk[i] = 0x20 + byte(i%16)
	}
	s.Write(chunk)
	if len(s.pIntermed) > maxCSIIntermed {
		t.Errorf("pIntermed grew to %d bytes, want <= %d", len(s.pIntermed), maxCSIIntermed)
	}
}

// --- CSI param clamping (P1 finding: DoS via huge loop counts) ---

func TestShiftLeftLargeN(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("Hello World"))
	// CSI 65535 SP @ — shift left by max clamped value.
	// Should complete instantly without panic or timeout.
	s.Write([]byte("\x1b[65535 @"))
	// All cells should be blank after shifting by >= width.
	for x := range s.Width {
		if s.Cells[0][x].Ch != ' ' {
			t.Errorf("cell[0][%d] = %q, want ' ' after large shift left", x, s.Cells[0][x].Ch)
			break
		}
	}
}

func TestShiftRightLargeN(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("Hello World"))
	// CSI 65535 SP A — shift right by max clamped value.
	s.Write([]byte("\x1b[65535 A"))
	for x := range s.Width {
		if s.Cells[0][x].Ch != ' ' {
			t.Errorf("cell[0][%d] = %q, want ' ' after large shift right", x, s.Cells[0][x].Ch)
			break
		}
	}
}

func TestDeleteCharsLargeN(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("Hello"))
	s.Write([]byte("\x1b[1G")) // back to col 0
	// Delete more chars than exist — should clear the row from cursor.
	s.Write([]byte("\x1b[65535P"))
	for x := range s.Width {
		if s.Cells[0][x].Ch != ' ' {
			t.Errorf("cell[0][%d] = %q, want ' '", x, s.Cells[0][x].Ch)
			break
		}
	}
}

func TestCSIArgClamped(t *testing.T) {
	// Verify that csiArg clamps to maxCSIArgValue.
	got := csiArg("999999999", 1)
	if got != maxCSIArgValue {
		t.Errorf("csiArg(999999999) = %d, want %d", got, maxCSIArgValue)
	}
	// Normal values pass through.
	got = csiArg("42", 1)
	if got != 42 {
		t.Errorf("csiArg(42) = %d, want 42", got)
	}
}

func TestScrollUpLargeN(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("Line1\r\nLine2\r\nLine3"))
	// Scroll up by a large (but clamped) value — should not timeout.
	s.Write([]byte("\x1b[65535S"))
	// All rows should be blank after scrolling everything off.
	for y := range s.Height {
		for x := range s.Width {
			if s.Cells[y][x].Ch != ' ' {
				t.Errorf("cell[%d][%d] = %q, want ' '", y, x, s.Cells[y][x].Ch)
				return
			}
		}
	}
}

func TestScrollDownLargeN(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("Line1\r\nLine2\r\nLine3"))
	// Scroll down by a large value — clamped to region height.
	s.Write([]byte("\x1b[65535T"))
	for y := range s.Height {
		for x := range s.Width {
			if s.Cells[y][x].Ch != ' ' {
				t.Errorf("cell[%d][%d] = %q, want ' '", y, x, s.Cells[y][x].Ch)
				return
			}
		}
	}
}

// TestWideCharNarrowScreenCursorClamp verifies that a width-2 character
// on a 1-column screen does not leave curX out of bounds. Regression
// test for a fuzz-discovered bug where put() failed to clamp curX after
// the wide-char spacer on screens narrower than 2 columns.
func TestWideCharNarrowScreenCursorClamp(t *testing.T) {
	s := New(5, 1)
	s.Write([]byte("漢")) // width-2 char on 1-col screen
	row, col := s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Errorf("cursor col %d out of bounds [0, %d) after wide char on 1-col screen", col, s.Width)
	}
	if row < 0 || row >= s.Height {
		t.Errorf("cursor row %d out of bounds [0, %d)", row, s.Height)
	}
	// Write more chars — cursor must stay in bounds.
	s.Write([]byte("漢漢漢ABCD"))
	_, col = s.CursorPos()
	if col < 0 || col >= s.Width {
		t.Errorf("cursor col %d out of bounds after multiple writes on 1-col screen", col)
	}
}

func TestInsertLinesLargeN(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("AAAA\r\nBBBB\r\nCCCC"))
	s.Write([]byte("\x1b[1;1H")) // home
	// Insert more lines than the scroll region — clamped.
	s.Write([]byte("\x1b[65535L"))
	for y := range s.Height {
		for x := range s.Width {
			if s.Cells[y][x].Ch != ' ' {
				t.Errorf("cell[%d][%d] = %q, want ' '", y, x, s.Cells[y][x].Ch)
				return
			}
		}
	}
}

func TestDeleteLinesLargeN(t *testing.T) {
	s := New(5, 10)
	s.Write([]byte("AAAA\r\nBBBB\r\nCCCC"))
	s.Write([]byte("\x1b[1;1H")) // home
	// Delete more lines than the scroll region — clamped.
	s.Write([]byte("\x1b[65535M"))
	for y := range s.Height {
		for x := range s.Width {
			if s.Cells[y][x].Ch != ' ' {
				t.Errorf("cell[%d][%d] = %q, want ' '", y, x, s.Cells[y][x].Ch)
				return
			}
		}
	}
}
