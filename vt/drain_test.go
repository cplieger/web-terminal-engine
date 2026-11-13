package vt

import (
	"fmt"
	"strings"
	"testing"
)

func numbered(prefix string, n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\r\n", prefix, i)
	}
	return b.String()
}

func drainedText(s *Screen) []string {
	out := make([]string, 0, len(s.Drained))
	for _, line := range s.Drained {
		out = append(out, strings.TrimRight(wireRowText(line), " "))
	}
	return out
}

// TestDrainedHoldsMainScreenHistoryOnly pins Drained as main-screen history,
// the xterm model: the alternate screen keeps no scrollback, and only an
// erase of saved lines discards lines already scrolled off the main screen.
func TestDrainedHoldsMainScreenHistoryOnly(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"alt-screen scrolling adds nothing", "\x1b[?1049h" + numbered("alt", 10), nil},
		{"main lines before an alt enter survive the alt session", numbered("main", 4) + "\x1b[?1049h" + numbered("alt", 10) + "\x1b[?1049l", []string{"main 1", "main 2"}},
		{"an alt exit while on the main screen keeps them", numbered("main", 4) + "\x1b[?1049l", []string{"main 1", "main 2"}},
		{"ED 2 erases the screen, not lines already scrolled off", numbered("main", 4) + "\x1b[2J", []string{"main 1", "main 2"}},
		{"DECCOLM erases the screen, not lines already scrolled off", numbered("main", 4) + "\x1b[?3l", []string{"main 1", "main 2"}},
		{"ED 3 erases them", numbered("main", 4) + "\x1b[3J", nil},
		{"RIS erases them", numbered("main", 4) + "\x1bc", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := New(3, 20)
			if _, err := s.Write([]byte(tc.input)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got := drainedText(s)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("Drained = %q, want %q", got, tc.want)
			}
		})
	}
}
