//go:build linux

package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain fails the run when a child process outlives the tests. A session's
// child survives its socket closing until the handler is shut down, and one
// that inherited SIGHUP ignored and does not exit on EIO keeps running after
// the test binary exits.
func TestMain(m *testing.M) {
	code := m.Run()
	if leaked := awaitNoLiveChildren(waitPatience); len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d child process(es) outlived their test (shut the handler down in t.Cleanup):\n", len(leaked))
		for _, pid := range leaked {
			fmt.Fprintf(os.Stderr, "  pid %d: %s\n", pid, cmdlineOf(pid))
			// A session child leads its own process group (pty.StartWithSize sets
			// Setsid), so the group kill also takes descendants still in that group.
			_ = unix.Kill(-pid, unix.SIGKILL)
			_ = unix.Kill(pid, unix.SIGKILL)
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// awaitNoLiveChildren polls until no non-zombie child of this process remains,
// returning the survivors when patience runs out. A Close-only teardown kills
// asynchronously, so a child seen once is not yet a leak.
func awaitNoLiveChildren(patience time.Duration) []int {
	deadline := time.Now().Add(patience)
	for {
		live := liveChildren()
		if len(live) == 0 || time.Now().After(deadline) {
			return live
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func liveChildren() []int {
	self := os.Getpid()
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	var live []int
	for _, p := range paths {
		state, ppid, ok := statStateAndPPID(p)
		if !ok || ppid != self || state == 'Z' {
			continue
		}
		if pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p))); err == nil {
			live = append(live, pid)
		}
	}
	return live
}

func cmdlineOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "(cmdline unreadable)"
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}
