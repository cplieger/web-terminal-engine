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
	// maxSubscribers is the fixed ceiling on concurrent status-stream (SSE)
	// subscribers; it bounds subscriber goroutines/buffers and stops runaway
	// connections. A few devices per deployment is the expected load.
	maxSubscribers = 10
	// statusSweepInterval is how often per-session status is recomputed.
	statusSweepInterval = 250 * time.Millisecond
	// subscriberBuffer bounds a subscriber's pending events; a subscriber that
	// falls this far behind is dropped rather than blocking the sweep.
	subscriberBuffer = 64
	// sseKeepAlive is the idle interval between SSE keepalive comments, so
	// proxies do not close a quiet stream.
	sseKeepAlive = 15 * time.Second
	// sseWriteTimeout bounds each SSE write so a wedged subscriber (client
	// socket dead but not yet FIN'd) is detected in seconds instead of waiting
	// for the OS TCP timeout. Mirrors the WS per-client write deadline in
	// dispatchFrame. 10s is far above a healthy client's sub-ms flush of a
	// small SSE frame and below the 15s keepalive, so a dead client is caught
	// before the next keepalive fires; a healthy-but-slow client is unaffected.
	sseWriteTimeout = 10 * time.Second
)

// statusEvent is one status-stream message: a session's current status and
// title. Removed=true signals the session is gone (closed or reaped) so the
// client drops the tab.
type statusEvent struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Title     string    `json:"title"` // OSC-first effective title (effectiveTitle)
	// ClientTitle is the raw stored client title, carried alongside Title so a
	// consumer can read the pushed label directly (bypassing the OSC-first
	// fallback in Title). Not carried on a Removed event.
	ClientTitle     string `json:"clientTitle"`
	Removed         bool   `json:"removed,omitempty"`
	ReportsActivity bool   `json:"reportsActivity"`
}

// statusTracker holds the per-session state the status computation needs beyond
// the handler: the last emitted status/title (to detect changes), the last
// notification sequence classified, and the latched needs-input/done state.
type statusTracker struct {
	lastStatus      string
	lastTitle       string
	lastClientTitle string // last emitted raw client title (to detect a title-only PUT)
	latched         string // "", StatusInput, or StatusDone
	notifSeen       uint64
	lastReports     bool // last emitted reportsActivity (to detect a false->true flip)
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
// returns the events for sessions whose status, effective title, raw client
// title, or reported activity changed since the last sweep, plus removed events
// for sessions that vanished. Broadcasting happens outside the lock (see
// sweepLoop).
func (m *SessionManager) diffStatuses() []statusEvent {
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
		status := m.computeStatus(s.handler, tr)
		title := effectiveTitle(s)
		clientTitle := s.clientTitle
		// reportsActivity is sticky: Progress() stays >= 0 once any OSC 9;4 has
		// been seen (state 0 is "cleared", not "never seen" = -1), and a latched
		// notification is the other genuine OSC 9 signal. The client reveals the
		// tab's activity dot only when this is set.
		reports := s.handler.Progress() >= 0 || tr.latched != ""
		// Emit on a raw client-title change too: a PUT /title can change only the
		// client title (OSC title and status unchanged), and a consumer reading
		// clientTitle directly needs that pushed even when the effective title is
		// unmoved (an OSC title is masking the fallback).
		if status != tr.lastStatus || title != tr.lastTitle || clientTitle != tr.lastClientTitle || reports != tr.lastReports {
			tr.lastStatus = status
			tr.lastTitle = title
			tr.lastClientTitle = clientTitle
			tr.lastReports = reports
			events = append(events, statusEvent{ID: id, Status: status, Title: title, ClientTitle: clientTitle, CreatedAt: s.createdAt, ReportsActivity: reports})
		}
	}
	for id, tr := range m.trackers {
		if _, ok := seen[id]; !ok {
			delete(m.trackers, id)
			events = append(events, statusEvent{ID: id, Status: StatusExited, Title: tr.lastTitle, Removed: true, ReportsActivity: tr.lastReports})
		}
	}
	m.mu.Unlock()
	return events
}

// computeStatus derives a session's status. Callers hold m.mu (it reads the
// handler and mutates the tracker's latch state). Precedence: exited, then
// working (OSC 9;4 progress active), then a latched notification state
// (needs-input or done), then idle (the default / new-session / at-rest state).
// Working comes ONLY from OSC 9;4 progress — never from raw output activity — so
// a program that reports no progress never flaps to working merely because the
// user is typing at its prompt (the reveal gate then keeps its dot hidden).
func (m *SessionManager) computeStatus(h *Handler, tr *statusTracker) string {
	if h.Exited() {
		return StatusExited
	}
	m.applyNotification(h, tr)
	// A progress-reporting program (kiro-cli, Claude Code) drives working from
	// its OSC 9;4 progress: an active state (1 value, 3 indeterminate) means the
	// agent is working. A new working phase supersedes a prior done/needs-input
	// latch.
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
			m.logger.Warn("terminal: status subscriber dropped (buffer full)", "buffer", subscriberBuffer)
			delete(m.subs, ch)
			close(ch)
		}
	}
	m.subsMu.Unlock()
}

func (m *SessionManager) subscribe() (chan statusEvent, bool) {
	// The subscriber cap is a fixed const (maxSubscribers): a small ceiling that
	// bounds runaway subscriber goroutines/buffers while leaving safe headroom
	// for several devices per deployment. The count and the compared ceiling are
	// both known under subsMu (the lock guarding the subscriber map), so no other
	// lock is involved.
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	if len(m.subs) >= maxSubscribers {
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
		tr := m.trackers[id]
		if tr != nil && tr.lastStatus != "" {
			status = tr.lastStatus
		}
		reports := s.handler.Progress() >= 0 || (tr != nil && tr.latched != "")
		out = append(out, statusEvent{ID: id, Status: status, Title: effectiveTitle(s), ClientTitle: s.clientTitle, CreatedAt: s.createdAt, ReportsActivity: reports})
	}
	return out
}

// EventsHandler serves the status stream at SessionEventsPath
// (/api/sessions/events, SSE). A subscriber is counted as a present client
// (suppressing the idle reaper) and first receives an initial sync of every
// session's current status, then a stream of changes. A subscriber that falls
// behind its bounded buffer is dropped; the number of concurrent subscribers
// is bounded by the fixed maxSubscribers cap. Mounted for you by
// MountSessionRoutes / MountAPI; exported so consumer tests can stub it.
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
			m.logger.Warn("terminal: status subscriber rejected (at cap)", "max_subscribers", maxSubscribers)
			http.Error(w, "too many subscribers", http.StatusServiceUnavailable)
			return
		}
		defer m.unsubscribe(ch)
		m.clientConnected()
		defer m.clientDisconnected()

		writeSSEHeaders(w)
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)) // bound the snapshot burst too
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
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)) // unsupported writer degrades to no deadline (prior behavior)
	if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// writeSSEFlush writes one event frame and flushes, returning false if the
// client connection is gone.
func writeSSEFlush(w http.ResponseWriter, rc *http.ResponseController, ev *statusEvent) bool {
	_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)) // unsupported writer degrades to no deadline (prior behavior)
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
