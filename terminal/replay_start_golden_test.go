package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestReplayStartGolden pins replayStart to wire-golden/replay-start.json, the
// hand-written table web/src/replay.test.ts pins the client's prediction to.
func TestReplayStartGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "wire-golden", "replay-start.json"))
	if err != nil {
		t.Fatalf("read replay-start.json: %v", err)
	}
	var rows []struct {
		Name        string `json:"name"`
		Committed   uint64 `json:"committed"`
		Oldest      uint64 `json:"oldest"`
		HaveThrough int64  `json:"haveThrough"`
		ReplayMax   *int64 `json:"replayMax"`
		Start       uint64 `json:"start"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse replay-start.json: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("replay-start.json holds no rows")
	}
	for _, r := range rows {
		t.Run(r.Name, func(t *testing.T) {
			if got := replayStart(r.Committed, r.Oldest, r.HaveThrough, r.ReplayMax); got != r.Start {
				replayMax := "absent"
				if r.ReplayMax != nil {
					replayMax = strconv.FormatInt(*r.ReplayMax, 10)
				}
				t.Errorf("replayStart(committed %d, oldest %d, haveThrough %d, replayMax %s) = %d, want %d"+
					" (the hand-written table web/src/replay.test.ts also reads)",
					r.Committed, r.Oldest, r.HaveThrough, replayMax, got, r.Start)
			}
		})
	}
}
