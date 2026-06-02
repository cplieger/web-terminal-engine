// Package terminal bridges a PTY to a browser WebSocket.
//
// Each WS connection spawns the configured command in its own PTY and
// pipes bytes both ways. Server-side state is kept in the VT screen;
// on reconnect the current cell snapshot is replayed to the new client.
// No external multiplexer is involved — the VT emulator IS the
// persistence layer.
//
// Wire protocol (binary WebSocket frames):
//
//	client → server: raw terminal input bytes
//	server → client: raw PTY output bytes
//	client → server: JSON control messages prefixed with 0x00:
//	  {"type":"resize","cols":N,"rows":N}
//
// The 0x00 prefix byte distinguishes control messages from raw
// input; no valid terminal input starts with NUL.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/cplieger/vterm/vt"
)

const (
	wsReadLimit   = 64 * 1024
	ptyReadBuf    = 4096
	defaultCols   = 120
	defaultRows   = 30
	flushInterval = 50 * time.Millisecond

	// minResizeCols/minResizeRows are the smallest dimensions we
	// accept from a resize control message. Anything below is floored
	// up rather than dropped — iPad keyboard slide reports near-zero
	// during animations and we want the start path to fire even if
	// the first resize comes from such a frame.
	minResizeCols = 20
	minResizeRows = 5

	// flushLoop emits in a single binary frame. /chat-load on the
	// server-side terminal can scroll hundreds of rows in a single
	// flush window; without a cap the wire frame grew to multi-MB on
	// slow links and the iPad client choked. Spreading the lines
	// across successive flushes keeps each frame small and the iPad
	// renderer paced.

	// flushes. If a flush hold accumulates lines faster than we can
	// oldest rather than letting the queue grow without bound.

	ctlTypeResize = "resize"
	ctlTypeResume = "resume"

	// scrollbackCapacity is the number of scrollback lines the server
	// retains for replay to new/reconnecting clients. Matches the
	// client's MAX_HISTORY so a full page refresh recovers all history
	// the client would have kept anyway.
	scrollbackCapacity = 1000
)

// Option configures optional behavior of the Handler.
type Option func(*handlerConfig)

// discardHandler is a slog.Handler that discards all log records.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// handlerConfig holds optional configuration applied via functional options.
type handlerConfig struct {
	logger             *slog.Logger
	acceptOptions      *websocket.AcceptOptions
	onProcessExit      func(error)
	workDir            string
	env                []string
	scrollbackCapacity int
}

// WithWorkDir sets the working directory for the spawned process.
func WithWorkDir(dir string) Option {
	return func(c *handlerConfig) { c.workDir = dir }
}

// WithLogger injects a structured logger; nil disables logging.
func WithLogger(l *slog.Logger) Option {
	return func(c *handlerConfig) {
		if l == nil {
			// A nil *slog.Logger panics on method calls; use a discard handler.
			l = slog.New(discardHandler{})
		}
		c.logger = l
	}
}

// WithEnv sets additional environment variables for the spawned process.
func WithEnv(env []string) Option {
	return func(c *handlerConfig) { c.env = env }
}

// WithScrollbackCapacity sets the number of scrollback lines retained
// for replay to reconnecting clients. Default is 1000. Negative values
// are treated as 0 (scrollback disabled).
func WithScrollbackCapacity(n int) Option {
	return func(c *handlerConfig) {
		if n < 0 {
			n = 0
		}
		c.scrollbackCapacity = n
	}
}

// WithAcceptOptions sets WebSocket accept options (e.g. allowed origins).
func WithAcceptOptions(o *websocket.AcceptOptions) Option {
	return func(c *handlerConfig) { c.acceptOptions = o }
}

// WithOnProcessExit registers a callback invoked when the child process exits.
func WithOnProcessExit(fn func(error)) Option {
	return func(c *handlerConfig) { c.onProcessExit = fn }
}

// sessionState persists across WS reconnects for the same logical
// client. The client identifies its session via the resume control
// message; the server uses sessionState.bytesReceived as the ack value
// to send back, which the client compares to its sent count to
// determine which bytes (if any) need retransmission after a blip.
type sessionState struct {
	lastSeen           time.Time
	bytesReceived      uint64
	replayedScrollback bool // true after first resume; prevents re-replay on reconnect
}

// clientState tracks per-WS-connection state. session is resolved
// from the sessionId in the resume control message. session is
// stored as an atomic pointer so flushLoop's snapshot pass can read
// it without the handler-wide mutex (snapshot copies the pointer
// value into a local; sessionState mutations stay under h.mu).
type ClientState struct {
	session atomic.Pointer[sessionState]
}

// Handler serves /ws and tracks shared screen state. Multiple WS clients
// can attach concurrently; the VT screen is the session state.
//
// h.started is atomic.Bool so the fast-path check in handleWS does not
// race with ensureStarted's write under h.mu. Screen and PTY state is
// guarded by h.mu; client tracking lives in the ClientRegistry with its
// own lock. flushLoop snapshots the per-flush data under h.mu and then
// performs ws.Write outside the lock so a slow client can't block
// readLoop / handleControl / new handleWS connections.
type Handler struct {
	ptmx       *os.File
	cmd        *exec.Cmd
	screen     *vt.Screen
	registry   *ClientRegistry
	builder    *FlushFrameBuilder
	scrollback *scrollbackRing
	cancel     context.CancelFunc
	command    []string
	rawRing    []byte
	cfg        handlerConfig
	bootEpoch  int64
	mu         sync.Mutex
	started    atomic.Bool
	resized    bool
}

// NewHandler returns a terminal handler. command is the argv to spawn
// (required, must be non-empty). Optional behavior is configured via
// functional Option values.
func NewHandler(command []string, opts ...Option) *Handler {
	cfg := handlerConfig{
		scrollbackCapacity: scrollbackCapacity,
		logger:             slog.Default(),
	}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	return &Handler{
		command:    command,
		cfg:        cfg,
		screen:     vt.New(defaultRows, defaultCols),
		registry:   NewClientRegistry(),
		builder:    &FlushFrameBuilder{},
		scrollback: newScrollbackRing(cfg.scrollbackCapacity),
		bootEpoch:  time.Now().UnixNano(),
	}
}

// RegisterRoutes wires /ws on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", h.handleWS)
	mux.HandleFunc("/debug/screen", h.debugScreen)
	mux.HandleFunc("/debug/raw", h.debugRaw)
}

// ServeHTTP implements http.Handler, delegating to the WebSocket handler.
// Used by the host application to wire the terminal as an http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleWS(w, r)
}

// Shutdown cancels the readLoop and flushLoop goroutines and closes
// the PTY. Safe to call even if the process was never started.
func (h *Handler) Shutdown() {
	if !h.started.Load() {
		return
	}
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Lock()
	if h.ptmx != nil {
		_ = h.ptmx.Close() // best-effort during shutdown
	}
	h.mu.Unlock()
}

func (h *Handler) debugRaw(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	raw := make([]byte, len(h.rawRing))
	copy(raw, h.rawRing)
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=pty-raw.bin")
	w.Write(raw) //nolint:errcheck // best-effort debug write
}

func (h *Handler) debugScreen(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	row, col := h.screen.CursorPos()
	fmt.Fprintf(w, "cursor: row=%d col=%d  screen: %dx%d  held=%v alt=%v\n",
		row, col, h.screen.Height, h.screen.Width, h.screen.IsFlushHeld(), h.screen.InAltScreen)
	for y := range h.screen.Cells {
		fmt.Fprintf(w, "%3d: %s\n", y, h.screen.RowString(y))
	}
}

// ensureStarted spawns the process if not already running, sized at
// the given dimensions. cols/rows ≤ 0 fall back to defaults so callers
// who don't yet know the client size can still start the process.
// Idempotent: concurrent callers all see started==true after the
// first returns; cols/rows on subsequent calls are ignored.
func (h *Handler) ensureStarted(cols, rows int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started.Load() {
		return nil
	}
	if len(h.command) == 0 {
		return errors.New("terminal: empty command")
	}
	cmd := exec.CommandContext(context.Background(), h.command[0], h.command[1:]...) // #nosec G204
	cmd.Dir = h.cfg.workDir
	env := append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	if len(h.cfg.env) > 0 {
		env = append(env, h.cfg.env...)
	}
	cmd.Env = env
	if cols < 1 {
		cols = defaultCols
	}
	if rows < 1 {
		rows = defaultRows
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})

	if err != nil {
		return err
	}
	h.ptmx = ptmx
	h.cmd = cmd
	h.started.Store(true)
	h.resized = true
	h.screen.Resize(rows, cols)
	h.cfg.logger.Info("terminal: process started",
		"pid", cmd.Process.Pid, "command", h.command, "cols", cols, "rows", rows)

	// PTY reader goroutine — feeds VT screen and notifies clients.
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go h.readLoop(ctx)
	// Flush ticker — sends screen updates to all clients.
	go h.flushLoop(ctx)
	return nil
}

func (h *Handler) readLoop(ctx context.Context) {
	buf := make([]byte, ptyReadBuf)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := h.ptmx.Read(buf)
		if n > 0 {
			// Capture raw bytes for debugging (last 16KB).
			h.mu.Lock()
			h.rawRing = append(h.rawRing, buf[:n]...)
			if len(h.rawRing) > 16384 {
				h.rawRing = h.rawRing[len(h.rawRing)-16384:]
			}
			h.screen.Write(buf[:n]) //nolint:errcheck // screen.Write always returns nil
			if len(h.screen.Response) > 0 {
				h.ptmx.Write(h.screen.Response) //nolint:errcheck // best-effort
				h.screen.Response = nil
			}
			h.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// flushFrame is the per-flush snapshot built under h.mu and consumed
// outside the lock. Holding the lock during the network write would
// stall every other goroutine on a slow client; the snapshot pattern
// keeps the lock window bounded to local memory work.
type FlushFrame struct {
	clients      map[*websocket.Conn]uint64
	rows         [][]vt.WireRun
	scrollLines  [][]vt.WireRun
	changed      []int
	modesPayload []byte
	titlePayload []byte
	curRow       int
	curCol       int
	screenHeight int
	cursorStyle  uint8
	cursorHidden bool
	cursorBlink  bool
	bell         bool
}

// buildFrame computes the next outbound frame under h.mu. Returns nil
// if there is nothing to send (no resize yet, flush held, or no
// changed rows and no scroll lines).
func (h *Handler) buildFrame() *FlushFrame {
	h.mu.Lock()
	clients := h.registry.Snapshot()
	frame := h.builder.Build(h.screen, h.resized, clients)
	if frame != nil && len(frame.scrollLines) > 0 {
		h.scrollback.Append(frame.scrollLines)
	}
	h.mu.Unlock()
	return frame
}

func (h *Handler) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		frame := h.buildFrame()
		if frame == nil {
			continue
		}

		if len(frame.changed) > 0 || len(frame.scrollLines) > 0 {
			h.cfg.logger.Info("terminal: flush",
				"changed", len(frame.changed),
				"scroll_lines", len(frame.scrollLines),
				"clients", len(frame.clients))
		}

		// Pre-encode payloads once; identical bytes for every client.
		var screenPayload, scrollPayload []byte
		if len(frame.changed) > 0 {
			screenPayload = encodeScreenMsg(frame.screenHeight, frame.curRow, frame.curCol,
				0, frame.changed, frame.rows, frame.cursorStyle, frame.cursorHidden, frame.cursorBlink, frame.bell)
		}
		if len(frame.scrollLines) > 0 {
			scrollPayload = encodeScrollMsg(0, frame.scrollLines)
		}

		// Send to all connected clients as binary frames. The lock is
		// NOT held here — a slow client only blocks itself, not other
		// clients or readLoop / handleControl / new handleWS.
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for ws, ack := range frame.clients {
			if frame.modesPayload != nil {
				ws.Write(writeCtx, websocket.MessageBinary, withClientAck(frame.modesPayload, ack)) //nolint:errcheck // best-effort
			}
			if screenPayload != nil {
				ws.Write(writeCtx, websocket.MessageBinary, withClientAck(screenPayload, ack)) //nolint:errcheck // best-effort
			}
			if scrollPayload != nil {
				ws.Write(writeCtx, websocket.MessageBinary, withClientAck(scrollPayload, ack)) //nolint:errcheck // best-effort
			}
		}
		cancel()
	}
}

// controlMsg is a JSON control message from the client.
type controlMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	SentBytes uint64 `json:"sentBytes,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	// ScrollbackHave is the number of scrollback rows the client
	// already has in DOM. Sent in resume control messages so the
	// server can replay only rows the client is missing — handles
	// iOS Safari DOM eviction (where sessionStorage preserves the
	// sessionId but the page reloads with empty DOM) without
	// duplicating scrollback when the DOM survived (e.g. brief WS
	// drop during the same page lifetime). Zero means "send me the
	// full scrollback". Negative or oversize values are treated as
	// zero by the server's clamp in handleResume.
	ScrollbackHave int `json:"scrollbackHave,omitempty"`
}

// handleWS upgrades to WebSocket, spawns the configured command in a
// PTY, and bridges bytes both ways until either side closes.
func (h *Handler) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, h.cfg.acceptOptions)
	if err != nil {
		h.cfg.logger.Warn("terminal: ws accept", "error", err)
		return
	}
	ws.SetReadLimit(wsReadLimit)

	// Note: the child process is preferably started on the first resize message so it
	// boots at the correct dimensions. As a fallback we still call ensureStarted
	// here in case the client never sends a resize (e.g. tests).

	// Register this client.
	state := h.registry.Add(ws)
	// Force the next flush to send all rows so this client sees the
	// current screen, even if no resize is sent.
	h.mu.Lock()
	h.builder.Reset()
	h.mu.Unlock()

	defer func() {
		h.registry.Remove(ws)
		ws.Close(websocket.StatusNormalClosure, "") // #nosec G104 -- best-effort
	}()

	// Cancellable context tied to the client's request — pingLoop
	// will cancel it if the WS becomes unresponsive (Jacobson/Karels
	// RTO timeout). The read loop below exits when ctx is canceled
	// because ws.Read() honors ctx cancellation.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go pingLoop(ctx, cancel, ws)

	// Read input from this client and write to the shared PTY.
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		if msg[0] == 0x00 {
			h.handleControl(ws, state, msg[1:])
			continue
		}
		// Ensure process is started (fallback if no resize was sent).
		// h.started is atomic.Bool so the fast-path read does not race
		// with ensureStarted's write. cols/rows of 0 select defaults.
		if !h.started.Load() {
			if err := h.ensureStarted(0, 0); err != nil {
				h.cfg.logger.Error("terminal: process start failed", "error", err)
				return
			}
		}
		if _, err := h.ptmx.Write(msg); err != nil {
			h.cfg.logger.Debug("terminal: pty write", "error", err)
			return
		}
		// Increment session bytesReceived for the resume protocol.
		// state.session is set when the client sends its first resume
		// control message; without it we silently skip — the client is
		// either not using the protocol or hasn't initialized yet.
		h.registry.IncrementReceived(state, len(msg))
	}
}

func (h *Handler) handleControl(ws *websocket.Conn, state *ClientState, payload []byte) {
	var c controlMsg
	if err := json.Unmarshal(payload, &c); err != nil {
		return
	}
	if c.Type == ctlTypeResume && c.SessionID != "" {
		h.handleResume(ws, state, c.SessionID, c.ScrollbackHave)
		return
	}
	if c.Type == ctlTypeResize {
		h.handleResize(c.Cols, c.Rows)
	}
}

// handleResume looks up or creates the session for sessionID, attaches
// it to state, replies with a resumeAck carrying the server's current
// bytesReceived count, and opportunistically GCs idle sessions.
//
// scrollbackHave is the number of scrollback rows the client says it
// already has in DOM (zero on cold load / iOS DOM eviction). The
// server replays rows[scrollbackHave:end] so the client gets the
// missing tail without duplicating rows it still has.
func (h *Handler) handleResume(ws *websocket.Conn, state *ClientState, sessionID string, scrollbackHave int) {
	ack, needsReplay := h.registry.ResolveSession(state, sessionID)
	// Force a full repaint on the next flush so the resuming client
	// sees the current screen state rather than diffing against a
	// `prevRowWires` it never saw (the prior client may have received
	// frames after this one disconnected, or the screen may have
	// updated while there were no clients connected at all).
	h.mu.Lock()
	h.builder.Reset()
	// Discard any scrollback that accumulated while the client was
	// disconnected — it's already stored in the ring buffer and will
	// be replayed below. Sending it via the normal flush path would
	// duplicate lines the replay already delivered.
	h.screen.DrainScrollback()
	// Replay scrollback the client doesn't have. needsReplay is true
	// on the first resume per sessionState; we additionally honour the
	// client-reported scrollbackHave so iOS Safari's "evict the page,
	// reload, sessionId still present" cycle gets a fresh replay
	// instead of the previous "we already replayed once for this
	// session, never again" short-circuit which left the user staring
	// at only the current screen with the start of long responses
	// scrolled past where the DOM ends.
	var scrollLines [][]vt.WireRun
	all := h.scrollback.Lines()
	if needsReplay || scrollbackHave < len(all) {
		// Clamp scrollbackHave into [0, len(all)] in case of bogus
		// or stale client values.
		start := min(max(scrollbackHave, 0), len(all))
		scrollLines = all[start:]
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Send resumeAck first so the client can trim its outbox.
	ws.Write(ctx, websocket.MessageBinary, encodeResumeAck(ack, h.bootEpoch)) //nolint:errcheck // best-effort

	// Replay the scrollback ring so the client has full history even
	// after a page refresh. Send in chunks to keep individual frames
	// small on slow links.
	const replayChunk = 50
	for i := 0; i < len(scrollLines); i += replayChunk {
		end := min(i+replayChunk, len(scrollLines))
		payload := encodeScrollMsg(ack, scrollLines[i:end])
		ws.Write(ctx, websocket.MessageBinary, payload) //nolint:errcheck // best-effort
	}
}

// handleResize floors the requested dimensions to a sane minimum and
// applies the resize. Floored (rather than dropped) so a near-zero
// reading from an iPad keyboard-slide animation still drives
// ensureStarted on first connect — dropping the resize would leave the
// process unstarted until the client sent raw input.
func (h *Handler) handleResize(cols, rows int) {
	if cols > 0xFFFF {
		cols = 0xFFFF
	}
	if rows > 0xFFFF {
		rows = 0xFFFF
	}
	if cols < minResizeCols {
		cols = minResizeCols
	}
	if rows < minResizeRows {
		rows = minResizeRows
	}
	// Start the child process on first resize so it knows the correct dimensions
	// from the start (avoids initial paint at wrong size).
	if !h.started.Load() {
		if err := h.ensureStarted(cols, rows); err != nil {
			h.cfg.logger.Error("terminal: process start failed", "error", err)
			return
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := pty.Setsize(h.ptmx, &pty.Winsize{
		Cols: uint16(cols), Rows: uint16(rows),
	}); err != nil {
		h.cfg.logger.Debug("terminal: resize", "error", err)
	}
	h.screen.Resize(rows, cols)
	h.screen.DrainScrollback() // discard pre-resize drain
	// Hold flushes during the SIGWINCH redraw window so the user
	// doesn't see the child process's transient cleared-screen state. Either
	// the child process's CSI ?2026l or the 1s deadline releases the hold.
	h.screen.HoldFlush(time.Now().Add(time.Second))
	h.cfg.logger.Info("terminal: resize received", "rows", rows, "cols", cols)
	h.resized = true
	h.builder.Reset()
}

// runsEqual compares two slices of WireRun for equality.
func runsEqual(a, b []vt.WireRun) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
