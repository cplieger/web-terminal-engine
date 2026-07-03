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
//	server → client: binary frames encoding screen/scroll/modes/title/
//	                 resumeAck/pong messages (see wire_binary.go) — PTY
//	                 output is rendered into the VT screen and sent as
//	                 absolute-indexed cell runs, not as raw bytes
//	client → server: JSON control messages prefixed with 0x00:
//	  {"type":"resize",...}, {"type":"resume",...}, {"type":"ping"}
//
// The 0x00 prefix byte distinguishes control messages from raw
// input; no valid terminal input starts with NUL.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/cplieger/web-terminal-engine/v2/vt"
	"github.com/creack/pty"
)

const (
	wsReadLimit   = 64 * 1024
	ptyReadBuf    = 4096
	defaultCols   = 120
	defaultRows   = 30
	flushInterval = 50 * time.Millisecond

	// statusProcessExited (4001) is the WebSocket close code the terminal WS
	// uses when the child process exits, so a client can tell a dead session
	// (the tab should close) apart from a transient disconnect (reconnect).
	// 4001 is in the private application close-code range (4000-4999).
	statusProcessExited websocket.StatusCode = 4001

	// maxScrollLinesPerFrame bounds the lines packed into one scroll frame so the wire
	// num_lines (a uint16) can never be exceeded by the payload and a large drained burst
	// (a fast child can produce far more than 65535 lines in one 50ms flush) is split into
	// several < ~100KB frames instead of one multi-MB message. Mirrors handleResume's
	// replayChunk. Any value well under 65535 works; 1000 keeps each frame small.
	maxScrollLinesPerFrame = 1000

	// minResizeCols/minResizeRows are the smallest dimensions we
	// accept from a resize control message. Anything below is floored
	// up rather than dropped — iPad keyboard slide reports near-zero
	// during animations and we want the start path to fire even if
	// the first resize comes from such a frame.
	minResizeCols = 20
	minResizeRows = 5

	ctlTypeResize = "resize"
	ctlTypeResume = "resume"
	ctlTypePing   = "ping"

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
	theme              *vt.Theme
	workDir            string
	env                []string
	scrollbackCapacity int
	keepUnfocused      bool
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
		c.scrollbackCapacity = max(n, 0)
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

// WithKeepUnfocused makes the server hold the child process in the "unfocused"
// state for DEC 1004 focus reporting: whenever the process enables focus
// reporting (CSI ?1004h), the server writes a focus-out (ESC [ O) to its PTY,
// and it never writes a focus-in. A process that gates behavior on focus (for
// example kiro-cli, which only emits its OSC 9 turn/permission notifications
// while it believes it is unfocused) then keeps emitting, so the session
// manager's status classifier can observe those notifications. Off by default:
// a generic terminal wants real focus reporting (vim, etc.), so only a consumer
// that relies on the unfocused-notifier behavior enables it. The browser client
// is expected to emit no focus bytes of its own under this model.
func WithKeepUnfocused() Option {
	return func(c *handlerConfig) { c.keepUnfocused = true }
}

// WithTheme sets the default foreground, background, and cursor colors the
// terminal reports to programs via OSC 10/11/12 queries (and restores on
// OSC 110/111/112 reset). Pass the colors your client actually renders so
// color-probing apps — light/dark detection, "reset to default" — see the real
// theme. Defaults to vt.DefaultTheme (a dark scheme). Build colors with vt.RGB.
func WithTheme(t vt.Theme) Option {
	return func(c *handlerConfig) { c.theme = &t }
}

// sessionState persists across WS reconnects for the same logical
// client. The client identifies its session via the resume control
// message; the server uses sessionState.bytesReceived as the ack value
// to send back, which the client compares to its sent count to
// determine which bytes (if any) need retransmission after a blip.
type sessionState struct {
	lastSeen      time.Time
	bytesReceived uint64
}

// clientState tracks per-WS-connection state. session is resolved
// from the sessionId in the resume control message. session is stored
// as an atomic.Pointer so IncrementReceived can test whether a session
// is attached without taking registry.mu; the pointed-to sessionState's
// fields are guarded by the clientRegistry's mutex (registry.mu), not h.mu.
type clientState struct {
	session atomic.Pointer[sessionState]
}

// Handler serves /ws and tracks shared screen state. Multiple WS clients
// can attach concurrently; the VT screen is the session state.
//
// h.started is atomic.Bool so the fast-path check in handleWS does not
// race with ensureStarted's write under h.mu. Screen and PTY state is
// guarded by h.mu; client tracking lives in the clientRegistry with its
// own lock. flushLoop snapshots the per-flush data under h.mu and then
// performs ws.Write outside the lock so a slow client can't block
// readLoop / handleControl / new handleWS connections.
type Handler struct {
	cmd                      *exec.Cmd
	screen                   *vt.Screen
	registry                 *clientRegistry
	builder                  *flushFrameBuilder
	scrollback               *scrollbackRing
	cancel                   context.CancelFunc
	ptmx                     *os.File
	procExitCh               chan struct{}
	pendingClipboard         []byte
	command                  []string
	cfg                      handlerConfig
	bootEpoch                int64
	lastActivity             atomic.Int64
	mu                       sync.Mutex
	started                  atomic.Bool
	resized                  bool
	scrollbackClearedPending bool
	paletteChangedPending    bool
	lastFocusReporting       bool
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
	var vtOpts []vt.Option
	if cfg.theme != nil {
		vtOpts = append(vtOpts, vt.WithTheme(*cfg.theme))
	}
	return &Handler{
		command:    command,
		cfg:        cfg,
		screen:     vt.New(defaultRows, defaultCols, vtOpts...),
		registry:   newClientRegistry(cfg.logger),
		builder:    &flushFrameBuilder{},
		scrollback: newScrollbackRing(cfg.scrollbackCapacity),
		bootEpoch:  time.Now().UnixNano(),
		procExitCh: make(chan struct{}),
	}
}

// RegisterRoutes wires /ws on mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", h.handleWS)
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	if h.ptmx != nil {
		_ = h.ptmx.Close() // best-effort during shutdown
	}
}

// Title returns the current window title (set by the process via OSC 0/2), for
// a session manager or UI to label the session. Empty until the process sets a
// title. Safe for concurrent use; read under the same lock that guards the VT
// screen.
func (h *Handler) Title() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.Title
}

// Exited reports whether the child process has exited. Non-blocking and
// race-free (procExitCh is closed exactly once, by the process monitor). False
// for a handler whose process was never started.
func (h *Handler) Exited() bool {
	select {
	case <-h.procExitCh:
		return true
	default:
		return false
	}
}

// LastActivity returns the time of the most recent PTY output, or the zero time
// if the process has produced nothing yet. The status stream uses it to derive
// working (recent output) vs idle (quiescent). Lock-free.
func (h *Handler) LastActivity() time.Time {
	ns := h.lastActivity.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Notification returns the last OSC 9 notification message and its sequence
// number (vt.Screen.NotificationSeq). A reader detects a fresh notification when
// the sequence advances, even if the message text repeats. Safe for concurrent
// use.
func (h *Handler) Notification() (msg string, seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.Notification, h.screen.NotificationSeq
}

// Progress returns the session's last ConEmu OSC 9;4 progress state: -1 when
// none has been seen (the process never reported progress), else the state
// (0 off, 1 value, 2 error, 3 indeterminate, 4 paused). The status stream maps
// an active state (1 or 3) to working, so a progress-reporting program (kiro-cli
// while the agent works) drives the working indicator without relying on raw
// output activity. Safe for concurrent use.
func (h *Handler) Progress() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.Progress
}

// StartEager starts the child process now at a default size, rather than lazily
// on the first client message. A session manager calls this at Create time so a
// new session's process (and its activity signal) exist from creation; the first
// client attach still sends the real resize. Idempotent.
func (h *Handler) StartEager() error {
	return h.ensureStarted(0, 0)
}

// exitAwareCloseCode returns statusProcessExited (4001) when the child process
// has exited (procExitCh is closed), otherwise a normal closure. The
// non-blocking receive is race-free: channel operations synchronize and a
// closed channel is always ready.
func (h *Handler) exitAwareCloseCode() websocket.StatusCode {
	select {
	case <-h.procExitCh:
		return statusProcessExited
	default:
		return websocket.StatusNormalClosure
	}
}

// closeOnProcExit closes the client WS with statusProcessExited (4001) when the
// child process exits, so the client can tell "process ended" (terminal, close
// the tab) from a transient drop (reconnect). Canceling the read's context
// instead would make coder/websocket fail the connection abnormally (1006)
// rather than send a clean 4001, so this closes the socket directly;
// coder/websocket permits Close concurrent with the read loop's Read. It also
// returns when the client leaves (ctx done), so it never leaks; if the process
// already exited, procExitCh is closed and it closes immediately.
func (h *Handler) closeOnProcExit(ctx context.Context, ws *websocket.Conn) {
	select {
	case <-ctx.Done():
	case <-h.procExitCh:
		ws.Close(statusProcessExited, "") // #nosec G104 -- best-effort
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
	env = append(env, h.cfg.env...)
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
	// Process monitor — reaps the child (so it does not linger as a
	// zombie), fires the documented onProcessExit callback with the
	// exit status, and cancels the read/flush loops on natural child
	// exit so flushLoop's ticker does not leak after the process dies.
	go func() {
		werr := cmd.Wait() // reap; werr carries the exit status
		// Symmetric with the "process started" INFO above so operators see the
		// session lifecycle end and its exit status in the logs, not just the
		// start. werr is nil on a clean (exit 0) shutdown; a child exiting
		// non-zero is a normal session end, not a server fault, so this stays
		// INFO (avoids WARN-spam on ordinary command exits).
		h.cfg.logger.Info("terminal: process exited",
			"pid", cmd.Process.Pid, "error", werr)
		if h.cfg.onProcessExit != nil {
			h.cfg.onProcessExit(werr)
		}
		// Broadcast process exit so attached WS clients close with
		// statusProcessExited (4001) rather than a normal closure. Closed
		// exactly once: this monitor goroutine runs once per handler.
		close(h.procExitCh)
		cancel() // stop readLoop/flushLoop on child exit
	}()
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
			h.handlePTYData(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// focusOutSeq is the DEC 1004 focus-out report (ESC [ O). Written to the PTY
// under WithKeepUnfocused when the process enables focus reporting.
var focusOutSeq = []byte("\x1b[O")

// focusOutOnEnable returns focusOutSeq when WithKeepUnfocused is set and focus
// reporting just rose from disabled to enabled since the last call, else nil. It
// updates the tracked last state, so it fires once per enable edge (a process
// that toggles 1004 off then on is re-pinned to unfocused). The caller holds
// h.mu and writes the returned bytes to the PTY outside the lock.
func (h *Handler) focusOutOnEnable() []byte {
	if !h.cfg.keepUnfocused {
		return nil
	}
	now := h.screen.FocusReporting
	rising := now && !h.lastFocusReporting
	h.lastFocusReporting = now
	if rising {
		return focusOutSeq
	}
	return nil
}

// handlePTYData feeds raw PTY output to the screen under h.mu and writes
// any query response back outside the lock so a slow write never stalls
// goroutines waiting on h.mu.
func (h *Handler) handlePTYData(data []byte) {
	h.lastActivity.Store(time.Now().UnixNano())
	var resp []byte
	h.mu.Lock()
	h.screen.Write(data) //nolint:errcheck // screen.Write always returns nil
	if h.screen.ScrollbackCleared {
		// ED3 (erase scrollback): the app discarded its saved lines (kiro-cli
		// does this on every resize redraw). Clear the retained ring to match a
		// real terminal — Clear preserves committed so absolute indices stay
		// monotonic — and flag the next frame to tell clients to drop history.
		h.screen.ScrollbackCleared = false
		h.scrollback.Clear()
		h.scrollbackClearedPending = true
	}
	if h.screen.PaletteChanged {
		// OSC 4/104 changed the palette; defer a full repaint to the next frame.
		h.screen.PaletteChanged = false
		h.paletteChangedPending = true
	}
	if len(h.screen.PendingClipboard) > 0 {
		// OSC 52 copy; hand it to the next frame as a clipboard message.
		h.pendingClipboard = h.screen.PendingClipboard
		h.screen.PendingClipboard = nil
	}
	if len(h.screen.Response) > 0 {
		resp = h.screen.Response
		h.screen.Response = nil
	}
	// Keep-unfocused: if the process just enabled focus reporting, pin it to
	// unfocused so a focus-gated notifier keeps emitting (see WithKeepUnfocused).
	if fo := h.focusOutOnEnable(); fo != nil {
		resp = append(resp, fo...)
	}
	h.mu.Unlock()
	if len(resp) > 0 {
		h.ptmx.Write(resp) //nolint:errcheck // best-effort
	}
}

// flushFrame is the per-flush snapshot built under h.mu and consumed
// outside the lock. Holding the lock during the network write would
// stall every other goroutine on a slow client; the snapshot pattern
// keeps the lock window bounded to local memory work.
type flushFrame struct {
	clients          map[*websocket.Conn]uint64
	rows             [][]vt.WireRun
	scrollLines      [][]vt.WireRun
	changed          []int
	modesPayload     []byte
	titlePayload     []byte
	clipboardPayload []byte
	base             uint64 // absolute index of the top screen row (changed[y] -> base+y)
	scrollFirstIdx   uint64 // absolute index of scrollLines[0]
	curRow           int
	curCol           int
	screenHeight     int
	cursorStyle      uint8
	cursorHidden     bool
	cursorBlink      bool
	altActive        bool
	bell             bool
	// scrollbackCleared signals the client to drop its scrollback history
	// (all indices below base) because the app issued ED3 (erase scrollback).
	scrollbackCleared bool
}

// buildFrame computes the next outbound frame under h.mu. Returns nil
// if there is nothing to send (no resize yet, flush held, or no
// changed rows and no scroll lines).
func (h *Handler) buildFrame() *flushFrame {
	h.mu.Lock()
	// An OSC 4/104 palette change re-colors already-drawn cells; force a full
	// repaint so every visible row re-resolves through the new palette. The
	// Reset persists if Build produces no frame this tick (flush held / no
	// resize yet), so the repaint still lands on the next frame.
	if h.paletteChangedPending {
		h.builder.Reset()
		h.paletteChangedPending = false
	}
	clients := h.registry.Snapshot()
	committedBefore := h.scrollback.Committed()
	frame := h.builder.Build(h.screen, h.resized, clients, committedBefore)
	if frame != nil && len(frame.scrollLines) > 0 {
		h.scrollback.Append(frame.scrollLines)
	}
	if frame != nil && h.scrollbackClearedPending {
		frame.scrollbackCleared = true
		h.scrollbackClearedPending = false
	}
	// OSC 52 clipboard is a one-shot event that can arrive with no screen
	// change, so ensure it rides a frame even when Build produced none.
	if len(h.pendingClipboard) > 0 {
		if frame == nil {
			frame = &flushFrame{clients: clients}
		}
		frame.clipboardPayload = encodeClipboardMsg(0, h.pendingClipboard)
		h.pendingClipboard = nil
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

		if frame := h.buildFrame(); frame != nil {
			h.dispatchFrame(frame)
		}
	}
}

// dispatchFrame encodes a frame's payloads once and fans them out to
// every connected client as binary WebSocket frames. It is called from
// flushLoop with h.mu NOT held — a slow client only blocks itself, not
// readLoop / handleControl / new handleWS connections. Extracted from
// flushLoop so that select-loop stays small and readable.
func (h *Handler) dispatchFrame(frame *flushFrame) {
	if len(frame.changed) > 0 || len(frame.scrollLines) > 0 {
		h.cfg.logger.Debug("terminal: flush",
			"changed", len(frame.changed),
			"scroll_lines", len(frame.scrollLines),
			"clients", len(frame.clients))
	}

	// Pre-encode payloads once; identical bytes for every client.
	var screenPayload []byte
	if len(frame.changed) > 0 {
		screenPayload = encodeScreenMsg(frame.base, frame.screenHeight, frame.curRow, frame.curCol,
			0, frame.changed, frame.rows, frame.cursorStyle, frame.cursorHidden, frame.cursorBlink, frame.bell, frame.altActive, frame.scrollbackCleared)
	}
	// Split a large drained burst across several frames so num_lines never overflows the
	// uint16 count and no single frame reaches multiple MB. Each chunk keeps its absolute
	// firstIndex, so the client applies every line at the right index (idempotent), exactly
	// as handleResume's chunked replay does.
	var scrollPayloads [][]byte
	for i := 0; i < len(frame.scrollLines); i += maxScrollLinesPerFrame {
		end := min(i+maxScrollLinesPerFrame, len(frame.scrollLines))
		scrollPayloads = append(scrollPayloads,
			encodeScrollMsg(0, frame.scrollFirstIdx+uint64(i), frame.scrollLines[i:end]))
	}

	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ws, ack := range frame.clients {
		if frame.modesPayload != nil {
			ws.Write(writeCtx, websocket.MessageBinary, withClientAck(frame.modesPayload, ack)) //nolint:errcheck // best-effort
		}
		if frame.titlePayload != nil {
			ws.Write(writeCtx, websocket.MessageBinary, withClientAck(frame.titlePayload, ack)) //nolint:errcheck // best-effort
		}
		if frame.clipboardPayload != nil {
			ws.Write(writeCtx, websocket.MessageBinary, withClientAck(frame.clipboardPayload, ack)) //nolint:errcheck // best-effort
		}
		if screenPayload != nil {
			ws.Write(writeCtx, websocket.MessageBinary, withClientAck(screenPayload, ack)) //nolint:errcheck // best-effort
		}
		for _, sp := range scrollPayloads {
			ws.Write(writeCtx, websocket.MessageBinary, withClientAck(sp, ack)) //nolint:errcheck // best-effort
		}
	}
}

// controlMsg is a JSON control message from the client.
type controlMsg struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId,omitempty"`
	SentBytes uint64 `json:"sentBytes,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	// HaveThrough is the highest absolute line index the client already
	// holds in its store. Sent in resume control messages so the server
	// replays exactly the lines the client is missing (indices greater
	// than HaveThrough), aligned by absolute identity rather than by a
	// fragile count. -1 means the client holds nothing (cold load / DOM
	// eviction) and wants the full retained history. The server clamps
	// the replay start into the retained range and reports any eviction
	// gap via the resumeAck bounds.
	HaveThrough int64 `json:"haveThrough"`
	// ProtocolVersion is the client's wire-protocol revision (resume only).
	// 0 = unset (an older client that predates the field). A non-zero value
	// differing from wireProtocolVersion means the client was built against a
	// different protocol revision; handleControl logs a warning so the skew
	// is visible rather than surfacing as a silent mis-decode.
	ProtocolVersion int `json:"protocolVersion,omitempty"`
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
		ws.Close(h.exitAwareCloseCode(), "") // #nosec G104 -- best-effort
	}()

	// Cancellable context tied to the client's request — pingLoop
	// will cancel it if the WS becomes unresponsive (Jacobson/Karels
	// RTO timeout). The read loop below exits when ctx is canceled
	// because ws.Read() honors ctx cancellation.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go pingLoop(ctx, cancel, ws, h.cfg.logger)

	// Close promptly with 4001 when the child process exits, so the client can
	// distinguish "process ended" from a transient drop (see closeOnProcExit
	// and exitAwareCloseCode).
	go h.closeOnProcExit(ctx, ws)

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

func (h *Handler) handleControl(ws *websocket.Conn, state *clientState, payload []byte) {
	var c controlMsg
	if err := json.Unmarshal(payload, &c); err != nil {
		h.cfg.logger.Debug("terminal: bad control frame", "error", err, "bytes", len(payload))
		return
	}
	if c.Type == ctlTypeResume && c.SessionID != "" {
		if c.ProtocolVersion != 0 && c.ProtocolVersion != wireProtocolVersion {
			h.cfg.logger.Warn("terminal: client wire-protocol version mismatch",
				"client", c.ProtocolVersion, "server", wireProtocolVersion,
				"hint", "client may be running a stale bundle; a hard refresh should fix it")
		}
		h.handleResume(ws, state, c.SessionID, c.HaveThrough)
		return
	}
	if c.Type == ctlTypeResize {
		h.handleResize(c.Cols, c.Rows)
		return
	}
	if c.Type == ctlTypePing {
		h.handlePing(ws)
	}
}

// handlePing answers a client liveness probe with a pong. The client
// sends a ping only after a stretch of inbound silence to tell apart an
// idle-but-healthy socket from one iOS froze during sleep; the pong (or
// any other frame) clears its probe. Best-effort: a write failure means
// the socket is already gone, which the client's probe timeout will catch.
func (h *Handler) handlePing(ws *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws.Write(ctx, websocket.MessageBinary, encodePongMsg()) //nolint:errcheck // best-effort liveness reply
}

// handleResume looks up or creates the session for sessionID, attaches
// it to state, replies with a resumeAck carrying the server's current
// bytesReceived count plus the absolute-index bounds of retained
// history, sends a full-repaint frame of the current window (carrying
// the live alt-screen state) and replays the lines the client is missing
// (by absolute index), and leaves the next flush to repaint the window
// idempotently.
//
// The order of the window frame and the replay depends on the live alt
// state, because the window frame is what sets the client's alt flag and
// the client drops scroll frames while that flag is set (store.ts
// applyScroll):
//   - main screen (winAlt == false): the window frame precedes the replay
//     so a client with a stale alt flag (disconnected in alt, app left alt
//     while away) leaves alt before the replayed history lands; otherwise
//     it silently drops those frames (finding l-f38).
//   - alt screen (winAlt == true): the replay precedes the window frame so
//     a client not yet in alt (fresh load / second tab on an in-alt
//     session) stores the main-screen history before the window frame
//     flips it into alt; otherwise that history is lost (the h-f1
//     regression).
//
// haveThrough is the highest absolute line index the client already
// holds (-1 = none). The server replays lines with index > haveThrough,
// clamped into the retained range; the resumeAck's oldestIndex lets the
// client detect an eviction gap when its haveThrough is older than what
// the ring still holds.
func (h *Handler) handleResume(ws *websocket.Conn, state *clientState, sessionID string, haveThrough int64) {
	ack := h.registry.ResolveSession(state, sessionID)

	h.mu.Lock()
	// Force a full repaint on the next flush so the resuming client sees
	// the current window rebuilt from scratch rather than diffed against
	// a previous-window cache it never received.
	h.builder.Reset()
	// Commit any pending drain to history at its absolute index before
	// computing the replay, so lines that scrolled while the client was
	// away are retained (the old code discarded them here).
	drained := h.screen.DrainScrollback()
	// Match Build's guard: drain that straddles an alt-screen transition belongs to the
	// buffer just left and must not enter main history.
	if !h.screen.InAltScreen && !h.builder.altTransitionPending(h.screen) && len(drained) > 0 {
		h.scrollback.Append(drained)
	}
	committed := h.scrollback.Committed()
	oldest := h.scrollback.OldestIndex()
	var from uint64
	if haveThrough >= 0 {
		from = uint64(haveThrough) + 1
	}
	firstAbs, replay := h.scrollback.LinesFrom(from)
	// Snapshot the current window under h.mu so it can be encoded into a
	// full-repaint screen frame and sent relative to the replay (below; the
	// order depends on winAlt). The base equals committed in all cases: on the
	// main screen the window sits just past committed history; in alt the base
	// is frozen there too.
	winBase := committed
	winRows := make([][]vt.WireRun, h.screen.Height)
	for y := range h.screen.Height {
		winRows[y] = h.screen.RenderRowWire(y)
	}
	winCurRow, winCurCol := h.screen.CursorPos()
	winHeight := h.screen.Height
	winAlt := h.screen.InAltScreen
	winCursorStyle := h.screen.CursorStyle
	winCursorHidden := h.screen.CursorHidden
	winCursorBlink := h.screen.CursorBlink
	h.mu.Unlock()

	// Build the full-repaint changed list (every window row) and encode the
	// window frame outside the lock.
	winChanged := make([]int, winHeight)
	for i := range winChanged {
		winChanged[i] = i
	}
	windowPayload := encodeScreenMsg(winBase, winHeight, winCurRow, winCurCol, ack,
		winChanged, winRows, winCursorStyle, winCursorHidden, winCursorBlink, false, winAlt, false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// resumeAck first so the client can trim its outbox and learn the
	// history bounds (for gap detection) before the replay lands.
	ws.Write(ctx, websocket.MessageBinary, encodeResumeAck(ack, h.bootEpoch, committed, oldest)) //nolint:errcheck // best-effort

	// replayHistory sends the missing committed lines in chunks, each tagged
	// with its absolute first index so the client applies them idempotently.
	replayHistory := func() {
		const replayChunk = 50
		for i := 0; i < len(replay); i += replayChunk {
			end := min(i+replayChunk, len(replay))
			payload := encodeScrollMsg(ack, firstAbs+uint64(i), replay[i:end])
			ws.Write(ctx, websocket.MessageBinary, payload) //nolint:errcheck // best-effort
		}
	}

	// The client gates scroll application on its alt flag (store.ts applyScroll),
	// and the window frame is what sets that flag to winAlt:
	//   - winAlt == false: window FIRST so a client with a stale alt flag
	//     (disconnected in alt, app left alt while away) leaves alt before the
	//     replay lands (finding l-f38).
	//   - winAlt == true: replay FIRST so a client not yet in alt (fresh load /
	//     second tab on an in-alt session) stores the main-screen history before
	//     the window frame puts it into alt; otherwise the replay is dropped and
	//     that history is lost until the next non-alt reconnect.
	if winAlt {
		replayHistory()
		ws.Write(ctx, websocket.MessageBinary, windowPayload) //nolint:errcheck // best-effort
	} else {
		ws.Write(ctx, websocket.MessageBinary, windowPayload) //nolint:errcheck // best-effort
		replayHistory()
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
