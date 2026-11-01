package terminal

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
)

var propTitles = []string{"", "fix build", "日本語", "a b c"}

// propSide picks a session id from ids, or nil.
func propSide(r *rand.Rand, ids []SessionID) *SessionID {
	if len(ids) == 0 || r.IntN(3) == 0 {
		return nil
	}
	return new(ids[r.IntN(len(ids))])
}

func TestSessionSetLayout_roundTripsThroughParse(t *testing.T) {
	for iter := range 500 {
		seed := uint64(iter)
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		n := r.IntN(9)
		list := make([]SessionInfo, n)
		cleanExit := make([]bool, n)
		ids := make([]SessionID, n)
		tokens := make(map[SessionID]string, n)
		for i := range list {
			ids[i] = SessionID(fmt.Sprintf("id-%d", i))
			list[i] = SessionInfo{
				ID: ids[i], Alias: fmt.Sprintf("alias-%d", i),
				PinnedTitle: propTitles[r.IntN(len(propTitles))],
			}
			cleanExit[i] = r.IntN(4) == 0
			if r.IntN(2) == 0 {
				tokens[ids[i]] = fmt.Sprintf("tab_%d", r.Uint32())
			}
		}
		layout := PaneLayout{Left: propSide(r, ids), Right: propSide(r, ids), Handle: r.Float64(), Open: r.IntN(2) == 0}
		if layout.Left != nil && layout.Right != nil && *layout.Left == *layout.Right {
			layout.Right = nil
		}
		layout.Selected = PaneLeft
		if r.IntN(2) == 0 {
			layout.Selected = PaneRight
		}

		set := captureSessionSet(list, cleanExit, layout, func(info SessionInfo) string { return tokens[info.ID] })
		data, err := json.Marshal(set)
		if err != nil {
			t.Fatalf("seed %d: json.Marshal = %v", seed, err)
		}
		got, err := ParseSessionSet(data)
		if err != nil {
			t.Fatalf("seed %d: ParseSessionSet(captured %s) = %v, want nil", seed, data, err)
		}
		if !sameSessionSet(got, set) {
			t.Fatalf("seed %d: parsed %+v, want the captured %+v", seed, got, set)
		}
		if again := normalizeSetLayout(set.Layout, len(set.Sessions)); !sameSetLayout(again, set.Layout) {
			t.Fatalf("seed %d: normalizing twice gave %s, want %s", seed, fmtSetLayout(again), fmtSetLayout(set.Layout))
		}
	}
}

func TestCaptureSessionSet_roundTripsThroughParse(t *testing.T) {
	m := NewSessionManager(catFactory)
	t.Cleanup(func() { shutdownManager(t, m) })
	for iter := range 25 {
		seed := uint64(iter)
		r := rand.New(rand.NewPCG(seed, seed^0x2545f4914f6cdd1d))
		for _, info := range m.List() {
			m.Close(info.ID)
		}
		n := r.IntN(7)
		ids := make([]SessionID, 0, n)
		var exited []SessionID
		for range n {
			spec := CreateSpec{PinnedTitle: propTitles[r.IntN(len(propTitles))]}
			ends := r.IntN(3) == 0
			if ends {
				spec.Build = handlerRunning(t, "/bin/true")
			}
			info, err := m.CreateWith(spec)
			if err != nil {
				t.Fatalf("seed %d: CreateWith = %v", seed, err)
			}
			ids = append(ids, info.ID)
			if ends {
				exited = append(exited, info.ID)
			}
		}
		for _, id := range exited {
			awaitStatus(t, m, id, StatusExited)
		}
		order := append([]SessionID(nil), ids...)
		r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		if !m.SetSessionOrder(order) {
			t.Fatalf("seed %d: SetSessionOrder = false", seed)
		}
		if n > 0 {
			setLayout(t, m, validRandomLayout(r, ids))
		}

		set := m.CaptureSessionSet(func(info SessionInfo) string { return info.Alias })
		data, err := json.Marshal(set)
		if err != nil {
			t.Fatalf("seed %d: json.Marshal = %v", seed, err)
		}
		got, err := ParseSessionSet(data)
		if err != nil {
			t.Fatalf("seed %d: ParseSessionSet(captured %s) = %v, want nil", seed, data, err)
		}
		if !sameSessionSet(got, set) {
			t.Fatalf("seed %d: parsed %+v, want the captured %+v", seed, got, set)
		}
		if want := n - len(exited); len(got.Sessions) != want {
			t.Fatalf("seed %d: captured %d sessions, want %d (exited left out)", seed, len(got.Sessions), want)
		}
	}
}

// validRandomLayout builds a layout SetPaneLayout accepts over the non-empty ids.
func validRandomLayout(r *rand.Rand, ids []SessionID) PaneLayout {
	a := ids[r.IntN(len(ids))]
	if r.IntN(2) == 0 || len(ids) < 2 {
		return PaneLayout{Left: new(a), Selected: PaneLeft, Handle: 0.5}
	}
	b := ids[r.IntN(len(ids))]
	for b == a {
		b = ids[r.IntN(len(ids))]
	}
	l := PaneLayout{Left: new(a), Right: new(b), Selected: PaneLeft, Handle: r.Float64(), Open: true}
	switch r.IntN(3) {
	case 0:
		l.Selected = PaneRight
	case 1:
		l.Left, l.Selected = nil, PaneRight
	}
	return l
}
