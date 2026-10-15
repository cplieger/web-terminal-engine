package terminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// seenManager is a classifier manager whose sweep the test drives by hand.
func seenManager(t *testing.T, opts ...ManagerOption) *SessionManager {
	t.Helper()
	m := NewSessionManager(catFactory, append([]ManagerOption{WithStatusClassifier(inputClassifier)}, opts...)...)
	t.Cleanup(func() { shutdownManager(t, m) })
	m.stopSweep()
	return m
}

func seenSession(t *testing.T, m *SessionManager) (SessionID, *Handler) {
	t.Helper()
	id, err := m.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return id, handlerOf(t, m, id)
}

// occur drives n status occurrences by flipping the progress state between
// working and cleared, sweeping after each.
func occur(t *testing.T, m *SessionManager, h *Handler, id SessionID, n int) {
	t.Helper()
	for range n {
		if listedInfo(t, m, id).Status == StatusWorking {
			h.handlePTYData([]byte("\x1b]9;4;0\x07"))
		} else {
			h.handlePTYData([]byte("\x1b]9;4;3\x07"))
		}
		_ = eventFor(t, m.diffStatuses(), id)
	}
}

func TestMarkSessionSeen_monotonic(t *testing.T) {
	t.Run("an older ack leaves the floor", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		_ = m.diffStatuses()
		occur(t, m, h, id, 4)
		if got := listedInfo(t, m, id).StatusSeq; got != 5 {
			t.Fatalf("precondition: StatusSeq = %d, want 5", got)
		}
		m.markSessionSeen(id, 5)
		m.markSessionSeen(id, 3)
		if got := listedInfo(t, m, id).SeenSeq; got != 5 {
			t.Errorf("SeenSeq after acks 5 then 3 = %d, want 5", got)
		}
	})
	t.Run("an ack above the latest occurrence is capped", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		_ = m.diffStatuses()
		occur(t, m, h, id, 1)
		m.markSessionSeen(id, 99)
		if got := listedInfo(t, m, id).SeenSeq; got != 2 {
			t.Errorf("SeenSeq after ack 99 at StatusSeq 2 = %d, want 2", got)
		}
	})
	t.Run("an unknown id is refused", func(t *testing.T) {
		m := seenManager(t)
		if m.markSessionSeen("deadbeef", 1) {
			t.Errorf("markSessionSeen(unknown) = true, want false")
		}
		m.mu.Lock()
		n := len(m.trackers)
		m.mu.Unlock()
		if n != 0 {
			t.Errorf("trackers after an unknown ack = %d, want 0", n)
		}
	})
	t.Run("an ack before the first sweep succeeds and covers nothing", func(t *testing.T) {
		m := seenManager(t)
		id, _ := seenSession(t, m)
		if !m.markSessionSeen(id, 3) {
			t.Errorf("markSessionSeen(live, untracked) = false, want true")
		}
		if ev := eventFor(t, m.diffStatuses(), id); ev.SeenSeq != 0 {
			t.Errorf("first sweep SeenSeq = %d, want 0", ev.SeenSeq)
		}
	})
}

func TestMarkSessionSeen_ConcurrentAcksConverge(t *testing.T) {
	m := seenManager(t)
	id, h := seenSession(t, m)
	_ = m.diffStatuses()
	occur(t, m, h, id, 7)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() { m.markSessionSeen(id, uint64(i%8)+1) })
	}
	wg.Wait()
	if got := listedInfo(t, m, id).SeenSeq; got != 8 {
		t.Errorf("SeenSeq after concurrent acks 1..8 = %d, want 8", got)
	}
}

func TestStatusSeq_numbering(t *testing.T) {
	t.Run("a status change advances by one", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
		done := eventFor(t, m.diffStatuses(), id)
		h.handlePTYData([]byte("\x1b]9;4;3\x07"))
		ev := eventFor(t, m.diffStatuses(), id)
		if ev.Status != StatusWorking || ev.StatusSeq != done.StatusSeq+1 {
			t.Errorf("done -> working = (%q, %d), want (%q, %d)", ev.Status, ev.StatusSeq, StatusWorking, done.StatusSeq+1)
		}
	})
	t.Run("a re-latched done advances by one", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
		first := eventFor(t, m.diffStatuses(), id)
		h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
		ev := eventFor(t, m.diffStatuses(), id)
		if ev.Status != StatusDone || ev.StatusSeq != first.StatusSeq+1 {
			t.Errorf("done -> done = (%q, %d), want (%q, %d)", ev.Status, ev.StatusSeq, StatusDone, first.StatusSeq+1)
		}
	})
	src := newActivitySource()
	m := seenManager(t, WithSessionActivity(src.read))
	id, h := seenSession(t, m)
	other, _ := seenSession(t, m)
	h.handlePTYData([]byte("\x1b]9;4;1;10\x07"))
	base := eventFor(t, m.diffStatuses(), id).StatusSeq
	for _, tc := range []struct {
		change func()
		name   string
	}{
		{name: "title", change: func() { h.handlePTYData([]byte("\x1b]2;renamed\x07")) }},
		{name: "progress", change: func() { h.handlePTYData([]byte("\x1b]9;4;1;60\x07")) }},
		{name: "activity", change: func() { src.set(id, SessionActivity{State: ActivityWorking, Count: 1}) }},
		{name: "order", change: func() { m.SetSessionOrder([]SessionID{other, id}) }},
		{name: "alias", change: func() { m.SetSessionAlias(id, "renamed-alias") }},
	} {
		t.Run("a "+tc.name+" change emits without advancing", func(t *testing.T) {
			tc.change()
			if ev := eventFor(t, m.diffStatuses(), id); ev.StatusSeq != base {
				t.Errorf("StatusSeq = %d, want %d", ev.StatusSeq, base)
			}
		})
	}
	t.Run("a sweep with no change emits nothing", func(t *testing.T) {
		for _, ev := range m.diffStatuses() {
			if ev.ID == id {
				t.Errorf("quiet sweep emitted %+v, want nothing", ev)
			}
		}
		if got := listedInfo(t, m, id).StatusSeq; got != base {
			t.Errorf("StatusSeq after a quiet sweep = %d, want %d", got, base)
		}
	})
}

func TestSeenSeq_broadcastsOnce(t *testing.T) {
	m := seenManager(t)
	id, _ := seenSession(t, m)
	_ = m.diffStatuses()
	a, okA := m.subscribe()
	b, okB := m.subscribe()
	if !okA || !okB {
		t.Fatal("subscribe refused below the cap")
	}
	sweep := func() {
		evs := m.diffStatuses()
		for i := range evs {
			m.broadcast(&evs[i])
		}
	}
	m.markSessionSeen(id, 1)
	sweep()
	for name, ch := range map[string]chan statusEvent{"a": a, "b": b} {
		if n := len(ch); n != 1 {
			t.Errorf("subscriber %s received %d events after an ack, want 1", name, n)
			continue
		}
		if ev := <-ch; ev.ID != id || ev.SeenSeq != 1 {
			t.Errorf("subscriber %s event = (%s, seen %d), want (%s, seen 1)", name, ev.ID, ev.SeenSeq, id)
		}
	}
	m.markSessionSeen(id, 1)
	sweep()
	sweep()
	if n := len(a) + len(b); n != 0 {
		t.Errorf("a duplicate ack and a quiet sweep delivered %d events, want 0", n)
	}
}

// nextData reads the next SSE data frame for id as generic JSON.
func nextData(t *testing.T, sc *bufio.Scanner, id SessionID) map[string]any {
	t.Helper()
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("frame %q: %v", line, err)
		}
		if obj["id"] == string(id) {
			return obj
		}
	}
	t.Fatalf("stream ended before a frame for %s (scan err: %v)", LogID(id), sc.Err())
	return nil
}

func assertSeqKeys(t *testing.T, where string, obj map[string]any, statusSeq, seenSeq float64) {
	t.Helper()
	if got, ok := obj["statusSeq"].(float64); !ok || got != statusSeq {
		t.Errorf("%s statusSeq = %v, want %v", where, obj["statusSeq"], statusSeq)
	}
	if got, ok := obj["seenSeq"].(float64); !ok || got != seenSeq {
		t.Errorf("%s seenSeq = %v, want %v", where, obj["seenSeq"], seenSeq)
	}
}

func TestSeenSeq_onTheWire(t *testing.T) {
	m := seenManager(t)
	id, h := seenSession(t, m)
	_ = m.diffStatuses()
	m.markSessionSeen(id, 1)
	_ = m.diffStatuses()

	mux := http.NewServeMux()
	MountSessionRoutes(mux, SessionHandlers{WS: m.WebSocketHandler(), REST: m.RESTHandler(), Events: m.EventsHandler()})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, SessionsPath, nil))
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("GET %s = %q (%v), want one session", SessionsPath, rec.Body.String(), err)
	}
	assertSeqKeys(t, "list", list[0], 1, 1)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+SessionEventsPath, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	assertSeqKeys(t, "snapshot frame", nextData(t, sc, id), 1, 1)

	h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
	evs := m.diffStatuses()
	for i := range evs {
		m.broadcast(&evs[i])
	}
	assertSeqKeys(t, "change frame", nextData(t, sc, id), 2, 1)
}

func putSeen(mux http.Handler, id SessionID, body string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, SessionsPath+"/"+string(id)+"/seen", strings.NewReader(body))
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestSeenRoute(t *testing.T) {
	m := seenManager(t)
	id, _ := seenSession(t, m)
	_ = m.diffStatuses()
	mux := http.NewServeMux()
	MountSessionRoutes(mux, SessionHandlers{WS: m.WebSocketHandler(), REST: m.RESTHandler(), Events: m.EventsHandler()})
	for _, tc := range []struct {
		name string
		id   SessionID
		body string
		want int
	}{
		{name: "a live session", id: id, body: `{"seq":1}`, want: http.StatusNoContent},
		{name: "an older ack", id: id, body: `{"seq":0}`, want: http.StatusNoContent},
		{name: "an unknown session", id: "deadbeef", body: `{"seq":1}`, want: http.StatusNotFound},
		{name: "an unknown session with a bad body", id: "deadbeef", body: `{}`, want: http.StatusBadRequest},
		{name: "a missing seq", id: id, body: `{}`, want: http.StatusBadRequest},
		{name: "a null seq", id: id, body: `{"seq":null}`, want: http.StatusBadRequest},
		{name: "a negative seq", id: id, body: `{"seq":-1}`, want: http.StatusBadRequest},
		{name: "a fractional seq", id: id, body: `{"seq":1.5}`, want: http.StatusBadRequest},
		{name: "an exponent seq", id: id, body: `{"seq":1e3}`, want: http.StatusBadRequest},
		{name: "a string seq", id: id, body: `{"seq":"3"}`, want: http.StatusBadRequest},
		{name: "trailing data", id: id, body: `{"seq":1}{"seq":1}`, want: http.StatusBadRequest},
		{name: "an oversized body", id: id, body: `{"seq":1}` + strings.Repeat(" ", 300), want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := putSeen(mux, tc.id, tc.body); got != tc.want {
				t.Errorf("PUT seen %s = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestSeenSeq_notPersisted(t *testing.T) {
	m := seenManager(t)
	a, _ := seenSession(t, m)
	b, _ := seenSession(t, m)
	_ = m.diffStatuses()
	capture := func() []byte {
		t.Helper()
		data, err := json.Marshal(m.CaptureSessionSet(func(SessionInfo) string { return "" }))
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return data
	}
	before := capture()
	m.markSessionSeen(a, 1)
	m.markSessionSeen(b, 1)
	_ = m.diffStatuses()
	if after := capture(); !bytes.Equal(before, after) {
		t.Errorf("captured set after acks = %s, want the set before them %s", after, before)
	}
}

func TestPointInTimeReadsCarryTheCommittedRecord(t *testing.T) {
	listed := func(t *testing.T, m *SessionManager, id SessionID) SessionInfo {
		t.Helper()
		return listedInfo(t, m, id)
	}
	snapped := func(t *testing.T, m *SessionManager, id SessionID) statusEvent {
		t.Helper()
		for _, ev := range m.snapshot() {
			if ev.ID == id {
				return ev
			}
		}
		t.Fatalf("snapshot has no session %s", LogID(id))
		return statusEvent{}
	}
	quiet := func(t *testing.T, m *SessionManager, id SessionID) {
		t.Helper()
		for _, ev := range m.diffStatuses() {
			if ev.ID == id {
				t.Fatalf("premise: the final sweep emitted %+v, so a frame repairs the pulse", ev)
			}
		}
	}

	t.Run("an activity pulse between sweeps", func(t *testing.T) {
		src := newActivitySource()
		m := seenManager(t, WithSessionActivity(src.read))
		id, h := seenSession(t, m)
		h.handlePTYData([]byte("\x1b]9;Response complete\x07"))
		ev := eventFor(t, m.diffStatuses(), id)
		src.set(id, SessionActivity{State: ActivityWorking, Count: 1})
		li, sn := listed(t, m, id), snapped(t, m, id)
		src.set(id, SessionActivity{})
		if li.Activity != "" || li.ActivityCount != 0 || li.Status != StatusDone || li.StatusSeq != ev.StatusSeq {
			t.Errorf("List = (%q, %q/%d, %d), want (%q, \"\"/0, %d)",
				li.Status, li.Activity, li.ActivityCount, li.StatusSeq, StatusDone, ev.StatusSeq)
		}
		if sn.Activity != "" || sn.ActivityCount != 0 || sn.Status != StatusDone || sn.StatusSeq != ev.StatusSeq {
			t.Errorf("snapshot = (%q, %q/%d, %d), want (%q, \"\"/0, %d)",
				sn.Status, sn.Activity, sn.ActivityCount, sn.StatusSeq, StatusDone, ev.StatusSeq)
		}
		quiet(t, m, id)
	})
	t.Run("a progress pulse between sweeps", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		h.handlePTYData([]byte("\x1b]9;4;1;10\x07"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.ProgressValue != 10 {
			t.Fatalf("precondition: ProgressValue = %d, want 10", ev.ProgressValue)
		}
		h.handlePTYData([]byte("\x1b]9;4;1;60\x07"))
		sn := snapped(t, m, id)
		h.handlePTYData([]byte("\x1b]9;4;1;10\x07"))
		if sn.ProgressValue != 10 {
			t.Errorf("snapshot ProgressValue = %d, want 10", sn.ProgressValue)
		}
		quiet(t, m, id)
	})
	t.Run("reports activity before the sweep", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		_ = m.diffStatuses()
		h.handlePTYData([]byte("\x1b]9;4;0\x07"))
		if listed(t, m, id).ReportsActivity {
			t.Errorf("List ReportsActivity before the sweep = true, want false")
		}
		if snapped(t, m, id).ReportsActivity {
			t.Errorf("snapshot ReportsActivity before the sweep = true, want false")
		}
		if ev := eventFor(t, m.diffStatuses(), id); !ev.ReportsActivity {
			t.Fatalf("sweep ReportsActivity = false, want true")
		}
		if !listed(t, m, id).ReportsActivity {
			t.Errorf("List ReportsActivity after the sweep = false, want true")
		}
		if !snapped(t, m, id).ReportsActivity {
			t.Errorf("snapshot ReportsActivity after the sweep = false, want true")
		}
	})
	t.Run("an OSC title pulse between sweeps", func(t *testing.T) {
		m := seenManager(t)
		id, h := seenSession(t, m)
		h.handlePTYData([]byte("\x1b]2;first\x07"))
		if ev := eventFor(t, m.diffStatuses(), id); ev.Title != "first" {
			t.Fatalf("precondition: Title = %q, want first", ev.Title)
		}
		h.handlePTYData([]byte("\x1b]2;second\x07"))
		li, sn := listed(t, m, id), snapped(t, m, id)
		h.handlePTYData([]byte("\x1b]2;first\x07"))
		if li.Title != "first" {
			t.Errorf("List Title = %q, want first", li.Title)
		}
		if sn.Title != "first" {
			t.Errorf("snapshot Title = %q, want first", sn.Title)
		}
		quiet(t, m, id)
		m.SetSessionPinnedTitle(id, "pin")
		if got := listed(t, m, id).Title; got != "pin" {
			t.Errorf("List Title after a pin, before a sweep = %q, want pin", got)
		}
	})
}
