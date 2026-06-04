package terminal

import (
	"encoding/binary"
	"testing"

	"github.com/cplieger/vterm/vt"
)

// FuzzWireRoundTrip generates random screen states and encodes them via
// encodeScreenMsg, then validates structural integrity of the output bytes.
func FuzzWireRoundTrip(f *testing.F) {
	f.Add(uint8(3), uint8(4), uint8(1), uint8(0), []byte("hi"))
	f.Add(uint8(1), uint8(1), uint8(0), uint8(0), []byte(""))
	f.Add(uint8(10), uint8(20), uint8(5), uint8(3), []byte("abc"))

	f.Fuzz(func(t *testing.T, height, width, numChanged, curStyle uint8, text []byte) {
		h := int(height)%50 + 1
		_ = int(width)%80 + 1
		nc := int(numChanged) % h
		if nc == 0 {
			nc = 1
		}

		// Build rows and changed indices
		changed := make([]int, nc)
		rows := make([][]vt.WireRun, h)
		for i := range nc {
			idx := i % h
			changed[i] = idx
			txt := "A"
			if len(text) > 0 {
				txt = string(text[:min(len(text), 20)])
			}
			rows[idx] = []vt.WireRun{{T: txt, F: -1, B: -1, A: 0, Uc: -1}}
		}

		buf := encodeScreenMsg(h, 0, 0, 0, changed, rows, curStyle, false, false, false)

		// Structural validation
		if len(buf) < 19 {
			t.Fatalf("encoded too short: %d bytes", len(buf))
		}
		if buf[0] != wireMsgScreen {
			t.Fatalf("msg type = %d, want %d", buf[0], wireMsgScreen)
		}
		screenH := binary.LittleEndian.Uint16(buf[13:15])
		if int(screenH) != h {
			t.Fatalf("screenHeight = %d, want %d", screenH, h)
		}
		numCh := binary.LittleEndian.Uint16(buf[15:17])
		if int(numCh) != nc {
			t.Fatalf("numChanged = %d, want %d", numCh, nc)
		}

		// Walk each changed row and validate row_idx is in bounds
		off := 19
		for i := range int(numCh) {
			if off+2 > len(buf) {
				t.Fatalf("truncated at row_idx %d", i)
			}
			rowIdx := binary.LittleEndian.Uint16(buf[off:])
			off += 2
			if int(rowIdx) >= h {
				t.Fatalf("row_idx %d >= screenHeight %d", rowIdx, h)
			}
			// Skip row payload: read num_runs then skip each run
			if off+2 > len(buf) {
				t.Fatalf("truncated at num_runs for row %d", i)
			}
			numRuns := int(binary.LittleEndian.Uint16(buf[off:]))
			off += 2
			for r := range numRuns {
				if off+2 > len(buf) {
					t.Fatalf("truncated at text_len run %d", r)
				}
				tlen := int(binary.LittleEndian.Uint16(buf[off:]))
				off += 2 + tlen + 4 + 4 + 2 + 4 // text + fg + bg + attrs + uc
				if off+2 > len(buf) {
					t.Fatalf("truncated at url_len run %d", r)
				}
				ulen := int(binary.LittleEndian.Uint16(buf[off:]))
				off += 2 + ulen
			}
		}
		if off != len(buf) {
			t.Fatalf("trailing bytes: consumed %d of %d", off, len(buf))
		}
	})
}
