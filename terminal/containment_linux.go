//go:build linux

package terminal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Containment owns the delegated cgroup v2 root that per-session cgroups are
// created under. Construct one per process with NewContainment; a nil
// *Containment disables the feature everywhere it is consumed.
type Containment struct {
	log    *slog.Logger
	root   string
	prefix string
}

// NewContainment prepares root for per-session containment and returns a handle,
// or errContainmentUnsupported wrapped with the reason.
//
// root must be this process's own writable cgroup v2 root (Docker mounts it
// read-only). prefix (e.g. "wt-") namespaces every directory, so the startup
// sweep removes only its own. Refusing checks run before the mutations; a later
// failure rolls back the controllers, not the processes moved to the server
// leaf. Needs Linux 6.1 for pids.peak.
func NewContainment(root CgroupRoot, prefix string, log *slog.Logger) (*Containment, error) {
	log = containLogger(log)
	rootDir := string(root)
	if rootDir == "" || prefix == "" {
		return nil, fmt.Errorf("%w: root and prefix are required", errContainmentUnsupported)
	}
	// The prefix is joined into a path, so it gets the same whitelist a session id
	// does: it must survive sanitization unchanged.
	if sanitizeCgroupName(prefix) != prefix {
		return nil, fmt.Errorf("%w: prefix %q must be [A-Za-z0-9_-] and at most %d chars", errContainmentUnsupported, prefix, containMaxIDLen)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(rootDir, &st); err != nil {
		return nil, fmt.Errorf("%w: statfs %s: %w", errContainmentUnsupported, rootDir, err)
	}
	if st.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, fmt.Errorf("%w: %s is not a cgroup2 mount", errContainmentUnsupported, rootDir)
	}

	c := &Containment{root: rootDir, prefix: prefix, log: log}

	if err := c.verifyOwnRoot(); err != nil {
		return nil, fmt.Errorf("%w: %w", errContainmentUnsupported, err)
	}
	c.sweep()
	if err := c.probeSpawn(); err != nil {
		return nil, fmt.Errorf("%w: %w", errContainmentUnsupported, err)
	}
	if err := c.vacateRoot(); err != nil {
		return nil, fmt.Errorf("%w: %w", errContainmentUnsupported, err)
	}
	if err := c.delegate(); err != nil {
		return nil, fmt.Errorf("%w: %w", errContainmentUnsupported, err)
	}
	if err := c.verifyControllerFiles(); err != nil {
		c.undelegate()
		return nil, fmt.Errorf("%w: %w", errContainmentUnsupported, err)
	}

	log.Info("terminal: containment enabled",
		"root", rootDir, "prefix", prefix,
		"controllers", readTrim(filepath.Join(rootDir, "cgroup.subtree_control")))
	return c, nil
}

// path returns the absolute cgroup directory for a leaf name.
func (c *Containment) path(leaf string) string {
	return filepath.Join(c.root, c.prefix+leaf)
}

// verifyOwnRoot refuses a root that is plainly not this process's own cgroup root.
//
// On a host root, vacateRoot would relocate unrelated workloads. The signal is
// a child cgroup without this package's prefix (a systemd host always has
// init.scope): one readdir, assuming nothing about where this process sits.
func (c *Containment) verifyOwnRoot() error {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return fmt.Errorf("read %s: %w", c.root, err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), c.prefix) {
			return fmt.Errorf("refusing to reshape %s: it already holds cgroup %q that this process did not create, so it is not this process's own root (host cgroup namespace?)", c.root, e.Name())
		}
	}
	return nil
}

// vacateRoot moves every process in the delegated root into the server leaf.
// Processes that exit between the read and the write are skipped: the goal is an
// empty root, and a process that left on its own achieved that.
func (c *Containment) vacateRoot() error {
	serverDir := c.path(containServerLeaf)
	if err := os.Mkdir(serverDir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create %s: %w", serverDir, err)
	}
	procs := filepath.Join(serverDir, "cgroup.procs")
	moved, failed := 0, 0
	for _, pid := range readPids(filepath.Join(c.root, "cgroup.procs")) {
		if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0o600); err != nil {
			if !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.ENOENT) {
				failed++
			}
			continue
		}
		moved++
	}
	if failed > 0 {
		return fmt.Errorf("could not vacate %d process(es) from the cgroup root", failed)
	}
	if moved > 0 {
		c.log.Debug("terminal: containment vacated cgroup root", "moved", moved, "into", serverDir)
	}
	return nil
}

// delegate enables the controllers session accounting needs for children of root.
func (c *Containment) delegate() error {
	f := filepath.Join(c.root, "cgroup.subtree_control")
	if err := os.WriteFile(f, []byte("+memory +pids"), 0o600); err != nil {
		return fmt.Errorf("enable controllers in %s: %w", f, err)
	}
	return nil
}

// undelegate restores the root's controller set after a later step failed, so a
// host that gets no containment is not left carrying the no-internal-process
// constraint it gains nothing from. Best-effort: failing here is strictly no
// worse than not trying.
func (c *Containment) undelegate() {
	f := filepath.Join(c.root, "cgroup.subtree_control")
	if err := os.WriteFile(f, []byte("-memory -pids"), 0o600); err != nil {
		c.log.Debug("terminal: containment could not restore subtree_control", "error", err)
	}
}

// verifyControllerFiles proves, in a throwaway child cgroup, that every file
// teardown and logging read is actually present. Requires delegate() first: the
// memory and pids files only appear once their controllers are enabled in the
// parent's subtree_control.
func (c *Containment) verifyControllerFiles() error {
	dir, err := c.scratchCgroup()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(dir) }()
	for _, name := range []string{"cgroup.kill", "cgroup.events", "cgroup.procs", "memory.peak", "pids.peak"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("kernel too old for containment: %s missing (%w)", name, err)
		}
	}
	return nil
}

// probeSpawn checks that the kernel permits placing a child in a cgroup at clone
// time. Needs no controllers, so it runs before delegation.
func (c *Containment) probeSpawn() error {
	dir, err := c.scratchCgroup()
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(dir) }()
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open probe cgroup: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	return probeCloneIntoCgroup(fd)
}

// scratchCgroup creates (or recreates) the throwaway cgroup the two probes use.
func (c *Containment) scratchCgroup() (string, error) {
	dir := c.path(containProbeLeaf)
	_ = os.Remove(dir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("create probe cgroup %s: %w", dir, err)
	}
	return dir, nil
}

// probeCloneIntoCgroup checks that the kernel permits the clone3 syscall with
// CLONE_INTO_CGROUP, which is what SysProcAttr.UseCgroupFD requires.
//
// It spawns a path that cannot exist, since a distroless image has no binary
// to borrow: ENOENT means clone3 succeeded and only exec failed, the pass.
// ENOSYS is usually a seccomp profile; any other error is reported as-is.
func probeCloneIntoCgroup(cgroupFD int) error {
	cmd := exec.CommandContext(context.Background(), "/nonexistent/web-terminal-engine-containment-probe")
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: cgroupFD}
	err := cmd.Run()
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil
	case errors.Is(err, syscall.ENOSYS):
		return errors.New("clone3 is not permitted (seccomp profile or kernel), so clone-time cgroup placement cannot work")
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return fmt.Errorf("clone-time cgroup placement denied by capability, seccomp, or cgroup permissions: %w", err)
	case err == nil:
		// Cannot happen: the path does not exist. Treat as unsupported rather
		// than assuming.
		return errors.New("containment probe unexpectedly succeeded")
	default:
		return fmt.Errorf("containment probe failed: %w", err)
	}
}

// sweep removes leftover session cgroups from a previous run of this process.
//
// The prefix and rmdir's refusal of a populated cgroup keep it off live
// sessions and foreign cgroups. Removal is depth-first, since rmdir also
// refuses a cgroup with children.
func (c *Containment) sweep() {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, c.prefix) || name == c.prefix+containServerLeaf {
			continue
		}
		if err := removeCgroupTree(filepath.Join(c.root, name)); err == nil {
			removed++
		}
	}
	if removed > 0 {
		c.log.Info("terminal: containment swept leftover session cgroups", "count", removed)
	}
}

// create makes the cgroup for one session. An existing directory is removed and
// recreated rather than adopted: an adopted cgroup carries the previous
// occupant's memory.peak and pids.peak, which would make the cost report
// silently wrong.
func (c *Containment) create(id SessionID) (*sessionCgroup, error) {
	leaf := sanitizeCgroupName(string(id))
	if leaf == "" {
		return nil, errors.New("empty session id after sanitization")
	}
	// The two internal leaves are not available to a session: colliding with the
	// server leaf would target the cgroup this process itself lives in.
	if leaf == containServerLeaf || leaf == containProbeLeaf {
		return nil, fmt.Errorf("session id sanitizes to the reserved cgroup name %q", leaf)
	}
	dir := c.path(leaf)
	if err := os.Mkdir(dir, 0o755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		if err := removeCgroupTree(dir); err != nil {
			return nil, fmt.Errorf("stale cgroup %s is not removable: %w", dir, err)
		}
		if err := os.Mkdir(dir, 0o755); err != nil {
			return nil, fmt.Errorf("recreate %s: %w", dir, err)
		}
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = os.Remove(dir)
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return &sessionCgroup{dir: dir, fd: fd, id: id, log: c.log}, nil
}

// sessionCgroup is one session's cgroup: the placement target at spawn and the
// unit of teardown afterwards.
type sessionCgroup struct {
	log *slog.Logger
	dir string
	id  SessionID
	fd  int
	// memPeak/pidsPeak are latched by teardown so the caller's exit log line can
	// report the session's high-water marks. Written inside once.Do and read only
	// after teardown returns, so the Once provides the ordering.
	memPeak  uint64
	pidsPeak int
	once     sync.Once
}

// peaks returns the high-water marks latched by teardown. Zero before teardown
// has run, and zero for a nil handle, so a caller can log it unconditionally.
func (s *sessionCgroup) peaks() (memBytes uint64, tasks int) {
	if s == nil {
		return 0, 0
	}
	return s.memPeak, s.pidsPeak
}

// applyTo points cmd at this cgroup so the kernel places the child at clone time.
//
// Clone-time placement is not a nicety: cgroup v2 does not migrate existing
// charges, so a child moved in after it starts reports memory it never accounted
// and the peak is meaningless. Callers must invoke this before starting cmd, and
// pty.StartWithSize preserves the struct (it only adds Setsid/Setctty), so the
// PTY semantics are unchanged.
func (s *sessionCgroup) applyTo(cmd *exec.Cmd) {
	if s == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = s.fd
}

// releaseFD closes the cgroup directory fd. Called once the child is started (or
// failed to start), because the fd is only needed at clone time and holding it
// would leak one descriptor per session.
func (s *sessionCgroup) releaseFD() {
	if s == nil || s.fd < 0 {
		return
	}
	_ = unix.Close(s.fd)
	s.fd = -1
}

// peak reports the session's high-water marks, which remain readable after the
// members are gone. Memory is a byte count; the task count stays an int, and it
// is a TASK count rather than a process count (see current).
func (s *sessionCgroup) peak() (memBytes uint64, tasks int) {
	if s == nil {
		return 0, 0
	}
	return readUint(filepath.Join(s.dir, "memory.peak")), readCount(filepath.Join(s.dir, "pids.peak"))
}

// current reports live usage for the periodic cost line.
//
// tasks comes from pids.current, which counts TASKS, not processes: the kernel's
// pids controller documents its numbers as TIDs, so one 11-thread process reports
// 11 (measured). It also counts a task that has exited but not been reaped. Those
// two are not separable from any cgroup file, which is why this reports the task
// count honestly rather than deriving a zombie count from it.
func (s *sessionCgroup) current() (memBytes uint64, tasks, procs int) {
	if s == nil {
		return 0, 0, 0
	}
	return readUint(filepath.Join(s.dir, "memory.current")),
		readCount(filepath.Join(s.dir, "pids.current")),
		len(s.liveProcs())
}

// liveProcs returns every live process in this session's cgroup AND in any
// sub-cgroup created beneath it.
//
// It must recurse: cgroup.procs is per-cgroup but the populated flag teardown
// waits on is recursive, so a top-level read would skip the SIGTERM pass for
// a session that made a sub-cgroup. The kernel excludes zombies.
func (s *sessionCgroup) liveProcs() []int {
	var out []int
	_ = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a cgroup that vanished mid-walk is not a member
		}
		if d.IsDir() {
			out = append(out, readPids(filepath.Join(p, "cgroup.procs"))...)
		}
		return nil
	})
	return out
}

// populated reports the kernel's own "any live process in this subtree" flag,
// which is what rmdir gates on.
func (s *sessionCgroup) populated() bool {
	for line := range strings.SplitSeq(readTrim(filepath.Join(s.dir, "cgroup.events")), "\n") {
		if v, ok := strings.CutPrefix(line, "populated "); ok {
			return strings.TrimSpace(v) == "1"
		}
	}
	return false
}

// waitDrained waits up to timeout for the cgroup to hold no live process.
//
// It polls cgroup.events for POLLPRI and reads through the polled fd: poll
// reports changes since that fd last read, so another fd would miss a drain.
func (s *sessionCgroup) waitDrained(timeout time.Duration) bool {
	f, err := os.Open(filepath.Join(s.dir, "cgroup.events"))
	if err != nil {
		return true // gone: nothing left to wait for
	}
	defer func() { _ = f.Close() }()

	deadline, buf := time.Now().Add(timeout), make([]byte, 128)
	for {
		if _, err := f.Seek(0, 0); err != nil {
			return !s.populated()
		}
		n, _ := f.Read(buf)
		if !eventsPopulated(string(buf[:n])) {
			return true
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return false
		}
		fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLPRI}} //nolint:gosec // a just-opened fd is far below MaxInt32
		if _, err := unix.Poll(fds, int(remain.Milliseconds())+1); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return !s.populated()
		}
	}
}

// eventsPopulated parses a cgroup.events body for the populated flag.
func eventsPopulated(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if v, ok := strings.CutPrefix(line, "populated "); ok {
			return strings.TrimSpace(v) == "1"
		}
	}
	return false
}

// termLive sends SIGTERM to every live member and returns the pids signalled.
//
// Signalling goes through a pidfd, never a bare kill(pid): a pid read from
// cgroup.procs can exit and be recycled before the signal lands, and this package
// rejects pid-set based containment partly on that hazard. Refusing to ship a
// smaller instance of the same defect costs about ten lines.
func (s *sessionCgroup) termLive() []int {
	var sent []int
	for _, pid := range s.liveProcs() {
		pidfd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue // already gone
		}
		if err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0); err == nil {
			sent = append(sent, pid)
		}
		_ = unix.Close(pidfd)
	}
	return sent
}

// killAll is the backstop: cgroup.kill SIGKILLs every member of this cgroup and
// of every descendant in one write, and the kernel documents it as handling
// concurrent forks and being protected against migrations, which is the guarantee
// no enumeration can offer.
func (s *sessionCgroup) killAll() error {
	return os.WriteFile(filepath.Join(s.dir, "cgroup.kill"), []byte("1"), 0o600)
}

// removeCgroupTree removes a cgroup and every sub-cgroup beneath it, deepest
// first.
//
// cgroup.kill kills a whole subtree but removes no directory, and rmdir refuses a
// cgroup that still has children, so a session that created one sub-cgroup would
// otherwise leave a directory neither teardown nor the startup sweep could ever
// remove.
func removeCgroupTree(dir string) error {
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = removeCgroupTree(filepath.Join(dir, e.Name()))
			}
		}
	}
	return os.Remove(dir)
}

// teardown ends the session's cgroup and reports what it had to reclaim. Safe to
// call more than once and from more than one path; only the first call acts.
//
// A crashed session tears down when its process exits, and the user may close the
// dead tab minutes later, so the second pass must find a missing cgroup and do
// nothing.
func (s *sessionCgroup) teardown() {
	if s == nil {
		return
	}
	s.once.Do(s.teardownOnce)
}

func (s *sessionCgroup) teardownOnce() {
	s.releaseFD()

	// Read the cost first so the report is independent of how teardown goes.
	memPeak, pidsPeak := s.peak()
	s.memPeak, s.pidsPeak = memPeak, pidsPeak

	// Settle window before classifying anything. cmd.Wait returning means the
	// HEAD was reaped, not that the tree finished exiting, so descendants are
	// routinely still unwinding at this instant. Without this window they would
	// be reported as escapees and the warning below would fire on healthy
	// sessions, which would destroy its only useful property.
	if s.waitDrained(containGrace) {
		s.remove()
		return
	}

	// Whatever is still here outlived its own session's teardown. Its resident
	// size is read now, while only survivors remain, because that is the number
	// the warning is about; the session's lifetime peak (above) is a different
	// question and would overstate a leak by an order of magnitude.
	survivors := len(s.liveProcs())
	residentBytes := readUint(filepath.Join(s.dir, "memory.current"))
	termed := s.termLive()
	forced := map[int]struct{}{}
	if !s.waitDrained(containGrace) {
		for _, pid := range s.liveProcs() {
			forced[pid] = struct{}{}
		}
	}

	if err := s.killAll(); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("terminal: containment kill failed", "session", LogID(s.id), "error", err)
	}
	s.waitDrained(containGrace)
	s.remove()

	if survivors > 0 {
		// Only fires when containment reclaimed something. term_reclaimed means a
		// well-behaved process nobody had signalled; kill_forced means something
		// was still alive when SIGTERM's window closed.
		s.log.Warn("terminal: containment reclaimed escaped processes",
			"session", LogID(s.id),
			"survivors", survivors,
			"term_reclaimed", termReclaimed(termed, forced),
			"kill_forced", len(forced),
			"resident_bytes", residentBytes)
	}
}

// remove deletes the session's cgroup tree, logging at DEBUG if it cannot.
func (s *sessionCgroup) remove() {
	if err := removeCgroupTree(s.dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Not expected: a zombie does not hold a cgroup, and sub-cgroups are
		// removed depth-first. Leave it for the next startup sweep.
		s.log.Debug("terminal: containment cgroup not removed", "session", LogID(s.id), "error", err)
	}
}

// readUint reads a single unsigned integer from a cgroup file; 0 if unreadable
// (including "max", which no file read here reports).
func readUint(path string) uint64 {
	n, err := strconv.ParseUint(readTrim(path), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// readCount reads a small non-negative count (a task tally) from a cgroup file; 0
// if unreadable or out of range.
func readCount(path string) int {
	n, err := strconv.Atoi(readTrim(path))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readTrim reads a small cgroup file, trimming trailing whitespace.
func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readPids reads a cgroup.procs-style file into pids, skipping malformed lines.
func readPids(path string) []int {
	var out []int
	for line := range strings.FieldsSeq(readTrim(path)) {
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			out = append(out, pid)
		}
	}
	return out
}
