package terminal

import "testing"

// The floor is enforced by the screen, so this checks NewHandler's plumbing
// through wire output. Slot 4 (SGR 34) sits just under 4.5:1 on black, so the
// floor must change it, while the default case leaves it raw.
func TestWithMinimumContrastReachesTheScreen(t *testing.T) {
	t.Parallel()

	// slot4 is the engine's default basic-16 blue (vt's basic16RGB index 4).
	const slot4 = int32(0x0d73cc)

	firstFG := func(t *testing.T, h *Handler) int32 {
		t.Helper()
		if _, err := h.screen.Write([]byte("\x1b[34mlink")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
		runs := h.screen.RenderRowWire(0)
		if len(runs) == 0 {
			t.Fatal("no wire runs")
		}
		return runs[0].F
	}

	t.Run("off by default, so the raw palette entry reaches the client", func(t *testing.T) {
		t.Parallel()
		got := firstFG(t, NewHandler([]string{"/bin/true"}))
		if got != slot4 {
			t.Errorf("F = %#06x, want the untouched palette entry %#06x", got, slot4)
		}
	})

	t.Run("the option reaches the screen and lifts the foreground", func(t *testing.T) {
		t.Parallel()
		got := firstFG(t, NewHandler([]string{"/bin/true"}, WithMinimumContrast(4.5)))
		if got == slot4 {
			t.Errorf("F = %#06x is the unlifted palette entry; the option never reached the screen", got)
		}
	})

	t.Run("a ratio at or below the off value changes nothing", func(t *testing.T) {
		t.Parallel()
		got := firstFG(t, NewHandler([]string{"/bin/true"}, WithMinimumContrast(1)))
		if got != slot4 {
			t.Errorf("F = %#06x, want the untouched palette entry %#06x", got, slot4)
		}
	})
}
