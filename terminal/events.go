package terminal

// The status stream (Server-Sent Events at /api/sessions/events) drives each
// tab's activity indicator. One sweep recomputes every session's status on a
// fixed interval and pushes only changes, which debounces the working/idle
// flap. Status derives from process liveness and the program's own reports,
// OSC 7501 program status or else OSC 9;4 progress; a consumer's classifier
// maps an OSC 9 notification to a latched status of its choosing. One
// stream serves all tabs to stay under the browser's per-origin connection cap.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/cplieger/web-terminal-engine/v6/vt"
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
	// progressAbsent is the OSC 9;4 marker for "no percentage reported" —
	// vt.Screen.ProgressValue's absent value, mirrored here so the event path can
	// name it. Distinct from 0, which is a real 0%.
	progressAbsent = -1
)

// statusEvent is one status-stream message: a session's current status and
// title. Removed=true signals the session is gone (closed or reaped) so the
// client drops the tab.
type statusEvent struct {
	// Order is the session's position in the shared display order (see
	// SessionInfo.Order). Carried on every status event for a LIVE session, so a
	// reorder made by another client reaches this one, which is the read side of
	// tab-order sync.
	//
	// A pointer, and omitted on a Removed event, because the session has left the
	// order and has no position to report. A plain int could only say 0 there, and
	// 0 is a real position at the FRONT of the strip: a consumer that reads fields
	// before it checks Removed would be told the closing session just became the
	// first tab. Present-and-zero and absent are different answers, so the wire
	// has to be able to express both.
	//
	// This is the RAW rank, not a renumbered index. The two enumerations (List and
	// snapshot) restate each position as its index in the sequence they serve,
	// because a client builds a whole strip from them; a change set has no sequence
	// to index into, so it cannot. The consequence is the window a client must
	// respect: one reorder arrives as one event per moved session, so until the
	// whole tick is applied two sessions can hold the same position. Apply the
	// tick, then sort, and never derive an order you write back from a partly
	// applied view (see SetSessionOrder).
	//
	// First in the struct so the GC's pointer-scan range does not have to reach
	// past the scalar tail to find it (govet fieldalignment).
	Order     *int      `json:"order,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ID        SessionID `json:"id"` // marshals as a plain string; the wire shape is unchanged
	Status    string    `json:"status"`
	Title     string    `json:"title"` // resolved display title (effectiveTitle)
	// ClientTitle is the raw stored client-derived title, carried alongside Title
	// so a consumer can read the pushed label directly (bypassing the precedence
	// in Title). Not carried on a Removed event.
	ClientTitle string `json:"clientTitle"`
	// PinnedTitle is the raw user-set name, carried for the same reason and so a
	// second client learns of a rename (or its removal) made elsewhere. Not
	// carried on a Removed event.
	PinnedTitle string `json:"pinnedTitle"`
	// Alias is the session's public name (see SessionInfo.Alias). Omitted only
	// on a Removed event.
	Alias string `json:"alias,omitempty"`
	// Activity is the session's SECONDARY activity state (see SessionActivity).
	// Never omitempty: "" is not a legal state, so the empty string IS the absence
	// and a sentinel would encode the same fact twice. It is ORTHOGONAL to Status
	// — never in computeStatus's precedence, never in tracker.latched, never an
	// input to ReportsActivity, which stays the PRIMARY dot's reveal gate. A change
	// in this value alone emits an event, like ProgressValue below.
	Activity string `json:"activity"`
	// Notification is the OSC 9 notification message captured for this session,
	// carried on the event so a consumer with NO status classifier still RECEIVES
	// it: "no classifier" means "I map notifications myself", not "discard them".
	// A notification is an EVENT, not a state — the engine latches nothing for it,
	// and it is carried only on the sweep that first observes it, never replayed.
	// NotificationSeq is vt.Screen.NotificationSeq, so a consumer detects a fresh
	// notification even when the text repeats.
	//
	// The text is untrusted program output. It is already sanitised by the vt
	// layer (control, bidi and line-terminator runes dropped, length clamped —
	// see vt.sanitizeNotification) and it travels to a client that already
	// receives the session's full terminal stream over the WebSocket, so carrying
	// it here is not a new exposure. A consumer still escapes it for whatever
	// surface it renders it on, exactly as it does the terminal stream.
	Notification    string `json:"notification,omitempty"`
	NotificationSeq uint64 `json:"notificationSeq,omitempty"`
	// StatusSeq numbers the Status beside it (see SessionInfo.StatusSeq) and
	// SeenSeq is the session's seen floor (see SessionInfo.SeenSeq). Not carried
	// on a Removed event.
	StatusSeq uint64 `json:"statusSeq,omitempty"`
	SeenSeq   uint64 `json:"seenSeq,omitempty"`
	// ProgressValue is the session's percentage: the OSC 7501 report's progress
	// when it carries one, else the OSC 9;4 percentage (Handler.ProgressValue);
	// -1 when absent or unknown, else 0-100. Always carried — a session
	// that has reported no percentage is not a session at 0%, so the field must
	// not be omitempty. A change in this value alone emits an event, or a
	// consumer's determinate bar would stay at the first value it ever saw.
	ProgressValue int `json:"progressValue"`
	// ActivityCount is how many sources produced Activity: >= 1 whenever Activity
	// is non-empty, 0 when it is empty or the host does not count.
	ActivityCount   int  `json:"activityCount"`
	Removed         bool `json:"removed,omitempty"`
	ReportsActivity bool `json:"reportsActivity"`
}

// statusTracker holds the per-session state the status computation needs beyond
// the handler: the last emitted status/title/OSC title/progress percentage (to
// detect changes, and the committed record List and snapshot serve), the last
// notification sequence classified and the last one delivered, the latched
// classifier status, the status occurrence number and seen floor, and the
// automatic-title confirmation window.
type statusTracker struct {
	candidateSince  time.Time // when candidatePGID was first observed
	lastStatus      string
	lastTitle       string
	lastClientTitle string // last emitted raw client title (to detect a title-only PUT)
	lastPinnedTitle string // last emitted raw pinned name (to detect a rename / clear)
	lastAlias       string // last emitted alias (a SetSessionAlias moves nothing else)
	latched         string // "" or the status the classifier returned
	// lastActivity is the last emitted secondary activity state. Without it the
	// field changes silently and only surfaces when something else moves.
	lastActivity string
	// lastOSCTitle is the OSC rung behind lastTitle, which point-in-time reads
	// compose with the stored titles.
	lastOSCTitle string
	notifSeen    uint64
	latchSeq     uint64
	// programOccurrence is the ProgramStatus.Seq the last sweep read, so a
	// rise reads as a fresh occurrence.
	programOccurrence uint64
	// statusSeq numbers each status occurrence (see sweepSession) and seenSeq is
	// the highest one a reader acknowledged (markSessionSeen); lastSeenSeq is the
	// seenSeq last emitted, so an ack reaches every subscriber.
	statusSeq   uint64
	seenSeq     uint64
	lastSeenSeq uint64
	// notifDelivered is the last notification sequence CARRIED on an event.
	// Separate from notifSeen, which only advances when a classifier consumed the
	// message: delivery is unconditional, so the two would otherwise disagree for
	// a consumer with no classifier — the very consumer the delivery exists for.
	notifDelivered uint64
	candidatePGID  int // foreground pgid awaiting the confirmation window
	// lastProgressValue is the last emitted ProgressValue. Zero-valued at 0
	// rather than -1 for a brand-new tracker, which only means the session's
	// first sweep also counts the percentage as changed — that sweep emits
	// anyway, because the status itself moves off "".
	lastProgressValue int
	// lastOrder is the last emitted display position. Zero-valued at 0 for a
	// brand-new tracker, same as lastProgressValue: that only means a session
	// already at position 0 does not count its position as changed on its first
	// sweep, which emits anyway because the status itself moves off "".
	lastOrder int
	// lastActivityCount is lastActivity's count half; without it a 2 -> 3 change
	// under one state emits nothing.
	lastActivityCount int
	lastReports       bool // last emitted reportsActivity (to detect a false->true flip)
}

// numberOccurrence advances statusSeq when status is a new occurrence: a change
// from the last emitted status, or fresh (SessionInfo.StatusSeq owns when a
// sweep's status is fresh). A second done or input repeats the status string,
// so only fresh tells it apart from a repeated frame. Call it before
// lastStatus takes status.
func (tr *statusTracker) numberOccurrence(status string, fresh bool) {
	if status != tr.lastStatus || fresh {
		tr.statusSeq++
	}
}

// committedRecord is a session's handler-derived record as the last sweep
// emitted it, plus its seen floor. List and snapshot serve only this, so a
// point-in-time read never pairs a status with another status's number or
// carries a value no sweep emitted, which no later frame would correct.
type committedRecord struct {
	status        string
	activity      string
	oscTitle      string
	statusSeq     uint64
	seenSeq       uint64
	activityCount int
	progressValue int
	reports       bool
}

// committedOf reads tr's committed record, or the new-session defaults when no
// sweep has emitted one yet. Callers hold m.mu.
func committedOf(tr *statusTracker) committedRecord {
	if tr == nil || tr.lastStatus == "" {
		// Not tr.lastProgressValue: a fresh tracker's zero means 0%.
		return committedRecord{status: StatusIdle, progressValue: progressAbsent}
	}
	return committedRecord{
		status: tr.lastStatus, activity: tr.lastActivity, oscTitle: tr.lastOSCTitle,
		statusSeq: tr.statusSeq, seenSeq: tr.seenSeq, activityCount: tr.lastActivityCount,
		progressValue: tr.lastProgressValue, reports: tr.lastReports,
	}
}

// autoTitleConfirm is how long a foreground process must hold the terminal
// before its name is adopted as the session's automatic title. A command that
// lives 30ms (ls, git status) must never flash into a tab label.
//
// It is elapsed time, not a sample count: "the same pgid on two consecutive
// sweeps" would be only statusSweepInterval apart and therefore phase-sensitive,
// adopting a 260ms command or missing a 400ms one depending on where the ticks
// landed. Adoption therefore lands within one sweep past this window, whatever
// statusSweepInterval happens to be — do not restate the arithmetic in terms of a
// particular tick count, which rots the moment the interval changes.
//
// 500ms is chosen, not inherited. tmux's NAME_INTERVAL is also 500ms but is a
// different mechanism — a rate limit on how often it recomputes a window name,
// which is why tmux CAN briefly show a short-lived command. This is a
// confirmation window, which cannot.
const autoTitleConfirm = 500 * time.Millisecond

// confirmAutoTitle folds one sweep's probe into the session's confirmed
// server-derived title. The sweep is the ONLY writer of session.autoTitle, so
// List and snapshot read a single confirmed value instead of each probing procfs
// and disagreeing with the live stream. Called from diffStatuses phase 3, with
// m.mu held (it writes session and tracker state).
//
// The asymmetry is deliberate. Adopting a running process is debounced by
// autoTitleConfirm; RESTING (the process finished, so the shell is in the
// foreground again) is immediate, because a stale name is worse than a brief
// correct one. While a candidate is inside its confirmation window the previous
// title is HELD rather than reset to the cwd, so `vim` giving way to `less` does
// not detour through the directory name.
func (*SessionManager) confirmAutoTitle(s *session, in *statusRaw, tr *statusTracker) {
	p := in.autoProbe
	if !p.ok {
		return // no information this sweep (OSC-titled, exited, unsupported platform)
	}
	if p.procName != "" {
		if tr.candidatePGID != p.pgid {
			tr.candidatePGID = p.pgid
			tr.candidateSince = time.Now()
			return // window restarts; hold the current title
		}
		if time.Since(tr.candidateSince) >= autoTitleConfirm {
			s.autoTitle = p.procName
		}
		return // hold while the window runs
	}
	// Nothing is running: the shell owns the terminal again. Rest at the cwd
	// basename, or keep the seeded command basename when the cwd is unreadable.
	tr.candidatePGID = 0
	tr.candidateSince = time.Time{}
	if p.cwdBase != "" {
		s.autoTitle = p.cwdBase
	}
}

func (m *SessionManager) sweepLoop(ctx context.Context) {
	t := time.NewTicker(statusSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Indexed, not ranged: a statusEvent is well past the value-copy
			// threshold, and broadcast takes a pointer anyway.
			evs := m.diffStatuses()
			for i := range evs {
				m.broadcast(&evs[i])
			}
		}
	}
}

// statusRaw carries one session's handler-derived status inputs, read in
// diffStatuses's lock-free phase (each getter takes only that handler's h.mu).
type statusRaw struct {
	createdAt time.Time
	handler   *Handler
	tr        *statusTracker
	id        SessionID
	notifMsg  string
	oscTitle  string
	// programState is the folded OSC 7501 state; meaningful only when programSeen.
	programState string
	// activity is the consumer's secondary activity for this session, read in
	// PHASE 2 — the opposite of what the order field below says for itself, so the
	// difference is worth stating. order is MANAGER state phase 2 can invalidate;
	// this is FOREIGN state, and phase 3 would hold m.mu across a consumer
	// callback. Its staleness is bounded by one sweep and self-correcting, since
	// nothing the manager does can invalidate it behind the tracker's back.
	activity SessionActivity
	// autoProbe is the foreground-process probe result for this sweep: the
	// candidate pgid plus the name/cwd it resolves to. Read in phase 2 (procfs +
	// one ioctl, no locks held) and folded into the confirmation window in
	// phase 3. Zero when no probe ran this sweep: an OSC-titled session (the
	// automatic title is the LAST rung, so it must not pay for procfs reads) or an
	// exited one.
	//
	// Placed after the strings and before the scalars deliberately: its own
	// trailing non-pointer tail (pgid, ok) then falls outside the struct's
	// pointer-scan range (govet fieldalignment). Adding a field here without
	// keeping pointer-bearing types ahead of scalar ones re-trips that linter.
	autoProbe autoTitleProbe
	notifSeq  uint64
	// programSeq is vt.Screen.ProgramReportSeq, on notifSeq's counter.
	programSeq uint64
	// programOccurrence is vt.ProgramStatus.Seq of the folded status.
	programOccurrence uint64
	// order is the session's position in the shared display order. Unlike every
	// other field here it is filled in PHASE 3, not phase 1: see the comment at
	// that site. A reorder changes no handler state, so this is the only input that
	// makes the sweep emit for it.
	order    int
	progress int
	// progressValue is the percentage the event carries (statusEvent.ProgressValue),
	// from the SAME snapshot as progress.
	progressValue int
	exited        bool
	// crashed is meaningful only when exited: true when the exit was a failure
	// rather than an ordinary end (see crashedExit).
	crashed     bool
	programSeen bool
}

// read fills the handler-derived fields (diffStatuses phase 2). It takes only
// the handler's own locks — never the manager's. The screen-derived fields
// (progress, notification, title, OSC 7501 status) come from ONE lock
// acquisition (statusSnapshot), so a PTY chunk parsed mid-read cannot pair a
// stale active progress with a fresh turn-end notification (the torn snapshot
// that lost a done latch).
func (it *statusRaw) read() {
	sc := it.handler.statusSnapshot()
	// After the snapshot: the monitor drops an exited program's records only
	// after procExitCh closes, so they are never missing beside a live outcome.
	if hold := testStatusReadHold.Load(); hold != nil {
		(*hold)()
	}
	it.exited, it.crashed = it.handler.exitOutcome()
	it.progress = sc.progress
	it.progressValue = sc.progressValue
	// A program status carrying its own percentage draws it; otherwise the
	// OSC 9;4 bar keeps drawing beside whatever status 7501 reports.
	if sc.programSeen && sc.program.Progress >= 0 {
		it.progressValue = sc.program.Progress
	}
	it.programSeen = sc.programSeen
	it.programState = sc.program.State
	it.programSeq = sc.programSeq
	it.programOccurrence = sc.program.Seq
	it.notifMsg = sc.notifMsg
	it.notifSeq = sc.notifSeq
	it.oscTitle = sc.title
	// Skip the probe entirely when the program named itself: the automatic title
	// is the LAST rung, so an OSC-titled session must never pay for procfs reads.
	if it.oscTitle == "" && !it.exited {
		it.autoProbe = it.handler.probeAutoTitle()
	}
}

// reportsActivity reports whether the session has sent a genuine activity
// signal (OSC 9;4 progress, an OSC 7501 report or a latched OSC 9
// notification), the gate the client reveals the tab's activity dot behind.
// Once true it stays true for the session's life; programSeq, unlike
// programSeen, survives RIS. The secondary activity is not an input: it would
// reveal the dot for a background task.
func (it *statusRaw) reportsActivity(tr *statusTracker) bool {
	return tr.lastReports || it.progress >= 0 || it.programSeq != 0 || tr.latched != ""
}

// diffStatuses returns one event per session whose emitted state changed since
// the last sweep (sweepSession owns which fields count), plus a removed event per
// vanished session. It is also the sole writer of the automatic title.
//
// Three phases keep m.mu off the handler getters, each of which takes h.mu: one
// wedged handler under m.mu would stall every manager path. Phase 1 snapshots
// under m.mu, phase 2 reads handlers lock-free, and phase 3 re-takes m.mu for the
// tracker state machine, whose fields snapshot() also reads under m.mu.
func (m *SessionManager) diffStatuses() []statusEvent {
	// Phase 1: snapshot sessions + tracker refs under m.mu. No handler calls.
	m.mu.Lock()
	items := make([]statusRaw, 0, len(m.sessions))
	for id, s := range m.sessions {
		tr := m.trackers[id]
		if tr == nil {
			tr = &statusTracker{}
			m.trackers[id] = tr
		}
		items = append(items, statusRaw{id: id, createdAt: s.createdAt, handler: s.handler, tr: tr})
	}
	m.mu.Unlock()

	// Phase 2: read handler inputs lock-free (per-handler h.mu only), plus the
	// consumer's secondary activity, which must not be read under m.mu either.
	for i := range items {
		items[i].read()
		items[i].activity = m.sessionActivity(items[i].id)
	}
	if hold := testDiffPhaseHold.Load(); hold != nil {
		(*hold)()
	}

	// Phase 3: tracker state machine + change detection under m.mu.
	var events []statusEvent
	m.mu.Lock()
	// The display order is read HERE, not in phase 1, and that placement is the
	// whole point. It is manager-owned state, so a Close or a SetSessionOrder
	// during phase 2 (which runs lock-free, and can block on a wedged handler
	// getter) invalidates anything phase 1 captured. Emitting a position from that
	// stale snapshot pushes clients an arrangement the server has already replaced
	// — silently, because the tracker then records the stale value as delivered and
	// the next sweep sees no change to correct. Reading it under this lock, in the
	// same section that decides what to emit, is what makes an emitted position
	// current by construction.
	rank := m.rankLocked()
	ordered := len(m.order)
	for i := range items {
		it := &items[i]
		s, live := m.sessions[it.id]
		if !live {
			continue // closed while computing; the removed sweep below emits it
		}
		it.order = rankOf(rank, it.id, ordered)
		if ev, changed := m.sweepSession(s, it); changed {
			events = append(events, ev)
		}
	}
	for id, tr := range m.trackers {
		if _, ok := m.sessions[id]; !ok {
			delete(m.trackers, id)
			events = append(events, statusEvent{
				ID: id, Status: StatusExited, Title: tr.lastTitle, Removed: true,
				ReportsActivity: tr.lastReports, ProgressValue: progressAbsent,
			})
		}
	}
	m.mu.Unlock()
	return events
}

// testDiffPhaseHold, when non-nil, is invoked by diffStatuses between phase 2 and
// phase 3. Test-only: phase 2 runs lock-free and can block on a wedged handler
// getter, so that gap is where manager state legitimately changes underneath a
// sweep in flight, and holding the sweep open at exactly that instant is the only
// way to drive the case deterministically. Atomic for the same reason as
// testResumeBatchHold. Never set in production.
var testDiffPhaseHold atomic.Pointer[func()]

// testStatusReadHold, when non-nil, is invoked by statusRaw.read between the
// screen snapshot and the exit outcome, so a test can finish a process exit
// at exactly that instant. Never set in production.
var testStatusReadHold atomic.Pointer[func()]

// sweepSession runs one session's tracker state machine and change detection
// (diffStatuses phase 3), returning the event to broadcast and whether anything
// changed. Callers hold m.mu: it reads the session's stored titles and mutates
// the tracker.
func (m *SessionManager) sweepSession(s *session, it *statusRaw) (statusEvent, bool) {
	tr := it.tr
	// A notification the tracker has not delivered yet. Read BEFORE
	// computeStatus, which advances the separate classifier cursor.
	notifNew := it.notifSeq > tr.notifDelivered
	status, fresh := m.computeStatus(it, tr)
	// The stored titles are re-read under m.mu — a PUT during phase 2 must not
	// be masked by the phase-1 capture.
	clientTitle := s.clientTitle
	pinnedTitle := s.pinnedTitle
	alias := s.alias
	// The sweep is the ONLY writer of the server-derived automatic title, so
	// List and snapshot read one confirmed value instead of each probing
	// procfs and disagreeing with this stream.
	m.confirmAutoTitle(s, it, tr)
	title := effectiveTitle(&titleSources{
		pinned: pinnedTitle, osc: it.oscTitle,
		client: clientTitle, auto: s.autoTitle,
	})
	reports := it.reportsActivity(tr)
	// Each clause below is a change some consumer must see even when nothing
	// else moves: a raw client title or pin under a masking rung, a percentage,
	// a reorder (its only signal; a close shifts every later position), the
	// secondary activity, the seen floor, a fresh notification (delivering it is
	// the point) and a fresh occurrence (a second done between two sweeps).
	if status == tr.lastStatus && title == tr.lastTitle && clientTitle == tr.lastClientTitle &&
		pinnedTitle == tr.lastPinnedTitle && alias == tr.lastAlias && reports == tr.lastReports &&
		it.progressValue == tr.lastProgressValue && it.order == tr.lastOrder &&
		it.activity.State == tr.lastActivity && it.activity.Count == tr.lastActivityCount &&
		tr.seenSeq == tr.lastSeenSeq && !notifNew && !fresh {
		return statusEvent{}, false
	}
	tr.numberOccurrence(status, fresh)
	tr.lastSeenSeq = tr.seenSeq
	tr.lastOSCTitle = it.oscTitle
	tr.lastStatus = status
	tr.lastTitle = title
	tr.lastClientTitle = clientTitle
	tr.lastPinnedTitle = pinnedTitle
	tr.lastAlias = alias
	tr.lastReports = reports
	tr.lastProgressValue = it.progressValue
	tr.lastOrder = it.order
	tr.lastActivity = it.activity.State
	tr.lastActivityCount = it.activity.Count
	// A copy, not &it.order: it points into diffStatuses' phase-1 slice, and an
	// event that outlives the sweep must not alias the sweep's own scratch state.
	pos := it.order
	ev := statusEvent{
		ID: it.id, Status: status, Title: title, ClientTitle: clientTitle,
		PinnedTitle: pinnedTitle, Alias: alias, CreatedAt: it.createdAt, ReportsActivity: reports,
		ProgressValue: it.progressValue, Order: &pos,
		Activity: it.activity.State, ActivityCount: it.activity.Count,
		StatusSeq: tr.statusSeq, SeenSeq: tr.seenSeq,
	}
	// A notification rides along on the sweep that first observes it, and only
	// that sweep: it is an event, so replaying it on a later status-only change
	// would show the consumer the same notification twice.
	if notifNew {
		tr.notifDelivered = it.notifSeq
		ev.Notification = it.notifMsg
		ev.NotificationSeq = it.notifSeq
	}
	return ev, true
}

// computeStatus derives a session's status from one sweep's inputs; callers
// hold m.mu. Precedence: exited or crashed (crashedExit); then, once the
// program has sent an OSC 7501 report since the last RIS, programStatusOf;
// otherwise a notification classified in this sweep, then the OSC 9;4 state
// (which clears an older latch per progressSupersedesLatch), then an older
// latch, then idle. Raw output activity is never an input, so typing at a
// prompt cannot flap a session to working.
func (m *SessionManager) computeStatus(in *statusRaw, tr *statusTracker) (status string, fresh bool) {
	// Consumed ahead of every return: an occurrence the chosen path does not
	// show (a newer notification wins, the process exited) is superseded, and
	// must not read as new once a later report brings the status back to it.
	programAdvanced := in.programOccurrence > tr.programOccurrence
	tr.programOccurrence = in.programOccurrence
	if in.exited {
		// Top precedence: a dead session's LAST progress state or latch says
		// nothing useful about how it died.
		if in.crashed {
			return StatusCrashed, false
		}
		return StatusExited, false
	}
	latchedNow := m.applyNotification(in, tr)
	if in.programSeen {
		return programStatusOf(in, tr, latchedNow, programAdvanced)
	}
	// An active OSC 9;4 state (1 value, 3 indeterminate) means working; error
	// and warning persist until the program changes them. A progress state
	// supersedes a latch from an EARLIER sweep (a new turn clears the stale
	// done/input dot) but never one applied in THIS sweep: a turn-boundary
	// snapshot can pair a fresh classified notification with a progress
	// value still active (flushed a chunk ahead of progress-off), and clearing the
	// fresh latch would lose that dot for good, since notifSeen has advanced. The
	// next sweep's progress supersedes it if progressSupersedesLatch says so.
	if st, ok := progressStatus(in.progress); ok && !latchedNow &&
		(tr.latched == "" || progressSupersedesLatch(in.progress)) {
		tr.latched = ""
		return st, false
	}
	// A latched notification status persists through the quiet gap after the
	// turn until a progress state that contradicts it arrives.
	if tr.latched != "" {
		return tr.latched, latchedNow
	}
	return StatusIdle, false
}

// programStatusOf derives the status of a session that speaks OSC 7501. A
// classified notification newer than the newest status change
// (vt.ProgramStatus.Seq) wins until the next one, which clears the latch; a
// report that repeats the status keeps it. Notifications and reports share one
// sequence counter, so "newer" is exact. Otherwise the report maps working,
// blocked, done and error to working, input, done and failed, and idle to
// idle. An input, done or failed status is a fresh occurrence when advanced
// (vt.ProgramStatus.Seq rose since the last sweep), even if it repeats.
func programStatusOf(in *statusRaw, tr *statusTracker, latchedNow, advanced bool) (status string, fresh bool) {
	if tr.latched != "" && tr.latchSeq > in.programOccurrence {
		return tr.latched, latchedNow
	}
	tr.latched = ""
	switch in.programState {
	case vt.ProgramStateWorking:
		return StatusWorking, false
	case vt.ProgramStateBlocked:
		return StatusInput, advanced
	case vt.ProgramStateDone:
		return StatusDone, advanced
	case vt.ProgramStateError:
		return StatusFailed, advanced
	}
	return StatusIdle, false
}

// progressStatus maps an OSC 9;4 progress state to the status it reports, and
// whether it reports one at all. State 0 (cleared) and -1 (never reported) carry
// no status of their own: they leave the decision to the latch and to idle.
func progressStatus(progress int) (string, bool) {
	switch progress {
	case 1, 3: // determinate value / indeterminate: the program is working
		return StatusWorking, true
	case 2: // error state (with a percentage, or indeterminate without one)
		return StatusFailed, true
	case 4: // iTerm2's warning at pr percent (ConEmu calls the same state paused)
		return StatusWarning, true
	}
	return "", false
}

// progressSupersedesLatch reports whether an OSC 9;4 state contradicts a latched
// notification status and so may clear it. Asked only of states progressStatus
// accepts. States 1 and 3 say work is under way; 2 reports a failure, which must
// not stay masked by a stale "done". State 4 (iTerm2's warning, Windows'
// TBPF_PAUSED: "No error condition exists") starts no work and reports no
// failure, so it does not clear a latch; it persists, so as a rival it would take
// the tab one sweep (250ms) after the notification. A latch state 4 cannot clear
// ends when the program resumes (1 or 3), fails (2) or dies.
func progressSupersedesLatch(progress int) bool {
	return progress != 4
}

// applyNotification updates the tracker's latch from a new OSC 9 notification
// via the consumer's classifier, if any, and reports whether this call latched
// a state — so computeStatus can give a notification classified in the current
// sweep precedence over a concurrently captured active progress value.
// WithStatusClassifier owns how long the latch lasts. An unclassified message
// leaves the latch unchanged.
func (m *SessionManager) applyNotification(in *statusRaw, tr *statusTracker) bool {
	if m.classifier == nil {
		return false
	}
	if in.notifSeq <= tr.notifSeen {
		return false
	}
	tr.notifSeen = in.notifSeq
	if cls, ok := m.classifier(in.notifMsg); ok {
		tr.latched = cls
		tr.latchSeq = in.notifSeq
		return true
	}
	return false
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
// new subscriber receives, in compareSessionOrder, from the same committed
// tracker record List serves (the two sources must agree, on order as well as
// on status; see committedOf and compareSessionOrder).
//
// One m.mu hold and no handler getter, so a wedged handler cannot stall a new
// subscriber. No notification is replayed here: a notification is an event,
// and a subscriber joining later did not miss a state.
func (m *SessionManager) snapshot() []statusEvent {
	type snapItem struct {
		ev   statusEvent
		rank int
	}
	m.mu.Lock()
	rank := m.rankLocked()
	ordered := len(m.order)
	out := make([]snapItem, 0, len(m.sessions))
	for id, s := range m.sessions {
		rec := committedOf(m.trackers[id])
		out = append(out, snapItem{
			rank: rankOf(rank, id, ordered),
			ev: statusEvent{
				ID: id, Alias: s.alias, ClientTitle: s.clientTitle, PinnedTitle: s.pinnedTitle,
				CreatedAt: s.createdAt, Status: rec.status,
				Title: effectiveTitle(&titleSources{
					pinned: s.pinnedTitle, osc: rec.oscTitle,
					client: s.clientTitle, auto: s.autoTitle,
				}),
				ProgressValue: rec.progressValue, ReportsActivity: rec.reports,
				Activity: rec.activity, ActivityCount: rec.activityCount,
				StatusSeq: rec.statusSeq, SeenSeq: rec.seenSeq,
			},
		})
	}
	m.mu.Unlock()
	// Same order List serves, for the same reason it sorts at all: this is an
	// ENUMERATION of the session set, and a client building a tab strip from it
	// reads the sequence as the strip's order. Phase 1 ranged m.sessions, so
	// without this the stream pushed a fresh random order on every connect.
	//
	// diffStatuses is deliberately NOT sorted this way: it returns a CHANGE SET,
	// its removed events carry a zero createdAt, and a per-tick burst of
	// independent state updates has no enumeration order to get right.
	slices.SortFunc(out, func(a, b snapItem) int {
		return compareSessionOrder(
			sessionOrder{createdAt: a.ev.CreatedAt, id: a.ev.ID, rank: a.rank},
			sessionOrder{createdAt: b.ev.CreatedAt, id: b.ev.ID, rank: b.rank},
		)
	})
	// The published position is the index in THIS sequence, not the raw rank, so
	// the field a client sorts by is 0-based, dense and unique whatever the
	// manager's internal state happens to be. Deriving it from the rank map
	// instead would let two sessions the order does not name (an invariant
	// violation, hence unreachable — but the wire contract should not depend on
	// that) both claim the same position.
	evs := make([]statusEvent, 0, len(out))
	for i := range out {
		pos := i
		out[i].ev.Order = &pos
		evs = append(evs, out[i].ev)
	}
	return evs
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
		snap := m.snapshot()
		for i := range snap {
			if !writeSSE(w, &snap[i]) {
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
//
// no-cache is the conventional SSE directive but it only forces revalidation — it
// still permits a cache to STORE the response, and every event on this stream
// carries a full session id (statusEvent.ID), the same capability token the REST
// responses now refuse to let a cache keep. no-store is therefore carried
// alongside it: the conventional token stays for any middlebox that sniffs for it,
// and the stronger prohibition is the one that matches what the body contains.
func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-store")
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
