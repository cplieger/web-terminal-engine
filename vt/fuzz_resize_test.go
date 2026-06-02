package vt

import "testing"

// FuzzScreenWriteResize exercises the VT parser with arbitrary byte sequences
// interleaved with resize operations.
func FuzzScreenWriteResize(f *testing.F) {
	f.Add([]byte("\x1b[?1049h\x1b[2J\x1b[H漢字テスト\x1b[?1049l"), uint8(10), uint8(20))
	f.Add([]byte("\x1b[5S\x1b[5T\x1b[10L\x1b[10M"), uint8(3), uint8(5))
	f.Add([]byte("\x1b[38;2;255;0;0m\xf0\x9f\x98\x80\x1b[0m"), uint8(1), uint8(2))

	f.Fuzz(func(t *testing.T, data []byte, rows, cols uint8) {
		r := int(rows)%50 + 1
		c := int(cols)%100 + 1
		s := New(24, 80)
		// Write half, resize, write rest
		mid := len(data) / 2
		s.Write(data[:mid])
		s.Resize(r, c)
		s.Write(data[mid:])
		// Verify cursor is in bounds
		row, col := s.CursorPos()
		if row < 0 || row >= s.Height {
			t.Errorf("cursor row %d out of bounds [0, %d)", row, s.Height)
		}
		if col < 0 || col >= s.Width {
			t.Errorf("cursor col %d out of bounds [0, %d)", col, s.Width)
		}
	})
}
