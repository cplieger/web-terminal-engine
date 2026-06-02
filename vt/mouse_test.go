package vt

import "testing"

func TestMouseModeTracking(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMode  uint16
		wantSGR   bool
		wantFocus bool
	}{
		{"mode 1000 enables normal tracking", "\x1b[?1000h", 1000, false, false},
		{"mode 1002 enables button-event tracking", "\x1b[?1002h", 1002, false, false},
		{"mode 1003 enables any-event tracking", "\x1b[?1003h", 1003, false, false},
		{"mode 1006 enables SGR encoding", "\x1b[?1000;1006h", 1000, true, false},
		{"mode 1004 enables focus reporting", "\x1b[?1004h", 0, false, true},
		{"combined modes", "\x1b[?1003;1006;1004h", 1003, true, true},
		{"disable mode 1000", "\x1b[?1000h\x1b[?1000l", 0, false, false},
		{"disable mode 1003", "\x1b[?1003h\x1b[?1003l", 0, false, false},
		{"disable SGR", "\x1b[?1006h\x1b[?1006l", 0, false, false},
		{"disable focus", "\x1b[?1004h\x1b[?1004l", 0, false, false},
		{"upgrade 1000 to 1003", "\x1b[?1000h\x1b[?1003h", 1003, false, false},
		{"soft reset clears mouse", "\x1b[?1003;1006;1004h\x1b[!p", 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(24, 80)
			if _, err := s.Write([]byte(tt.input)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if s.MouseMode != tt.wantMode {
				t.Errorf("MouseMode = %d, want %d", s.MouseMode, tt.wantMode)
			}
			if s.MouseSGR != tt.wantSGR {
				t.Errorf("MouseSGR = %v, want %v", s.MouseSGR, tt.wantSGR)
			}
			if s.FocusReporting != tt.wantFocus {
				t.Errorf("FocusReporting = %v, want %v", s.FocusReporting, tt.wantFocus)
			}
		})
	}
}
