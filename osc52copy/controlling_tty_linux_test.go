package osc52copy_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/cplieger/web-terminal-engine/v6/osc52copy"
	"github.com/creack/pty"
)

// childEnv makes the test binary run osc52copy.Main as the named command with
// a nil Streams.TTY, so it writes to its own controlling terminal.
const childEnv = "OSC52COPY_TEST_ARGV0"

func TestMain(m *testing.M) {
	if argv0, ok := os.LookupEnv(childEnv); ok {
		os.Exit(osc52copy.Main(argv0, os.Args[1:], osc52copy.Streams{Stdin: os.Stdin, Stderr: os.Stderr}))
	}
	m.Run()
}

// runChild runs Main as xclip in a child that is a new session leader, with
// ctty as its controlling terminal when it is not nil.
func runChild(t *testing.T, stdin io.Reader, ctty *os.File) (stderr string, code int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), self, "-selection", "clipboard")
	cmd.Env = append(os.Environ(), childEnv+"=xclip")
	cmd.Stdin = stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if ctty != nil {
		cmd.Stdout = ctty
		cmd.SysProcAttr.Setctty = true
		cmd.SysProcAttr.Ctty = 1
	}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("run as xclip: %v", err)
	}
	return errOut.String(), code
}

// Another writer keeps the same terminal's buffer full: a write split into
// several write(2) calls lets its bytes land inside the sequence.
func TestMainWritesOneUnbrokenSequenceToTheControllingTerminal(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })

	var out bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(&out, master) // ends with EIO once every slave fd is closed
	}()
	stop := make(chan struct{})
	foreignDone := make(chan struct{})
	go func() {
		defer close(foreignDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := slave.Write([]byte("################")); err != nil {
				return
			}
		}
	}()

	text := strings.Repeat("abc", maxText/3)
	stderr, code := runChild(t, strings.NewReader(text), slave)
	close(stop)
	<-foreignDone
	_ = slave.Close()
	<-readDone

	if code != 0 || stderr != "" {
		t.Fatalf("exit = %d, stderr %q; want 0 and nothing", code, stderr)
	}
	if !strings.Contains(out.String(), osc52(text)) {
		start := strings.Index(out.String(), "\x1b]52;c;")
		t.Errorf("the terminal did not receive the %d-byte sequence unbroken (sequence starts at %d of %d bytes read)",
			len(osc52(text)), start, out.Len())
	}
}

func TestMainWithoutAControllingTerminalNamesIt(t *testing.T) {
	stderr, code := runChild(t, strings.NewReader("copied text"), nil)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (stderr %q)", code, stderr)
	}
	if want := "xclip: write to the terminal: open /dev/tty: "; !strings.HasPrefix(stderr, want) || strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", stderr, want)
	}
}

// A refused copy reports its own reason, which it can only do when the
// terminal is opened at the write.
func TestMainRefusesOversizeInputWithoutOpeningTheTerminal(t *testing.T) {
	stderr, code := runChild(t, strings.NewReader(strings.Repeat("z", maxText+1)), nil)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (stderr %q)", code, stderr)
	}
	if want := "xclip: copy too large to send to the browser"; !strings.HasPrefix(stderr, want) || strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", stderr, want)
	}
}
