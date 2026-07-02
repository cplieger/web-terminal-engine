package esctest

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestConformance runs the esctest2 suite against the engine's VT and asserts
// that the set of failing tests matches the checked-in known-deviations
// allowlist (known_failures.txt). It is skipped unless ESCTEST2_DIR points at
// an esctest2 checkout (run `bash scripts/esctest.sh` to fetch it and drive
// this test), so a normal `go test ./...` stays fast and dependency-free.
//
// When known_failures.txt is absent the test runs in triage mode: it logs the
// full failing set without asserting, so a first run can be inspected to seed
// the allowlist.
func TestConformance(t *testing.T) {
	dir := os.Getenv("ESCTEST2_DIR")
	if dir == "" {
		t.Skip("ESCTEST2_DIR not set; run scripts/esctest.sh to fetch esctest2 and drive this test")
	}

	level := 5 // aim at the highest VT level; genuine constraints go in the allowlist
	if v := os.Getenv("ESCTEST_LEVEL"); v != "" {
		if n, cerr := strconv.Atoi(v); cerr == nil {
			level = n
		}
	}

	// Maximum strictness: enable xtermWinopsEnabled so the tests gated behind
	// optionRequired(XTERM_WINOPS_ENABLED) — OSC 52 selection query, the
	// Set/Reset-Title-Mode hex/UTF-8 tests, and DECNCSM — become must-pass
	// rather than skipped-as-option-missing. ESCTEST_OPTIONS overrides the set
	// (space/comma separated); "none" disables all options.
	opts := []string{"xtermWinopsEnabled"}
	if v := os.Getenv("ESCTEST_OPTIONS"); v != "" {
		if v == "none" {
			opts = nil
		} else {
			opts = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
		}
	}

	res, err := Run(t.Context(), &Options{
		Dir:        dir,
		Timeout:    750 * time.Millisecond,
		Include:    os.Getenv("ESCTEST_INCLUDE"),
		MaxVTLevel: level,
		Options:    opts,
	})
	if err != nil {
		t.Fatalf("esctest harness failed to run: %v", err)
	}

	if dst := os.Getenv("ESCTEST_LOGCOPY"); dst != "" {
		if werr := os.WriteFile(dst, []byte(res.RawLog), 0o600); werr != nil {
			t.Logf("could not write ESCTEST_LOGCOPY %s: %v", dst, werr)
		}
	}

	sort.Strings(res.Failed)
	t.Logf("esctest: %d passed, %d skipped/known-bug, %d failed", res.Passed, res.Skipped, len(res.Failed))
	for _, name := range res.Failed {
		t.Logf("  FAIL %s", name)
	}

	allow, ok := loadAllowlist(t)
	if !ok {
		t.Logf("no known_failures.txt yet: triage mode, not asserting (seed the allowlist from the FAILs above)")
		return
	}

	failed := make(map[string]bool, len(res.Failed))
	for _, n := range res.Failed {
		failed[n] = true
	}
	for _, n := range res.Failed {
		if !allow[n] {
			t.Errorf("unexpected esctest failure (regression): %s", n)
		}
	}
	for n := range allow {
		if !failed[n] {
			t.Errorf("allowlisted test %s now PASSES; remove it from known_failures.txt", n)
		}
	}
}

// loadAllowlist reads known_failures.txt (one test name per line, # comments and
// blank lines ignored). The bool is false when the file does not exist.
func loadAllowlist(t *testing.T) (map[string]bool, bool) {
	t.Helper()
	f, err := os.Open("known_failures.txt")
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	allow := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		allow[line] = true
	}
	return allow, true
}
