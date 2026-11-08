package terminal

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// numberedLines returns "line 1\r\n" … "line n\r\n".
func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	return b.String()
}

// writeScreen feeds s to the handler's screen.
func writeScreen(t *testing.T, h *Handler, s string) {
	t.Helper()
	if _, err := h.screen.Write([]byte(s)); err != nil {
		t.Fatalf("screen write: %v", err)
	}
}

// ringText returns the retained ring as text, oldest first.
func ringText(h *Handler) []string {
	out := make([]string, 0, h.scrollback.Len())
	for i := range h.scrollback.Len() {
		line, _ := h.scrollback.at(i)
		out = append(out, runsText(line))
	}
	return out
}

// latchAltInBuilder runs one Build while the screen is on the alternate
// screen, as the flush loop does while a client watches vim.
func latchAltInBuilder(t *testing.T, h *Handler) {
	t.Helper()
	writeScreen(t, h, "\x1b[?1049h")
	h.builder.Build(h.screen, true, noClients, nil, h.scrollback.Committed())
	if !h.builder.prevAlt {
		t.Fatal("precondition: the builder did not observe the alternate screen")
	}
}

// suspendedHandler is a 3-row handler whose builder last saw vim's alternate
// screen. vim then exits and the shell prints 20 lines, all with no client
// attached: 18 of them scroll off the top.
func suspendedHandler(t *testing.T) *Handler {
	t.Helper()
	h := NewHandler([]string{"/bin/cat"}, WithLogger(nil))
	h.screen = vt.New(3, 20)
	h.sizeEstablished = true
	latchAltInBuilder(t, h)
	writeScreen(t, h, "\x1b[?1049l"+numberedLines(20))
	return h
}

func TestDrainedLinesAfterAltExitAreRetained(t *testing.T) {
	t.Run("a zero-client flush pass retains every line", func(t *testing.T) {
		h := suspendedHandler(t)
		h.buildFrame()
		got := ringText(h)
		if len(got) != 18 || got[0] != "line 1" || got[17] != "line 18" {
			t.Errorf("ring after the suspended pass = %d lines %q, want line 1 … line 18", len(got), got)
		}
	})
	t.Run("the resume commits every line before its replay", func(t *testing.T) {
		h := suspendedHandler(t)
		server, cleanup := serverSideConn(t)
		defer cleanup()
		h.handleResume(server, &clientState{}, "sid", -1, 0, nil)
		if got := h.scrollback.Committed(); got != 18 {
			t.Errorf("committed after resume = %d, want 18", got)
		}
	})
	t.Run("the plain-text reader includes the pending drain", func(t *testing.T) {
		h := suspendedHandler(t)
		tail := textTail{}
		h.collectTextLocked(&tail)
		if len(tail.lines) != 20 || tail.lines[0] != "line 20" || tail.lines[19] != "line 1" {
			t.Errorf("text lines (newest first) = %q, want line 20 … line 1", tail.lines)
		}
	})
}

func TestBuildCommitsMainLinesAcrossAnAltTransition(t *testing.T) {
	t.Run("output then an alt enter in one read", func(t *testing.T) {
		b := &flushFrameBuilder{}
		screen := vt.New(3, 20)
		b.Build(screen, true, noClients, nil, 0)
		if _, err := screen.Write([]byte(numberedLines(20) + "\x1b[?1049h")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
		frame := b.Build(screen, true, noClients, nil, 0)
		if frame == nil {
			t.Fatal("Build returned nil")
		}
		if len(frame.scrollLines) != 18 || runsText(frame.scrollLines[0]) != "line 1" {
			t.Errorf("scrollLines = %d lines, want 18 starting at line 1", len(frame.scrollLines))
		}
		if !frame.altActive || frame.base != 18 {
			t.Errorf("frame altActive=%v base=%d, want an alt frame whose base is the committed count 18", frame.altActive, frame.base)
		}
	})
	t.Run("an alt exit then output in one read", func(t *testing.T) {
		b := &flushFrameBuilder{}
		screen := vt.New(3, 20)
		if _, err := screen.Write([]byte("\x1b[?1049h")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
		b.Build(screen, true, noClients, nil, 0)
		if _, err := screen.Write([]byte("\x1b[?1049l" + numberedLines(20))); err != nil {
			t.Fatalf("screen write: %v", err)
		}
		frame := b.Build(screen, true, noClients, nil, 0)
		if frame == nil || len(frame.scrollLines) != 18 {
			t.Fatalf("frame = %+v, want 18 scroll lines", frame)
		}
	})
}
