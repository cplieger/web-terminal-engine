package terminal

import (
	"encoding/binary"
	"testing"

	"github.com/cplieger/web-terminal-engine/vt"
)

// FuzzEncodeScreenMsg_structuralIntegrity generates random screen states,
// encodes them via encodeScreenMsg, and walks the output bytes to confirm the
// frame is self-consistent: the header counts match, every changed row index
// is in bounds, and the payload consumes exactly the encoded length with no
// trailing bytes.
func FuzzEncodeScreenMsg_structuralIntegrity(f *testing.F) {
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

		buf := encodeScreenMsg(0, h, 0, 0, 0, changed, rows, curStyle, false, false, false, false)

		// Structural validation. Header is now:
		//   [0] type, [1:9] ack, [9:17] base, [17:19] curRow,
		//   [19:21] curCol, [21:23] screenHeight, [23:25] numChanged,
		//   [25] cursorStyle, [26] cursorFlags, [27:] changed rows.
		if len(buf) < 27 {
			t.Fatalf("encoded too short: %d bytes", len(buf))
		}
		if buf[0] != wireMsgScreen {
			t.Fatalf("msg type = %d, want %d", buf[0], wireMsgScreen)
		}
		screenH := binary.LittleEndian.Uint16(buf[21:23])
		if int(screenH) != h {
			t.Fatalf("screenHeight = %d, want %d", screenH, h)
		}
		numCh := binary.LittleEndian.Uint16(buf[23:25])
		if int(numCh) != nc {
			t.Fatalf("numChanged = %d, want %d", numCh, nc)
		}

		// Walk each changed row and validate row_idx is in bounds
		off := 27
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
