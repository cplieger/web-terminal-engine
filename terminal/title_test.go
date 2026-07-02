package terminal

import "testing"

// TestHandlerTitle verifies Title() reports the window title set via OSC 0/2.
// OSC 2 generates no PTY reply, so handlePTYData never touches the (unstarted)
// PTY (same seam the clipboard test relies on).
func TestHandlerTitle(t *testing.T) {
	h := NewHandler([]string{"/bin/true"})

	if got := h.Title(); got != "" {
		t.Fatalf("initial Title() = %q, want empty", got)
	}

	h.handlePTYData([]byte("\x1b]2;my session\x07"))
	if got := h.Title(); got != "my session" {
		t.Fatalf("Title() = %q, want %q", got, "my session")
	}

	// A later OSC 2 replaces it.
	h.handlePTYData([]byte("\x1b]2;renamed\x07"))
	if got := h.Title(); got != "renamed" {
		t.Fatalf("Title() after rename = %q, want %q", got, "renamed")
	}
}
