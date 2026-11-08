package terminal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLayoutShapesGolden pins invalidShape to web/layout-shapes.json, the
// hand-written table the npm package ships so a client validating a layout
// before it sends one tests against the server's rules.
func TestLayoutShapesGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "web", "layout-shapes.json"))
	if err != nil {
		t.Fatalf("read layout-shapes.json: %v", err)
	}
	var rows []struct {
		Name   string     `json:"name"`
		Layout PaneLayout `json:"layout"`
		Valid  bool       `json:"valid"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse layout-shapes.json: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("layout-shapes.json holds no rows")
	}
	for _, r := range rows {
		t.Run(r.Name, func(t *testing.T) {
			reason := r.Layout.invalidShape()
			if (reason == "") != r.Valid {
				t.Errorf("invalidShape() = %q, want valid=%v (the table web-terminal-ui's layout parser also reads)",
					reason, r.Valid)
			}
		})
	}
}
