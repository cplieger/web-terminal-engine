//go:build linux

package terminal

import (
	"bytes"
	"fmt"
	"io"
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
// the test binary exits. A zombie child counts too: nothing ever waited on it.
func TestMain(m *testing.M) {
	code := m.Run()
	if leaked := awaitNoChildren(waitPatience); len(leaked) > 0 {
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

// awaitNoChildren polls until this process has no child, live or zombie,
// returning the survivors when patience runs out. A Close-only teardown kills
// and waits asynchronously, so a child seen once is not yet a leak.
func awaitNoChildren(patience time.Duration) []int {
	deadline := time.Now().Add(patience)
	for {
		left := children()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func children() []int {
	self := os.Getpid()
	paths, _ := filepath.Glob("/proc/[0-9]*/stat")
	var out []int
	for _, p := range paths {
		_, ppid, ok := statStateAndPPID(p)
		if !ok || ppid != self {
			continue
		}
		if pid, err := strconv.Atoi(filepath.Base(filepath.Dir(p))); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

func cmdlineOf(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "(cmdline unreadable)"
	}
	if s := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " ")); s != "" {
		return s
	}
	if state, _, ok := statStateAndPPID(fmt.Sprintf("/proc/%d/stat", pid)); ok && state == 'Z' {
		return "(zombie: never waited on)"
	}
	return ""
}

// statStateAndPPID reads the process state and parent pid from a procfs stat
// line.
func statStateAndPPID(path string) (state byte, ppid int, ok bool) {
	f, err := os.Open(path) // #nosec G304 -- procfs path built from a numeric pid
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = f.Close() }()
	buf, err := io.ReadAll(io.LimitReader(f, reapStatusMaxBytes))
	if err != nil {
		return 0, 0, false
	}
	// Everything before the LAST ')' is pid + comm; the fields after it are
	// positionally reliable regardless of what the executable was called.
	_, afterComm, found := bytes.CutLast(buf, []byte(")"))
	if !found {
		return 0, 0, false
	}
	fields := bytes.Fields(afterComm)
	if len(fields) < 2 || len(fields[0]) == 0 {
		return 0, 0, false
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], parent, true
}
