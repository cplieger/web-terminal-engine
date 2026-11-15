// Package osc52copy stands in for the xclip, xsel, wl-copy and wl-paste
// commands inside a terminal served by this engine. A program that copies by
// running one of them reaches the browser clipboard through one OSC 52 write
// to its terminal instead of an X11 or Wayland server the session does not
// have.
//
// A consumer binary dispatches on its argv[0] (a symlink per name in [Names])
// and calls [Main], leaving [Streams].TTY nil to write to its controlling
// terminal. Only a copy of stdin to the clipboard selection is
// supported, as text: a paste, another selection, a file or text argument, or
// any option that changes what is copied fails with a message on stderr.
package osc52copy

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// Streams are the files Main reads and writes.
type Streams struct {
	// Stdin carries the text to copy.
	Stdin io.Reader
	// TTY is the terminal the OSC 52 sequence is written to. Nil means the
	// controlling terminal, /dev/tty, opened for the write with a blocking fd,
	// and makes Main ignore SIGWINCH for the rest of the process, so neither a
	// full terminal nor a resize splits the write and lets other output inside
	// the sequence. A job-control stop during the write can still split it.
	TTY io.Writer
	// Stderr receives one line explaining a failure.
	Stderr io.Writer
}

// Exit codes Main returns.
const (
	exitOK    = 0
	exitFail  = 1 // the copy did not happen: too large, a paste, an I/O error
	exitUsage = 2 // arguments or a name this stand-in does not accept
)

const osc52Prefix = "52;c;"

var names = []string{"xclip", "xsel", "wl-copy", "wl-paste"}

// Names returns the command names Main stands in for, in a new slice.
func Names() []string {
	return slices.Clone(names)
}

// Main runs the command named by argv0's base name with args and returns its
// exit status: 0 when the text read from Stdin was written to TTY as one
// OSC 52 clipboard write, 1 when the copy was refused (larger than the
// terminal accepts, or a paste) or an I/O error stopped it, and 2 for an
// unknown name or an unsupported argument. Nothing is written to TTY unless
// the copy succeeds, and a nil TTY is opened only then, so a refused copy needs
// no terminal.
func Main(argv0 string, args []string, s Streams) int {
	name := filepath.Base(argv0)
	var err error
	paste := false
	switch name {
	case "xclip":
		paste, err = parseXclip(args)
	case "xsel":
		paste, err = parseXsel(args)
	case "wl-copy":
		err = parseWlCopy(args)
	case "wl-paste":
		paste = true
	default:
		fmt.Fprintf(s.Stderr, "%s: unknown command name; want one of %s\n", name, strings.Join(names, ", "))
		return exitUsage
	}
	if paste {
		fmt.Fprintf(s.Stderr, "%s: paste is not supported in this terminal\n", name)
		return exitFail
	}
	if err != nil {
		fmt.Fprintf(s.Stderr, "%s: %v\n", name, err)
		return exitUsage
	}
	return writeCopy(name, s)
}

// maxText is the longest text whose OSC 52 payload fits vt.MaxOSC52Len.
const maxText = (vt.MaxOSC52Len - len(osc52Prefix)) / 4 * 3

func writeCopy(name string, s Streams) int {
	text, err := io.ReadAll(io.LimitReader(s.Stdin, int64(maxText)+1))
	if err != nil {
		fmt.Fprintf(s.Stderr, "%s: read stdin: %v\n", name, err)
		return exitFail
	}
	if len(text) > maxText {
		fmt.Fprintf(s.Stderr, "%s: copy too large to send to the browser (over %d bytes)\n", name, maxText)
		return exitFail
	}
	seq := make([]byte, 0, len("\x1b]")+len(osc52Prefix)+base64.StdEncoding.EncodedLen(len(text))+len("\x1b\\"))
	seq = append(seq, "\x1b]"+osc52Prefix...)
	seq = base64.StdEncoding.AppendEncode(seq, text)
	seq = append(seq, "\x1b\\"...)
	if err := writeTTY(s.TTY, seq); err != nil {
		fmt.Fprintf(s.Stderr, "%s: write to the terminal: %v\n", name, err)
		return exitFail
	}
	return exitOK
}

const ttyPath = "/dev/tty"

func writeTTY(tty io.Writer, seq []byte) error {
	if tty != nil {
		_, err := tty.Write(seq)
		return err
	}
	ignoreResize()
	f, err := openBlockingWrite(ttyPath)
	if err != nil {
		return err
	}
	_, err = f.Write(seq)
	return errors.Join(err, f.Close())
}

// sysOpen is a test seam: Go's signal handlers carry SA_RESTART, so a test on
// a local filesystem cannot make open return EINTR.
var sysOpen = syscall.Open

// openBlockingWrite opens path write-only and leaves the fd blocking. Opened
// with os.OpenFile, a terminal joins Go's poller, which sets O_NONBLOCK: a
// write to a full terminal then takes several write(2) calls, another
// process's output lands inside the OSC 52 string, and the terminal ends the
// string at its first ESC. os.NewFile keeps a blocking fd out of the poller.
// EINTR is retried as os.OpenFile does (https://go.dev/issue/11180, 39237).
func openBlockingWrite(path string) (*os.File, error) {
	for {
		fd, err := sysOpen(path, syscall.O_WRONLY|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		return os.NewFile(uintptr(fd), path), nil
	}
}

// The xclip options parseXclip handles by name.
const (
	xclipOut       = "-out"
	xclipSelection = "-selection"
	xclipTarget    = "-target"
)

// xclipOptions is xclip's option table. xclip parses it with Xlib's
// XrmParseCommand, which takes any unique prefix of a name ("-sel", "-i", "-t").
var xclipOptions = []string{
	"-alt-text", "-debug", "-display", "-filter", "-help", "-in", "-loops", "-noutf8", xclipOut,
	"-quiet", "-rmlastnl", xclipSelection, "-sensitive", "-silent", xclipTarget, "-verbose", "-version", "-wait",
}

// xclipOption returns the xclip option arg names, or "" when it names none or
// several, which xclip would read as a file name.
func xclipOption(arg string) string {
	if slices.Contains(xclipOptions, arg) {
		return arg
	}
	match := ""
	for _, o := range xclipOptions {
		if len(arg) > 1 && strings.HasPrefix(o, arg) {
			if match != "" {
				return ""
			}
			match = o
		}
	}
	return match
}

// isTextTarget reports whether an xclip -target or wl-copy --type value copies
// the bytes as text.
func isTextTarget(v string) bool {
	switch v {
	case "UTF8_STRING", "STRING", "TEXT":
		return true
	}
	return strings.EqualFold(v, "text/plain") || strings.EqualFold(v, "text/plain;charset=utf-8")
}

// parseXclip accepts xclip's copy form: -selection with a value starting with
// c or C (xclip reads only the first letter), optionally -in, -quiet, -silent,
// -verbose and a text -target, each by any unique prefix. An -out makes it a
// paste.
func parseXclip(args []string) (paste bool, err error) {
	if slices.ContainsFunc(args, func(a string) bool { return xclipOption(a) == xclipOut }) {
		return true, nil
	}
	clipboard := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch opt := xclipOption(a); opt {
		case "-in", "-quiet", "-silent", "-verbose":
		case xclipSelection, xclipTarget:
			if i+1 == len(args) {
				return false, fmt.Errorf("%s needs a value", a)
			}
			i++
			if err := checkXclipValue(opt, args[i]); err != nil {
				return false, err
			}
			clipboard = clipboard || opt == xclipSelection
		default:
			return false, fmt.Errorf("unsupported argument %q", a)
		}
	}
	if !clipboard {
		return false, errors.New("only the clipboard selection is supported; pass -selection clipboard")
	}
	return false, nil
}

// checkXclipValue refuses a -selection other than the clipboard and a -target
// other than text.
func checkXclipValue(opt, v string) error {
	if opt == xclipTarget {
		if !isTextTarget(v) {
			return fmt.Errorf("target %q is not supported; only text is", v)
		}
		return nil
	}
	if v == "" || (v[0] != 'c' && v[0] != 'C') {
		return fmt.Errorf("selection %q is not supported; only clipboard is", v)
	}
	return nil
}

// splitXselFlags splits combined short options the way xsel does before it
// parses them: "-bi" is "-b -i".
func splitXselFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) > 2 && a[0] == '-' && a[1] != '-' {
			for _, c := range a[1:] {
				out = append(out, "-"+string(c))
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// parseXsel accepts xsel's copy form: --clipboard (-b), optionally --input
// (-i), --nodetach (-n) and --verbose (-v), short ones combined or not. A -o
// (--output) anywhere makes it a paste.
func parseXsel(args []string) (paste bool, err error) {
	args = splitXselFlags(args)
	if slices.Contains(args, "-o") || slices.Contains(args, "--output") {
		return true, nil
	}
	clipboard := false
	for _, a := range args {
		switch a {
		case "-i", "--input", "-n", "--nodetach", "-v", "--verbose":
		case "-b", "--clipboard":
			clipboard = true
		default:
			return false, fmt.Errorf("unsupported argument %q", a)
		}
	}
	if !clipboard {
		return false, errors.New("only the clipboard selection is supported; pass --clipboard")
	}
	return false, nil
}

// parseWlCopy accepts wl-copy's copy of stdin, optionally --foreground (-f)
// and a text --type (-t). A text argument, which wl-copy copies in place of
// stdin, and every other option are refused.
func parseWlCopy(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		var typ string
		switch {
		case a == "-f" || a == "--foreground":
			continue
		case a == "-t" || a == "--type":
			if i+1 == len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			i++
			typ = args[i]
		case strings.HasPrefix(a, "--type="):
			typ = strings.TrimPrefix(a, "--type=")
		case strings.HasPrefix(a, "-t"):
			typ = strings.TrimPrefix(a, "-t")
		default:
			return fmt.Errorf("unsupported argument %q; only a copy of stdin is supported", a)
		}
		if !isTextTarget(typ) {
			return fmt.Errorf("type %q is not supported; only text is", typ)
		}
	}
	return nil
}
