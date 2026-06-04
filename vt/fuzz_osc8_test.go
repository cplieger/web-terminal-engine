package vt

import (
	"strings"
	"testing"
)

// FuzzOSC8 writes OSC 8 sequences with randomized URIs to a Screen.
// Invariant: never panics; if a hyperlink is stored, it is a prefix of
// the input URI (control chars like ST/BEL may truncate it early).
func FuzzOSC8(f *testing.F) {
	f.Add([]byte("http://example.com"))
	f.Add([]byte("javascript:alert(1)"))
	f.Add([]byte("data:text/html,<script>"))
	f.Add([]byte(""))
	f.Add([]byte("https://a.com/path?q=1&r=2#frag"))
	f.Add([]byte("\x00\x01\x02\xff"))

	f.Fuzz(func(t *testing.T, uri []byte) {
		s := New(24, 80)
		// OSC 8 ; ; <uri> BEL
		seq := append([]byte("\x1b]8;;"), uri...)
		seq = append(seq, '\x07')
		s.Write(seq)
		s.Write([]byte("X"))
		// Close hyperlink
		s.Write([]byte("\x1b]8;;\x07"))

		got := s.Cells[0][0].Hyperlink
		// The hyperlink must be empty or a prefix of the input URI string.
		// Control characters (BEL, ST=\x9c, ESC) inside the URI terminate
		// the OSC sequence early, so truncation is valid behavior.
		if got != "" && !strings.HasPrefix(string(uri), got) {
			t.Fatalf("hyperlink = %q is not a prefix of input %q", got, string(uri))
		}
	})
}
