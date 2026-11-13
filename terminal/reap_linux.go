//go:build linux

package terminal

// The marker reap domain needs only procfs and pidfd: no capability, writable
// cgroup tree or mount.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// reapEnvMaxBytes bounds the per-pid environ read, which envPair's prepend
	// makes enough; an environment can reach ARG_MAX.
	reapEnvMaxBytes = 16 << 10

	// reapStatusMaxBytes bounds the /proc/<pid>/status read for VmRSS and state.
	reapStatusMaxBytes = 4 << 10
)

// reapFindByMarker returns every live pid whose environment carries marker.
//
// Skips this process (it would signal itself), anything unreadable (vanished
// mid-scan, or another user's), and zombies, which are already dead. A pid
// caught mid-execve is re-read until its new image settles, for at most
// containGrace; one still unsettled then is left out, because only a proven
// member may be signalled.
func reapFindByMarker(marker string) []int {
	if marker == "" {
		return nil
	}
	want := []byte(reapMarkerEnv + "=" + marker)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid != self {
			pids = append(pids, pid)
		}
	}
	scratch := make([]byte, reapEnvMaxBytes)
	return collectMembers(pids, func(pid int) reapState {
		return reapMembership(pid, want, scratch)
	}, containGrace)
}

func collectMembers(pids []int, classify func(int) reapState, budget time.Duration) []int {
	var out, unsettled []int
	for _, pid := range pids {
		switch classify(pid) {
		case reapMember:
			out = append(out, pid)
		case reapExecing:
			unsettled = append(unsettled, pid)
		}
	}
	deadline := time.Now().Add(budget)
	for len(unsettled) > 0 && time.Now().Before(deadline) {
		time.Sleep(reapPoll)
		next := unsettled[:0]
		for _, pid := range unsettled {
			switch classify(pid) {
			case reapMember:
				out = append(out, pid)
			case reapExecing:
				next = append(next, pid)
			}
		}
		unsettled = next
	}
	return out
}

// reapState is one pid's standing against a domain's marker.
type reapState int

const (
	reapOutside reapState = iota // no marker, gone, unreadable, or exiting
	reapMember
	reapExecing // mid-execve: membership cannot be read yet
)

// execPhase is what /proc/<pid>/stat says about a task whose environ read empty.
type execPhase int

const (
	phaseGone    execPhase = iota // zombie, exiting, kernel thread, or unparseable
	phaseExecing                  // env_end still zero: execve has not set the bounds
	phaseSettled                  // bounds set: a re-read of environ is authoritative
)

// Task flags from include/linux/sched.h, as printed in field 9 of
// /proc/<pid>/stat.
const (
	pfExiting = 0x00000004
	pfKthread = 0x00200000
)

// reapMembership matches the exact KEY=VALUE pair in a bounded prefix of pid's
// environ block; the per-session random value keeps a WithEnv reuse of the key
// or a stale marker from matching.
//
// An empty read is a zombie, an exiting task, a kernel thread, or a live task
// mid-execve whose new image has no environ bounds yet (or whose old image was
// released after the open); statExecPhase tells them apart. scratch is the
// read buffer, reapEnvMaxBytes long.
func reapMembership(pid int, want, scratch []byte) reapState {
	dir := "/proc/" + strconv.Itoa(pid)
	buf, ok := readEnvironOnce(dir+"/environ", scratch)
	if !ok {
		return reapOutside
	}
	if len(buf) == 0 {
		stat, ok := readProcBounded(dir+"/stat", reapStatusMaxBytes)
		if !ok {
			return reapOutside
		}
		switch statExecPhase(stat) {
		case phaseExecing:
			return reapExecing
		case phaseSettled:
			// A task whose environment really is empty reads empty again.
			if buf, ok = readEnvironOnce(dir+"/environ", scratch); !ok {
				return reapOutside
			}
		case phaseGone:
			return reapOutside
		}
	}
	for kv := range bytes.SplitSeq(buf, []byte{0}) {
		if bytes.Equal(kv, want) {
			return reapMember
		}
	}
	return reapOutside
}

// statExecPhase classifies a /proc/<pid>/stat line. Fields are counted from
// the RIGHT of the last ')' because field 2, the executable name, may itself
// contain spaces and parentheses.
func statExecPhase(stat []byte) execPhase {
	_, afterComm, found := bytes.CutLast(stat, []byte(")"))
	if !found {
		return phaseGone
	}
	f := bytes.Fields(afterComm)
	// f[0] is field 3 (state), f[6] field 9 (flags), f[48] field 51 (env_end).
	if len(f) < 49 {
		return phaseGone
	}
	switch f[0][0] {
	case 'Z', 'X', 'x':
		return phaseGone
	}
	flags, err := strconv.ParseUint(string(f[6]), 10, 64)
	if err != nil || flags&(pfExiting|pfKthread) != 0 {
		return phaseGone
	}
	if string(f[48]) == "0" {
		return phaseExecing
	}
	return phaseSettled
}

// readEnvironOnce reads a procfs environ block with ONE read(2), which the
// kernel serves from the single memory image it pins for that call. A read
// loop can straddle an execve: the first chunk comes from the old image and
// the next finds it released, returning a truncated block that may have cut
// off the marker (a shell re-exports its environment in its own order, so a
// grandchild does not carry the marker first).
func readEnvironOnce(path string, buf []byte) ([]byte, bool) {
	f, err := os.Open(path) // #nosec G304 -- procfs path built from a numeric pid
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false
	}
	return buf[:n], true
}

func readProcBounded(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path) // #nosec G304 -- procfs path built from a numeric pid
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, false
	}
	return buf, true
}

// reapAlive reports whether pid is still a live member of this domain.
//
// Re-checks the marker rather than merely testing existence, which is the
// pid-recycle guard: between two polls a pid can exit and be reused by an
// unrelated process, and treating that as "still draining" would stall teardown
// while treating it as ours would signal a stranger. A pid mid-execve counts as
// alive, so the caller's next poll reads its settled image.
func reapAlive(pid int, marker string) bool {
	if marker == "" {
		return false
	}
	return reapMembership(pid, []byte(reapMarkerEnv+"="+marker), make([]byte, reapEnvMaxBytes)) != reapOutside
}

// reapTerm sends SIGTERM to pid and reapKill sends SIGKILL; each reports
// whether the kernel accepted the signal.
//
// Signalling goes through a pidfd, never a bare kill(pid), for the reason
// containment_linux.go's termLive states: a pid read from an enumeration can
// exit and be recycled before the signal lands, and this package refuses to ship
// that defect in either boundary.
func reapTerm(pid int) bool { return reapSignal(pid, unix.SIGTERM) }

func reapKill(pid int) bool { return reapSignal(pid, unix.SIGKILL) }

func reapSignal(pid int, sig unix.Signal) bool {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false // gone, or no descriptor to signal through
	}
	defer func() { _ = unix.Close(pidfd) }()
	return unix.PidfdSendSignal(pidfd, sig, nil, 0) == nil
}

// reapResident sums the resident memory of the given pids, in bytes.
//
// Reported in the reclaim WARN so the line answers "how much was this session
// still holding" — the number that makes an operator care about the leak at all.
// Best-effort: a pid that exits mid-sum contributes nothing.
func reapResident(pids []int) uint64 {
	var total uint64
	for _, pid := range pids {
		if kb, ok := statusFieldKB("/proc/"+strconv.Itoa(pid)+"/status", "VmRSS:"); ok {
			total += kb * 1024
		}
	}
	return total
}

// statusFieldKB reads one "Name: <n> kB" field out of a procfs status file.
func statusFieldKB(path, field string) (uint64, bool) {
	f, err := os.Open(path) // #nosec G304 -- procfs path built from a numeric pid
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, reapStatusMaxBytes))
	if err != nil {
		return 0, false
	}
	for line := range bytes.SplitSeq(buf, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte(field)) {
			continue
		}
		fields := bytes.Fields(line[len(field):])
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseUint(string(fields[0]), 10, 64)
		if err != nil {
			return 0, false
		}
		return kb, true
	}
	return 0, false
}
