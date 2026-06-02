package vt

import "testing"

func TestDECKPAM_DECKPNM(t *testing.T) {
	t.Run("ESC= enables AppKeypad", func(t *testing.T) {
		s := New(24, 80)
		if s.AppKeypad {
			t.Fatal("AppKeypad should be false initially")
		}
		s.Write([]byte("\x1b="))
		if !s.AppKeypad {
			t.Fatal("AppKeypad should be true after ESC =")
		}
	})

	t.Run("ESC> disables AppKeypad", func(t *testing.T) {
		s := New(24, 80)
		s.Write([]byte("\x1b="))
		s.Write([]byte("\x1b>"))
		if s.AppKeypad {
			t.Fatal("AppKeypad should be false after ESC >")
		}
	})

	t.Run("soft reset clears AppKeypad", func(t *testing.T) {
		s := New(24, 80)
		s.Write([]byte("\x1b="))
		s.Write([]byte("\x1b[!p")) // DECSTR
		if s.AppKeypad {
			t.Fatal("AppKeypad should be false after soft reset")
		}
	})

	t.Run("full reset clears AppKeypad", func(t *testing.T) {
		s := New(24, 80)
		s.Write([]byte("\x1b="))
		s.Write([]byte("\x1bc")) // RIS
		if s.AppKeypad {
			t.Fatal("AppKeypad should be false after full reset")
		}
	})

	t.Run("toggle multiple times", func(t *testing.T) {
		s := New(24, 80)
		s.Write([]byte("\x1b="))
		s.Write([]byte("\x1b=")) // idempotent
		if !s.AppKeypad {
			t.Fatal("AppKeypad should remain true")
		}
		s.Write([]byte("\x1b>"))
		s.Write([]byte("\x1b>")) // idempotent
		if s.AppKeypad {
			t.Fatal("AppKeypad should remain false")
		}
	})
}
