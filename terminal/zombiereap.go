package terminal

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// A PID 1 server inherits every orphan, which os/exec never waits on. The sweep
// skips spawnRegistry's pids, or cmd.Wait would fail with "no child processes".
// It polls instead of handling SIGCHLD: signal.Notify is process-global state.

const (
	// zombieSweepDefault is short enough that a forker cannot exhaust pid_max
	// between passes.
	zombieSweepDefault = 30 * time.Second

	// zombieSweepFloor keeps the /proc walk from burning CPU.
	zombieSweepFloor = time.Second
)

// spawnRegistry records the pids whose exit status os/exec still owns, so the
// zombie sweep can refuse to wait on them.
//
// Spawn holds mu for write across pty.StartWithSize and registration, and the
// sweep for read across its check and wait(), so no unregistered pid is waited.
type spawnRegistry struct {
	m  map[int]struct{}
	mu sync.RWMutex
}

var spawned = spawnRegistry{m: make(map[int]struct{})}

// spawnLock/spawnUnlock bracket a spawn. Held across the fork, not just the map
// write — see the comment on spawnRegistry.
func spawnLock()   { spawned.mu.Lock() }
func spawnUnlock() { spawned.mu.Unlock() }

// spawnRegister records a freshly spawned pid. The caller holds the write lock.
func spawnRegister(pid int) {
	if pid > 0 {
		spawned.m[pid] = struct{}{}
	}
}

// spawnForget releases a pid once os/exec has reaped it, after which the sweep
// may collect it if it somehow reappears as an orphan's parent.
func spawnForget(pid int) {
	spawned.mu.Lock()
	delete(spawned.m, pid)
	spawned.mu.Unlock()
}

// reapIfUnowned waits on pid unless os/exec owns it, reporting whether a status
// was collected.
func reapIfUnowned(pid int) bool {
	spawned.mu.RLock()
	defer spawned.mu.RUnlock()
	if _, owned := spawned.m[pid]; owned {
		return false
	}
	return waitNoHang(pid)
}

// StartZombieReaper makes this process collect the exit statuses of orphans that
// have been re-parented onto it, and returns a stop function.
//
// Call it once from the composition root of a server that may be its
// container's PID 1; it sets the child-subreaper flag, so it works behind an
// init shim too. An interval of 0 means the default, and anything below one
// second is raised to it. Off Linux it is a no-op.
func StartZombieReaper(log *slog.Logger, interval time.Duration) (stop func()) {
	l := containLogger(log)
	if interval <= 0 {
		interval = zombieSweepDefault
	}
	interval = max(interval, zombieSweepFloor)

	if err := installSubreaper(); err != nil {
		// Not fatal, and not even necessarily a degradation: when the process is
		// already PID 1 the flag changes nothing, and orphans arrive regardless.
		l.Debug("terminal: child-subreaper flag not set", "error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := sweepZombies(); n > 0 {
					// INFO: reaping others' orphans is routine on PID 1.
					l.Info("terminal: reaped orphaned processes", "count", n)
				}
			}
		}
	})
	return func() {
		cancel()
		wg.Wait()
	}
}

// sweepZombies collects every orphan zombie parented on this process that the
// engine does not own, returning how many statuses it took.
func sweepZombies() int {
	n := 0
	for _, pid := range findOrphanZombies() {
		if reapIfUnowned(pid) {
			n++
		}
	}
	return n
}
