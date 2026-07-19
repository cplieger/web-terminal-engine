// Standalone module for the typed-framing e2e test server: lives under web/
// (which web/go.mod deliberately carves out of the root Go module) and pulls
// the engine in via a local replace, so `go test ./...` at the repo root
// never builds or ships it while the Playwright e2e can `go run .` here.
module testserver

go 1.26.5

require github.com/cplieger/web-terminal-engine/v3 v3.0.0

require (
	github.com/coder/websocket v1.8.15 // indirect
	github.com/creack/pty v1.1.24 // indirect
)

replace github.com/cplieger/web-terminal-engine/v3 => ../../..
