package terminal

import (
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// TestOSC52ClipboardFrame verifies the handler drains an OSC 52 clipboard copy
// from the VT into a clipboard wire frame, even when the copy arrives with no
// accompanying screen change (Build returns nil, buildFrame synthesizes a
// frame). OSC 52 SET generates no PTY reply, so handlePTYData never touches the
// (unstarted) PTY.
func TestOSC52ClipboardFrame(t *testing.T) {
	h := NewHandler([]string{"/bin/true"})

	h.registry.Add(&websocket.Conn{})                 // attached client: the render path, not zero-client suspension
	h.handlePTYData([]byte("\x1b]52;c;aGVsbG8=\x07")) // base64("hello")
	if got := string(h.pendingClipboard); got != "hello" {
		t.Fatalf("handlePTYData did not stage clipboard: got %q, want hello", got)
	}

	frame, _ := h.buildFrame()
	if frame == nil {
		t.Fatal("buildFrame returned nil; a clipboard event must synthesize a frame")
	}
	if frame.clipboardPayload == nil {
		t.Fatal("frame is missing clipboardPayload")
	}
	if got := clipboardFrameText(t, frame.clipboardPayload); got != "hello" {
		t.Errorf("payload text = %q, want hello", got)
	}
	if h.pendingClipboard != nil {
		t.Error("buildFrame should have drained pendingClipboard")
	}
}

// TestOSC52ClipboardSurvivesLaterOutput pins that a staged copy is held until a
// frame carries it. Output keeps arriving between flushes, and each arrival
// re-reads the screen's clipboard slot — which is empty by then, because the copy
// was already taken. Letting that empty read overwrite the staged copy would drop
// the user's clipboard whenever the program printed anything after the OSC 52.
func TestOSC52ClipboardSurvivesLaterOutput(t *testing.T) {
	h := NewHandler([]string{"/bin/true"})

	h.registry.Add(&websocket.Conn{})
	h.handlePTYData([]byte("\x1b]52;c;aGVsbG8=\x07")) // base64("hello")
	h.handlePTYData([]byte("more output\r\n"))        // no copy in this chunk

	if got := string(h.pendingClipboard); got != "hello" {
		t.Fatalf("pendingClipboard = %q after later output, want hello", got)
	}
	frame, _ := h.buildFrame()
	if frame == nil || frame.clipboardPayload == nil {
		t.Fatal("no clipboard payload in the frame; the staged copy was lost")
	}
	if got := clipboardFrameText(t, frame.clipboardPayload); got != "hello" {
		t.Errorf("payload text = %q, want hello", got)
	}
}

// clipboardFrameText decodes a clipboard frame per docs/wire-protocol.md:
// [1B type=6][8B ack][2B text_byte_len][text] up to 65,535 bytes of text, and
// [1B type=9][8B ack][4B text_byte_len][text] past that.
func clipboardFrameText(t *testing.T, p []byte) string {
	t.Helper()
	if len(p) < 11 {
		t.Fatalf("clipboard frame is %d bytes, want at least the 11-byte header", len(p))
	}
	var textLen, header int
	switch p[0] {
	case 6:
		textLen, header = int(binary.LittleEndian.Uint16(p[9:11])), 11
	case 9:
		if len(p) < 13 {
			t.Fatalf("opcode 9 frame is %d bytes, want at least the 13-byte header", len(p))
		}
		textLen, header = int(binary.LittleEndian.Uint32(p[9:13])), 13
		if textLen <= 0xFFFF {
			t.Fatalf("opcode 9 carries %d bytes, which opcode 6 fits", textLen)
		}
	default:
		t.Fatalf("payload opcode = %d, want 6 or 9 (clipboard)", p[0])
	}
	if textLen != len(p)-header {
		t.Fatalf("text_byte_len = %d, but the frame carries %d text bytes", textLen, len(p)-header)
	}
	return string(p[header:])
}

// TestOSC52LongCopyFrame pins that a copy longer than the generic OSC cap
// reaches the client whole, under a 2-byte length (10,240) and past it (100,000).
func TestOSC52LongCopyFrame(t *testing.T) {
	for _, n := range []int{10_240, 100_000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			h := NewHandler([]string{"/bin/true"})
			h.registry.Add(&websocket.Conn{})
			text := strings.Repeat("0123456789abcdef\u2713\n", n/20)
			h.handlePTYData([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"))
			frame, _ := h.buildFrame()
			if frame == nil || frame.clipboardPayload == nil {
				t.Fatal("no clipboard payload in the frame; the copy was dropped")
			}
			if got := clipboardFrameText(t, frame.clipboardPayload); got != text {
				t.Errorf("payload text is %d bytes, want the %d-byte copy", len(got), len(text))
			}
		})
	}
}

// TestOSC52OversizeCopyIsRefusedVisibly pins that a copy over the OSC 52 cap
// sends the client a refusal frame instead of nothing, and that a later copy
// replaces a refusal still waiting for a frame.
func TestOSC52OversizeCopyIsRefusedVisibly(t *testing.T) {
	over := "\x1b]52;c;" + strings.Repeat("QUJD", (1<<20)/4) + "\x07"

	t.Run("refusal_frame", func(t *testing.T) {
		h := NewHandler([]string{"/bin/true"})
		h.registry.Add(&websocket.Conn{})
		h.handlePTYData([]byte(over))
		frame, _ := h.buildFrame()
		if frame == nil || frame.clipboardPayload == nil {
			t.Fatal("an oversize copy produced no frame; it was dropped silently")
		}
		// [1B type=10][8B ack], no body.
		if p := frame.clipboardPayload; len(p) != 9 || p[0] != 10 {
			t.Errorf("payload = % x, want the 9-byte clipboardRefused frame (opcode 10)", p)
		}
	})

	t.Run("a_later_copy_replaces_the_refusal", func(t *testing.T) {
		h := NewHandler([]string{"/bin/true"})
		h.registry.Add(&websocket.Conn{})
		h.handlePTYData([]byte(over))
		h.handlePTYData([]byte("\x1b]52;c;aGVsbG8=\x07")) // base64("hello")
		frame, _ := h.buildFrame()
		if frame == nil || frame.clipboardPayload == nil {
			t.Fatal("no clipboard payload in the frame")
		}
		if got := clipboardFrameText(t, frame.clipboardPayload); got != "hello" {
			t.Errorf("payload text = %q, want hello", got)
		}
	})

	t.Run("a_later_refusal_replaces_the_copy", func(t *testing.T) {
		h := NewHandler([]string{"/bin/true"})
		h.registry.Add(&websocket.Conn{})
		h.handlePTYData([]byte("\x1b]52;c;aGVsbG8=\x07"))
		h.handlePTYData([]byte(over))
		frame, _ := h.buildFrame()
		if frame == nil || frame.clipboardPayload == nil {
			t.Fatal("no clipboard payload in the frame")
		}
		if p := frame.clipboardPayload; len(p) != 9 || p[0] != 10 {
			t.Errorf("payload = % x, want the 9-byte clipboardRefused frame (opcode 10)", p)
		}
	})

	t.Run("later_output_keeps_the_refusal", func(t *testing.T) {
		h := NewHandler([]string{"/bin/true"})
		h.registry.Add(&websocket.Conn{})
		h.handlePTYData([]byte(over))
		h.handlePTYData([]byte("more output\r\n"))
		frame, _ := h.buildFrame()
		if frame == nil || frame.clipboardPayload == nil {
			t.Fatal("later output dropped the pending refusal")
		}
		if p := frame.clipboardPayload; len(p) != 9 || p[0] != 10 {
			t.Errorf("payload = % x, want the 9-byte clipboardRefused frame (opcode 10)", p)
		}
		if next, _ := h.buildFrame(); next != nil && next.clipboardPayload != nil {
			t.Error("the refusal was sent twice, want a one-shot")
		}
	})
}

// TestOSC4ForcesRepaintPending verifies an OSC 4 palette change raises the
// handler's repaint-pending flag, which buildFrame consumes (calling
// builder.RepaintWindow so already-drawn cells re-resolve to the new palette).
func TestOSC4ForcesRepaintPending(t *testing.T) {
	h := NewHandler([]string{"/bin/true"})

	h.handlePTYData([]byte("\x1b]4;1;rgb:00/ff/00\x07"))
	if !h.paletteChangedPending {
		t.Fatal("OSC 4 must set paletteChangedPending")
	}

	h.registry.Add(&websocket.Conn{}) // attached client: the render path, not zero-client suspension
	_, _ = h.buildFrame()
	if h.paletteChangedPending {
		t.Error("buildFrame should have consumed paletteChangedPending")
	}
}
