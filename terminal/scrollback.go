package terminal

import "github.com/cplieger/web-terminal-engine/v6/vt"

// scrollbackRing is a capacity-bounded ring buffer of scrollback lines,
// addressed by absolute line index.
//
// Each line scrolled off the screen gets the next absolute index, never reused
// after eviction; the top screen row's index is Committed(). The buffer grows
// to capacity instead of preallocating, since an operator may set capacity
// absurdly high.
type scrollbackRing struct {
	buf [][]vt.WireRun
	// wrap[i] mirrors buf[i]: the line soft-wraps from the line before it.
	wrap      []bool
	capacity  int    // retained-line ceiling; buf grows up to this
	start     int    // ring index of the oldest retained line
	count     int    // number of retained lines (<= len(buf))
	committed uint64 // total lines ever appended = absolute index of the next line
}

func newScrollbackRing(capacity int) *scrollbackRing {
	return &scrollbackRing{capacity: max(capacity, 0)}
}

// Append adds lines to the ring in order, assigning each the next
// absolute index and evicting the oldest retained line when at capacity.
// committed advances by len(lines) regardless of capacity, so absolute
// indices stay monotonic even after eviction. wrapped[i] marks lines[i] as a
// soft-wrap continuation of the line before it; a missing entry is false.
func (r *scrollbackRing) Append(lines [][]vt.WireRun, wrapped []bool) {
	if r.capacity == 0 {
		// Scrollback disabled: still advance committed so the screen
		// window's absolute base stays correct. Lines are unrecoverable
		// on resume, which is the documented behavior of capacity 0.
		r.committed += uint64(len(lines))
		return
	}
	for i, line := range lines {
		w := i < len(wrapped) && wrapped[i]
		if len(r.buf) < r.capacity {
			// Growing. start is 0 and count == len(buf) in this phase, so the
			// append position IS the ring index and no modulo is needed.
			r.buf = append(r.buf, line)
			r.wrap = append(r.wrap, w)
			r.count++
		} else {
			// At capacity: the slot holding the oldest line is also the next
			// slot to write, so one store plus one start advance is the whole
			// eviction. count stays at capacity.
			r.buf[r.start] = line
			r.wrap[r.start] = w
			r.start = (r.start + 1) % r.capacity
		}
		r.committed++
	}
}

// at returns the retained line at position i (0 = oldest) and its wrap flag,
// without copying: the caller reads it under the lock that guards the ring.
func (r *scrollbackRing) at(i int) (line []vt.WireRun, wrapped bool) {
	j := (r.start + i) % len(r.buf)
	return r.buf[j], r.wrap[j]
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

// copyLine returns a fresh copy of one retained line, so a caller mutating it
// cannot rewrite what later replays see.
func copyLine(line []vt.WireRun) []vt.WireRun {
	if line == nil {
		return nil
	}
	out := make([]vt.WireRun, len(line))
	copy(out, line)
	return out
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
		out = append(out, copyLine(r.buf[(r.start+i)%n]))
	}
	return start, out
}

// LinesRange is LinesFrom bounded to maxLines, so a page request copies a page
// rather than the ring. maxLines <= 0 returns no lines.
func (r *scrollbackRing) LinesRange(abs uint64, maxLines int) (firstAbs uint64, lines [][]vt.WireRun) {
	if r.count == 0 || abs >= r.committed || maxLines <= 0 {
		return r.committed, nil
	}
	oldest := r.OldestIndex()
	start := max(abs, oldest)
	skip := int(start - oldest) // #nosec G115 -- bounded by count
	avail := r.count - skip
	take := min(avail, maxLines)
	out := make([][]vt.WireRun, 0, take)
	n := len(r.buf)
	for i := skip; i < skip+take; i++ {
		out = append(out, copyLine(r.buf[(r.start+i)%n]))
	}
	return start, out
}

// Lines returns all retained lines in order (oldest first). Retained for
// tests; the live and resume paths use LinesFrom.
func (r *scrollbackRing) Lines() [][]vt.WireRun {
	if r.count == 0 {
		return nil
	}
	out := make([][]vt.WireRun, r.count)
	n := len(r.buf)
	for i := range r.count {
		out[i] = copyLine(r.buf[(r.start+i)%n])
	}
	return out
}

// Clear discards all retained lines. committed is preserved so absolute
// indices never repeat within a session.
//
// The array is dropped, not resliced or kept: a kept one makes Append's growth
// branch write past index 0 while readers start at 0, and dropping it frees
// the retained rows.
func (r *scrollbackRing) Clear() {
	r.buf = nil
	r.wrap = nil
	r.start = 0
	r.count = 0
}

// Len returns the number of lines currently retained.
func (r *scrollbackRing) Len() int {
	return r.count
}
