package terminal

import (
	"slices"
	"testing"

	"github.com/coder/websocket"
	"github.com/cplieger/vterm/vt"
)

// noClients is an empty client snapshot for tests that only care
// about the frame contents, not its fan-out.
var noClients = map[*websocket.Conn]uint64{}

// TestBuild_CursorOnlyMoveAddsRowToChanged drives the bug from the
// "cursor visually doesn't move on left/right arrow or space-over-space"
// report:
//
//   - First Build is a full repaint (all rows in changed) — establishes
//     the prev cache.
//   - Second Build with no PTY input at all: must return nil.
//   - Third Build after CSI D (cursor back): row content unchanged but
//     cursor moved, so the cursor row must appear in changed so the
//     wire frame carries its payload and the client can repaint the
//     inline cursor span at the new column. Without this fix the
//     server emits a non-nil frame but flushLoop drops the screen
//     payload (it gates on len(changed) > 0) and the client never
//     sees the move.
func TestBuild_CursorOnlyMoveAddsRowToChanged(t *testing.T) {
	screen := vt.New(10, 40)
	// Establish some content so the cursor is on a row that has runs.
	if _, err := screen.Write([]byte("hello world")); err != nil {
		t.Fatalf("screen write: %v", err)
	}
	b := &FlushFrameBuilder{}

	// First frame: full repaint baseline.
	frame := b.Build(screen, true, noClients)
	if frame == nil {
		t.Fatalf("first Build returned nil; expected full repaint")
	}
	if len(frame.changed) != screen.Height {
		t.Fatalf("first Build: want all %d rows changed, got %d", screen.Height, len(frame.changed))
	}
	row, _ := screen.CursorPos()
	if !slices.Contains(frame.changed, row) {
		t.Fatalf("first Build: cursor row %d missing from changed %v", row, frame.changed)
	}

	// Second frame with no input: nothing to send.
	if frame := b.Build(screen, true, noClients); frame != nil {
		t.Fatalf("idle Build returned non-nil frame: changed=%v", frame.changed)
	}

	// Move cursor left without changing any cell content (CSI D).
	prevRow, prevCol := screen.CursorPos()
	if _, err := screen.Write([]byte{0x1b, '[', 'D'}); err != nil {
		t.Fatalf("write CSI D: %v", err)
	}
	curRow, curCol := screen.CursorPos()
	if curRow == prevRow && curCol == prevCol {
		t.Fatalf("CSI D did not move cursor: still at row=%d col=%d", curRow, curCol)
	}

	frame = b.Build(screen, true, noClients)
	if frame == nil {
		t.Fatalf("post-cursor-move Build returned nil; expected a frame so the client can repaint")
	}
	if !slices.Contains(frame.changed, curRow) {
		t.Fatalf("post-cursor-move Build: cursor row %d missing from changed %v", curRow, frame.changed)
	}
	// Ensure the cursor coordinates reported by the frame match the
	// post-move position (this is what the wire encoder reads).
	if frame.curRow != curRow || frame.curCol != curCol {
		t.Fatalf("frame cursor pos = (%d,%d); want (%d,%d)",
			frame.curRow, frame.curCol, curRow, curCol)
	}
}

// TestBuild_CursorBetweenRowsTouchesBothRows covers the other shape:
// when the cursor moves to a different row without altering content,
// both the previous and current cursor rows must be in `changed` so
// the inline cursor span is removed from the old row and inserted on
// the new row.
func TestBuild_CursorBetweenRowsTouchesBothRows(t *testing.T) {
	screen := vt.New(10, 40)
	// Land the cursor at (5, 5) with content on both rows 5 and 7.
	if _, err := screen.Write([]byte("\x1b[6;1Habcde\x1b[8;1Hxyz\x1b[6;6H")); err != nil {
		t.Fatalf("screen write: %v", err)
	}
	b := &FlushFrameBuilder{}
	if frame := b.Build(screen, true, noClients); frame == nil {
		t.Fatal("baseline Build returned nil")
	}
	prevRow, _ := screen.CursorPos()
	if prevRow != 5 {
		t.Fatalf("setup: cursor row = %d, want 5", prevRow)
	}

	// Move cursor to (7, 4) — different row, no cell content change.
	if _, err := screen.Write([]byte("\x1b[8;5H")); err != nil {
		t.Fatalf("write CUP: %v", err)
	}
	curRow, _ := screen.CursorPos()
	if curRow != 7 {
		t.Fatalf("post-CUP cursor row = %d, want 7", curRow)
	}

	frame := b.Build(screen, true, noClients)
	if frame == nil {
		t.Fatal("inter-row cursor move Build returned nil")
	}
	if !slices.Contains(frame.changed, prevRow) {
		t.Fatalf("changed missing previous cursor row %d: got %v", prevRow, frame.changed)
	}
	if !slices.Contains(frame.changed, curRow) {
		t.Fatalf("changed missing current cursor row %d: got %v", curRow, frame.changed)
	}
}

// TestBuild_scrollbackProducesScrollLines verifies that when scrollback has
// drained from the screen and we are not in alt-screen, Build surfaces those
// lines as the frame's scrollLines.
func TestBuild_scrollbackProducesScrollLines(t *testing.T) {
	screen := vt.New(3, 20) // tiny screen so writing many lines scrolls
	for range 15 {
		if _, err := screen.Write([]byte("scrollback line\r\n")); err != nil {
			t.Fatalf("screen write: %v", err)
		}
	}

	b := &FlushFrameBuilder{}
	frame := b.Build(screen, true, noClients)
	if frame == nil {
		t.Fatalf("Build returned nil; expected a full-repaint baseline frame")
	}
	if len(frame.scrollLines) == 0 {
		t.Errorf("Build: scrollLines empty; want non-empty (scrollback drained, not alt-screen)")
	}
}

// TestBuildTitlePayload_changeDetection verifies buildTitlePayload emits a
// frame on first announce and whenever the title changes, and suppresses
// (returns nil) when the title is unchanged.
func TestBuildTitlePayload_changeDetection(t *testing.T) {
	screen := vt.New(5, 20)
	b := &FlushFrameBuilder{}

	screen.Title = "first"
	// First call: not yet announced -> must emit a payload.
	if got := b.buildTitlePayload(screen); len(got) == 0 {
		t.Fatalf("first buildTitlePayload = empty, want a title frame")
	}
	// Same title, now announced -> suppressed (nil).
	if got := b.buildTitlePayload(screen); len(got) != 0 {
		t.Errorf("unchanged-title buildTitlePayload = %d bytes, want 0 (suppress when title unchanged)", len(got))
	}
	// Changed title -> emit again.
	screen.Title = "second"
	if got := b.buildTitlePayload(screen); len(got) == 0 {
		t.Errorf("changed-title buildTitlePayload = empty, want a title frame")
	}
}

// TestAppendRowIfMissing_rowCountBoundary verifies the [0, rowCount) range
// guard: an index equal to rowCount is rejected (out of range) while the last
// in-range index (rowCount-1) is appended.
func TestAppendRowIfMissing_rowCountBoundary(t *testing.T) {
	// y == rowCount is out of [0, rowCount): must NOT be appended.
	if got := appendRowIfMissing(nil, 5, 5); len(got) != 0 {
		t.Errorf("appendRowIfMissing(nil, 5, 5) = %v, want empty (y==rowCount is out of range)", got)
	}
	// y == rowCount-1 is in range: must be appended.
	got := appendRowIfMissing(nil, 4, 5)
	if len(got) != 1 || got[0] != 4 {
		t.Errorf("appendRowIfMissing(nil, 4, 5) = %v, want [4]", got)
	}
}
