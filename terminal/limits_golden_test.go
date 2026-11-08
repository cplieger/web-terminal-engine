package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLimitsGolden pins the server's protocol limits to wire-golden/limits.json,
// the hand-written file web/src/limits.test.ts pins the client's copies to. A
// limit changes in that file first, then on both sides.
func TestLimitsGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "wire-golden", "limits.json"))
	if err != nil {
		t.Fatalf("read limits.json: %v", err)
	}
	var want struct {
		MaxInputMessageBytes int `json:"maxInputMessageBytes"`
		MinResizeCols        int `json:"minResizeCols"`
		MinResizeRows        int `json:"minResizeRows"`
		MaxReplayLines       int `json:"maxReplayLines"`
		HistoryPageSize      int `json:"historyPageSize"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("parse limits.json: %v", err)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"wsReadLimit", wsReadLimit, want.MaxInputMessageBytes},
		{"minResizeCols", minResizeCols, want.MinResizeCols},
		{"minResizeRows", minResizeRows, want.MinResizeRows},
		{"maxReplayLines", maxReplayLines, want.MaxReplayLines},
		{"historyPageSize", historyPageSize, want.HistoryPageSize},
	} {
		if c.got != c.want || c.want == 0 {
			t.Errorf("%s = %d, limits.json says %d", c.name, c.got, c.want)
		}
	}
}
