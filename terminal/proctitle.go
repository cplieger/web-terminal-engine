package terminal

// The automatic session title is effectiveTitle's last rung. Like tmux, it is
// the foreground group leader's name when that is not the session's root
// process, else the root's cwd basename, else the command's basename (the only
// rung off Linux). A pipeline whose leader exited (`sleep 1 | grep x`) rests
// at the cwd: finding another member would scan all of procfs every sweep.

import "path/filepath"

// autoTitleProbe is one sweep's worth of automatic-title inputs, gathered by
// probeAutoTitle outside every lock. The manager folds it into the confirmation
// window (see confirmAutoTitle) rather than using it directly, because a
// foreground process that lives 30ms must not flash into a tab label.
type autoTitleProbe struct {
	// err says why a probe with ok false learned nothing. It leads the struct
	// for govet fieldalignment.
	err error
	// procName is the foreground process-group leader's name, or "" when the
	// session's own shell is in the foreground, when the leader is gone, or when
	// the platform cannot tell. Non-empty means "something is running".
	procName string
	// cwdBase is the root process's cwd basename, so it tracks the shell's cd;
	// "" when unreadable or skipped because a foreground process outranks it.
	cwdBase string
	// pgid identifies the candidate for the confirmation window: the window
	// restarts whenever this changes. Zero when unknown.
	pgid int
	// ok false means no information this sweep, which holds the last confirmed
	// title; nothing running is ok true with an empty procName.
	ok bool
}

// commandBase is the automatic title's last rung: the basename of the configured
// command. Always available and immutable, so the manager seeds every session
// with it at Create time and a List served before the first sweep still names
// the session.
func (h *Handler) commandBase() string {
	if len(h.command) == 0 {
		return ""
	}
	return filepath.Base(h.command[0])
}

// probeAutoTitle gathers this sweep's automatic-title inputs, holding h.mu only
// to snapshot the pty and root pid, so a slow procfs stalls only the sweep.
// Only the status sweep may call it: confirmAutoTitle's single writer is what
// keeps List and the SSE snapshot in agreement.
func (h *Handler) probeAutoTitle() autoTitleProbe {
	h.mu.Lock()
	ptmx := h.ptmx
	warned := h.autoTitleWarned
	var rootPID int
	if h.cmd != nil && h.cmd.Process != nil {
		rootPID = h.cmd.Process.Pid
	}
	h.mu.Unlock()
	if ptmx == nil || rootPID <= 0 {
		return autoTitleProbe{} // not started: no information, hold what we have
	}
	p := probeForeground(ptmx, rootPID)
	// One debug line per session tells "this host cannot" from a broken build.
	// The sweep is the only caller, so the check-then-set cannot interleave.
	if p.err != nil && !warned {
		h.mu.Lock()
		h.autoTitleWarned = true
		h.mu.Unlock()
		h.cfg.logger.Debug("terminal: automatic session title unavailable, using the command name",
			"error", p.err)
	}
	return p
}

// probeForeground, per build tag, resolves the pty's foreground group and the
// session's cwd, returning a zero autoTitleProbe on any failure.
