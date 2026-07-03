package terminal

// The status stream (Server-Sent Events at /api/sessions/events) drives each
// tab's activity indicator. A single sweep recomputes every session's status on
// a fixed interval and pushes only changes to subscribers, which debounces the
// working/idle flap for free. Status derives from process liveness, OSC 9;4
// progress, and output activity (working/idle/exited); a consumer's classifier
// maps an OSC 9 notification to a latched needs-input or done state (Tier 2).
// One stream serves all tabs (not one per tab) to stay under the browser's
// per-origin connection cap.

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
	// This is the fallback only for a program that does not report OSC 9;4
	// progress; a progress-reporting program (kiro-cli) drives working from
	// progress instead, so the user typing at the prompt does not read as work.
	workingWindow = 750 * time.Millisecond
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
// notification sequence classified, and the latched needs-input/done state.
type statusTracker struct {
	lastStatus string
	lastTitle  string
	latched    string // "", StatusInput, or StatusDone
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
// handler and mutates the tracker's latch state). Precedence: exited, then
// working (OSC 9;4 progress active), then a latched notification state
// (needs-input or done), then the output-activity fallback for a program that
// reports no progress, then idle (the default / new-session state).
func (m *SessionManager) computeStatus(h *Handler, tr *statusTracker, now time.Time) string {
	if h.Exited() {
		return StatusExited
	}
	m.applyNotification(h, tr)
	// A progress-reporting program (kiro-cli) drives working from its OSC 9;4
	// progress: an active state (1 value, 3 indeterminate) means the agent is
	// working. Progress is emitted only while the agent runs, so it does not
	// flare on the user typing at the prompt the way raw output activity would.
	// A new working phase supersedes a prior done/needs-input latch.
	prog := h.Progress()
	if prog == 1 || prog == 3 {
		tr.latched = ""
		return StatusWorking
	}
	// A latched notification state (needs-input or done) persists through the
	// quiet gap after the turn until the next working phase clears it.
	if tr.latched != "" {
		return tr.latched
	}
	// Fallback for a program that never reports progress (a plain shell under
	// web-terminal-server, prog == -1): recent output means working, else idle.
	// A progress-reporting program at rest (prog >= 0) with no latch stays idle,
	// the default / new-session state.
	if prog < 0 {
		if last := h.LastActivity(); !last.IsZero() && now.Sub(last) < workingWindow {
			return StatusWorking
		}
	}
	return StatusIdle
}

// applyNotification updates the tracker's latch from a new OSC 9 notification
// via the consumer's classifier, if any. The classified state (StatusInput or
// StatusDone) is latched; it persists until the next working phase clears it
// (see computeStatus). An unclassified message leaves the latch unchanged.
func (m *SessionManager) applyNotification(h *Handler, tr *statusTracker) {
	if m.classifier == nil {
		return
	}
	msg, seq := h.Notification()
	if seq <= tr.notifSeen {
		return
	}
	tr.notifSeen = seq
	if cls, ok := m.classifier(msg); ok {
		tr.latched = cls
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
		// Flush through a ResponseController so the stream works behind
		// middleware that wraps the ResponseWriter (an access log, security
		// headers): unlike a direct w.(http.Flusher) assertion, it follows the
		// Unwrap chain. Probe support up front with the same chain walk so we can
		// 500 before committing a status — a probe Flush() before the headers are
		// written would implicitly send a 200 and drop the SSE headers below.
		if !supportsFlush(w) {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		rc := http.NewResponseController(w)
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
		_ = rc.Flush()
		streamEvents(r.Context(), w, rc, ch)
	})
}

// supportsFlush reports whether w, or any ResponseWriter it unwraps to, supports
// flushing. It walks the Unwrap chain the way http.ResponseController does, so
// the SSE stream works behind a ResponseWriter-wrapping middleware whose wrapper
// implements Unwrap() (e.g. an access-log recorder). It is an upfront probe
// because a real Flush() before the headers are written would implicitly commit
// a 200 and drop the event-stream headers.
func supportsFlush(w http.ResponseWriter) bool {
	for {
		if _, ok := w.(http.Flusher); ok {
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
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
func streamEvents(ctx context.Context, w http.ResponseWriter, rc *http.ResponseController, ch <-chan statusEvent) {
	keep := time.NewTicker(sseKeepAlive)
	defer keep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keep.C:
			if !writeKeepAlive(w, rc) {
				return
			}
		case ev, ok := <-ch:
			if !ok || !writeSSEFlush(w, rc, &ev) {
				return
			}
		}
	}
}

// writeKeepAlive emits an SSE keepalive comment and flushes, returning false if
// the client connection is gone (so the stream loop exits).
func writeKeepAlive(w http.ResponseWriter, rc *http.ResponseController) bool {
	if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// writeSSEFlush writes one event frame and flushes, returning false if the
// client connection is gone.
func writeSSEFlush(w http.ResponseWriter, rc *http.ResponseController, ev *statusEvent) bool {
	if !writeSSE(w, ev) {
		return false
	}
	return rc.Flush() == nil
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
