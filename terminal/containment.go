package terminal

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Containment places each session's tree in its own cgroup v2 at clone time,
// the one boundary forking and setsid() cannot escape. Teardown is SIGTERM then
// cgroup.kill. memory.peak is a true high-water mark only because the child is
// placed at clone time: cgroup v2 does not move existing charges when a process
// migrates in. It sets no memory ceiling, which would OOM-kill a live session.
// reap.go is the default boundary without cgroups.

// errContainmentUnsupported is returned by NewContainment on any platform or host
// that cannot support the feature. Consumers should log it once and continue
// without containment rather than failing to start.
var errContainmentUnsupported = errors.New("terminal: per-session containment unsupported on this host")

// CgroupRoot is the writable cgroup v2 mount NewContainment reshapes. It is a
// type of its own so it cannot silently swap places with the prefix.
type CgroupRoot string

const (
	// containGrace bounds each teardown settle window; a measured runtime
	// exits about 100 ms after SIGTERM.
	containGrace = 250 * time.Millisecond

	// containServerLeaf receives every process in root: cgroup v2 forbids
	// enabling controllers on a cgroup that holds processes.
	containServerLeaf = "server"

	// containProbeLeaf is the throwaway cgroup the startup probes use. Reserved
	// against session ids for the same reason containServerLeaf is.
	containProbeLeaf = "probe"

	// containMaxIDLen bounds a sanitized session id used as a directory name.
	containMaxIDLen = 64
)

// WithContainment places this session's process tree in its own cgroup under c
// and makes that cgroup the unit of teardown. id names the cgroup (sanitized)
// and should be the session id the consumer logs. A nil c disables containment
// but still sets id as the session's log label, so NewContainment's result can
// be passed through unconditionally.
func WithContainment(c *Containment, id SessionID) Option {
	return func(cfg *handlerConfig) {
		cfg.containment = c
		cfg.containmentID = id
	}
}

// WithContainmentSampleInterval logs a live cost line per contained session on
// this interval (session id, current memory, pid count, age). Zero, the default,
// logs only at session end.
func WithContainmentSampleInterval(d time.Duration) Option {
	return func(cfg *handlerConfig) { cfg.containSample = d }
}

// sanitizeCgroupName reduces name to a single safe path segment for use as a
// cgroup directory name, or returns "" if nothing usable remains.
//
// It takes a string because NewContainment's prefix uses it too. Keeping only
// [A-Za-z0-9_-] makes a traversal under mkdir impossible.
func sanitizeCgroupName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= containMaxIDLen {
			break
		}
	}
	return b.String()
}

// containLogger returns a non-nil logger, matching WithLogger's handling of a nil
// logger so containment code can log unconditionally without nil checks.
func containLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}
	return l
}

// startCostSampler launches the periodic cost line for a contained session when
// the consumer asked for one. A no-op otherwise, so the caller needs no branch.
// The monitor calls stop before teardown, since ctx is cancelled only after
// it and a tick between would log a removed cgroup.
func (h *Handler) startCostSampler(ctx context.Context) (stop func()) {
	if h.contain == nil || h.cfg.containSample <= 0 {
		return func() {}
	}
	sampleCtx, cancel := context.WithCancel(ctx)
	h.wg.Go(func() { h.costSampleLoop(sampleCtx, h.cfg.containSample) })
	return cancel
}

// costSampleLoop logs one cost line per interval for a contained session, until
// the session's context is cancelled.
//
// tasks is pids.current, which counts threads and unreaped tasks alike, so no
// zombie count can be derived from it.
func (h *Handler) costSampleLoop(ctx context.Context, interval time.Duration) {
	started := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			mem, tasks, procs := h.contain.current()
			h.cfg.logger.Info("terminal: session cost",
				"session", LogID(h.cfg.containmentID),
				"mem_bytes", mem,
				"procs", procs,
				"tasks", tasks,
				"age_s", int(time.Since(started).Seconds()))
		}
	}
}

// beginContainment creates this session's cgroup and points cmd at it so the
// kernel places the child at clone time, returning nil when containment is off or
// unavailable; a failure leaves the session uncontained, never refused.
func (h *Handler) beginContainment(cmd *exec.Cmd) *sessionCgroup {
	if h.cfg.containment == nil {
		return nil
	}
	sc, err := h.cfg.containment.create(h.cfg.containmentID)
	if err != nil {
		h.cfg.logger.Warn("terminal: containment unavailable for session",
			"session", LogID(h.cfg.containmentID), "error", err)
		return nil
	}
	sc.applyTo(cmd)
	return sc
}
