// Package terminal bridges a PTY to a browser WebSocket.
//
// Each session runs the configured command in its own PTY. The VT screen is
// the persistence layer: a reconnecting client receives the current cell
// snapshot, with no external multiplexer.
//
// The client sends raw input bytes, and JSON control messages prefixed with
// 0x00 (no valid terminal input starts with NUL). The server sends binary
// frames of absolute-indexed cell runs, never raw PTY bytes.
// docs/wire-protocol.md has the full format.
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
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/cplieger/go/textsafe"
	"github.com/cplieger/web-terminal-engine/v6/vt"
	"github.com/creack/pty"
)

const (
	wsReadLimit = 64 * 1024
	ptyReadBuf  = 4096
	// defaultCols is the spawn width before any client reports one. Keep it at or
	// below minResizeCols: a spawn wider than a real client renders past the
	// viewport's clipped right edge, while a narrower one only wraps early.
	defaultCols   = minResizeCols
	defaultRows   = 30
	flushInterval = 50 * time.Millisecond

	// After a size change, armRedrawSettle holds flushes until the child's
	// redraw has been quiet for redrawSettleQuiet, which must exceed a DEC 2026
	// batch's inter-chunk gaps. Lapsing redrawSettleCap lets one full repaint
	// through and re-arms, at most redrawSettleMaxRearms times, so a redraw
	// longer than the cap still shows whole screens, and once the budget is
	// spent output streams normally (`tail -f`). Never drain vt.Screen on a
	// held pass: only flushFrameBuilder.Build turns drained lines into a frame
	// clients receive.
	redrawSettleQuiet     = 150 * time.Millisecond
	redrawSettleCap       = time.Second
	redrawSettleMaxRearms = 3

	// healDebounce delays relaxing the shared screen after a disconnect, so a
	// client re-attaching at the same size (iOS wake, network blip) causes no
	// grow-then-shrink flap: the recompute at fire time counts its new socket.
	healDebounce = 3 * time.Second

	// statusProcessExited tells a client the session is dead (close the tab),
	// not transiently disconnected. 4000-4999 is the private close-code range.
	statusProcessExited websocket.StatusCode = 4001

	// WireIncompatibleCloseCode is the definitive WebSocket close code for an
	// explicitly declared peer revision below the receiver's supported floor.
	// The server sends it to a stale client; a client may send it to a server
	// whose declared revision is below its own floor.
	WireIncompatibleCloseCode websocket.StatusCode = 4002

	// wireIncompatibleClientReason must fit the WebSocket close-reason limit
	// (RFC 6455 section 5.5: 123 bytes); the detailed versions are logged.
	wireIncompatibleClientReason = "client wire protocol is below the server minimum; reload or upgrade the client"

	// statusUnknownSession closes an accepted upgrade whose ?session= id the
	// manager does not know. A pre-upgrade 404 reaches JS only as an opaque
	// 1006, which a client must treat as transient and would retry forever.
	// Like 4001 it is definitive, and clients route both to the ended state.
	statusUnknownSession websocket.StatusCode = 4004

	// exitedAttachReplayGrace delays the 4001 close to a client attaching to an
	// already-exited session, so its resumeAck and final-screen replay land
	// first. A client sends resume as its first frame, so only a client that
	// never speaks resume waits the full grace.
	exitedAttachReplayGrace = 3 * time.Second

	// maxScrollLinesPerFrame keeps a scroll frame under the wire's uint16
	// num_lines and splits a large drained burst into small frames.
	maxScrollLinesPerFrame = 1000

	// historyPageSize makes one page reply exactly one scroll frame;
	// pageByteBudget can shorten it further.
	historyPageSize = maxScrollLinesPerFrame
	// paginationMinRing is the ring depth at which the server declares paging
	// (exported as MinPagingCapacity). It must stay derived from maxReplayLines:
	// a deeper ring withholds rows from the bounded resume replay, and paging is
	// the only way a client can fetch them.
	paginationMinRing = maxReplayLines + 1
	// maxReplayLines caps controlMsg.ReplayMax, sized to bound the resume
	// batch's bytes within its write context, not to the ring depth. The client
	// clamps to the same constant, so the sent value equals the honored one,
	// which its replay-jump prediction depends on.
	maxReplayLines = 2000
	// historyBurst/historyRefill are the per-socket history token bucket, a
	// floor against bursts and socket churn, not an aggregate abuse bound. The
	// client paces itself at one token per 2 s, so a healthy client stays under.
	historyBurst  = 4.0
	historyRefill = 1500 * time.Millisecond
	// historyWriteTimeout matches the resume batch, not live dispatch: a page
	// reply holds writeMu, which dispatchFrame's fan-out waits on. The client's
	// 8 s data timeout fires first by design.
	historyWriteTimeout = 10 * time.Second

	// A resize below the minimum is floored, not dropped: an iPad keyboard
	// slide reports near-zero sizes, and the start path must still fire.
	minResizeCols = 20
	minResizeRows = 5

	// maxResizeCols/maxResizeRows must not exceed vt.Screen's grid cap, or the
	// PTY is sized past the screen; TestResizeCeilingFitsScreen pins them.
	maxResizeCols = 1000
	maxResizeRows = 1000

	ctlTypeResize = "resize"
	ctlTypeResume = "resume"
	// ctlTypeUpgrade, the first TEXT message from a client that saw resumeAck
	// serverWireVersion >= 4, latches the connection to typed framing.
	ctlTypeUpgrade = "upgrade"
	ctlTypePing    = "ping"
	// ctlTypeHistory requests a page of retained scrollback by absolute index.
	ctlTypeHistory = "history"
	// ctlTypeFocus reports the client widget's focus; focusReportLocked derives
	// the DEC 1004 answer. Against a server without resumeAckFlagServerFocus the
	// client drops it, so one party alone answers for a session.
	ctlTypeFocus = "focus"
	// ctlTypeEphemeralInput carries best-effort input outside the resume ledger
	// (ephemeralInputControl).
	ctlTypeEphemeralInput = "ephemeralInput"
	// maxLoggedControlTypeBytes bounds the client-chosen type an unrecognized
	// control logs; every real control type is under 16 bytes.
	maxLoggedControlTypeBytes = 32

	// defaultScrollbackCapacity is sized from measured long sessions (p99 about
	// 61,000 lines). The ring grows on demand at about 345 bytes per line,
	// about 34 MB at this ceiling.
	defaultScrollbackCapacity = 100_000
)

// ScrollbackEnvVar names the environment variable an operator sets to override
// the retained-history depth. The engine never reads it: a consumer reads it
// and passes the value through ClampScrollbackCapacity to
// WithScrollbackCapacity, so every embedding app interprets it alike.
const ScrollbackEnvVar = "SCROLLBACK"

// DefaultScrollbackCapacity is the retained-history depth a handler uses when a
// consumer sets none, exported so a consumer can report the effective depth.
const DefaultScrollbackCapacity = defaultScrollbackCapacity

// MinPagingCapacity is the retained-history depth at or above which the handler
// declares demand-paged scrollback to its client. Below it the resume replay
// carries the whole ring, so there is nothing to page.
// Prefer ClampScrollbackCapacity over comparing against it.
const MinPagingCapacity = paginationMinRing

// ClampScrollbackCapacity turns an operator-supplied retained-history depth into
// the one to configure, plus a human-readable reason when it changed it; the
// reason is empty otherwise. A negative depth becomes 0, which disables
// scrollback. A depth between 1 and MinPagingCapacity is clamped up: too shallow
// to declare paging, it would make the browser hold its whole legacy buffer, so
// less server history would cost more client memory. There is no upper bound;
// the ring allocates only what it fills.
func ClampScrollbackCapacity(n int) (capacity int, reason string) {
	if n < 0 {
		return 0, fmt.Sprintf("%s=%d is negative; treating it as 0 (scrollback disabled)", ScrollbackEnvVar, n)
	}
	if n > 0 && n < MinPagingCapacity {
		return MinPagingCapacity, fmt.Sprintf(
			"%s=%d is below the %d lines needed to offer demand-paged history, which would make the browser "+
				"hold its whole legacy buffer instead of paging: using %d",
			ScrollbackEnvVar, n, MinPagingCapacity, MinPagingCapacity,
		)
	}
	return n, ""
}

// Option configures optional behavior of the Handler.
type Option func(*handlerConfig)

// handlerConfig holds optional configuration applied via functional options.
type handlerConfig struct {
	logger        *slog.Logger
	originPolicy  *OriginPolicy
	onProcessExit func(error)
	theme         *vt.Theme
	// A nil containment, the default, disables process containment.
	containment        *Containment
	containmentID      SessionID
	workDir            string
	commandLogValue    string
	env                []string
	containSample      time.Duration
	scrollbackCapacity int
	// minContrast of 0, like 1, leaves the contrast floor off.
	minContrast   float64
	keepUnfocused bool
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
			l = slog.New(slog.DiscardHandler)
		}
		c.logger = l
	}
}

// WithCommandLogValue replaces the child's argv in the process-start log
// line's "command" attribute, the engine's only argv-bearing log site. Pass a
// fixed marker such as "[redacted]" when argv may carry a credential
// (CWE-532). An empty v is ignored, keeping the default argv logging.
func WithCommandLogValue(v string) Option {
	return func(c *handlerConfig) {
		if v != "" {
			c.commandLogValue = v
		}
	}
}

// WithEnv sets additional environment variables for the spawned process.
func WithEnv(env []string) Option {
	return func(c *handlerConfig) { c.env = env }
}

// WithScrollbackCapacity sets how many scrollback lines are retained for
// resume replay and demand-paged history; the default is
// DefaultScrollbackCapacity. A negative n is treated as 0, which disables
// scrollback: nothing survives scrolling off the live screen. The buffer grows
// on demand, so for "unlimited" pass a number no session will reach. Below
// MinPagingCapacity the handler does not declare paging; pass the value
// through ClampScrollbackCapacity first.
func WithScrollbackCapacity(n int) Option {
	return func(c *handlerConfig) {
		c.scrollbackCapacity = max(n, 0)
	}
}

// WithOriginPolicy widens the browser origins allowed to open this handler's
// WebSocket beyond same-origin. Build the policy with NewOriginPolicy; a nil
// policy (the default) means same-origin only.
//
// Pass the same policy to WithManagerOriginPolicy when the handler is served
// through a SessionManager, so a widened origin also reaches the manager's
// unknown-session socket.
func WithOriginPolicy(p *OriginPolicy) Option {
	return func(c *handlerConfig) { c.originPolicy = p }
}

// WithOnProcessExit registers a callback invoked when the child process exits.
// The child's final output is on the screen by then, unless a process still
// holding the terminal keeps it open past a one-second drain bound.
func WithOnProcessExit(fn func(error)) Option {
	return func(c *handlerConfig) { c.onProcessExit = fn }
}

// WithKeepUnfocused holds the child, when keep is true, in the unfocused DEC
// 1004 state: whenever it enables focus reporting the server writes a
// focus-out and never a focus-in, so a program that emits OSC 9 notifications
// only while unfocused keeps emitting them. The default false reports real
// focus; the last WithKeepUnfocused wins.
func WithKeepUnfocused(keep bool) Option {
	return func(c *handlerConfig) { c.keepUnfocused = keep }
}

// WithTheme sets the default foreground, background, and cursor colors the
// terminal reports to programs via OSC 10/11/12 queries (and restores on
// OSC 110/111/112 reset). Pass the colors your client actually renders so
// color-probing apps — light/dark detection, "reset to default" — see the real
// theme. Defaults to vt.DefaultTheme (a dark scheme). Build colors with vt.RGB.
func WithTheme(t vt.Theme) Option {
	return func(c *handlerConfig) { c.theme = &t }
}

// WithMinimumContrast sets a floor on the WCAG contrast ratio between a run's
// text and its background, clamped to 1..21; off (1) by default. A foreground
// below the floor is blended toward white or black; backgrounds and default
// foregrounds are untouched. 4.5 is the WCAG AA floor for body text.
// vt.WithMinimumContrast owns the details.
func WithMinimumContrast(ratio float64) Option {
	return func(c *handlerConfig) { c.minContrast = ratio }
}

// sessionState is one sender's input ledger, kept across WS reconnects and
// named by the resume control message. received is the ack the client compares
// to its sent count to decide what to retransmit. A sender has one live
// transport, so a ledger has at most one owner socket: a resume transfers
// ownership and fences the previous owner, and a non-owner's input is dropped.
type sessionState struct {
	// lastSeen is guarded by clientRegistry.mu.
	lastSeen time.Time
	// owner is guarded by mu.
	owner *clientState
	// received is written only under mu, so the PTY write and the count are
	// one step against a resume's ack read; readers that need no such
	// ordering (Snapshot, the ack sweep, the GC) load it without mu.
	received atomic.Uint64
	// mu is held across the owner's PTY write on purpose: it exists to
	// serialize that write with the count. Lock order: never take mu while
	// holding clientRegistry.mu.
	mu sync.Mutex
}

// clientState tracks per-WS-connection state. session is atomic so the input
// path can test for an attached ledger without taking clientRegistry.mu.
type clientState struct {
	// ws is set once by clientRegistry.Add, so a superseding resume can close it.
	ws      *websocket.Conn
	session atomic.Pointer[sessionState]
	// resumeLast/resumeTokens are resumeControl's token bucket. The three
	// buckets are owned by the socket's serial read loop, so they take no lock;
	// their time.Time halves lead the struct for fieldalignment.
	resumeLast time.Time
	// historyLast/historyTokens are takeHistoryToken's bucket.
	historyLast time.Time
	// ephemeralLast/ephemeralTokens are takeEphemeralToken's bucket.
	ephemeralLast time.Time
	// lastAckSent is the last inputAck written to this socket, by dispatchFrame,
	// the ackOnly sweep or handleResume; the sweep compares it to the ledger's
	// count so input into a silent app is still acknowledged. Atomic because
	// handleResume and flushLoop both write it.
	lastAckSent atomic.Uint64
	// cols/rows are guarded by clientRegistry.mu and feed MinLiveSize.
	cols int
	rows int
	// resumeTokens is resumeLast's fill.
	resumeTokens float64
	// historyTokens is historyLast's fill.
	historyTokens float64
	// ephemeralTokens is ephemeralLast's fill.
	ephemeralTokens float64
	// focused is guarded by clientRegistry.mu and feeds AnyClientFocused.
	focused bool
	// fenced is set under the ledger's mu once a resume moved ownership to
	// another socket, and never cleared: this socket's input is dropped.
	fenced atomic.Bool
	// writeMu serializes content-frame sequences: coder/websocket serializes
	// single Writes only. handleResume holds it across snapshot and batch, so
	// no older frame lands after a newer one; dispatchFrame blocks on it rather
	// than dropping, because a built frame's payloads cannot be regenerated.
	writeMu sync.Mutex
	// resumeGen is bumped by handleResume in the h.mu section of its snapshot.
	// writeClientPayloads strips a frame built under an older generation to
	// its durable payloads.
	resumeGen atomic.Uint64
}

// resumeBurst / resumeRefillEvery parameterize the per-socket resume
// throttle: a full bucket serves resumeBurst back-to-back resumes, then one
// more per resumeRefillEvery. Deliberately generous — the gate exists to
// bound a hostile flood's amplification (each resume is a full write
// transaction), not to meter legitimate churn: a reconnect storm, rapid
// ledger switches and a wire upgrade together stay under the burst, and the
// sustained rate still caps a 1000/s spammer at 30 batches/min.
const (
	resumeBurst       = 10
	resumeRefillEvery = 2 * time.Second
)

// takeResumeToken spends one token from the socket's resume bucket,
// reporting false when it is empty. Called only from the socket's read loop
// (control messages are serialized per socket), so the state needs no lock.
// The bucket starts full: resumeLast's zero value dates the last refill to
// the epoch, so the first call tops it up to the burst.
func (st *clientState) takeResumeToken() bool {
	now := time.Now()
	if !st.resumeLast.IsZero() {
		st.resumeTokens += now.Sub(st.resumeLast).Seconds() / resumeRefillEvery.Seconds()
	} else {
		st.resumeTokens = resumeBurst
	}
	if st.resumeTokens > resumeBurst {
		st.resumeTokens = resumeBurst
	}
	st.resumeLast = now
	if st.resumeTokens < 1 {
		return false
	}
	st.resumeTokens--
	return true
}

// takeHistoryToken spends one token from the socket's history bucket,
// reporting false when it is empty. Same read-loop-owned, lock-free shape as
// takeResumeToken (controls are serialized per socket) and the same
// starts-full convention: historyLast's zero value dates the last refill to
// the epoch, so the first call tops it up to the burst. historyBurst owns the
// bucket's scope and sizing.
func (st *clientState) takeHistoryToken() bool {
	now := time.Now()
	if !st.historyLast.IsZero() {
		st.historyTokens += now.Sub(st.historyLast).Seconds() / historyRefill.Seconds()
	} else {
		st.historyTokens = historyBurst
	}
	if st.historyTokens > historyBurst {
		st.historyTokens = historyBurst
	}
	st.historyLast = now
	if st.historyTokens < 1 {
		return false
	}
	st.historyTokens--
	return true
}

// Handler serves /ws and tracks shared screen state. Multiple WS clients
// can attach concurrently; the VT screen is the session state.
//
// h.mu guards screen and PTY state; clientRegistry has its own lock. flushLoop
// writes to sockets outside h.mu, so a slow client cannot block input,
// control messages or new connections.
type Handler struct {
	cmd        *exec.Cmd
	screen     *vt.Screen
	registry   *clientRegistry
	builder    *flushFrameBuilder
	scrollback *scrollbackRing
	cancel     context.CancelFunc
	ptmx       *os.File
	// contain is nil with containment off. The process monitor and cost sampler
	// read it lock-free only because ensureStarted starts them after the write;
	// any other reader, Close included, needs h.mu or an atomic.
	contain *sessionCgroup
	// reap is this session's marker reap domain, nil until a spawn starts, after
	// a failed spawn, or when its marker could not be minted. Read under exactly the same rules as
	// contain above: written before the monitor goroutine is created, read only
	// by that goroutine.
	reap       *sessionReap
	procExitCh chan struct{}
	// done closes once every goroutine this handler started, teardown included,
	// has returned. It is derived from h.wg, never closed by hand, so it cannot
	// claim teardown finished while a loop still unwinds. Nil until
	// ensureStarted, which makes wait() a no-op on an unspawned handler.
	// Guarded by h.mu.
	done chan struct{}
	// dirty is 1-buffered so markDirty pokes coalesce; an idle flushLoop
	// sleeps on it with no ticker.
	dirty     chan struct{}
	healTimer *time.Timer
	// The redraw-settle hold's fields are guarded by h.mu; a zero
	// redrawSettleUntil means the hold is inactive.
	redrawSettleUntil time.Time
	redrawLastData    time.Time
	pendingClipboard  []byte
	command           []string
	// exitErr retains what cmd.Wait() reported when the process monitor reaped
	// the child: nil for a clean exit, an *exec.ExitError for a non-zero or
	// signalled one. Guarded by h.mu and written exactly once, BEFORE
	// procExitCh closes, so Exited() == true implies this value is final (see
	// the monitor in ensureStarted). Read through ExitError.
	exitErr      error
	cfg          handlerConfig
	bootEpoch    int64
	lastActivity atomic.Int64
	// wg counts every goroutine ensureStarted launches, so the handler can answer
	// "is anything of mine still running" instead of leaving the caller to assume
	// it from the process having exited. Added to only from ensureStarted, which
	// holds h.mu, and never after done is armed.
	wg      sync.WaitGroup
	mu      sync.Mutex
	started atomic.Bool
	// shutdownRequested latches when Close ends the session, keeping that exit
	// out of crashedExit's crashed class. Atomic: the monitor reads it without h.mu.
	shutdownRequested atomic.Bool
	// redrawRearms counts how many times the redraw-settle cap has lapsed
	// mid-redraw and coalesced instead of releasing, bounding the coalescing at
	// redrawSettleMaxRearms. Guarded by h.mu. Placed ahead of the bool block so
	// the bools stay contiguous (govet fieldalignment).
	redrawRearms int
	// sizeEstablished is latched true once the PTY has real dimensions (the
	// eager start's default size, or a client resize) and never cleared: the
	// flush builder emits nothing before it, so clients never see a frame
	// rendered against the zero-size screen. It does NOT mean "a resize
	// happened this tick" (its former name, `resized`, invited that reading).
	sizeEstablished          bool
	scrollbackClearedPending bool
	paletteChangedPending    bool
	// redrawCoalesceNow marks the one flush pass that must build a FULL repaint
	// because the redraw-settle cap lapsed while the child was still writing
	// (see redrawHoldUntil). Consumed by buildFrame. Guarded by h.mu.
	redrawCoalesceNow bool
	// ptyClosed latches once closePTYLocked closed the PTY master. Guarded by
	// h.mu.
	ptyClosed bool
	// autoTitleWarned makes the automatic-title probe's failure note once-per-
	// session rather than once-per-sweep (see probeAutoTitle). Guarded by h.mu.
	autoTitleWarned bool
	// focusTold is the DEC 1004 state this session's process has been told, so
	// the derivation writes on transitions only (see focusReportLocked).
	// Guarded by h.mu.
	focusTold toldFocus
}

// NewHandler returns a terminal handler. command is the argv to spawn
// (required, must be non-empty). Optional behavior is configured via
// functional Option values.
func NewHandler(command []string, opts ...Option) *Handler {
	cfg := handlerConfig{
		scrollbackCapacity: defaultScrollbackCapacity,
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
	if cfg.minContrast > vt.MinimumContrastOff {
		vtOpts = append(vtOpts, vt.WithMinimumContrast(cfg.minContrast))
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
		dirty:      make(chan struct{}, 1),
	}
}

// markDirty pokes the flush scheduler: some state that could produce a frame
// changed (PTY output, resize, resume, input needing an ack sweep). Non-
// blocking — the 1-buffered channel coalesces any number of pokes into one
// pending wakeup, which is all the scheduler needs.
func (h *Handler) markDirty() {
	select {
	case h.dirty <- struct{}{}:
	default:
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

// Close ends the session and returns without waiting for the teardown it
// starts. It is safe before any start and safe to repeat, and every later start
// is refused. It kills the child (SIGKILL via the cancelled context) and closes
// the PTY; reaping the child, ending the containment cgroup, sweeping escapees
// and telling clients the process is gone continue on the process monitor's
// goroutine. Call Shutdown to wait for that work. The child's exit then counts
// as server-initiated, never as a crash (see crashedExit).
func (h *Handler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Stored before cancel() so the store is ordered ahead of the kill it
	// triggers, and therefore ahead of the cmd.Wait() return the monitor
	// classifies.
	h.shutdownRequested.Store(true)
	if h.cancel != nil {
		h.cancel()
	}
	h.closePTYLocked()
}

// closePTYLocked closes the PTY master once and stops the size heal; from then
// on applySize and maybeHealSize do nothing. Caller holds h.mu. h.ptmx keeps
// its value: readLoop and the input paths read it without the lock, so a nil
// store here would race them.
func (h *Handler) closePTYLocked() {
	if h.healTimer != nil {
		h.healTimer.Stop()
	}
	if h.ptmx == nil || h.ptyClosed {
		return
	}
	_ = h.ptmx.Close() // best-effort during teardown
	h.ptyClosed = true
}

// Shutdown ends the session and waits for its teardown to finish, bounded by
// ctx. It returns ctx.Err() if the budget expires first and nil once every
// goroutine the handler started has returned. Call it, not Close, before the
// process exits: Close leaves the cgroup teardown, the /proc sweep and the
// client notification in flight. An expiry is the only sign a teardown overran,
// so log it. Size ctx for the child's reap, at most 5s, plus the escape
// teardown: about a second for a tree that must be killed, under ten for one
// that keeps forking or cannot act on SIGKILL.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.Close()
	return h.wait(ctx)
}

// wait blocks until every goroutine ensureStarted launched has returned, or ctx
// expires. Returns nil for a handler that never started one.
//
// Unexported because the only caller that needs the halves apart is
// SessionManager, which must signal all of its sessions before waiting on any of
// them so their teardown windows overlap instead of summing. A consumer holding
// one handler wants Shutdown.
func (h *Handler) wait(ctx context.Context) error {
	h.mu.Lock()
	done := h.done
	h.mu.Unlock()
	if done == nil {
		return nil
	}
	return waitClosed(ctx, done)
}

// waitClosed blocks until done closes or ctx expires, preferring done when both
// are already ready. The first non-blocking check is required: a two-case
// select picks a ready case at random, so a finished teardown would report an
// expiry half the time once the caller's budget has also run out.
func waitClosed(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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

// ExitError returns what cmd.Wait() reported for the child: nil when the process
// exited cleanly (status 0), was never started, or has not exited yet, and
// otherwise the wait error — an *exec.ExitError for a non-zero or signalled
// exit, the same error WithOnProcessExit's callback receives. Pair it with
// Exited to tell "not dead yet" from "died cleanly"; the value is final once
// Exited reports true. Safe for concurrent use.
func (h *Handler) ExitError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exitErr
}

// setExitError retains the reaped child's wait error. Called exactly once, by
// the process monitor, before procExitCh closes.
func (h *Handler) setExitError(werr error) {
	h.mu.Lock()
	h.exitErr = werr
	h.mu.Unlock()
}

// exitOutcome reports whether the child has exited and, when it has, whether the
// exit counts as a crash under crashedExit's rule. One call so the status sweep
// reads liveness and outcome as a pair rather than deriving the second from a
// second, later observation.
func (h *Handler) exitOutcome() (exited, crashed bool) {
	if !h.Exited() {
		return false, false
	}
	return true, crashedExit(h.ExitError(), h.shutdownRequested.Load())
}

// crashedExit classifies a reaped child's exit: true means a failure an operator
// should see (StatusCrashed), false a normal end (StatusExited). Exit 0 is clean.
// serverInitiated wins over the wait status: a killed or hung-up child of Close,
// Shutdown or the idle reaper is not a crash, or every closed tab would show red.
// SIGHUP is excluded independently, because only the engine closes the PTY
// master. Any other *exec.ExitError (non-zero status, unrequested signal) is the
// crash case. Any other Wait error carries no evidence about the program, so it
// is not a crash: a false crash is the expensive direction.
func crashedExit(werr error, serverInitiated bool) bool {
	if werr == nil || serverInitiated {
		return false
	}
	ee, isExitErr := errors.AsType[*exec.ExitError](werr)
	if !isExitErr {
		return false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGHUP {
		return false
	}
	return true
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
// (0 off, 1 value, 2 error, 3 indeterminate, 4 paused). While the program has
// sent no OSC 7501 report since the last RIS, the status stream maps an active
// state (1 or 3) to working, so OSC 9;4 progress drives the working indicator
// without relying on raw output activity. Safe for concurrent use.
func (h *Handler) Progress() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.Progress
}

// ProgressValue returns the percentage carried by the session's last OSC 9;4
// progress sequence: -1 when absent or unknown (no sequence seen, or a state
// that carries no percentage), else 0-100; -1 is not 0%. It omits an OSC 7501
// report's progress, which the status stream's progressValue, the value a
// determinate bar draws, prefers. Safe for concurrent use.
//
// The engine's own status sweep reads the same field through statusSnapshot,
// which batches every status input under one lock.
func (h *Handler) ProgressValue() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.screen.ProgressValue
}

// ScrollbackBounds returns the absolute-index bounds of the retained
// scrollback, the values the resume handshake reports: every index in
// [oldest, committed) is still retained, and committed is the absolute index
// of the top screen row. Both are 0 before the first commit. Indices are
// monotonic and never reused. The pair is read under one lock, so it is always
// consistent. Safe for concurrent use.
func (h *Handler) ScrollbackBounds() (committed, oldest uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.scrollback.Committed(), h.scrollback.OldestIndex()
}

// screenStatus is one atomic read of the status-relevant screen state, returned
// as a struct rather than a result list so the sweep can grow another status
// input without either growing a longer tuple or taking the lock twice.
type screenStatus struct {
	notifMsg string           // the last OSC 9 notification message
	title    string           // the OSC 0/2 window title
	program  vt.ProgramStatus // the folded OSC 7501 status
	notifSeq uint64           // rises per captured notification (a repeat is still new)
	// programSeq rises per accepted OSC 7501 report, on notifSeq's counter.
	programSeq uint64
	progress   int // OSC 9;4 state: -1 none, 0 clear, 1 value, 2 error, 3 indeterminate, 4 warning
	// progressValue is the OSC 9;4 percentage: -1 absent/unknown, else 0-100.
	progressValue int
	// programSeen is whether an OSC 7501 report arrived since the last RIS.
	programSeen bool
}

// statusSnapshot returns every status input the sweep reads (OSC 9;4 state
// and percentage, OSC 9 notification and sequence, OSC 7501 status, title)
// under ONE lock. Reading them through the individual getters can pair a stale
// active progress with a fresh turn-end notification, or a bar with a state
// that has moved on, a torn read computeStatus must never see.
func (h *Handler) statusSnapshot() screenStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return screenStatus{
		notifMsg:      h.screen.Notification,
		title:         h.screen.Title,
		notifSeq:      h.screen.NotificationSeq,
		progress:      h.screen.Progress,
		progressValue: h.screen.ProgressValue,
		program:       h.screen.ProgramStatus(),
		programSeq:    h.screen.ProgramReportSeq(),
		programSeen:   h.screen.ProgramReported(),
	}
}

// StartEager starts the child process now at a default size, rather than lazily
// on the first client message. A session manager calls this at Create time so a
// new session's process (and its activity signal) exist from creation; the first
// client attach still sends the real resize. Idempotent. On a handler closed
// before its first start it returns an error.
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

// closeOnProcExit closes the client WS with statusProcessExited when the child
// exits, or returns when ctx is done. It closes the socket directly: canceling
// the read's context makes coder/websocket fail with 1006 instead of a clean
// 4001. A client attaching to an already-exited session gets up to
// exitedAttachReplayGrace for resumeServed (closed once handleResume wrote the
// final state) before the close; a client attached at exit is closed at once.
func (h *Handler) closeOnProcExit(ctx context.Context, ws *websocket.Conn, resumeServed <-chan struct{}) {
	alreadyExited := h.Exited()
	select {
	case <-ctx.Done():
		return
	case <-h.procExitCh:
	}
	if alreadyExited {
		t := time.NewTimer(exitedAttachReplayGrace)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-resumeServed:
		case <-t.C:
		}
	}
	ws.Close(statusProcessExited, "") // #nosec G104 -- best-effort
}

// errHandlerClosed is ensureStarted's refusal to start a child once Close has run.
var errHandlerClosed = errors.New("terminal: session closed")

// logStartFailure logs a client-triggered start that failed. A refusal after
// Close is the expected end of a session, not a failure.
func (h *Handler) logStartFailure(err error) {
	if errors.Is(err, errHandlerClosed) {
		h.cfg.logger.Debug("terminal: start refused after close")
		return
	}
	h.cfg.logger.Error("terminal: process start failed", "error", err)
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
	// Shutdown may already have returned, so a child started now would outlive
	// it. Close stores the latch under h.mu, which orders this check against it.
	if h.shutdownRequested.Load() {
		return errHandlerClosed
	}
	if len(h.command) == 0 {
		return errors.New("terminal: empty command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	cmd := exec.CommandContext(ctx, h.command[0], h.command[1:]...) // #nosec G204
	// Force-kill a child that ignores the PTY-close SIGHUP: Close/reap cancels ctx
	// (default Cancel = SIGKILL) and WaitDelay bounds the grace so cmd.Wait cannot
	// block the monitor goroutine forever.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = h.cfg.workDir
	h.reap = h.newSessionReap()
	cmd.Env = h.childEnv(h.reap)
	if cols < 1 {
		cols = defaultCols
	}
	if rows < 1 {
		rows = defaultRows
	}
	h.contain = h.beginContainment(cmd)
	ptmx, err := startSessionPTY(cmd, cols, rows)
	// The cgroup fd is only needed at clone time; holding it would leak one
	// descriptor per session. Released on the failure path too.
	h.contain.releaseFD()
	if err != nil {
		h.contain.teardown()
		h.contain = nil
		// A spawn that never produced a process has no tree to reap, but the
		// domain is dropped anyway so a later teardown cannot scan for a marker
		// nothing ever carried.
		h.reap = nil
		return err
	}
	h.ptmx = ptmx
	h.cmd = cmd
	h.started.Store(true)
	h.sizeEstablished = true
	h.screen.Resize(rows, cols)
	loggedCommand := any(h.command)
	if h.cfg.commandLogValue != "" {
		loggedCommand = h.cfg.commandLogValue
	}
	// Captured once: the monitor below needs it after cmd.Wait, where reading
	// cmd.Process would race the exec package's own bookkeeping.
	pid := cmd.Process.Pid
	h.cfg.logger.Info("terminal: process started",
		"pid", pid, "command", loggedCommand, "cols", cols, "rows", rows)

	// PTY reader goroutine — feeds VT screen and notifies clients.
	readDone := make(chan struct{})
	h.wg.Go(func() {
		defer close(readDone)
		h.readLoop(ctx)
	})
	// Flush scheduler — sends screen updates to all clients.
	h.wg.Go(func() { h.flushLoop(ctx) })
	// Periodic per-session cost line, when the consumer asked for one. Stopped
	// explicitly by the monitor before teardown, not just by ctx.
	stopSampler := h.startCostSampler(ctx)
	// Process monitor — reaps the child (so it does not linger as a
	// zombie), fires the documented onProcessExit callback with the
	// exit status, and cancels the read/flush loops on natural child
	// exit so the scheduler goroutine does not leak after the process dies.
	h.wg.Go(func() {
		werr := cmd.Wait() // reap; werr carries the exit status
		// os/exec is done with this pid, so the zombie sweep may stop excluding
		// it. One mutex, no allocation, and ahead of anything that can block.
		spawnForget(pid)
		awaitReadDrain(readDone)
		// Retain the outcome BEFORE procExitCh closes, so any reader that sees
		// Exited() == true also sees the final exit error (the status sweep reads
		// the pair through exitOutcome). Nothing here can panic — a mutex and one
		// assignment — so it is safe ahead of the teardown defer below.
		h.setExitError(werr)
		// Deferred first so it runs last: the /proc walk must not delay the
		// procExitCh broadcast, Exited() and every client's 4001 close.
		defer h.reap.teardown()
		// Guarantee client notification (procExitCh drives the 4001 close) and
		// loop teardown even if the consumer onProcessExit callback panics; a
		// callback panic must not crash the server or strand attached clients.
		// procExitCh is closed exactly once: this monitor runs once per handler.
		defer func() {
			// Broadcast process exit so attached WS clients close with
			// statusProcessExited (4001) rather than a normal closure.
			close(h.procExitCh)
			// After the close, and statusRaw.read snapshots before exitOutcome,
			// so a sweep that reads the dropped records also reads the exit.
			h.markProgramExited()
			cancel() // stop readLoop/flushLoop on child exit
			// Free the PTY master fd immediately on natural exit; otherwise an
			// exited-but-undeleted session holds it until Close/reap (reaper
			// is off by default).
			h.mu.Lock()
			h.closePTYLocked()
			h.mu.Unlock()
			if r := recover(); r != nil {
				h.cfg.logger.Error("terminal: onProcessExit callback panicked", "panic", r)
			}
		}()
		// Containment teardown runs only here, Close's path included (its cancel
		// kills the head, so this Wait returns). Stop sampling before removal.
		stopSampler()
		h.contain.teardown()

		// INFO, not WARN: a non-zero exit is an ordinary session end. The peaks
		// are cgroup high-water marks; tasks_peak counts TIDs (pids.peak), and
		// both are 0 with containment off.
		memPeak, tasksPeak := h.contain.peaks()
		h.cfg.logger.Info("terminal: process exited",
			"pid", pid, "error", werr,
			"mem_peak_bytes", memPeak, "tasks_peak", tasksPeak)
		if h.cfg.onProcessExit != nil {
			h.cfg.onProcessExit(werr)
		}
	})
	// Arm the completion signal. A bare `go` on purpose: this goroutine's whole
	// job is to observe h.wg, so counting it in h.wg would deadlock. It holds no
	// session state and ends with the four above.
	h.done = make(chan struct{})
	go func(done chan struct{}) {
		h.wg.Wait()
		close(done)
	}(h.done)
	return nil
}

// childEnv assembles a session's environment. TERM/COLORTERM advertise 256-color
// and truecolor; TERM_PROGRAM=iTerm.app at version 3.6.6 advertises an iTerm2
// identity, whose OSC 9;4 progress and DEC 2026 synchronized output the engine
// implements. h.cfg.env is appended last so WithEnv wins. The reap marker goes
// FIRST so the reap scan reads a bounded environ prefix, and both sources are
// stripped of it: os/exec keeps the last value of a repeated key, and an
// inherited marker (a server started inside a session) would silently disable
// reaping.
func (h *Handler) childEnv(reap *sessionReap) []string {
	inherited := stripReapMarker(os.Environ())
	consumer := stripReapMarker(h.cfg.env)
	env := make([]string, 0, len(inherited)+len(consumer)+5)
	if pair := reap.envPair(); pair != "" {
		env = append(env, pair)
	}
	env = append(env, inherited...)
	env = append(env,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=iTerm.app",
		"TERM_PROGRAM_VERSION=3.6.6",
	)
	return append(env, consumer...)
}

// startSessionPTY forks the session's process on a new PTY and records its pid as
// os/exec-owned.
//
// The spawn lock is held across the fork itself, not merely the registration:
// the zombie sweep must never be able to see a child that exists but is not yet
// recorded as owned, or it could collect the head's exit status out from under
// cmd.Wait and turn every session's exit into an unknown one (see zombiereap.go).
func startSessionPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	spawnLock()
	defer spawnUnlock()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) // #nosec G115 -- cols/rows are floored above and bounded by the client protocol
	if err == nil && cmd.Process != nil {
		spawnRegister(cmd.Process.Pid)
	}
	return ptmx, err
}

// markProgramExited tells the screen the process exited (vt.Screen.ProgramExited
// owns what that drops).
func (h *Handler) markProgramExited() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.screen.ProgramExited()
}

// exitDrainBound caps how long the process monitor waits, after the child is
// reaped, for readLoop to drain the PTY to EOF/EIO. It must be finite: a
// backgrounded process still holding the slave keeps the master open, so the
// read may never end on its own.
const exitDrainBound = time.Second

// awaitReadDrain blocks until readDone closes or exitDrainBound elapses, so the
// exit callback, procExitCh and the ptmx close all follow the child's final
// output instead of racing it.
func awaitReadDrain(readDone <-chan struct{}) {
	if hold := testExitDrainHold.Load(); hold != nil {
		(*hold)()
	}
	t := time.NewTimer(exitDrainBound)
	defer t.Stop()
	select {
	case <-readDone:
	case <-t.C:
	}
}

// testExitDrainHold, when non-nil, is invoked by awaitReadDrain before it waits,
// and testPTYReadHold by readLoop after a read returns data and before the data
// reaches the screen. Test-only: together they hold the reader at the instant the
// monitor starts draining, which no real timing reaches deterministically. Atomic
// for the same reason as testResumeBatchHold. Never set in production.
var (
	testExitDrainHold atomic.Pointer[func()]
	testPTYReadHold   atomic.Pointer[func()]
)

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
			if hold := testPTYReadHold.Load(); hold != nil {
				(*hold)()
			}
			h.handlePTYData(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// handlePTYData feeds raw PTY output to the screen under h.mu and writes
// any query response back outside the lock so a slow write never stalls
// goroutines waiting on h.mu.
func (h *Handler) handlePTYData(data []byte) {
	h.lastActivity.Store(time.Now().UnixNano())
	var resp []byte
	h.mu.Lock()
	if !h.redrawSettleUntil.IsZero() {
		// Redraw-settle hold armed (post-resize): every PTY byte is redraw
		// output, so it extends the quiet window (redrawHoldUntil).
		h.redrawLastData = time.Now()
	}
	h.screen.Write(data) //nolint:errcheck // screen.Write always returns nil
	if h.screen.TakeScrollbackCleared() {
		// ED3 (erase scrollback): the app discarded its saved lines (an inline
		// TUI does this on every resize redraw). Clear the retained ring to
		// match a real terminal — Clear preserves committed so absolute indices
		// stay monotonic — and flag the next frame to tell clients to drop
		// history.
		h.scrollback.Clear()
		h.scrollbackClearedPending = true
	}
	if h.screen.TakePaletteChanged() {
		// OSC 4/104 changed the palette; defer a full repaint to the next frame.
		h.paletteChangedPending = true
	}
	if clip := h.screen.TakeClipboard(); len(clip) > 0 {
		// OSC 52 copy; hand it to the next frame as a clipboard message.
		h.pendingClipboard = clip
	}
	resp = h.screen.TakeResponse()
	// The screen write above may have changed the 1004 mode, so re-derive: on
	// the enable edge this reports the session's CURRENT focus state. Read under
	// h.mu like every other trigger (see reportFocus).
	if seq := h.focusReportLocked(h.registry.AnyClientFocused()); len(seq) > 0 {
		resp = append(resp, seq...)
	}
	h.mu.Unlock()
	// PTY output is the primary dirty source: wake the flush scheduler.
	h.markDirty()
	if len(resp) > 0 {
		h.ptmx.Write(resp) //nolint:errcheck // best-effort
	}
}

// flushFrame is the per-flush snapshot built under h.mu and consumed
// outside the lock. Holding the lock during the network write would
// stall every other goroutine on a slow client; the snapshot pattern
// keeps the lock window bounded to local memory work.
type flushFrame struct {
	clients map[*websocket.Conn]uint64
	// writers/gens carry the dispatch-safety metadata captured with the same
	// registry snapshot as clients: the per-socket state whose writeMu/
	// resumeGen gate the actual writes, and the resume generation this frame
	// was built against (see clientState.resumeGen and dispatchFrame).
	writers          map[*websocket.Conn]*clientState
	gens             map[*websocket.Conn]uint64
	rows             [][]vt.WireRun
	scrollLines      [][]vt.WireRun
	scrollWrapped    []bool // per scrollLines entry, see scrollbackRing.Append
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

// retainSuspendedScrollback drains lines produced with no attached clients into
// the retained main-screen history. The caller holds h.mu. One-shot signals
// deliberately remain pending for the next attach.
func (h *Handler) retainSuspendedScrollback() {
	if !h.sizeEstablished {
		return
	}
	drained, wrapped := drainScrollback(h.screen)
	if h.screen.InAltScreen || h.builder.altTransitionPending(h.screen) || len(drained) == 0 {
		return
	}
	h.scrollback.Append(drained, wrapped)
}

// drainScrollback drains the screen's scrolled-off lines with their wrap flags.
func drainScrollback(screen *vt.Screen) (lines [][]vt.WireRun, wrapped []bool) {
	wrapped = screen.DrainedWrapped()
	return screen.DrainScrollback(), wrapped
}

// buildFrame computes the next outbound frame under h.mu. Returns a nil frame
// if there is nothing to send (no resize yet, flush held, no attached
// clients, or no changed rows and no scroll lines). holdUntil is non-zero
// when a flush hold is active — a DEC 2026 synchronized-output hold or the
// post-resize redraw-settle hold — and the scheduler arms a retry at that
// deadline so a final held redraw with no subsequent PTY byte still flushes
// (a trigger-only scheduler would strand it).
func (h *Handler) buildFrame() (frame *flushFrame, holdUntil time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	clients, writers, gens := h.registry.Snapshot()
	if len(clients) == 0 {
		// Zero-client suspension (P4): nobody to render for. Retain history
		// but skip RenderRowWire/diff entirely. One-shot signals (palette
		// repaint, ED3 clear, OSC 52 clipboard) stay pending for the next
		// attach, whose builder reset forces their visible effect.
		h.retainSuspendedScrollback()
		return nil, time.Time{}
	}
	// An OSC 4/104 palette change re-colors already-drawn cells; force a full
	// repaint so every visible row re-resolves through the new palette. The
	// Reset persists if Build produces no frame this pass (flush held / no
	// resize yet), so the repaint still lands on the next frame.
	if h.paletteChangedPending {
		h.builder.Reset()
		h.paletteChangedPending = false
	}
	if h.screen.IsFlushHeld() {
		holdUntil = h.screen.FlushHoldUntil
	}
	// The redraw-settle hold (armRedrawSettle) gates frames exactly like a
	// DEC 2026 hold but is immune to the child's ESU; fold its deadline in so
	// the scheduler arms a retry at whichever hold lapses last.
	if hu := h.redrawHoldUntil(time.Now()); hu.After(holdUntil) {
		holdUntil = hu
	}
	committedBefore := h.scrollback.Committed()
	if holdUntil.IsZero() {
		if h.redrawCoalesceNow {
			// A cap lapse's coalesced update is a full repaint, never a diff of a
			// mid-redraw screen. Consumed here, not at the lapse, so an
			// overlapping DEC 2026 hold cannot swallow it.
			h.builder.Reset()
			h.redrawCoalesceNow = false
		}
		frame = h.builder.Build(h.screen, h.sizeEstablished, clients, committedBefore)
	}
	if frame != nil && len(frame.scrollLines) > 0 {
		h.scrollback.Append(frame.scrollLines, frame.scrollWrapped)
	}
	if frame != nil && h.scrollbackClearedPending {
		frame.scrollbackCleared = true
		// scrollbackCleared rides only a screen payload, which needs a changed
		// row; fold in the cursor row (as flush_builder.go does for bell) so a
		// title- or modes-only frame after ED3 still carries the clear.
		frame.changed = appendRowIfMissing(frame.changed, frame.curRow, len(frame.rows))
		h.scrollbackClearedPending = false
	}
	// OSC 52 clipboard is a one-shot event that can arrive with no screen
	// change, so ensure it rides a frame even when Build produced none.
	if len(h.pendingClipboard) > 0 {
		if frame == nil {
			frame = &flushFrame{clients: clients}
		}
		frame.clipboardPayload = encodeClipboardMsg(h.pendingClipboard)
		h.pendingClipboard = nil
	}
	if frame != nil {
		frame.writers, frame.gens = writers, gens
	}
	return frame, holdUntil
}

// flushPass runs one scheduler pass: build, then dispatch the frame or sweep
// bare acks (input into a silent app must still trim the client outbox).
// Returns the DEC 2026 hold deadline when the pass was suppressed by a hold.
func (h *Handler) flushPass() (holdUntil time.Time) {
	frame, holdUntil := h.buildFrame()
	if frame != nil {
		h.dispatchFrame(frame)
		return holdUntil
	}
	h.sweepAcks()
	return holdUntil
}

// flushLoop is the coalescing flush scheduler. Idle, it sleeps on h.dirty with
// no wakeups. The first output after idle flushes at once, so a keystroke echo
// waits on no tick; sustained output keeps frames at least flushInterval
// apart. A DEC 2026 hold arms a retry at its release deadline, so the final
// held redraw flushes even when no PTY byte follows it.
func (h *Handler) flushLoop(ctx context.Context) {
	for h.waitForDirty(ctx) {
		if !h.flushBurst(ctx) {
			return
		}
	}
}

// flushBurst passes immediately, then keeps passing while work arrives with
// passes spaced one flushInterval apart. A full quiet window ends the burst;
// context cancellation ends the scheduler.
func (h *Handler) flushBurst(ctx context.Context) bool {
	for {
		holdUntil := h.flushPass()
		if !holdUntil.IsZero() {
			if !h.waitForHoldRetry(ctx, holdUntil) {
				return false
			}
			continue
		}
		gotMore, alive := h.waitForBatchWindow(ctx)
		if !alive {
			return false
		}
		if !gotMore {
			return true
		}
	}
}

// waitForDirty blocks without a timer while the scheduler is idle.
func (h *Handler) waitForDirty(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-h.dirty:
		return true
	}
}

// waitForHoldRetry waits until the current synchronized-output hold expires,
// or returns early on another dirty poke so the live deadline can be re-read.
func (h *Handler) waitForHoldRetry(ctx context.Context, holdUntil time.Time) bool {
	timer := time.NewTimer(max(time.Until(holdUntil), 0))
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-h.dirty:
		timer.Stop()
		return true
	case <-timer.C:
		return true
	}
}

// waitForBatchWindow preserves the ticker-era sustained-output cadence. A
// dirty poke during the window waits out its remainder; a quiet window returns
// gotMore=false so flushLoop can go back to its timer-free idle wait.
func (h *Handler) waitForBatchWindow(ctx context.Context) (gotMore, alive bool) {
	timer := time.NewTimer(flushInterval)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false, false
	case <-h.dirty:
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, false
		case <-timer.C:
			return true, true
		}
	case <-timer.C:
		// A poke racing the timer edge stays buffered in h.dirty and
		// re-enters through waitForDirty immediately.
		return false, true
	}
}

// sweepAcks sends a bare ackOnly frame to every client whose session ledger
// advanced without a content frame carrying the new value this pass — input
// into a silent app (no echo, no output; e.g. `read -s`) would otherwise stay
// unacked indefinitely, leaving the client outbox untrimmed and widening the
// window where a later ledger loss drops or duplicates input.
// Called from flushLoop only on passes that produced no frame; passes WITH a
// frame stamp the fresh ack on every payload via withClientAck instead.
func (h *Handler) sweepAcks() {
	targets := h.registry.AckSweepTargets()
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ws, ack := range targets {
		ws.Write(ctx, websocket.MessageBinary, encodeAckOnly(ack)) //nolint:errcheck // best-effort
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

	// Assemble the per-client write sequence once, preserving the send order
	// (modes, title, clipboard, screen, scroll chunks).
	payloads := make([][]byte, 0, 4+len(scrollPayloads))
	for _, p := range [][]byte{frame.modesPayload, frame.titlePayload, frame.clipboardPayload, screenPayload} {
		if p != nil {
			payloads = append(payloads, p)
		}
	}
	payloads = append(payloads, scrollPayloads...)
	if len(payloads) == 0 {
		return
	}
	durable := durableSubset(frame.clipboardPayload, scrollPayloads)
	// One goroutine per client, so a wedged client stalls only itself and the
	// pass waits at most one 5 s write window. clientState.writeMu owns
	// sequence ordering against a resume batch. sweepAcks needs no writeMu: the
	// client ignores an ack at or below its trimmed count.
	var wg sync.WaitGroup
	// delivered records the ack each client's payloads actually carried, so the
	// bookkeeping below never records a value no frame carried.
	var deliveredMu sync.Mutex
	delivered := make(map[*websocket.Conn]uint64, len(frame.clients))
	for ws, ack := range frame.clients {
		wg.Go(func() {
			if stamped, ok := writeClientPayloads(ws, ack, frame.writers[ws], frame.gens[ws], payloads, durable); ok {
				deliveredMu.Lock()
				delivered[ws] = stamped
				deliveredMu.Unlock()
			}
		})
	}
	wg.Wait()
	// Record what each client was told, so the ack sweep does not resend it. A
	// client with nothing written is absent: its resumeAck already told it.
	h.registry.NoteAcksSent(delivered)
}

// durableSubset assembles the payloads that survive a resume-generation
// mismatch: the ones a resume batch does NOT re-deliver (see
// writeClientPayloads). Relative order matches the full dispatch sequence
// (clipboard, then scroll chunks). Returns nil when the frame carries no
// durable payloads — the common case for pure screen/modes diffs — and
// returns scrollPayloads as-is when there is no clipboard (no copy; the
// slice and its payloads are read-only downstream).
func durableSubset(clipboardPayload []byte, scrollPayloads [][]byte) [][]byte {
	if clipboardPayload == nil {
		return scrollPayloads
	}
	durable := make([][]byte, 0, 1+len(scrollPayloads))
	durable = append(durable, clipboardPayload)
	return append(durable, scrollPayloads...)
}

// writeClientPayloads writes one dispatch pass's payloads to one client under
// the resume gate. It returns the ack actually stamped on the written payloads
// (restamped on a strip) and whether anything was written, so the caller's
// delivered bookkeeping records what the wire carried. A nil state (test
// frames built without a registry snapshot) is written ungated.
func writeClientPayloads(ws *websocket.Conn, ack uint64, st *clientState, gen uint64, payloads, durable [][]byte) (uint64, bool) {
	stripped := false
	if st != nil {
		// BLOCKING: a resume batch in flight delays this frame rather than
		// dropping it. buildFrame already committed its scroll lines to the ring
		// and consumed the one-shots, and resume replays only above haveThrough,
		// so a skipped frame is a permanent scrollback hole. The batch's writes
		// run under a 10s budget, which this pass's wg.Wait inherits.
		st.writeMu.Lock()
		defer st.writeMu.Unlock()
		if st.resumeGen.Load() != gen {
			// Built before the batch's snapshot: drop screen, modes and title,
			// which would regress the client. Scroll lines and clipboard stay, since
			// the batch's replay does not resend them. Bell and the ED3 flag ride
			// the dropped screen payload and are lost for this client.
			payloads = durable
			stripped = true
		}
	}
	if len(payloads) == 0 {
		// Nothing to write (a state-only frame stripped bare): report
		// undelivered BEFORE the restamp below, so the empty path's
		// contract — nothing written, nothing recorded — stays observable
		// against the captured ack.
		return 0, false
	}
	if stripped {
		// The captured ack predates the resume and, after a ledger-loss resume,
		// can exceed the client's reset outbox, falsely trimming unacked input.
		// Under writeMu lastAckSent is exactly the latest resumeAck (no resume
		// runs while it is held, and flushLoop never sweeps during a dispatch),
		// which the client's monotone applyAck treats as a no-op.
		ack = st.lastAckSent.Load()
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range payloads {
		ws.Write(writeCtx, websocket.MessageBinary, withClientAck(p, ack)) //nolint:errcheck // best-effort
	}
	return ack, true
}

// controlMsg is a JSON control message from the client.
//
// Field ORDER is alignment-driven (govet fieldalignment), not semantic: the
// slice and strings lead, then the pointer, then the 8-byte scalars, then the
// machine ints. Read the comments for meaning, not the layout.
type controlMsg struct {
	// HaveThrough is the highest absolute line index the client holds; resume
	// replays only indices above it. -1 asks for the full retained history.
	HaveThrough *int64    `json:"haveThrough"`
	Type        string    `json:"type"`
	SessionID   SessionID `json:"sessionId,omitempty"`
	// Data carries an `ephemeralInput` payload: the input bytes as a JSON string.
	// An ESC is \u001b, so every escape sequence this channel exists for is
	// expressible.
	Data string `json:"data,omitempty"`
	// ReplayMax asks for a resume replay smaller than maxReplayLines. Decoded
	// as RawMessage and parsed FIELD-LOCALLY (parseReplayMax) because this
	// handler drops any control whose unmarshal returns an error: a malformed
	// value on an ADVISORY field must read as absent, never cost the client
	// its whole resume.
	ReplayMax json.RawMessage `json:"replayMax,omitempty"`
	SentBytes uint64          `json:"sentBytes,omitempty"`
	// FromAbs/MaxLines carry a `history` request: serve at most MaxLines
	// retained lines starting at absolute index FromAbs. Signed on the wire and
	// validated BEFORE any conversion to uint64 — the subtraction-form
	// overflow guard in historyControl is the reason these are int64 rather
	// than uint64.
	FromAbs  int64 `json:"fromAbs,omitempty"`
	MaxLines int64 `json:"maxLines,omitempty"`
	Cols     int   `json:"cols,omitempty"`
	Rows     int   `json:"rows,omitempty"`
	// ProtocolVersion is the client's wire-protocol revision (resume only).
	// 0 means version-silent legacy client and remains tolerated. A declared
	// revision below MinSupportedClientWireVersion is refused; a higher-than-
	// current revision warns but continues because it may retain this server's
	// compatible baseline.
	ProtocolVersion int `json:"protocolVersion,omitempty"`
	// Focused carries a `focus` control: whether the client's terminal widget is
	// focused. A plain bool, because the client always writes the field and a
	// missing one reading as unfocused needs no separate arm.
	Focused bool `json:"focused,omitempty"`
}

// handleWS upgrades to WebSocket, spawns the configured command in a
// PTY, and bridges bytes both ways until either side closes.
func (h *Handler) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := acceptWS(w, r, h.cfg.originPolicy)
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
	// Wake the scheduler for the attach itself: a client that never speaks
	// resume or resize (a bare reader) must still receive the current screen
	// — under the old ticker the next tick delivered it; the event-driven
	// loop needs the poke (this also ends any zero-client suspension).
	h.markDirty()
	// Re-derive the DEC 1004 answer for the new attachment. It cannot change it
	// today — a socket starts unfocused, so AnyClientFocused is unmoved — but the
	// trigger set stays complete rather than resting on that invariant, and the
	// write is transition-gated so a no-op costs no report.
	h.reportFocus()

	defer func() {
		dCols, dRows := h.registry.Remove(ws)
		// A departing client can drop the session's focus to nobody, and it
		// emits no focus-out of its own on the way out.
		h.reportFocus()
		h.maybeHealSize(dCols, dRows)
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
	// and exitAwareCloseCode). resumeServed defers that close on an
	// attach-to-already-exited session until the first resume exchange has been
	// written, so the client renders the final screen before the 4001.
	resumeServed := make(chan struct{})
	var resumeOnce sync.Once
	markResumeServed := func() { resumeOnce.Do(func() { close(resumeServed) }) }
	go h.closeOnProcExit(ctx, ws, resumeServed)

	// Read input from this client and write to the shared PTY.
	h.clientReadLoop(ctx, ws, state, markResumeServed)
}

// clientReadLoop pumps one client's messages until the socket dies, a
// protocol violation closes it, or a resume on another socket fences it.
//
// v4 typed framing (contract: WireProtocolVersion in wire_binary.go): a binary
// resume declaring protocolVersion >= 4 ARMS the connection; the first valid
// recognized TEXT control then LATCHES typed mode (text is control, binary is
// PTY input). Until the latch, binary frames keep exact v3 semantics (the 0x00
// sentinel plus the parse-fallback), so v3 and version-silent clients stay there.
func (h *Handler) clientReadLoop(ctx context.Context, ws *websocket.Conn, state *clientState, markResumeServed func()) {
	var armed, latched bool
	for {
		typ, msg, err := ws.Read(ctx)
		if err != nil || state.fenced.Load() {
			return
		}
		if typ == websocket.MessageText {
			var closed bool
			armed, latched, closed = h.handleTextControl(ws, state, msg, armed, latched, markResumeServed)
			if closed {
				return
			}
			continue
		}
		if len(msg) == 0 {
			continue // ignored without latching (documented in the design)
		}
		var ok bool
		armed, ok = h.handleBinaryFrame(ws, state, msg, armed, latched, markResumeServed)
		if !ok {
			return
		}
	}
}

// handleBinaryFrame routes one binary message: pre-latch, a 0x00-leading
// frame is tried as a v3 sentinel control (with the parse-fallback delivering
// non-JSON frames to the PTY whole, leading NUL included, so no input byte is
// ever silently swallowed and acks stay on frame boundaries); post-latch, and
// for all non-sentinel frames, the bytes are PTY input. Returns the updated
// armed state and ok=false when the connection must end (PTY start/write
// failure, or the socket's ledger has moved to another socket).
func (h *Handler) handleBinaryFrame(ws *websocket.Conn, state *clientState, msg []byte, armed, latched bool, onResumeServed func()) (newArmed, ok bool) {
	if !latched && msg[0] == 0x00 {
		if d := h.handleControl(ws, state, msg[1:], onResumeServed); d.parsed {
			return armed || d.armsV4, !d.closed
		}
		// Parse-fallback: fall through and deliver the WHOLE frame as input.
	}
	// Ensure process is started (fallback if no resize was sent).
	// h.started is atomic.Bool so the fast-path read does not race
	// with ensureStarted's write. cols/rows of 0 select defaults.
	if !h.started.Load() {
		if err := h.ensureStarted(0, 0); err != nil {
			h.logStartFailure(err)
			return armed, false
		}
	}
	applied, err := h.registry.ApplyInput(state, h.ptmx, msg)
	if err != nil {
		h.cfg.logger.Debug("terminal: pty write", "error", err)
		return armed, false
	}
	if !applied {
		return armed, false
	}
	// Wake the scheduler even though no PTY output may follow (a silent
	// reader, e.g. `read -s`): the pass's ack sweep is what trims the
	// client's outbox for input that produces no echo.
	h.markDirty()
	return armed, true
}

// handleTextControl applies the v4 text-frame policy (see
// WireProtocolVersion in wire_binary.go) to one text message: text is only ever a
// control channel, it can never become PTY input, and anything that is not a
// valid control on an armed connection closes the connection rather than
// risking framing-state poison. Returns the updated (armed, latched) state and
// closed=true when the read loop must end: the connection is then either closed
// by this call or left open for the caller's deferred close.
func (h *Handler) handleTextControl(ws *websocket.Conn, state *clientState, msg []byte, armed, latched bool, onResumeServed func()) (newArmed, newLatched, closed bool) {
	if !utf8.Valid(msg) {
		// RFC 6455 §5.6 requires valid UTF-8 in text messages and
		// coder/websocket does not validate it; Go's encoding/json would
		// silently replace invalid sequences, so reject explicitly.
		h.cfg.logger.Warn("terminal: closing on invalid UTF-8 text frame", "bytes", len(msg))
		_ = ws.Close(websocket.StatusInvalidFramePayloadData, "control frames must be valid UTF-8")
		return armed, latched, true
	}
	if len(msg) == 0 || !armed {
		// No v3 peer ever sends text, so pre-arm (or empty) text is a
		// protocol violation; closing is the only response that cannot
		// poison the framing state.
		h.cfg.logger.Warn("terminal: closing on unexpected text frame", "armed", armed, "bytes", len(msg))
		_ = ws.Close(websocket.StatusUnsupportedData, "unexpected text frame")
		return armed, latched, true
	}
	d := h.handleControl(ws, state, msg, onResumeServed)
	switch {
	case d.closed:
		return armed, latched, true
	case !d.parsed:
		h.cfg.logger.Warn("terminal: closing on unparseable text control", "bytes", len(msg))
		_ = ws.Close(websocket.StatusUnsupportedData, "unparseable control frame")
		return armed, latched, true
	case d.known:
		latched = true // the transition (and every later recognized text control) latches
		if d.armsV4 {
			armed = true // a text resume keeps the arm current (idempotent)
		}
	case !latched:
		// Valid JSON but an unrecognized type before any latch: refuse
		// rather than guess (post-latch, unknown types are tolerated and
		// dropped, matching the binary path's long-standing behavior).
		h.cfg.logger.Warn("terminal: closing on unrecognized text control before upgrade")
		_ = ws.Close(websocket.StatusUnsupportedData, "unrecognized control before upgrade")
		return armed, latched, true
	}
	return armed, latched, false
}

// controlDisposition reports how handleControl classified one control payload,
// so the read loop can drive the v4 framing state machine (arm/latch) and the
// v3 parse-fallback without re-parsing the JSON.
type controlDisposition struct {
	parsed bool // payload was valid control JSON
	known  bool // c.Type was a recognized control type
	armsV4 bool // a resume declaring protocolVersion >= typedFramingMinVersion
	closed bool // the connection was closed, or must end
}

// resumeControl is handleControl's resume case: the wire-version gate, the
// v4 framing latch, the per-socket spam throttle, and the handleResume
// dispatch. Extracted verbatim (gocognit) — behavior is handleControl's.
func (h *Handler) resumeControl(ws *websocket.Conn, state *clientState, c *controlMsg, d controlDisposition, onResumeServed func()) controlDisposition {
	d.known = true
	if c.ProtocolVersion != 0 && c.ProtocolVersion < minSupportedClientWireVersion {
		h.cfg.logger.Warn("terminal: refusing client below wire-protocol compatibility floor",
			"client", c.ProtocolVersion, "server", wireProtocolVersion,
			"min_supported", minSupportedClientWireVersion,
			"hint", "reload or upgrade the client")
		_ = ws.Close(WireIncompatibleCloseCode, wireIncompatibleClientReason)
		d.closed = true
		return d
	}
	d.armsV4 = c.ProtocolVersion >= typedFramingMinVersion
	// A higher revision may retain this server's compatible baseline, so it
	// warns but is not refused. Version-silent clients remain tolerated.
	if c.ProtocolVersion > wireProtocolVersion {
		h.cfg.logger.Warn("terminal: client wire-protocol version is newer than server",
			"client", c.ProtocolVersion, "server", wireProtocolVersion,
			"min_supported", minSupportedClientWireVersion,
			"hint", "upgrade the server if terminal behavior is incorrect")
	}
	if c.SessionID == "" {
		return d
	}
	if !state.takeResumeToken() {
		// An over-limit resume is dropped with no resumeAck, indistinguishable
		// from network loss; the v4 framing latch above stays armed.
		h.cfg.logger.Warn("terminal: resume throttled",
			"session_id", LogID(c.SessionID))
		return d
	}
	// A nil (omitted) haveThrough means the client holds nothing and wants
	// full history (-1), not "have line 0" (which would drop index 0).
	ht := int64(-1)
	if c.HaveThrough != nil {
		ht = *c.HaveThrough
	}
	replayMax := parseReplayMax(c.ReplayMax)
	if h.handleResume(ws, state, c.SessionID, ht, c.SentBytes, replayMax) {
		d.closed = true
		return d
	}
	if onResumeServed != nil {
		onResumeServed()
	}
	return d
}

// maxSafeInteger is JavaScript's exact-integer ceiling (2^53 − 1). Absolute
// line indices cross the wire as JSON numbers, so a value above this cannot be
// represented exactly on the client and is rejected rather than silently
// rounded.
const maxSafeInteger int64 = 1<<53 - 1

// shrinkToBudget returns how many leading lines, plus the scroll header, fit
// pageByteBudget, always at least one (capRowRuns guarantees a row fits). It
// keeps a PREFIX by necessity: shrinking from the low end would move the
// reply's firstIndex above fromAbs, which the client reads as the CLAMP signal
// and paints as a false "history permanently trimmed" marker.
func shrinkToBudget(lines [][]vt.WireRun) int {
	size := encodedScrollHeaderSize
	for i, line := range lines {
		size += encodedRowSize(capRowRuns(line))
		if size > pageByteBudget {
			return max(i, 1)
		}
	}
	return len(lines)
}

// historyControl serves a `history` request for at most maxLines retained lines
// from fromAbs, inline on the read loop and under writeMu so a reply never
// interleaves into a resume batch. It serves the INTERSECTION of the request
// and the retained range, so a non-empty reply's firstIndex lies in
// [fromAbs, end); an empty reply carries the request's own fromAbs and means
// "nothing here is retained", which the client reads as a permanent trim.
func (h *Handler) historyControl(ws *websocket.Conn, state *clientState, c *controlMsg) {
	// A history control before the socket's first successful resume has no
	// attached session to answer for, and ignoring it keeps pre-resume sockets
	// cost-free. The client orders this by construction (its bootstrap sends
	// resume first and controls are FIFO).
	if state.session.Load() == nil {
		h.cfg.logger.Debug("terminal: history control before resume; ignoring")
		return
	}
	if !h.historyPagingDeclared() {
		// Not advertised for this ring depth, so a client should never have
		// sent one. Debug rather than Warn: a stale bundle is a pairing
		// artifact, not abuse.
		h.cfg.logger.Debug("terminal: history control but paging not declared",
			"scrollback_capacity", h.cfg.scrollbackCapacity)
		return
	}
	// Validation order is normative: maxLines FIRST, then fromAbs against the
	// SUBTRACTION form of the safe-integer bound. The addition form
	// (fromAbs+maxLines <= maxSafeInteger) is itself the overflow it exists to
	// reject, so `end` below is exact in both languages.
	if c.MaxLines < 1 || c.MaxLines > historyPageSize {
		h.cfg.logger.Debug("terminal: history maxLines out of range", "max_lines", c.MaxLines)
		return
	}
	if c.FromAbs < 0 || c.FromAbs > maxSafeInteger-c.MaxLines {
		h.cfg.logger.Debug("terminal: history fromAbs out of range", "from_abs", c.FromAbs)
		return
	}
	if !state.takeHistoryToken() {
		h.cfg.logger.Warn("terminal: history throttled")
		return
	}

	fromAbs := uint64(c.FromAbs) // #nosec G115 -- validated non-negative above
	end := fromAbs + uint64(c.MaxLines)

	h.mu.Lock()
	oldest := h.scrollback.OldestIndex()
	committed := h.scrollback.Committed()
	start := max(fromAbs, oldest)
	lim := min(end, committed)
	var lines [][]vt.WireRun
	if start < lim {
		_, lines = h.scrollback.LinesRange(start, int(lim-start)) // #nosec G115 -- bounded by maxLines
	}
	h.mu.Unlock()

	firstIndex := fromAbs
	if len(lines) > 0 {
		// The byte budget can serve FEWER lines than asked for. A short page is
		// never a terminator: the client's next paced trigger requests the
		// remainder.
		lines = lines[:shrinkToBudget(lines)]
		firstIndex = start
	}

	ctx, cancel := context.WithTimeout(context.Background(), historyWriteTimeout)
	defer cancel()
	// Stamped with the socket's current ack but deliberately NOT recorded in
	// lastAckSent: the next ack-only sweep may send one redundant ack, which is
	// harmless because the client's apply is monotonic.
	ack := state.lastAckSent.Load()
	state.writeMu.Lock()
	defer state.writeMu.Unlock()
	ws.Write(ctx, websocket.MessageBinary, encodeScrollMsg(ack, firstIndex, lines)) //nolint:errcheck // best-effort
}

// handleControl dispatches one client control message (binary sentinel payload
// or whole text message — the transport is the caller's concern). onResumeServed
// is invoked after a resume exchange has been fully written to the socket
// (resumeAck + modes/title + window frame + history replay); handleWS uses it to
// release the deferred process-exited close for a client that attached to an
// already-exited session (see closeOnProcExit).
func (h *Handler) handleControl(ws *websocket.Conn, state *clientState, payload []byte, onResumeServed func()) controlDisposition {
	var c controlMsg
	if err := json.Unmarshal(payload, &c); err != nil {
		h.cfg.logger.Debug("terminal: bad control frame", "error", err, "bytes", len(payload))
		return controlDisposition{}
	}
	d := controlDisposition{parsed: true}
	switch c.Type {
	case ctlTypeResume:
		return h.resumeControl(ws, state, &c, d, onResumeServed)
	case ctlTypeResize:
		d.known = true
		d.closed = h.handleResize(state, c.Cols, c.Rows)
	case ctlTypePing:
		d.known = true
		h.handlePing(ws)
	case ctlTypeHistory:
		d.known = true
		h.historyControl(ws, state, &c)
	case ctlTypeFocus:
		d.known = true
		h.focusControl(state, c.Focused)
	case ctlTypeEphemeralInput:
		d.known = true
		h.ephemeralInputControl(state, &c)
	case ctlTypeUpgrade:
		// The v4 transition control: recognizing it is what latches typed
		// framing in the read loop; nothing else to do.
		d.known = true
	default:
		h.cfg.logger.Debug("terminal: unrecognized control type",
			"type", textsafe.SanitizeSingleLineBounded(c.Type, maxLoggedControlTypeBytes))
	}
	return d
}

// handlePing answers a client liveness probe with a pong. The client
// sends a ping only after a stretch of inbound silence to tell apart an
// idle-but-healthy socket from one iOS froze during sleep; the pong (or
// any other frame) clears its probe. Best-effort: a write failure means
// the socket is already gone, which the client's probe timeout will catch.
func (*Handler) handlePing(ws *websocket.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws.Write(ctx, websocket.MessageBinary, encodePongMsg()) //nolint:errcheck // best-effort liveness reply
}

// testResumeBatchHold, when non-nil, is invoked by handleResume after its
// batch writes complete but BEFORE the write-lock release. Test-only
// (resume_ordering_test.go): it lets a test hold a REAL resume batch open at
// its most adversarial instant and drive live output against the dispatch
// gate deterministically. Atomic so a test's store cannot race a straggling
// handler goroutine (httptest.Close does not wait for hijacked conns).
// Never set in production.
var testResumeBatchHold atomic.Pointer[func()]

// historyPagingDeclared reports whether this server advertises demand-paged
// scrollback (resumeAckFlagHistoryPaging) and so serves the `history` control.
// It keys on paginationMinRing: a ring the resume replay can TRUNCATE must
// declare paging, or the withheld rows are unreachable for the life of the
// session.
func (h *Handler) historyPagingDeclared() bool {
	return h.cfg.scrollbackCapacity >= paginationMinRing
}

// parseReplayMax extracts controlMsg.ReplayMax's advisory value: nil for an
// absent, null, malformed (fractional, string, overflowing) or < 1 value, so
// replayStart applies maxReplayLines alone; otherwise the value clamped DOWN to
// maxReplayLines. The clamp mirrors the client's own pre-send clamp, so the
// sent and honored values are the same number, which the client's replay-jump
// prediction depends on.
func parseReplayMax(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil
	}
	if n < 1 {
		return nil
	}
	bounded := min(n, maxReplayLines)
	return &bounded
}

// replayStart returns the absolute index the resume replay begins at: the first
// line the client is missing (haveThrough + 1), clamped so the replay carries at
// most maxReplayLines. The clamp is UNCONDITIONAL because the ring can hold
// hundreds of thousands of lines, and replaying all of them is tens of megabytes
// under one write lock inside a single 10s context; a client may only ask for
// LESS. Clamping the START rather than the end is what lets the client detect
// the jump: it sees a first index above what it holds.
func replayStart(committed uint64, haveThrough int64, replayMax *int64) uint64 {
	var from uint64
	if haveThrough >= 0 {
		from = uint64(haveThrough) + 1
	}
	bound := uint64(maxReplayLines)
	if replayMax != nil {
		bound = min(bound, uint64(*replayMax)) // #nosec G115 -- parseReplayMax guarantees 1..maxReplayLines
	}
	// uint64 guard: when committed is at or below the bound the floor is 0, so
	// the clamp contributes nothing and `from` stands.
	if committed > bound {
		return max(from, committed-bound)
	}
	return from
}

// handleResume attaches state to sessionID's input ledger and replies with a
// resumeAck (the ledger's received count, the retained index bounds, and a
// ledger-lost flag when the key missed the registry while sentBytes > 0), then
// sends the window frame and replays lines after haveThrough (-1 = none),
// bounded by replayMax. On the main screen the frame goes first so a stale-alt
// client leaves alt before history lands; in alt the replay goes first, because
// the client drops scroll frames while its alt flag is set. It reports
// superseded, sending nothing, when another socket already took the ledger.
func (h *Handler) handleResume(ws *websocket.Conn, state *clientState, sessionID SessionID, haveThrough int64, sentBytes uint64, replayMax *int64) (superseded bool) {
	ack, created, displaced := h.registry.ResolveSession(state, sessionID)
	if displaced != nil {
		h.cfg.logger.Info("terminal: resume superseded the ledger's previous socket",
			"session_id", LogID(sessionID))
		// Close waits on the displaced socket's close handshake, which must not
		// block this resume.
		go func(prev *websocket.Conn) { _ = prev.Close(websocket.StatusNormalClosure, "superseded") }(displaced.ws)
	}
	if state.fenced.Load() {
		return true
	}
	// Capability declaration for the resumeAck's historyPaging bit. It no longer
	// gates the replay clamp: that bound is unconditional, so a shallow-ring
	// server bounds its replay too.
	paging := h.historyPagingDeclared()
	ledgerLost := created && sentBytes > 0
	if ledgerLost {
		// The client half of the event gcIdleSessions logged server-side;
		// together the two lines make a forgotten-ledger incident correlatable
		// end to end.
		h.cfg.logger.Info("terminal: resume key missed with claimed sent bytes; signaling ledger loss",
			"session_id", LogID(sessionID), "sent_bytes", sentBytes)
	}

	// writeMu spans snapshot and batch, and the generation bump shares the
	// snapshot's h.mu section: an earlier flush finished before this snapshot,
	// and a later one waits and is stripped if built before it, so no stale
	// screen lands after the batch.
	state.writeMu.Lock()
	defer state.writeMu.Unlock()

	h.mu.Lock()
	state.resumeGen.Add(1)
	// Reset inside the snapshot's h.mu section, so the first frame built after
	// it is a full repaint, never a diff against a cache this client lacks.
	h.builder.Reset()
	// Commit any pending drain to history at its absolute index before
	// computing the replay, so lines that scrolled while the client was
	// away are retained.
	drained, wrapped := drainScrollback(h.screen)
	// Match Build's guard: drain that straddles an alt-screen transition belongs to the
	// buffer just left and must not enter main history.
	if !h.screen.InAltScreen && !h.builder.altTransitionPending(h.screen) && len(drained) > 0 {
		h.scrollback.Append(drained, wrapped)
	}
	committed := h.scrollback.Committed()
	oldest := h.scrollback.OldestIndex()
	from := replayStart(committed, haveThrough, replayMax)
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
	// Encode modes straight from the screen, not builder.buildModesPayload:
	// that mutates the shared announce state and would starve a client
	// connecting concurrently of its own modes frame.
	modesPayload := encodeModesMsg(h.screen.BracketedPaste, h.screen.AppCursorKeys,
		h.screen.MouseSGR, h.screen.FocusReporting, h.screen.AppKeypad,
		h.screen.ReverseVideo, h.screen.MousePixels, h.screen.MouseMode, h.screen.KeyboardFlags())
	titlePayload := encodeTitleMsg(h.screen.Title)
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

	// resumeAck first, so the client trims its outbox and learns the history
	// bounds before the replay lands. This handler always serves focus and
	// ephemeral input, WithKeepUnfocused included.
	flags := resumeAckFlags{LedgerLost: ledgerLost, HistoryPaging: paging, ServerFocus: true, EphemeralInput: true}
	ws.Write(ctx, websocket.MessageBinary, encodeResumeAck(ack, h.bootEpoch, committed, oldest, flags)) //nolint:errcheck // best-effort
	state.lastAckSent.Store(ack)

	// Resend current modes/title inline (before the window/replay) so input
	// encoding is correct before the user can type; a fresh tab starts at
	// default modes and would otherwise misencode arrows/mouse for one flush.
	ws.Write(ctx, websocket.MessageBinary, withClientAck(modesPayload, ack)) //nolint:errcheck // best-effort
	if titlePayload != nil {
		ws.Write(ctx, websocket.MessageBinary, withClientAck(titlePayload, ack)) //nolint:errcheck // best-effort
	}

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

	// The client drops scroll lines while its alt flag is set (store.ts
	// applyScroll), and the window frame sets that flag to winAlt. So in alt the
	// main-screen replay goes first, and out of alt the window goes first to
	// clear a stale alt flag before the replay lands.
	if winAlt {
		replayHistory()
		ws.Write(ctx, websocket.MessageBinary, windowPayload) //nolint:errcheck // best-effort
	} else {
		ws.Write(ctx, websocket.MessageBinary, windowPayload) //nolint:errcheck // best-effort
		replayHistory()
	}

	// Test seam: lets resume_ordering_test hold a REAL batch open (writeMu
	// still held, batch writes done) to drive live output against the gate
	// deterministically. Nil in production.
	if hold := testResumeBatchHold.Load(); hold != nil {
		(*hold)()
	}

	// A fresh attach ends any zero-client suspension: poke the scheduler so
	// the full-repaint flush (against the builder reset taken with the
	// snapshot above) repaints the window idempotently on the first pass.
	// The deferred writeMu.Unlock runs right after this poke; the pass it
	// wakes blocks on the lock for at most that gap.
	h.markDirty()
	return false
}

// clampResize floors the requested dimensions to a sane minimum and caps them
// at the eager-allocation ceiling. Floored (rather than dropped) so a near-zero
// reading from an iPad keyboard-slide animation still drives ensureStarted on
// first connect — dropping the resize would leave the process unstarted until
// the client sent raw input.
func clampResize(cols, rows int) (clampedCols, clampedRows int) {
	clampedCols = min(max(cols, minResizeCols), maxResizeCols)
	clampedRows = min(max(rows, minResizeRows), maxResizeRows)
	return clampedCols, clampedRows
}

// handleResize applies a client's requested size to the shared PTY + screen
// (last-writer-wins: the most recent resize from any attached client sets the
// size) and records the clamped size on that client's socket, so a later
// disconnect can heal the screen to the smallest size the remaining clients
// need (see maybeHealSize). It reports whether the start was refused because
// Close has run: no process exit will close that client's socket, so the
// caller must end it.
func (h *Handler) handleResize(state *clientState, cols, rows int) (closed bool) {
	cols, rows = clampResize(cols, rows)
	// Start the child process on first resize so it knows the correct dimensions
	// from the start (avoids initial paint at wrong size).
	if !h.started.Load() {
		if err := h.ensureStarted(cols, rows); err != nil {
			h.logStartFailure(err)
			return errors.Is(err, errHandlerClosed)
		}
	}
	h.registry.RecordSize(state, cols, rows)
	h.applySize(cols, rows, "resize received")
	return false
}

// armRedrawSettle starts the redraw-settle hold: flushes stay suppressed until
// the child's redraw output has been quiet for redrawSettleQuiet, capped at
// redrawSettleCap from now. Caller holds h.mu. vt.Screen.HoldFlush cannot hide
// a SIGWINCH redraw: it shares DEC 2026's deadline, so the first CSI ?2026l of
// an inline TUI's chunked reprint releases it and clients see the reprint churn.
// This hold is handler state escape sequences cannot touch: the redraw ends when
// the child goes quiet, not when it closes a bracket.
func (h *Handler) armRedrawSettle(now time.Time) {
	h.redrawSettleUntil = now.Add(redrawSettleCap)
	// The arm itself counts as activity so the hold lasts at least
	// redrawSettleQuiet even if the child's first redraw byte is still in
	// flight (SIGWINCH delivery latency); flushing before it arrives would
	// show the pre-redraw reflowed screen — the state the hold exists to hide.
	h.redrawLastData = now
	// A fresh arm is a fresh redraw, so it gets the full coalescing budget.
	h.redrawRearms = 0
	h.redrawCoalesceNow = false
}

// disarmRedrawSettle ends the hold for good and records which exit fired, so a
// program that never goes quiet after a resize is observable rather than silent.
// Caller holds h.mu.
func (h *Handler) disarmRedrawSettle(reason string) {
	h.redrawSettleUntil = time.Time{}
	h.cfg.logger.Debug("terminal: redraw-settle hold released", "reason", reason, "rearms", h.redrawRearms)
}

// redrawHoldUntil returns the moment the redraw-settle hold could next change
// state, or the zero time when the hold is inactive or this pass may flush.
// Caller holds h.mu. The hold releases for good when the child goes quiet or
// the re-arm budget is spent; a cap lapse with budget left lets one full
// repaint through and re-arms.
func (h *Handler) redrawHoldUntil(now time.Time) time.Time {
	if h.redrawSettleUntil.IsZero() {
		return time.Time{}
	}
	quietAt := h.redrawLastData.Add(redrawSettleQuiet)
	if !now.Before(quietAt) {
		h.disarmRedrawSettle("settled")
		return time.Time{}
	}
	if !now.Before(h.redrawSettleUntil) {
		if h.redrawRearms >= redrawSettleMaxRearms {
			// The child has been writing continuously for the whole budget, so
			// this is a stream rather than a redraw. Stop holding it.
			h.disarmRedrawSettle("rearm budget exhausted")
			return time.Time{}
		}
		h.redrawRearms++
		h.redrawCoalesceNow = true
		h.redrawSettleUntil = now.Add(redrawSettleCap)
		return time.Time{}
	}
	if h.redrawSettleUntil.Before(quietAt) {
		return h.redrawSettleUntil
	}
	return quietAt
}

// applySize resizes the PTY and the shared VT screen and, when the dimensions
// actually change, holds flushes over the SIGWINCH redraw window so clients
// don't see the child's transient cleared-screen / mid-reprint states. The
// hold releases when the redraw output settles (see armRedrawSettle). A
// same-size call (a client reconnect re-sending its size) arms nothing: the
// kernel suppresses SIGWINCH for an unchanged winsize, so there is no redraw
// to hide and live output must not stall. Shared by the live resize path and
// the disconnect heal.
func (h *Handler) applySize(cols, rows int, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started.Load() || h.ptmx == nil || h.ptyClosed {
		return
	}
	// Clamp again so applySize is safe regardless of caller (the heal path
	// passes MinLiveSize values). Idempotent for the live path, which already
	// clamped in handleResize.
	cols, rows = clampResize(cols, rows)
	if err := pty.Setsize(h.ptmx, &pty.Winsize{
		// #nosec G115 -- clampResize bounds cols/rows to [minResize, maxResize<=1000], >0, just above; no uint16 overflow. gosec can't see through the helper.
		Cols: uint16(cols), Rows: uint16(rows),
	}); err != nil {
		// The PTY keeps the winsize it had, so the child never learns the new one
		// and keeps painting at the width it last read. Moving the screen anyway
		// models a width nothing on the wire is written for, and a width shrink
		// DROPS each row's tail (vt.Screen.resizeWidth), so the disagreement
		// destroys output.
		h.cfg.logger.Warn("terminal: "+reason+" not applied",
			"error", err,
			"cols", h.screen.Width, "rows", h.screen.Height,
			"requested_cols", cols, "requested_rows", rows)
		return
	}
	sizeChanged := cols != h.screen.Width || rows != h.screen.Height
	h.screen.Resize(rows, cols)
	if sizeChanged {
		h.armRedrawSettle(time.Now())
	}
	h.cfg.logger.Info("terminal: "+reason, "rows", rows, "cols", cols)
	h.sizeEstablished = true
	h.builder.Reset()
	// The resize hold suppresses passes until the app's redraw settles; the
	// poke makes the scheduler arm the release deadline so the repaint
	// flushes even if the app writes nothing after the hold window.
	h.markDirty()
}

// maybeHealSize arms a debounced size recompute when the client that just
// disconnected was the one dictating the current shared screen size (its last
// reported size equals the screen's). Only that case can strand a survivor at a
// departed client's size — e.g. a desktop left clamped to a phone's size after
// the phone closes its tab. Any other departure is skipped: some other client,
// or a live resize, still holds the current size, so there is nothing to relax.
// Debounced via healDebounce so a brief reconnect at the same size is a no-op
// rather than a grow-then-shrink flap.
func (h *Handler) maybeHealSize(dCols, dRows int) {
	if dCols <= 0 || dRows <= 0 {
		return // the departed client never reported a size
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started.Load() || h.ptyClosed {
		return
	}
	if dCols != h.screen.Width || dRows != h.screen.Height {
		return // the departed client was not holding the current size
	}
	if h.healTimer != nil {
		h.healTimer.Stop()
	}
	h.healTimer = time.AfterFunc(healDebounce, h.healSize)
}

// healSize relaxes the shared screen to the smallest size across the clients
// still connected, so a survivor no longer stays clamped to a departed client's
// size. Runs from the debounced healTimer. A no-op when no surviving client has
// a known size, or when the smallest already equals the current screen (e.g.
// the departed client reconnected within the debounce and re-reported the same
// size, so it is counted again).
func (h *Handler) healSize() {
	cols, rows, ok := h.registry.MinLiveSize()
	if !ok {
		return
	}
	h.mu.Lock()
	unchanged := !h.started.Load() || (cols == h.screen.Width && rows == h.screen.Height)
	h.mu.Unlock()
	if unchanged {
		return
	}
	h.applySize(cols, rows, "size healed after client departure")
}
