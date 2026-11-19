//go:build unix

package terminal

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// goldenFrames are the cross-language wire contract: the Go encoder must emit
// these bytes and web/src/wire-golden.node.test.ts must decode them. After an
// intentional change, regenerate and update the TS side in the same change:
// UPDATE_GOLDEN=1 go test ./terminal/ -run TestWireGolden
func goldenFrames() map[string][]byte {
	row := func(text string) []vt.WireRun {
		return []vt.WireRun{{T: text, F: -1, B: -1, Uc: -1}}
	}
	screenRows := [][]vt.WireRun{row("ab"), {}, row("cd")}
	return map[string][]byte{
		// screen: base=100, height=3, cursor=(1,2), changed rows 0 and 2,
		// cursorStyle=2, blink=true.
		"screen": encodeScreenMsg(100, 3, 1, 2, 0, []int{0, 2}, screenRows, 2, false, true, false, false, false),
		// scroll: two history lines starting at absolute index 50.
		"scroll": encodeScrollMsg(0, 50, [][]vt.WireRun{row("h0"), row("h1")}),
		// resumeAck: ack=7, epoch, committed=200, oldest=10, no flags set;
		// carries the third length-gated tail (serverWireVersion + ackFlags).
		"resumeack": encodeResumeAck(7, 1234567890, 200, 10, resumeAckFlags{}),
		// resumeAck with the ledger-lost flag set (ackFlags bit0).
		"resumeack-ledgerlost": encodeResumeAck(7, 1234567890, 200, 10, resumeAckFlags{LedgerLost: true}),
		// resumeAck with the history-paging capability declared (ackFlags bit1),
		// the demand-paged-scrollback signal. Independent of bit0, so the pair
		// pins that a client reads the bits separately.
		"resumeack-historypaging": encodeResumeAck(7, 1234567890, 200, 10, resumeAckFlags{HistoryPaging: true}),
		// resumeAck with the server-owns-focus capability declared (ackFlags
		// bit2). One fixture per bit, each with every other bit clear, is what
		// pins each bit's POSITION independently: a shift in any one of them
		// changes exactly one fixture.
		"resumeack-serverfocus": encodeResumeAck(7, 1234567890, 200, 10, resumeAckFlags{ServerFocus: true}),
		// resumeAck with the ephemeral-input capability declared (ackFlags bit3),
		// independent of the other three for the same reason.
		"resumeack-ephemeralinput": encodeResumeAck(7, 1234567890, 200, 10, resumeAckFlags{EphemeralInput: true}),
		// ackOnly: bare ack (type + inputAck, no body), ack=42.
		"ackonly": encodeAckOnly(42),
		// clipboard (opcode 6): OSC 52 text for the system clipboard.
		"clipboard": encodeClipboardMsg([]byte("copy me")),
		// clipboard-10k: a 10,240-byte OSC 52 copy taken through the parser,
		// past the generic OSC cap. The TS test rebuilds the same text.
		"clipboard-10k": encodeClipboardMsg(parsedOSC52(strings.Repeat("0123456789abcdef\u2713\n", 512))),
		// clipboard-long (opcode 9): a 65,540-byte copy, past a 2-byte length.
		"clipboard-long": encodeClipboardMsg(parsedOSC52(strings.Repeat("0123456789abcdef\u2713\n", 3277))),
		// clipboard-refused (opcode 10): a copy over the OSC 52 cap.
		"clipboard-refused": encodeClipboardRefusedMsg(),
		// modes: bracketed paste + SGR mouse + reverse video on, mouseMode 1002,
		// kitty disambiguate flag (1).
		"modes": encodeModesMsg(true, false, true, false, false, true, false, 1002, 1),
		// modes-power-on: a fresh screen's announcement, the modes a client
		// assumes (POWER_ON_MODES) before its socket's first modes frame.
		"modes-power-on": new(flushFrameBuilder).refreshAnnouncement(vt.New(24, 80)).modes,
		"title":          encodeTitleMsg("hello world"),
		"pong":           encodePongMsg(),
		// grid: 132 columns, 43 rows.
		"grid": encodeGridMsg(132, 43),
	}
}

// parsedOSC52 returns what a screen queues for an OSC 52 copy of text.
func parsedOSC52(text string) []byte {
	s := vt.New(24, 80)
	s.Write([]byte("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"))
	return s.TakeClipboard()
}

func TestWireGolden(t *testing.T) {
	dir := filepath.Join("..", "wire-golden")
	update := os.Getenv("UPDATE_GOLDEN") != ""
	if update {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
	}
	for name, got := range goldenFrames() {
		path := filepath.Join(dir, name+".bin")
		if update {
			if err := os.WriteFile(path, got, 0o600); err != nil {
				t.Fatalf("write golden %s: %v", path, err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 go test to generate): %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: Go encoding drifted from the golden fixture (%d bytes now, %d in fixture). "+
				"If this wire change is intentional, regenerate with UPDATE_GOLDEN=1 and update the TS decoder + web/src/wire-golden.node.test.ts in the same change.",
				name, len(got), len(want))
		}
	}
}
