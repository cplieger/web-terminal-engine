package terminal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"
)

// SessionSet is the persistable shape of the session set: the display order,
// each session's public name, pinned title and an opaque host resume token,
// and the pane layout by position. It holds no SessionID, because an id is a
// capability.
type SessionSet struct {
	Sessions []SessionSetEntry `json:"sessions"`
	Layout   SessionSetLayout  `json:"layout"`
	Version  int               `json:"version"` // SessionSetVersion
}

// SessionSetEntry is one session of a SessionSet, in display order.
type SessionSetEntry struct {
	Alias       string `json:"alias"`
	PinnedTitle string `json:"pinnedTitle,omitempty"`
	Resume      string `json:"resume,omitempty"` // host-owned; untrusted on read
}

// SessionSetLayout is PaneLayout with each side an index into
// SessionSet.Sessions instead of a session id.
type SessionSetLayout struct {
	Left     *int     `json:"left"` // nil is an empty pane
	Right    *int     `json:"right"`
	Selected PaneSide `json:"selected"`
	Handle   float64  `json:"handle"`
	Open     bool     `json:"open"`
}

// Session-set limits. ParseSessionSet refuses a set past any of them, and
// CaptureSessionSet never produces one.
const (
	SessionSetVersion     = 1
	MaxSessionSetBytes    = 1 << 20
	MaxSessionSetSessions = 512
	MaxResumeTokenBytes   = 1024
	// RestoringMessage is the 503 text while RestoreSessionSet runs; a host
	// gate holding creates before its restore starts reuses it.
	RestoringMessage = "restoring tabs"
)

var (
	// ErrInvalidSessionSet wraps every ParseSessionSet refusal.
	ErrInvalidSessionSet = errors.New("terminal: invalid session set")
	// ErrRestoreRunning is returned by RestoreSessionSet while another call runs.
	ErrRestoreRunning = errors.New("terminal: a session-set restore is already running")
)

// CaptureSessionSet snapshots the live set: the sessions, their order and the
// layout as one manager state. resume is called once per kept session, outside
// the manager lock (it may call List). A session whose process has exited
// cleanly is left out, read live; a crashed one is kept. Only the first
// MaxSessionSetSessions in display order are kept. A token over
// MaxResumeTokenBytes or not valid UTF-8 is dropped, then tokens are dropped
// from the last session backwards until the set marshals within
// MaxSessionSetBytes, so the result always passes ParseSessionSet.
func (m *SessionManager) CaptureSessionSet(resume func(SessionInfo) string) SessionSet {
	var layout PaneLayout
	var cleanExit []bool
	list := m.listSnapshot(&layout, &cleanExit)
	return captureSessionSet(list, cleanExit, layout, resume)
}

// captureSessionSet builds the set from list; cleanExit[i] drops list[i].
func captureSessionSet(list []SessionInfo, cleanExit []bool, layout PaneLayout, resume func(SessionInfo) string) SessionSet {
	kept := make([]SessionInfo, 0, len(list))
	for i := range list {
		if !cleanExit[i] && len(kept) < MaxSessionSetSessions {
			kept = append(kept, list[i])
		}
	}
	ids := make([]SessionID, len(kept))
	entries := make([]SessionSetEntry, len(kept))
	for i := range kept {
		info := &kept[i]
		ids[i] = info.ID
		// json.Marshal would rewrite invalid UTF-8 to U+FFFD, silently handing
		// the host back a different token than it gave.
		token := resume(*info)
		if len(token) > MaxResumeTokenBytes || !utf8.ValidString(token) {
			token = ""
		}
		entries[i] = SessionSetEntry{Alias: info.Alias, PinnedTitle: info.PinnedTitle, Resume: token}
	}
	set := SessionSet{
		Version:  SessionSetVersion,
		Sessions: entries,
		Layout:   normalizeSetLayout(indexLayout(layout, ids), len(ids)),
	}
	fitSessionSet(&set)
	return set
}

// fitSessionSet drops resume tokens from the last entry backwards until set
// marshals within MaxSessionSetBytes. MaxResumeTokenBytes alone does not bound
// the JSON: an escaped control byte takes six. Without tokens a set at its
// caps (aliases, sanitized pinned titles) marshals well under the limit, so
// the loop always ends inside it.
func fitSessionSet(set *SessionSet) {
	data, err := json.Marshal(set)
	if err != nil {
		return
	}
	size := len(data)
	for i := len(set.Sessions) - 1; i >= 0 && size > MaxSessionSetBytes; i-- {
		token := set.Sessions[i].Resume
		if token == "" {
			continue
		}
		enc, _ := json.Marshal(token)
		size -= len(`,"resume":`) + len(enc)
		set.Sessions[i].Resume = ""
	}
}

// ParseSessionSet decodes and validates bytes a host stored. Every refusal
// wraps ErrInvalidSessionSet with the first broken rule. Unknown fields are
// ignored, so a later version-1 field does not break a downgrade.
func ParseSessionSet(data []byte) (SessionSet, error) {
	if len(data) > MaxSessionSetBytes {
		return SessionSet{}, fmt.Errorf("%w: larger than %d bytes", ErrInvalidSessionSet, MaxSessionSetBytes)
	}
	// encoding/json would decode invalid UTF-8 to U+FFFD and accept bytes no
	// writer produced, handing the host a token it never stored.
	if !utf8.Valid(data) {
		return SessionSet{}, fmt.Errorf("%w: not valid UTF-8", ErrInvalidSessionSet)
	}
	var set SessionSet
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&set); err != nil {
		return SessionSet{}, fmt.Errorf("%w: %w", ErrInvalidSessionSet, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return SessionSet{}, fmt.Errorf("%w: unexpected data after the set", ErrInvalidSessionSet)
	}
	if set.Version != SessionSetVersion {
		return SessionSet{}, fmt.Errorf("%w: version %d, want %d", ErrInvalidSessionSet, set.Version, SessionSetVersion)
	}
	if len(set.Sessions) > MaxSessionSetSessions {
		return SessionSet{}, fmt.Errorf("%w: more than %d sessions", ErrInvalidSessionSet, MaxSessionSetSessions)
	}
	if rule := invalidEntries(set.Sessions); rule != "" {
		return SessionSet{}, fmt.Errorf("%w: %s", ErrInvalidSessionSet, rule)
	}
	if rule := set.Layout.invalid(len(set.Sessions)); rule != "" {
		return SessionSet{}, fmt.Errorf("%w: layout: %s", ErrInvalidSessionSet, rule)
	}
	return set, nil
}

// invalidEntries names the first entry rule broken, or "".
func invalidEntries(entries []SessionSetEntry) string {
	seen := make(map[string]struct{}, len(entries))
	for i, e := range entries {
		if e.Alias != "" {
			if !validSessionAlias(e.Alias) {
				return fmt.Sprintf("entry %d: alias outside [A-Za-z0-9_-]{1,64}", i)
			}
			if _, dup := seen[e.Alias]; dup {
				return fmt.Sprintf("entry %d: alias held by an earlier entry", i)
			}
			seen[e.Alias] = struct{}{}
		}
		if e.PinnedTitle != sanitizeTitle(e.PinnedTitle, maxPinnedTitleRunes) {
			return fmt.Sprintf("entry %d: pinned title is not sanitized", i)
		}
		if len(e.Resume) > MaxResumeTokenBytes {
			return fmt.Sprintf("entry %d: resume token longer than %d bytes", i, MaxResumeTokenBytes)
		}
	}
	return ""
}

// invalid names the first rule l breaks against a set of n entries, or "".
func (l SessionSetLayout) invalid(n int) string {
	for _, side := range []*int{l.Left, l.Right} {
		if side != nil && (*side < 0 || *side >= n) {
			return "index out of range"
		}
	}
	if l.Left != nil && l.Right != nil && *l.Left == *l.Right {
		return "left and right name the same entry"
	}
	shape := l.asPaneLayout()
	return shape.invalidShape()
}

// asPaneLayout carries the index form into PaneLayout so invalidShape judges
// both forms by one rule set; each index becomes a distinct stand-in id.
func (l SessionSetLayout) asPaneLayout() PaneLayout {
	stand := func(i *int) *SessionID {
		if i == nil {
			return nil
		}
		return new(SessionID(strconv.Itoa(*i)))
	}
	return PaneLayout{Left: stand(l.Left), Right: stand(l.Right), Selected: l.Selected, Handle: l.Handle, Open: l.Open}
}

// indexLayout maps l's sides to their positions in ids; a side ids does not
// hold becomes an empty pane.
func indexLayout(l PaneLayout, ids []SessionID) SessionSetLayout {
	at := func(id *SessionID) *int {
		if id == nil {
			return nil
		}
		if i := slices.Index(ids, *id); i >= 0 {
			return new(i)
		}
		return nil
	}
	return SessionSetLayout{Left: at(l.Left), Right: at(l.Right), Selected: l.Selected, Handle: l.Handle, Open: l.Open}
}

// idLayout maps l's indices back to ids; an index out of range becomes an
// empty pane.
func idLayout(l SessionSetLayout, ids []SessionID) PaneLayout {
	at := func(i *int) *SessionID {
		if i == nil || *i < 0 || *i >= len(ids) {
			return nil
		}
		return new(ids[*i])
	}
	return PaneLayout{Left: at(l.Left), Right: at(l.Right), Selected: l.Selected, Handle: l.Handle, Open: l.Open}
}

// normalizeSetLayout is the one rule set every derived layout passes through,
// for a set of n entries with in-range indices: a closed split has no right
// pane, selects left and sits at 0.5; a selection on an empty pane moves to a
// shown one; two empty panes show entry 0 in the selected pane; an empty set
// gets defaultPaneLayout.
func normalizeSetLayout(l SessionSetLayout, n int) SessionSetLayout {
	if n == 0 {
		def := defaultPaneLayout()
		return SessionSetLayout{Selected: def.Selected, Handle: def.Handle}
	}
	if !l.Open {
		l.Right, l.Selected, l.Handle = nil, PaneLeft, 0.5
	}
	other := l.Selected.other()
	if l.side(l.Selected) == nil && l.side(other) != nil {
		l.Selected = other
	}
	if l.Left == nil && l.Right == nil {
		if l.Selected == PaneRight {
			l.Right = new(0)
		} else {
			l.Left = new(0)
		}
	}
	return l
}

func (l SessionSetLayout) side(s PaneSide) *int {
	if s == PaneRight {
		return l.Right
	}
	return l.Left
}

// normalizePaneLayout applies normalizeSetLayout to an id-form layout over
// order; a side order does not hold becomes an empty pane first.
func normalizePaneLayout(l PaneLayout, order []SessionID) PaneLayout {
	return idLayout(normalizeSetLayout(indexLayout(l, order), len(order)), order)
}

// RestorePlan maps stored entry index to the session that replaces it, or
// ok=false to skip it. Called in index order on RestoreSessionSet's goroutine;
// it may block (pacing) and should honour ctx.
type RestorePlan func(ctx context.Context, index int, entry SessionSetEntry) (spec CreateSpec, ok bool)

// RestoredSession is one session RestoreSessionSet created.
type RestoredSession struct {
	Info  SessionInfo
	Index int // position in SessionSet.Sessions
}

// RestoreResult is what RestoreSessionSet did: the sessions it created in
// index order, how many entries it skipped (plan refused, or the restore
// stopped before them) and how many creates failed.
type RestoreResult struct {
	Restored        []RestoredSession
	Skipped, Failed int
}

// RestoreSessionSet re-creates set through plan, then applies the stored order
// and layout unless a client set its own while the restore ran. It blocks until
// done. While it runs, POST /api/sessions and GET /api/sessions/layout answer
// 503 with Retry-After: 1 and RestoringMessage. It returns ErrRestoreRunning
// when another call is in flight, and ctx.Err() or ErrManagerClosed with the
// partial result when the restore stopped early; the arrangement is applied
// to whatever was restored either way. set is only read.
func (m *SessionManager) RestoreSessionSet(ctx context.Context, set *SessionSet, plan RestorePlan) (RestoreResult, error) {
	m.mu.Lock()
	if m.restoring {
		m.mu.Unlock()
		return RestoreResult{}, ErrRestoreRunning
	}
	m.restoring = true
	orderGen, layoutGen := m.orderGen, m.layoutGen
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.restoring = false
		m.mu.Unlock()
	}()

	var res RestoreResult
	created := make([]*SessionID, len(set.Sessions))
	var stop error
	for i, entry := range set.Sessions {
		if stop = ctx.Err(); stop != nil {
			res.Skipped += len(set.Sessions) - i
			break
		}
		spec, ok := plan(ctx, i, entry)
		if !ok {
			res.Skipped++
			continue
		}
		info, err := m.CreateWith(spec)
		if errors.Is(err, ErrManagerClosed) {
			stop = err
			res.Skipped += len(set.Sessions) - i
			break
		}
		if err != nil {
			m.logger.Warn("session restore: create failed", "index", i, "error", err)
			res.Failed++
			continue
		}
		created[i] = &info.ID
		res.Restored = append(res.Restored, RestoredSession{Index: i, Info: info})
	}

	m.mu.Lock()
	m.applyRestoredLocked(set.Layout, created, orderGen, layoutGen)
	m.mu.Unlock()
	m.logger.Info("session restore: done",
		"restored", len(res.Restored), "skipped", res.Skipped, "failed", res.Failed)
	return res, stop
}

// applyRestoredLocked puts the restored sessions first, in index order, and
// shows the stored layout over them, each only when no client wrote its own
// since the restore began (orderGen, layoutGen). created holds, per stored
// index, the id that replaced it or nil. With no restored session live it
// changes nothing: the stored layout would name no one, and normalizing it
// would replace a live arrangement with a default. Caller holds m.mu.
func (m *SessionManager) applyRestoredLocked(stored SessionSetLayout, created []*SessionID, orderGen, layoutGen uint64) {
	live := make([]*SessionID, len(created))
	anyLive := false
	for i, id := range created {
		if id != nil && m.sessions[*id] != nil {
			live[i] = id
			anyLive = true
		}
	}
	if !anyLive {
		return
	}
	if m.orderGen == orderGen {
		m.order = restoredOrder(live, m.order)
	}
	if m.layoutGen == layoutGen {
		m.layout = normalizePaneLayout(idLayout(stored, liveIDs(live)), m.order)
	}
}

// restoredOrder is the live restored ids in index order, then every other id
// of current in its relative order.
func restoredOrder(live []*SessionID, current []SessionID) []SessionID {
	order := make([]SessionID, 0, len(current))
	for _, id := range live {
		if id != nil {
			order = append(order, *id)
		}
	}
	for _, id := range current {
		if !slices.Contains(order, id) {
			order = append(order, id)
		}
	}
	return order
}

// liveIDs turns a per-index list of live ids into idLayout's form; a nil
// entry becomes an id no order holds, so normalizePaneLayout empties its pane.
func liveIDs(live []*SessionID) []SessionID {
	ids := make([]SessionID, len(live))
	for i, id := range live {
		if id != nil {
			ids[i] = *id
		}
	}
	return ids
}
