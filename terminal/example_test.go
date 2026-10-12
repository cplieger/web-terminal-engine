package terminal_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/cplieger/web-terminal-engine/v6/terminal"
)

func Example() {
	h := terminal.NewHandler(
		[]string{"/bin/bash"},
		terminal.WithWorkDir("/home/user"),
		terminal.WithLogger(slog.Default()),
	)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	_ = mux
	// Output:
}

// sessionStore is the host's storage for the saved set; the engine does no
// file I/O. Load returns the last save and refuses one over limit bytes
// without reading it whole. Save replaces it atomically with owner-only
// permissions: write a temporary file, sync it, rename it over the old one,
// then sync the directory.
type sessionStore interface {
	Load(limit int) ([]byte, error)
	Save(data []byte) error
}

// The host owns the bytes, where they live and the cadence: restore what the
// last run saved, then save the set whenever it changes.
func ExampleSessionManager_CaptureSessionSet() {
	ctx := context.Background()
	m := terminal.NewSessionManager(func(terminal.SessionID) *terminal.Handler {
		return terminal.NewHandler([]string{"/bin/bash"})
	})
	var store sessionStore // the host's implementation

	if data, err := store.Load(terminal.MaxSessionSetBytes); err == nil {
		if set, err := terminal.ParseSessionSet(data); err == nil {
			_, _ = m.RestoreSessionSet(ctx, &set, func(_ context.Context, _ int, e terminal.SessionSetEntry) (terminal.CreateSpec, bool) {
				return terminal.CreateSpec{Alias: e.Alias, PinnedTitle: e.PinnedTitle}, true
			})
		}
	}

	var last []byte
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for range tick.C {
		data, err := json.Marshal(m.CaptureSessionSet(func(terminal.SessionInfo) string { return "" }))
		if err != nil || bytes.Equal(data, last) {
			continue
		}
		if store.Save(data) == nil {
			last = data
		}
	}
}
