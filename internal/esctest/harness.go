// Package esctest runs the esctest2 VT conformance suite
// (github.com/ThomasDickey/esctest2, George Nachman's esctest with Thomas
// Dickey's fixes) against the engine's vt.Screen.
//
// esctest2 is GPL-2.0; the engine is GPL-3.0. To keep the incompatible-for-
// combination licenses at arm's length, esctest2 is NEVER vendored into this
// repo. It is fetched separately (see scripts/esctest.sh) and located at run
// time via the ESCTEST2_DIR environment variable. The suite is executed as an
// ordinary subprocess and communicated with over a PTY — it is never linked
// into the engine — so this is mere aggregation, not a derived work.
//
// The model: esctest runs as the child process of a terminal. It writes escape
// sequences to its stdout (which the terminal renders) and reads state back
// through queries (cursor-position reports, and rectangular-area checksums via
// DECRQCRA). It never inspects pixels, so it validates the Go VT state machine
// directly with no browser involved. Run drives a real PTY: the child's stdout
// feeds vt.Screen, and vt.Screen's Response bytes (query answers) are written
// back to the child's stdin.
package esctest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cplieger/web-terminal-engine/v2/vt"
	"github.com/creack/pty"
)

// Options configures a conformance run.
type Options struct {
	// Dir is the esctest2 checkout (the directory containing esctest/esctest.py).
	Dir string
	// Python is the interpreter to run esctest with (default "python3").
	Python string
	// Include is a regexp restricting which tests run (esctest --include; default ".*").
	Include string
	// Options are esctest --options flags (e.g. "xtermWinopsEnabled"). Enabling
	// xtermWinopsEnabled is the maximum-strictness setting: it turns the tests
	// gated behind optionRequired(XTERM_WINOPS_ENABLED) — OSC 52 selection
	// query, the Set/Reset-Title-Mode hex/UTF-8 tests, and DECNCSM — from
	// "expected to fail (option missing)" into MUST-PASS. Do NOT add
	// disableWideChars (it would force the 8-bit C1 control tests, which are
	// incompatible with a UTF-8 terminal, into hard failures).
	Options []string
	// Timeout is how long esctest waits for each query response (default 1s).
	Timeout time.Duration
	// Rows, Cols size the PTY and the screen. esctest resets to 25x80 per test
	// via a programmatic resize the engine treats as a no-op, so these MUST be
	// 25x80 for the suite's assumptions to hold (default 25x80).
	Rows int
	Cols int
	// MaxVTLevel is esctest --max-vt-level. Must be >=4 or every screen-content
	// assertion is skipped (DECRQCRA is a level-4 feature). Default 4.
	MaxVTLevel int
}

// Result is the parsed outcome of a conformance run.
type Result struct {
	// RawLog is the full esctest logfile, kept for diagnosis.
	RawLog string
	// Failed is the list of test names that failed (excludes known bugs/skips).
	Failed []string
	// Passed is the number of tests that passed.
	Passed int
	// Skipped counts tests esctest skipped (known bugs + insufficient VT level).
	Skipped int
}

var (
	// esctest logs one such line per failing test.
	failedRe  = regexp.MustCompile(`\*{3} TEST (\S+) FAILED`)
	summaryRe = regexp.MustCompile(`\*{3} .*passed.*\*{3}`)
)

// Run executes the esctest suite against a fresh vt.Screen and returns the
// parsed result. ctx cancels the child process (e.g. a test timeout). A non-nil
// error means the harness itself failed to run the suite (missing interpreter,
// PTY error); test failures reported by esctest are carried in Result, not the
// error.
func Run(ctx context.Context, opts *Options) (*Result, error) {
	cfg := *opts
	applyDefaults(&cfg)

	script := filepath.Join(cfg.Dir, "esctest", "esctest.py")
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("esctest.py not found at %s: %w", script, err)
	}

	logFile, err := os.CreateTemp("", "esctest-*.log")
	if err != nil {
		return nil, fmt.Errorf("create logfile: %w", err)
	}
	logPath := logFile.Name()
	_ = logFile.Close()
	defer func() { _ = os.Remove(logPath) }()

	cmd := exec.CommandContext(ctx, cfg.Python, esctestArgs(script, logPath, &cfg)...) // #nosec G204 -- fixed esctest invocation
	cmd.Dir = filepath.Join(cfg.Dir, "esctest")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(cfg.Rows), Cols: uint16(cfg.Cols)}) // #nosec G115 -- terminal dims are small
	if err != nil {
		return nil, fmt.Errorf("start esctest under pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// esctest runs with --expected-terminal=xterm and its per-test reset()
	// establishes xterm's stock black-on-white resources (OSC 10 #000, OSC 11
	// #fff). Configure the screen with that same theme so dynamic-color reset
	// (OSC 110/111/112) restores the xterm default esctest asserts, rather than
	// the engine's real dark default (which is correct for the browser but not
	// what the xterm-conformance suite expects). This is test-harness config,
	// like AllowScreenReport below; the shipped engine default stays dark.
	screen := vt.New(cfg.Rows, cfg.Cols, vt.WithTheme(vt.Theme{
		Foreground: vt.RGB(0, 0, 0),
		Background: vt.RGB(0xff, 0xff, 0xff),
		Cursor:     vt.RGB(0, 0, 0),
	}))
	screen.AllowScreenReport = true // enable DECRQCRA so esctest can read the screen back

	done := make(chan struct{})
	go func() {
		pump(ptmx, screen)
		close(done)
	}()

	// esctest exits 0 even when tests fail (it reports via the log), so a Wait
	// error is only a real problem if we also can't parse a summary.
	waitErr := cmd.Wait()
	_ = ptmx.Close()
	<-done

	data, _ := os.ReadFile(logPath)
	log := string(data)
	if !summaryRe.MatchString(log) && waitErr != nil {
		return nil, fmt.Errorf("esctest did not complete (%w); log tail:\n%s", waitErr, tail(log, 2000))
	}

	return parseLog(log), nil
}

// applyDefaults fills in the zero-value Options fields in place.
func applyDefaults(o *Options) {
	if o.Python == "" {
		o.Python = "python3"
	}
	if o.Rows <= 0 {
		o.Rows = 25
	}
	if o.Cols <= 0 {
		o.Cols = 80
	}
	if o.MaxVTLevel <= 0 {
		o.MaxVTLevel = 4
	}
	if o.Timeout <= 0 {
		o.Timeout = time.Second
	}
	if o.Include == "" {
		o.Include = ".*"
	}
}

// esctestArgs builds the esctest.py command line. --xterm-checksum=0 selects the
// pre-279 xterm checksum (the negated ordinal sum vt.reportRectChecksum emits);
// --no-print-logs keeps the trailing log dump out of the PTY (the logfile still
// gets it); --v=2 (LOG_INFO) puts per-test pass/fail + the summary in the log.
func esctestArgs(script, logPath string, o *Options) []string {
	args := []string{
		script,
		"--expected-terminal=xterm",
		fmt.Sprintf("--max-vt-level=%d", o.MaxVTLevel),
		"--xterm-checksum=0",
		"--logfile=" + logPath,
		"--no-print-logs",
		fmt.Sprintf("--timeout=%g", o.Timeout.Seconds()),
		"--v=2",
		"--include=" + o.Include,
	}
	// esctest's --options takes nargs="+", so the flag is followed by each
	// option token as a separate argument.
	if len(o.Options) > 0 {
		args = append(args, "--options")
		args = append(args, o.Options...)
	}
	return args
}

// pump feeds the child's escape output into the screen and writes the screen's
// query answers back to the child's stdin, until the PTY closes. A single
// goroutine owns the screen, so no synchronization is needed.
func pump(ptmx *os.File, screen *vt.Screen) {
	buf := make([]byte, 8192)
	for {
		n, readErr := ptmx.Read(buf)
		if n > 0 {
			_, _ = screen.Write(buf[:n])
			if len(screen.Response) > 0 {
				_, _ = ptmx.Write(screen.Response)
				screen.Response = screen.Response[:0]
			}
		}
		if readErr != nil {
			return
		}
	}
}

// parseLog derives pass/skip counts and the failing-test list from the esctest
// logfile. Counts come from the per-test markers esclog emits, which are more
// robust than the human-readable summary line.
func parseLog(log string) *Result {
	res := &Result{RawLog: log}
	for _, m := range failedRe.FindAllStringSubmatch(log, -1) {
		res.Failed = append(res.Failed, m[1])
	}
	for line := range strings.SplitSeq(log, "\n") {
		switch {
		case line == "Passed.":
			res.Passed++
		case strings.HasPrefix(line, "Fails as expected:"),
			strings.HasPrefix(line, "Skipped because"):
			res.Skipped++
		}
	}
	return res
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
