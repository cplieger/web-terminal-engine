package vt

import "testing"

// FuzzScreenWrite exercises the VT parser with arbitrary byte sequences to
// detect panics, out-of-bounds accesses, and infinite loops.
func FuzzScreenWrite(f *testing.F) {
	// Seed corpus: empty, plain text, CSI sequences, UTF-8, OSC.
	f.Add([]byte{})
	f.Add([]byte("hello world"))
	f.Add([]byte("\x1b[1;31mred\x1b[0m"))
	f.Add([]byte("\x1b[H\x1b[2J"))
	f.Add([]byte("\x1b[?1049h\x1b[?1049l"))
	f.Add([]byte("\xc3\xa9\xe2\x9c\x93\xf0\x9f\x98\x80")) // UTF-8 multi-byte
	f.Add([]byte("\x1b]0;title\x07"))                     // OSC
	f.Add([]byte("\x1b[38;2;255;128;0mrgb\x1b[0m"))       // 24-bit color

	f.Fuzz(func(t *testing.T, data []byte) {
		s := New(24, 80)
		s.Write(data)
	})
}
