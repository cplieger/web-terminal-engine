package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Session reaping ends descendants that setsid() out of reach of kill(-pgid)
// and the PTY-close SIGHUP. Each session's tree inherits an unguessable
// environment marker, found by a /proc scan. env -i escapes it; Containment
// is the stronger boundary.

const (
	// reapMarkerEnv keeps a WT_ prefix because it lands in the child's flat
	// environment.
	reapMarkerEnv = "WT_SESSION_REAP"

	// reapMarkerBytes keeps the pair inside the first reapEnvMaxBytes read.
	reapMarkerBytes = 16

	// reapPoll is the per-pid liveness poll inside a settle window.
	reapPoll = 20 * time.Millisecond

	// reapKillRounds caps the SIGKILL rescans; one forker's tree empties in
	// three, and only cgroup.kill ends a tree that out-forks every scan.
	reapKillRounds = 8
)

// sessionReap is one session's reap domain: the marker its tree carries, plus
// the once-guard that makes teardown idempotent across the crash-then-close
// sequence.
type sessionReap struct {
	log    *slog.Logger
	id     SessionID
	marker string
	once   sync.Once
}

// newSessionReap mints a reap domain for one session, or nil when no random
// marker could be made: a predictable one could match another session's tree.
func (h *Handler) newSessionReap() *sessionReap {
	buf := make([]byte, reapMarkerBytes)
	if _, err := rand.Read(buf); err != nil {
		containLogger(h.cfg.logger).Warn("terminal: session reaping unavailable for session",
			"session", LogID(h.cfg.containmentID), "error", err)
		return nil
	}
	return &sessionReap{
		log:    containLogger(h.cfg.logger),
		id:     h.cfg.containmentID,
		marker: hex.EncodeToString(buf),
	}
}

// envPair returns the marker assignment to prepend to the child environment, or
// "" for a nil domain so the caller needs no branch.
//
// Prepending puts it at the front of /proc/<pid>/environ, so the scan reads a
// bounded prefix.
func (s *sessionReap) envPair() string {
	if s == nil {
		return ""
	}
	return reapMarkerEnv + "=" + s.marker
}

// stripReapMarker drops any assignment to the marker key from env. os/exec
// keeps the last value of a repeated key, so a later assignment would
// replace the engine's marker and silently disable reaping. childEnv strips
// both WithEnv and the inherited environment, which carries a live marker for
// a server started inside a session.
func stripReapMarker(env []string) []string {
	prefix := reapMarkerEnv + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// teardown reclaims whatever the session's tree left behind. Nil-safe and
// exactly-once, matching sessionCgroup.teardown so the monitor can call both
// unconditionally.
func (s *sessionReap) teardown() {
	if s == nil {
		return
	}
	s.once.Do(s.teardownOnce)
}

// teardownOnce is the escalation ladder: settle, SIGTERM, settle, SIGKILL. Its
// WARN fields match sessionCgroup.teardownOnce, so logs read alike for both
// boundaries. A clean tree costs one scan and no log line.
func (s *sessionReap) teardownOnce() {
	members := reapFindByMarker(s.marker)
	if len(members) == 0 {
		return
	}
	// cmd.Wait reaped only the head. Rescan after the window even if the list
	// drained: a survivor may have forked a child owed SIGTERM.
	s.waitGone(members, containGrace)
	if members = reapFindByMarker(s.marker); len(members) == 0 {
		return
	}

	residentBytes := reapResident(members)
	survivors := len(members)
	var termed []int
	for _, pid := range members {
		if reapTerm(pid) {
			termed = append(termed, pid)
		}
	}

	// Unconditional: a SIGTERM handler can fork and then exit, so the snapshot
	// draining proves nothing about the domain.
	s.waitGone(members, containGrace)
	forced := s.killUntilGone(reapKill)

	// Logged only when something was reclaimed.
	s.log.Warn("terminal: session reap reclaimed escaped processes",
		"session", LogID(s.id),
		"survivors", survivors,
		"term_reclaimed", termReclaimed(termed, forced),
		"kill_forced", len(forced),
		"resident_bytes", residentBytes)
}

// killUntilGone SIGKILLs every member through kill and rescans until a scan
// finds the domain empty, for at most reapKillRounds rounds, and returns every
// pid it found alive. A member can fork between scan and kill, and an accepted
// signal is not an exit, so only an empty scan returns early. Logs what it
// could not end.
func (s *sessionReap) killUntilGone(kill func(pid int) bool) (forced map[int]struct{}) {
	forced = map[int]struct{}{}
	sent := map[int]struct{}{}
	for range reapKillRounds {
		left := reapFindByMarker(s.marker)
		if len(left) == 0 {
			return forced
		}
		for _, pid := range left {
			forced[pid] = struct{}{}
			if _, done := sent[pid]; !done && kill(pid) {
				sent[pid] = struct{}{}
			}
		}
		s.waitGone(left, containGrace)
	}
	if left := reapFindByMarker(s.marker); len(left) != 0 {
		s.log.Warn("terminal: session reap could not end escaped processes",
			"session", LogID(s.id),
			"remaining", len(left))
	}
	return forced
}

// termReclaimed counts the SIGTERMed pids that needed no SIGKILL. Matching by
// pid rather than subtracting counts keeps a child born after SIGTERM, which is
// forced but was never termed, from hiding a member that did exit.
func termReclaimed(termed []int, forced map[int]struct{}) int {
	n := 0
	for _, pid := range termed {
		if _, killed := forced[pid]; !killed {
			n++
		}
	}
	return n
}

// waitGone polls the given pids until none is still a live member of this
// domain, or the budget expires.
//
// Checks only the pids handed to it, never the whole host, so a poll never
// costs a full scan.
func (s *sessionReap) waitGone(pids []int, budget time.Duration) {
	deadline := time.Now().Add(budget)
	for {
		alive := false
		for _, pid := range pids {
			if reapAlive(pid, s.marker) {
				alive = true
				break
			}
		}
		if !alive || time.Now().After(deadline) {
			return
		}
		time.Sleep(reapPoll)
	}
}
