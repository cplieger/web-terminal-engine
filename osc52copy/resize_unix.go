//go:build unix

package osc52copy

import (
	"os/signal"
	"syscall"
)

// ignoreResize sets SIGWINCH to SIG_IGN for the rest of the process. Go
// catches it, and a caught signal pending during a write to a full terminal
// ends write(2) with a short count (Linux n_tty_write,
// https://github.com/torvalds/linux/blob/master/drivers/tty/n_tty.c);
// os.File.Write then writes the rest in a second call, after another writer's
// output. The kernel discards an ignored signal when it is sent, so it never
// pends.
func ignoreResize() {
	signal.Ignore(syscall.SIGWINCH)
}
