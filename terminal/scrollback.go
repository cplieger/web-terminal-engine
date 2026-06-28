package terminal

import "github.com/cplieger/vterm/vt"

// scrollbackRing is a fixed-capacity ring buffer of scrollback lines,
// addressed by absolute line index.
//
// Every line that scrolls off the top of the VT screen is appended here
// and assigned a monotonic absolute index: the first line ever committed
// is index 0, the next is 1, and so on, growing without bound for the
// life of the session. The ring retains only the most recent `cap` lines;
// older lines are evicted, but their indices are never reused. The
// absolute index of the current top screen row equals Committed().
//
// Absolute indexing is the backbone of the rebuild (see docs/REBUILD.md
// section 6.1). It makes resume alignment exact (the client asks for
// "everything after index H") and makes duplicate delivery structurally
// impossible (writing the same index twice overwrites the same row),
// replacing the old count-based scheme whose two independently-capped
// buffers drifted into overlaps and gaps.
type scrollbackRing struct {
	buf       [][]vt.WireRun
	start     int    // ring index of the oldest retained line
	count     int    // number of retained lines (<= len(buf))
	committed uint64 // total lines ever appended = absolute index of the next line
}

func newScrollbackRing(capacity int) *scrollbackRing {
	return &scrollbackRing{buf: make([][]vt.WireRun, capacity)}
}

// Append adds lines to the ring in order, assigning each the next
// absolute index and evicting the oldest retained line when at capacity.
// committed advances by len(lines) regardless of capacity, so absolute
// indices stay monotonic even after eviction.
func (r *scrollbackRing) Append(lines [][]vt.WireRun) {
	n := len(r.buf)
	if n == 0 {
		// Scrollback disabled: still advance committed so the screen
		// window's absolute base stays correct. Lines are unrecoverable
		// on resume, which is the documented behavior of capacity 0.
		r.committed += uint64(len(lines))
		return
	}
	for _, line := range lines {
		idx := (r.start + r.count) % n
		r.buf[idx] = line
		if r.count < n {
			r.count++
		} else {
			r.start = (r.start + 1) % n
		}
		r.committed++
	}
}

// Committed returns the total number of lines ever committed to history,
// which equals the absolute index of the current top screen row (the
// next line to be appended).
func (r *scrollbackRing) Committed() uint64 {
	return r.committed
}

// OldestIndex returns the absolute index of the oldest line still
// retained in the ring. Lines below this index have been evicted and
// cannot be replayed; a resuming client that needs them is shown a
// history-trimmed marker rather than a misaligned stitch.
func (r *scrollbackRing) OldestIndex() uint64 {
	return r.committed - uint64(r.count) // #nosec G115 -- count is non-negative and bounded by len(buf)
}

// LinesFrom returns the retained lines with absolute index >= abs, in
// order, along with the absolute index of the first returned line.
// When abs is older than what the ring retains, it clamps up to
// OldestIndex (the caller compares the returned firstAbs against the
// requested abs to detect an eviction gap). When abs is at or beyond
// Committed, it returns no lines.
func (r *scrollbackRing) LinesFrom(abs uint64) (firstAbs uint64, lines [][]vt.WireRun) {
	if r.count == 0 || abs >= r.committed {
		return r.committed, nil
	}
	oldest := r.OldestIndex()
	start := max(abs, oldest)
	skip := int(start - oldest) // #nosec G115 -- bounded by count
	out := make([][]vt.WireRun, 0, r.count-skip)
	n := len(r.buf)
	for i := skip; i < r.count; i++ {
		out = append(out, r.buf[(r.start+i)%n])
	}
	return start, out
}

// Lines returns all retained lines in order (oldest first). Retained for
// tests and debug paths; the live and resume paths use LinesFrom.
func (r *scrollbackRing) Lines() [][]vt.WireRun {
	if r.count == 0 {
		return nil
	}
	out := make([][]vt.WireRun, r.count)
	n := len(r.buf)
	for i := range r.count {
		out[i] = r.buf[(r.start+i)%n]
	}
	return out
}

// Clear discards all retained lines. committed is preserved so absolute
// indices never repeat within a session.
func (r *scrollbackRing) Clear() {
	r.start = 0
	r.count = 0
}

// Len returns the number of lines currently retained.
func (r *scrollbackRing) Len() int {
	return r.count
}
