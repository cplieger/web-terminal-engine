//go:build linux

package osc52copy

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO stands in for the terminal: on Linux Go's poller takes both, so an
// opener that goes through it hands back a non-blocking fd for either. Darwin
// never polls a FIFO (https://go.dev/issue/24164), hence the linux tag.
func TestOpenBlockingWriteLeavesFdBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "tty")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := syscall.Open(fifo, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(reader) })

	f, err := openBlockingWrite(fifo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	conn, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags uintptr
	var errno syscall.Errno
	// SyscallConn, not f.Fd: Fd switches the fd back to blocking.
	if err := conn.Control(func(fd uintptr) {
		flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("fcntl F_GETFL: %v", errno)
	}
	if flags&syscall.O_NONBLOCK != 0 {
		t.Error("openBlockingWrite returned a non-blocking fd")
	}
	if flags&syscall.O_ACCMODE != syscall.O_WRONLY {
		t.Errorf("openBlockingWrite access mode = %#x, want O_WRONLY", flags&syscall.O_ACCMODE)
	}
}

// A signal during the open, as a network or FUSE filesystem reports it, must
// not fail the copy.
func TestOpenBlockingWriteRetriesAnInterruptedOpen(t *testing.T) {
	interrupts := 2
	sysOpen = func(path string, mode int, perm uint32) (int, error) {
		if interrupts > 0 {
			interrupts--
			return -1, syscall.EINTR
		}
		return syscall.Open(path, mode, perm)
	}
	t.Cleanup(func() { sysOpen = syscall.Open })

	f, err := openBlockingWrite(os.DevNull)
	if err != nil {
		t.Fatalf("openBlockingWrite after two EINTR: %v", err)
	}
	_ = f.Close()
}

func TestOpenBlockingWriteNamesPathOnFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	_, err := openBlockingWrite(missing)
	var pe *os.PathError
	if !errors.As(err, &pe) || pe.Op != "open" || pe.Path != missing || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("openBlockingWrite(%q) error = %v, want an open PathError naming the path and wrapping ENOENT", missing, err)
	}
}
