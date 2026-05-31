package terminal

import "github.com/cplieger/vterm/vt"

// scrollbackRing is a fixed-capacity ring buffer of scrollback lines.
// Lines are appended as they drain from the VT screen; oldest lines
// are evicted when capacity is reached. On new client connect or page
// refresh, the entire buffer is replayed so the client sees history
// that predates its connection.
type scrollbackRing struct {
	buf   [][]vt.WireRun
	start int // index of the oldest line in buf
	count int // number of valid lines (≤ cap)
}

func newScrollbackRing(capacity int) *scrollbackRing {
	return &scrollbackRing{buf: make([][]vt.WireRun, capacity)}
}

// Append adds lines to the ring, evicting oldest if at capacity.
func (r *scrollbackRing) Append(lines [][]vt.WireRun) {
	n := len(r.buf)
	for _, line := range lines {
		idx := (r.start + r.count) % n
		r.buf[idx] = line
		if r.count < n {
			r.count++
		} else {
			r.start = (r.start + 1) % n
		}
	}
}

// Lines returns all buffered lines in order (oldest first).
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



// Clear discards all buffered lines.
func (r *scrollbackRing) Clear() {
	r.start = 0
	r.count = 0
}

// Len returns the number of lines currently buffered.
func (r *scrollbackRing) Len() int {
	return r.count
}
