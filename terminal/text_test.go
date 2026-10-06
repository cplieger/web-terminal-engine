package terminal

import (
	"strings"
	"sync"
	"testing"
)

// newTextHandler returns a handler that reads as started at rows x cols with no
// process behind it. None of the sequences these tests feed produce a PTY reply,
// so the nil ptmx is never written.
func newTextHandler(rows, cols int) *Handler {
	h := NewHandler([]string{"/bin/true"}, WithLogger(nil))
	h.started.Store(true)
	h.sizeEstablished = true
	h.screen.Resize(rows, cols)
	return h
}

// commitHistory moves the pending drain into the ring the way a flush pass
// does with no client attached.
func commitHistory(t *testing.T, h *Handler) {
	t.Helper()
	if frame, _ := h.buildFrame(); frame != nil {
		t.Fatalf("buildFrame with no client = %+v, want nil", frame)
	}
}

func readText(t *testing.T, h *Handler, maxLines int) TextSnapshot {
	t.Helper()
	snap, ok := h.Text(maxLines)
	if !ok {
		t.Fatalf("Text(%d) ok = false on a started handler", maxLines)
	}
	return snap
}

func TestText_notStartedReportsNothingToRead(t *testing.T) {
	h := NewHandler([]string{"/bin/true"}, WithLogger(nil))
	snap, ok := h.Text(10)
	if ok || snap != (TextSnapshot{}) {
		t.Errorf("Text(10) before start = %+v, %v; want zero, false", snap, ok)
	}
}

func TestText_emptyScreenIsEmptyText(t *testing.T) {
	h := newTextHandler(5, 20)
	if got := readText(t, h, 10); got != (TextSnapshot{}) {
		t.Errorf("Text(10) on an empty screen = %+v, want empty text, main screen", got)
	}
}

func TestText_carriageReturnOverwriteReadsAsFinalState(t *testing.T) {
	h := newTextHandler(5, 20)
	h.handlePTYData([]byte("abc\rX\r\n50%\x1b[1G100%"))
	if got := readText(t, h, 10).Text; got != "Xbc\n100%" {
		t.Errorf("Text after CR overwrites = %q, want %q", got, "Xbc\n100%")
	}
}

func TestText_cursorUpRewriteReadsAsFinalState(t *testing.T) {
	h := newTextHandler(5, 20)
	h.handlePTYData([]byte("step 1 of 2\r\nworking\x1b[1A\r\x1b[2Kstep 2 of 2"))
	if got := readText(t, h, 10).Text; got != "step 2 of 2\nworking" {
		t.Errorf("Text after a cursor-up rewrite = %q, want %q", got, "step 2 of 2\nworking")
	}
}

func TestText_joinsSoftWrappedRowsAcrossHistory(t *testing.T) {
	const want = "0123456789ABCDEFGHIJ-tail\nnext\nlast"
	tests := []struct {
		name   string
		commit bool
	}{
		{name: "pending_drain", commit: false},
		{name: "ring", commit: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 25 characters at width 10 wrap onto three rows; two rows scroll
			// off a 3-row screen, so the line spans history and the screen.
			h := newTextHandler(3, 10)
			h.handlePTYData([]byte("0123456789ABCDEFGHIJ-tail\r\nnext\r\nlast"))
			if tc.commit {
				commitHistory(t, h)
			}
			if got := readText(t, h, 0).Text; got != want {
				t.Errorf("Text(0) = %q, want %q", got, want)
			}
		})
	}
}

func TestText_hardNewlineAfterAFullRowIsNotJoined(t *testing.T) {
	h := newTextHandler(2, 10)
	h.handlePTYData([]byte("0123456789\r\nABC\r\nDEF"))
	commitHistory(t, h)
	if got := readText(t, h, 0).Text; got != "0123456789\nABC\nDEF" {
		t.Errorf("Text(0) = %q, want %q", got, "0123456789\nABC\nDEF")
	}
}

func TestText_eraseDisplayEndsAWrapChainThatReachedHistory(t *testing.T) {
	// "abcde" scrolls into history while its continuation "fg" is on screen;
	// ED2 then erases that continuation, so the next screen row is a new line.
	h := newTextHandler(2, 5)
	h.handlePTYData([]byte("x\r\nabcdefg\r\n"))
	commitHistory(t, h)
	h.handlePTYData([]byte("\x1b[2J\x1b[Hnew"))
	if got := readText(t, h, 0).Text; got != "x\nabcde\nnew" {
		t.Errorf("Text(0) = %q, want %q", got, "x\nabcde\nnew")
	}
}

func TestText_trimsTrailingBlanksAndEmptyRows(t *testing.T) {
	// A styled trailing blank survives the wire encoding of a history row, so
	// the first two rows reach the ring with blanks still on them.
	const out = "hi   \r\n\x1b[41mbg  \x1b[0m\r\nend\r\n\r\n"
	for _, rows := range []int{6, 2} {
		h := newTextHandler(rows, 20)
		h.handlePTYData([]byte(out))
		commitHistory(t, h)
		if got := readText(t, h, 0).Text; got != "hi\nbg\nend" {
			t.Errorf("Text(0) on a %d-row screen = %q, want %q", rows, got, "hi\nbg\nend")
		}
	}
}

func TestText_wideCharacterSpacersAreDropped(t *testing.T) {
	h := newTextHandler(2, 10)
	h.handlePTYData([]byte("日本\r\nx\r\n日本"))
	commitHistory(t, h)
	if got := readText(t, h, 0).Text; got != "日本\nx\n日本" {
		t.Errorf("Text(0) = %q, want %q", got, "日本\nx\n日本")
	}
}

func TestText_maxLinesKeepsTheNewestLines(t *testing.T) {
	h := newTextHandler(3, 10)
	h.handlePTYData([]byte("1\r\n2\r\n3\r\n4\r\n5\r\n6\r\n"))
	commitHistory(t, h)
	tests := []struct {
		max  int
		want string
	}{
		{max: 2, want: "5\n6"},
		{max: 4, want: "3\n4\n5\n6"},
		{max: 0, want: "1\n2\n3\n4\n5\n6"},
		{max: 50, want: "1\n2\n3\n4\n5\n6"},
	}
	for _, tc := range tests {
		if got := readText(t, h, tc.max).Text; got != tc.want {
			t.Errorf("Text(%d) = %q, want %q", tc.max, got, tc.want)
		}
	}
}

func TestText_altScreenReadsTheMainScreenBeneathIt(t *testing.T) {
	h := newTextHandler(3, 20)
	h.handlePTYData([]byte("$ ls\r\nmain line\r\n"))
	// Enough alt output to scroll, so alt rows also reach the pending drain.
	h.handlePTYData([]byte("\x1b[?1049h\x1b[Hvim 1\r\nvim 2\r\nvim 3\r\nvim 4"))
	got := readText(t, h, 0)
	if got.Text != "$ ls\nmain line" || !got.AltScreen {
		t.Errorf("Text(0) in the alt screen = %+v, want main text and AltScreen", got)
	}
	commitHistory(t, h)
	h.handlePTYData([]byte("\x1b[?1049l"))
	commitHistory(t, h)
	got = readText(t, h, 0)
	if got.Text != "$ ls\nmain line" || got.AltScreen {
		t.Errorf("Text(0) after leaving the alt screen = %+v, want main text only", got)
	}
}

func TestText_ed3ClearsTheHistoryItReads(t *testing.T) {
	h := newTextHandler(2, 10)
	h.handlePTYData([]byte("old1\r\nold2\r\nnow"))
	commitHistory(t, h)
	h.handlePTYData([]byte("\x1b[3J"))
	if got := readText(t, h, 0).Text; got != "old2\nnow" {
		t.Errorf("Text(0) after ED3 = %q, want %q", got, "old2\nnow")
	}
}

func TestText_leavesThePendingDrainForTheNextFrame(t *testing.T) {
	h := newTextHandler(2, 10)
	h.handlePTYData([]byte("a\r\nb\r\nc\r\nd"))
	before := len(h.screen.Drained)
	readText(t, h, 0)
	if after := len(h.screen.Drained); before != 2 || after != before {
		t.Fatalf("pending drain = %d lines before Text and %d after, want 2 and 2", before, after)
	}
	commitHistory(t, h)
	if got := h.scrollback.Len(); got != 2 {
		t.Errorf("ring after the next flush = %d lines, want 2", got)
	}
}

func TestText_ConcurrentWithPTYOutput(t *testing.T) {
	h := newTextHandler(4, 16)
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			h.handlePTYData([]byte("line of output that wraps\r\n"))
			h.buildFrame()
		}
	})
	for range 200 {
		snap := readText(t, h, 50)
		if snap.Text == "" {
			continue // read before the first write
		}
		for line := range strings.SplitSeq(snap.Text, "\n") {
			if line != "line of output that wraps" {
				t.Fatalf("Text(50) line = %q, want a whole wrapped line", line)
			}
		}
	}
	wg.Wait()
}
