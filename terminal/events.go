package terminal

// The status stream (Server-Sent Events at /api/sessions/events) drives each
// tab's activity indicator. A single sweep recomputes every session's status on
// a fixed interval and pushes only changes to subscribers, which debounces the
// working/idle flap for free. Status derives from process liveness and output
// activity (Tier 1: working/idle/exited); a consumer's classifier maps an OSC 9
// notification to a latched needs-input state (Tier 2). One stream serves all
// tabs (not one per tab) to stay under the browser's per-origin connection cap.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultMaxSubscribers = 32
	// statusSweepInterval is how often per-session status is recomputed.
	statusSweepInterval = 250 * time.Millisecond
	// workingWindow marks a session working when it produced output within it.
	workingWindow = 750 * time.Millisecond
	// inputResumeGuard: an input latch clears only once output resumes at least
	// this long after the notification, so the prompt's own output (which
	// precedes the notification) does not immediately clear the latch.
	inputResumeGuard = 400 * time.Millisecond
	// subscriberBuffer bounds a subscriber's pending events; a subscriber that
	// falls this far behind is dropped rather than blocking the sweep.
	subscriberBuffer = 64
	// sseKeepAlive is the idle interval between SSE keepalive comments, so
	// proxies do not close a quiet stream.
	sseKeepAlive = 15 * time.Second
)

// statusEvent is one status-stream message: a session's current status and
// title. Removed=true signals the session is gone (closed or reaped) so the
// client drops the tab.
type statusEvent struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Title     string    `json:"title"`
	Removed   bool      `json:"removed,omitempty"`
}

// statusTracker holds the per-session state the status computation needs beyond
// the handler: the last emitted status/title (to detect changes), the last
// notification sequence classified, and the latched needs-input state.
type statusTracker struct {
	latchAt    time.Time
	lastStatus string
	lastTitle  string
	latched    string // "" or StatusInput
	notifSeen  uint64
}

func (m *SessionManager) sweepLoop(ctx context.Context) {
	t := time.NewTicker(statusSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, ev := range m.diffStatuses() {
				m.broadcast(&ev)
			}
		}
	}
}

// diffStatuses recomputes every session's status under the manager lock and
// returns the events for sessions whose status or title changed since the last
// sweep, plus removed events for sessions that vanished. Broadcasting happens
// outside the lock (see sweepLoop).
func (m *SessionManager) diffStatuses() []statusEvent {
	now := time.Now()
	var events []statusEvent
	m.mu.Lock()
	seen := make(map[string]struct{}, len(m.sessions))
	for id, s := range m.sessions {
		seen[id] = struct{}{}
		tr := m.trackers[id]
		if tr == nil {
			tr = &statusTracker{}
			m.trackers[id] = tr
		}
		status := m.computeStatus(s.handler, tr, now)
		title := s.handler.Title()
		if status != tr.lastStatus || title != tr.lastTitle {
			tr.lastStatus = status
			tr.lastTitle = title
			events = append(events, statusEvent{ID: id, Status: status, Title: title, CreatedAt: s.createdAt})
		}
	}
	for id, tr := range m.trackers {
		if _, ok := seen[id]; !ok {
			delete(m.trackers, id)
			events = append(events, statusEvent{ID: id, Status: StatusExited, Title: tr.lastTitle, Removed: true})
		}
	}
	m.mu.Unlock()
	return events
}

// computeStatus derives a session's status. Callers hold m.mu (it reads the
// handler and mutates the tracker's latch state). exited wins; otherwise output
// activity gives working/idle, and a classified OSC 9 notification latches
// input until the turn resumes (output after the prompt) or a done message
// clears it.
func (m *SessionManager) computeStatus(h *Handler, tr *statusTracker, now time.Time) string {
	if h.Exited() {
		return StatusExited
	}
	m.applyNotification(h, tr, now)
	last := h.LastActivity()
	base := StatusIdle
	if !last.IsZero() && now.Sub(last) < workingWindow {
		base = StatusWorking
	}
	if tr.latched == StatusInput {
		// The turn has resumed once output arrives after the prompt (the prompt
		// itself precedes the notification, so guard against clearing on it).
		if last.After(tr.latchAt.Add(inputResumeGuard)) {
			tr.latched = ""
		} else {
			return StatusInput
		}
	}
	return base
}

// applyNotification updates the tracker's input latch from a new OSC 9
// notification via the consumer's classifier, if any. A classified input
// latches; any other classified message clears the latch.
func (m *SessionManager) applyNotification(h *Handler, tr *statusTracker, now time.Time) {
	if m.classifier == nil {
		return
	}
	msg, seq := h.Notification()
	if seq <= tr.notifSeen {
		return
	}
	tr.notifSeen = seq
	cls, ok := m.classifier(msg)
	if !ok {
		return
	}
	if cls == StatusInput {
		tr.latched = StatusInput
		tr.latchAt = now
	} else {
		tr.latched = ""
	}
}

func (m *SessionManager) broadcast(ev *statusEvent) {
	m.subsMu.Lock()
	for ch := range m.subs {
		select {
		case ch <- *ev:
		default:
			// Subscriber is too far behind; drop it. The hub owns closing the
			// channel; the handler goroutine sees !ok and unsubscribes (a no-op
			// once already removed here).
			delete(m.subs, ch)
			close(ch)
		}
	}
	m.subsMu.Unlock()
}

func (m *SessionManager) subscribe() (chan statusEvent, bool) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	if len(m.subs) >= m.maxSubs {
		return nil, false
	}
	ch := make(chan statusEvent, subscriberBuffer)
	m.subs[ch] = struct{}{}
	return ch, true
}

func (m *SessionManager) unsubscribe(ch chan statusEvent) {
	m.subsMu.Lock()
	if _, ok := m.subs[ch]; ok {
		delete(m.subs, ch)
		close(ch)
	}
	m.subsMu.Unlock()
}

// snapshot returns the current status of every session for the initial sync a
// new subscriber receives, using the tracker's computed status when available
// (else the coarse liveness status).
func (m *SessionManager) snapshot() []statusEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]statusEvent, 0, len(m.sessions))
	for id, s := range m.sessions {
		status := statusOf(s.handler)
		if tr := m.trackers[id]; tr != nil && tr.lastStatus != "" {
			status = tr.lastStatus
		}
		out = append(out, statusEvent{ID: id, Status: status, Title: s.handler.Title(), CreatedAt: s.createdAt})
	}
	return out
}

// EventsHandler serves the status stream at /api/sessions/events (SSE). A
// subscriber is counted as a present client (suppressing the idle reaper) and
// first receives an initial sync of every session's current status, then a
// stream of changes. A subscriber that falls behind its bounded buffer is
// dropped; the stream is capped by WithMaxSubscribers.
func (m *SessionManager) EventsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		// Subscribe before the snapshot so a change during the snapshot is
		// queued (delivered after it) rather than missed.
		ch, ok := m.subscribe()
		if !ok {
			http.Error(w, "too many subscribers", http.StatusServiceUnavailable)
			return
		}
		defer m.unsubscribe(ch)
		m.clientConnected()
		defer m.clientDisconnected()

		writeSSEHeaders(w)
		for _, ev := range m.snapshot() {
			if !writeSSE(w, &ev) {
				return
			}
		}
		flusher.Flush()
		streamEvents(r.Context(), w, flusher, ch)
	})
}

// writeSSEHeaders sets the SSE response headers and the 200 status.
func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // ask proxies not to buffer the stream
	w.WriteHeader(http.StatusOK)
}

// streamEvents forwards status events and periodic keepalives to one subscriber
// until the client disconnects (ctx done) or the subscriber is dropped (channel
// closed by the hub for falling behind).
func streamEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, ch <-chan statusEvent) {
	keep := time.NewTicker(sseKeepAlive)
	defer keep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !writeSSE(w, &ev) {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE writes one event as an SSE data frame. Returns false if the client
// connection is gone (write failed). A malformed event is skipped, not fatal.
func writeSSE(w http.ResponseWriter, ev *statusEvent) bool {
	b, err := json.Marshal(ev)
	if err != nil {
		return true
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err == nil
}
