package vt

import (
	"encoding/base64"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// OSC 7501 (Program Status Protocol rev 0.4) conformance tests:
// https://www.superlogical.com/rex/docs/build/program-status

func report7501(body string) []byte { return []byte("\x1b]7501;" + body + "\x1b\\") }

func write7501(s *Screen, body string) { s.Write(report7501(body)) }

func b64(text string) string { return base64.StdEncoding.EncodeToString([]byte(text)) }

func TestOSC7501RootReport(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ProgramStatus
	}{
		{"working", "state=working", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"working with progress", "state=working:progress=40", ProgramStatus{State: ProgramStateWorking, Progress: 40}},
		{"progress bounds", "state=working:progress=100", ProgramStatus{State: ProgramStateWorking, Progress: 100}},
		{"progress over 100 is absent", "state=working:progress=101", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"progress not an integer is absent", "state=working:progress=4.5", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"signed progress is absent", "state=working:progress=-5", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"blocked permission", "state=blocked:kind=permission", ProgramStatus{State: ProgramStateBlocked, Kind: "permission", Progress: -1}},
		{"blocked question", "state=blocked:kind=question:progress=7", ProgramStatus{State: ProgramStateBlocked, Kind: "question", Progress: 7}},
		{"blocked auth", "state=blocked:kind=auth", ProgramStatus{State: ProgramStateBlocked, Kind: "auth", Progress: -1}},
		{"unknown kind is absent", "state=blocked:kind=coffee", ProgramStatus{State: ProgramStateBlocked, Progress: -1}},
		{"kind ignored off blocked", "state=working:kind=permission", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"done", "state=done", ProgramStatus{State: ProgramStateDone, Progress: -1}},
		{"progress ignored on done", "state=done:progress=50", ProgramStatus{State: ProgramStateDone, Progress: -1}},
		{"error", "state=error", ProgramStatus{State: ProgramStateError, Progress: -1}},
		{"idle", "state=idle", ProgramStatus{State: ProgramStateIdle, Progress: -1}},
		{"app", "state=working:app=build-tool", ProgramStatus{State: ProgramStateWorking, App: "build-tool", Progress: -1}},
		{"app outside its alphabet is absent", "state=working:app=a,b", ProgramStatus{State: ProgramStateWorking, Progress: -1}},
		{"msg and title accepted", "state=done:msg=" + b64("Built 3 targets") + ":title=" + b64("Build"), ProgramStatus{State: ProgramStateDone, Progress: -1}},
		{"unpadded base64", "state=done:msg=" + strings.TrimRight(b64("ab"), "="), ProgramStatus{State: ProgramStateDone, Progress: -1}},
	}
	for _, c := range cases {
		for _, term := range []string{"\x1b\\", "\a"} {
			t.Run(fmt.Sprintf("%s/%q", c.name, term), func(t *testing.T) {
				s := New(24, 80)
				s.Write([]byte("\x1b]7501;" + c.body + term))
				if !s.ProgramReported() {
					t.Fatal("ProgramReported() = false after an accepted report")
				}
				want := c.want
				want.Seq = 1
				if got := s.ProgramStatus(); got != want {
					t.Errorf("ProgramStatus() = %+v, want %+v", got, want)
				}
				if s.ProgramReportSeq() != 1 {
					t.Errorf("ProgramReportSeq() = %d, want 1", s.ProgramReportSeq())
				}
			})
		}
	}
}

func TestOSC7501ReportReplacesRecord(t *testing.T) {
	s := New(24, 80)
	write7501(s, "state=blocked:kind=permission:app=x:progress=10")
	write7501(s, "state=working")
	if got, want := s.ProgramStatus(), (ProgramStatus{State: ProgramStateWorking, Progress: -1, Seq: 2}); got != want {
		t.Errorf("ProgramStatus() = %+v, want %+v: a report replaces its record whole", got, want)
	}
}

func TestOSC7501DiscardRules(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"no state", "kind=permission"},
		{"unknown state", "state=sleeping"},
		{"empty id", "state=done:id="},
		{"id with an empty segment", "state=done:id=a//b"},
		{"id outside its alphabet", "state=done:id=a,b"},
		{"id segment over 32 bytes", "state=done:id=" + strings.Repeat("a", 33)},
		{"id over 8 levels", "state=done:id=a/b/c/d/e/f/g/h/i"},
		{"id over 128 bytes", "state=done:id=" + strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 31)+"/", 5), "/")},
		{"app over 32 bytes", "state=done:app=" + strings.Repeat("a", 33)},
		{"msg not base64", "state=done:msg=no-t_base64"},
		{"msg with bad padding", "state=done:msg=QQ="},
		{"msg decoding to a newline", "state=done:msg=" + b64("two\nlines")},
		{"msg decoding to a tab", "state=done:msg=" + b64("a\tb")},
		{"msg decoding to DEL", "state=done:msg=" + b64("a\x7fb")},
		{"msg decoding to a C1 control", "state=done:msg=" + b64("a\u0085b")},
		{"msg not UTF-8", "state=done:msg=" + b64("\xff\xfe")},
		{"msg over 2732 bytes encoded", "state=done:msg=" + strings.Repeat("QUFB", 684)},
		{"msg over 2048 bytes decoded", "state=done:msg=" + strings.TrimRight(b64(strings.Repeat("a", 2049)), "=")},
		{"title over 256 bytes encoded", "state=done:title=" + strings.Repeat("QUFB", 65)},
		{"payload over the OSC cap", "state=done:pad=" + strings.Repeat("a", maxOSCLen)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fresh := New(24, 80)
			write7501(fresh, c.body)
			if fresh.ProgramReported() || fresh.ProgramReportSeq() != 0 || len(fresh.program.records) != 0 {
				t.Errorf("discarded report left seen = %v seq = %d records = %d", fresh.ProgramReported(), fresh.ProgramReportSeq(), len(fresh.program.records))
			}
			kept := New(24, 80)
			write7501(kept, "state=working:progress=5")
			write7501(kept, c.body)
			want := ProgramStatus{State: ProgramStateWorking, Progress: 5, Seq: 1}
			if got := kept.ProgramStatus(); got != want || kept.ProgramReportSeq() != 1 {
				t.Errorf("discarded report changed the record: %+v seq %d, want %+v seq 1", got, kept.ProgramReportSeq(), want)
			}
		})
	}
}

var (
	pairPriorRoot  = programRecord{state: ProgramStateWorking, app: "p", progress: 5}
	pairPriorChild = programRecord{state: ProgramStateIdle, progress: -1}
)

// pairRecords is the record set after the pair fixture with root as the root.
func pairRecords(root programRecord) map[string]programRecord {
	return map[string]programRecord{"": root, "w": pairPriorChild}
}

// checkPairReport writes body after a root and a child report and checks every
// record and the report number; a nil want means body is ignored whole.
func checkPairReport(t *testing.T, body string, want map[string]programRecord) {
	t.Helper()
	s := New(24, 80)
	write7501(s, "state=working:progress=5:app=p")
	write7501(s, "state=idle:id=w")
	write7501(s, body)
	wantSeq := uint64(3)
	if want == nil {
		want, wantSeq = pairRecords(pairPriorRoot), 2
	}
	got := make(map[string]programRecord, len(s.program.records))
	for id, r := range s.program.records {
		r.touched = 0
		got[id] = r
	}
	if !maps.Equal(got, want) {
		t.Errorf("records = %+v, want %+v", got, want)
	}
	if s.ProgramReportSeq() != wantSeq {
		t.Errorf("ProgramReportSeq() = %d, want %d", s.ProgramReportSeq(), wantSeq)
	}
}

// TestOSC7501PairClassification has one group of rows per arm of
// parseProgramReport's pair switch.
func TestOSC7501PairClassification(t *testing.T) {
	prior, child, root := pairPriorRoot, pairPriorChild, pairRecords
	done := root(programRecord{state: ProgramStateDone, progress: -1})
	key17 := strings.Repeat("k", 17)
	cases := []struct {
		name string
		body string
		want map[string]programRecord // nil: the report is ignored whole
	}{
		// A key over 16 bytes discards the report, with or without "=".
		{"key over 16 bytes", "state=done:" + key17 + "=1", nil},
		{"key over 16 bytes without =", "state=done:" + key17, nil},
		{"key over 16 bytes with a value outside the value set", "state=done:" + key17 + "=;", nil},
		{"key over 16 bytes before the state", key17 + ":state=done", nil},
		{"key at 16 bytes without =", "state=done:" + strings.Repeat("k", 16), done},
		// A value over its key's limit discards the report, in any pair.
		{"app over 32 bytes before an app", "state=done:app=" + strings.Repeat("a", 33) + ":app=x", nil},
		{"app over 32 bytes outside the value set", "state=done:app=" + strings.Repeat("a", 33) + "!", nil},
		{"id segment over 32 bytes before an id", "state=done:id=" + strings.Repeat("a", 33) + ":id=c", nil},
		{"id over 8 levels before an id", "state=done:id=a/b/c/d/e/f/g/h/i:id=c", nil},
		{"msg over 2732 bytes encoded before a msg", "state=done:msg=" + strings.Repeat("QUFB", 684) + ":msg=QUI", nil},
		{"msg over 2732 bytes encoded outside the value set", "state=done:msg=" + strings.Repeat("QUFB", 684) + "!", nil},
		{"title over 256 bytes encoded before a title", "state=done:title=" + strings.Repeat("QUFB", 65) + ":title=QUI", nil},
		// An id pair is the report's id, in the value set or not; the report is ignored unless the last one is an id.
		{"id outside the value set", "state=done:id=a!b", nil},
		{"id with an inner space", "state=done:id=a b", nil},
		{"repeated id whose last value is outside the value set", "state=done:id=c:id=a!b", nil},
		{"repeated id whose last value is an id", "state=blocked:id=a!b:id=c", map[string]programRecord{
			"": prior, "w": child, "c": {state: ProgramStateBlocked, progress: -1},
		}},
		// A pair without "=" is skipped and leaves the earlier value.
		{"id without =", "state=done:id", done},
		{"bare id after an id", "state=done:id=c:id", map[string]programRecord{
			"": prior, "w": child, "c": {state: ProgramStateDone, progress: -1},
		}},
		{"bare state after a state", "state=done:state", done},
		{"bare state alone", "state", nil},
		{"bare progress after a progress", "state=working:progress=42:progress", root(programRecord{state: ProgramStateWorking, progress: 42})},
		{"bare kind after a kind", "state=blocked:kind=question:kind", root(programRecord{state: ProgramStateBlocked, kind: "question", progress: -1})},
		{"bare app after an app", "state=working:app=x:app", root(programRecord{state: ProgramStateWorking, app: "x", progress: -1})},
		// A value outside the value set skips the pair and leaves the earlier value.
		{"state outside the value set after a state", "state=done:state=a!b", done},
		{"state outside the value set before a state", "state=a!b:state=done", done},
		{"state outside the value set alone", "state=do!ne", nil},
		{"msg outside the value set", "state=done:msg=QU!B", done},
		{"msg with a newline in its base64", "state=done:msg=QU\nFB", done},
		{"title outside the value set", "state=done:title=QU!B", done},
		{"kind outside the value set after a kind", "state=blocked:kind=question:kind=a!b", root(programRecord{state: ProgramStateBlocked, kind: "question", progress: -1})},
		{"kind outside the value set alone", "state=blocked:kind=permission!", root(programRecord{state: ProgramStateBlocked, progress: -1})},
		{"progress outside the value set after a progress", "state=working:progress=5:progress=5!", root(programRecord{state: ProgramStateWorking, progress: 5})},
		{"app outside the value set after an app", "state=working:app=x:app=a!b", root(programRecord{state: ProgramStateWorking, app: "x", progress: -1})},
		{"value with a semicolon", "state=done:state=a;b", done},
		// A msg or title that is not base64 of control-free text discards the report, in any pair.
		{"msg not base64 before a msg", "state=done:msg=no-t_base64:msg=QUI", nil},
		{"title decoding to a tab before a title", "state=done:title=" + b64("a\tb") + ":title=QUI", nil},
		// An empty, uppercase or unknown key is skipped.
		{"empty key", "state=done:=error", done},
		{"uppercase key", "state=done:State=error", done},
		{"unknown key", "state=done:future=1", done},
		{"several malformed pairs", "nokey:=x:Upper=1:state=done:bad=a;b:msg", done},
		// A defined key is stored, trimmed, and a repeat keeps the last value.
		{"whitespace around keys and values", " state = done : app = x ", root(programRecord{state: ProgramStateDone, app: "x", progress: -1})},
		{"repeated state", "state=working:state=done", done},
		{"repeated kind whose last value is unknown", "state=blocked:kind=question:kind=coffee", root(programRecord{state: ProgramStateBlocked, progress: -1})},
		{"repeated progress whose last value is out of range", "state=working:progress=5:progress=500", root(programRecord{state: ProgramStateWorking, progress: -1})},
		{"repeated state whose last value is unknown", "state=done:state=sleeping", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkPairReport(t, c.body, c.want) })
	}
}

// pairOutcome is what the pair rule a pair meets first does to its report.
type pairOutcome uint8

const (
	pairDiscards pairOutcome = iota // ignored whole, whatever pairs follow
	pairIsTheID                     // a malformed id ignores the report while it is the last id
	pairSkipped                     // the rest of the report applies
)

var (
	specValueSet  = regexp.MustCompile(`^[A-Za-z0-9_.,+/=-]*$`)
	specIDGrammar = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,32}(/[A-Za-z0-9_.+-]{1,32})*$`)
)

// specOverLimit is the rev 0.4 Limits table for one pair's value.
func specOverLimit(key string, _ bool, v string) bool {
	switch key {
	case "app":
		return len(v) > 32
	case "id":
		segs := strings.Split(v, "/")
		return len(v) > 128 || len(segs) > 8 || slices.ContainsFunc(segs, func(s string) bool { return len(s) > 32 })
	case "msg":
		return len(v) > 2732
	case "title":
		return len(v) > 256
	}
	return false
}

// specBadText is rev 0.4's free-text rule: standard base64, padding optional,
// of UTF-8 within the decoded cap and with no control character.
func specBadText(key string, _ bool, v string) bool {
	limit := map[string]int{"msg": 2048, "title": 192}[key]
	if limit == 0 || v == "" {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(v)
	}
	return err != nil || len(b) > limit || !utf8.Valid(b) || strings.ContainsFunc(string(b), unicode.IsControl)
}

// programPairRules restates parseProgramReport's pair precedence, highest
// first, with oracles written from the rev 0.4 text rather than the parser.
var programPairRules = []struct {
	name    string
	meets   func(key string, hasEq bool, value string) bool
	outcome pairOutcome
}{
	{"key over 16 bytes", func(k string, _ bool, _ string) bool { return len(k) > 16 }, pairDiscards},
	{"no =", func(_ string, eq bool, _ string) bool { return !eq }, pairSkipped},
	{"value over its limit", specOverLimit, pairDiscards},
	{"malformed id", func(k string, _ bool, v string) bool { return k == "id" && !specIDGrammar.MatchString(v) }, pairIsTheID},
	{"byte outside the value set", func(_ string, _ bool, v string) bool { return !specValueSet.MatchString(v) }, pairSkipped},
	{"msg or title not base64 of control-free text", specBadText, pairDiscards},
	{"unknown key", func(k string, _ bool, _ string) bool {
		return !slices.Contains([]string{"state", "id", "kind", "progress", "app", "msg", "title"}, k)
	}, pairSkipped},
}

// TestOSC7501PairPrecedence generates, for every two rules of
// programPairRules, the pairs that meet both, and checks the higher rule
// decides. A discarding row ends with a valid id, which a lower rule would
// let through.
func TestOSC7501PairPrecedence(t *testing.T) {
	keys := []string{"", "zz", "kind", "id", "app", "msg", "title", strings.Repeat("k", 17)}
	values := []string{
		"c", "a!b", "a,b", "a//b", strings.Repeat("a", 33), strings.Repeat("a", 33) + "!", "a/b/c/d/e/f/g/h/i",
		"QUI", "QU!B", "no-t_base64", b64("a\tb"),
		strings.Repeat("QUFB", 684), strings.Repeat("QUFB", 684) + "!", strings.Repeat("QUFB", 65),
	}
	type pair struct {
		key, value string
		hasEq      bool
	}
	pool := make([]pair, 0, len(keys)*(len(values)+1))
	for _, k := range keys {
		pool = append(pool, pair{key: k})
		for _, v := range values {
			pool = append(pool, pair{key: k, value: v, hasEq: true})
		}
	}
	// Rule pairs no single pair can meet: a key over 16 bytes is no defined
	// key, a pair with no "=" has an empty value, and the rest need two keys.
	exclusive := map[[2]int]bool{
		{0, 2}: true, {0, 3}: true, {0, 5}: true, {1, 2}: true, {1, 4}: true,
		{1, 5}: true, {2, 6}: true, {3, 5}: true, {3, 6}: true, {5, 6}: true,
	}
	met := map[[2]int]bool{}
	for n, p := range pool {
		var rules []int
		for i, r := range programPairRules {
			if r.meets(p.key, p.hasEq, p.value) {
				rules = append(rules, i)
			}
		}
		for a := range rules {
			for _, b := range rules[a+1:] {
				met[[2]int{rules[a], b}] = true
			}
		}
		if len(rules) < 2 {
			continue
		}
		first := programPairRules[rules[0]]
		text := p.key
		if p.hasEq {
			text += "=" + p.value
		}
		body := "state=done:" + text
		var want map[string]programRecord
		switch first.outcome {
		case pairDiscards:
			body += ":id=c"
		case pairSkipped:
			want = pairRecords(programRecord{state: ProgramStateDone, progress: -1})
		}
		name := fmt.Sprintf("%s over %s %d", first.name, programPairRules[rules[1]].name, n)
		t.Run(name, func(t *testing.T) { checkPairReport(t, body, want) })
	}
	for a := range programPairRules {
		for b := a + 1; b < len(programPairRules); b++ {
			if k := [2]int{a, b}; met[k] == exclusive[k] {
				t.Errorf("rules %q and %q: met by a pair = %v, listed exclusive = %v",
					programPairRules[a].name, programPairRules[b].name, met[k], exclusive[k])
			}
		}
	}
}

func TestOSC7501LimitsAccepted(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"id at 8 levels", "state=done:id=a/b/c/d/e/f/g/h"},
		{"id at 128 bytes", "state=done:id=" + strings.Repeat(strings.Repeat("a", 31)+"/", 4)[:127] + "b"},
		{"key at 16 bytes", "state=done:" + strings.Repeat("k", 16) + "=1"},
		{"app at 32 bytes", "state=done:app=" + strings.Repeat("a", 32)},
		{"msg at 2048 bytes decoded", "state=done:msg=" + strings.TrimRight(b64(strings.Repeat("a", 2048)), "=")},
		{"title at 192 bytes decoded", "state=done:title=" + b64(strings.Repeat("a", 192))},
		{"empty msg", "state=done:msg="},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(24, 80)
			s.Write([]byte("\x1b]7501;" + c.body + "\a"))
			if !s.ProgramReported() || len(s.program.records) != 1 {
				t.Errorf("report at the limit dropped: seen = %v records = %d", s.ProgramReported(), len(s.program.records))
			}
		})
	}
}

func TestOSC7501WholeSequenceCap(t *testing.T) {
	const prefix = "\x1b]7501;state=done:pad="
	fits := 4096 - len(prefix) - len("\x1b\\") // pad for an ST-terminated sequence of exactly 4096 bytes
	for _, term := range []string{"\x1b\\", "\a"} {
		t.Run(fmt.Sprintf("%q", term), func(t *testing.T) {
			at := prefix + strings.Repeat("a", fits) + term
			if len(at) > 4096 {
				t.Fatalf("accepted-boundary sequence is %d bytes, over 4096", len(at))
			}
			s := New(24, 80)
			s.Write([]byte(at))
			if !s.ProgramReported() {
				t.Errorf("%d-byte sequence dropped, want accepted", len(at))
			}
			over := prefix + strings.Repeat("a", fits+1) + term
			s = New(24, 80)
			s.Write([]byte(over))
			if s.ProgramReported() || len(s.program.records) != 0 {
				t.Errorf("%d-byte sequence accepted, want discarded", len(over))
			}
		})
	}
}

func TestOSC7501Fold(t *testing.T) {
	t.Run("a blocked child outranks a working root", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=working:app=tool")
		write7501(s, "state=blocked:kind=question:id=worker/1:progress=30")
		want := ProgramStatus{State: ProgramStateBlocked, Kind: "question", App: "tool", Progress: 30, Seq: 2}
		if got := s.ProgramStatus(); got != want {
			t.Errorf("ProgramStatus() = %+v, want %+v", got, want)
		}
	})
	t.Run("a blocked root outranks a blocked child", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=blocked:kind=auth")
		write7501(s, "state=blocked:kind=question:id=w")
		if got := s.ProgramStatus(); got.Kind != "auth" {
			t.Errorf("Kind = %q, want auth from the root", got.Kind)
		}
	})
	t.Run("the most recently updated blocked child wins", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=blocked:kind=question:id=a")
		write7501(s, "state=blocked:kind=permission:id=b")
		if got := s.ProgramStatus(); got.Kind != "permission" {
			t.Errorf("Kind = %q, want permission from b", got.Kind)
		}
		write7501(s, "state=blocked:kind=question:id=a")
		if got := s.ProgramStatus(); got.Kind != "question" || got.Seq != 2 {
			t.Errorf("Kind = %q Seq = %d, want question from a at Seq 2: a rewrite that keeps state and kind is no occurrence", got.Kind, got.Seq)
		}
	})
	t.Run("a child other than blocked does not move the root", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=idle")
		write7501(s, "state=error:id=w")
		if got := s.ProgramStatus(); got.State != ProgramStateIdle {
			t.Errorf("State = %q, want idle from the root", got.State)
		}
	})
	t.Run("no root and nothing blocked is idle", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=working:id=w:progress=9")
		if got, want := s.ProgramStatus(), (ProgramStatus{State: ProgramStateIdle, Progress: -1}); got != want {
			t.Errorf("ProgramStatus() = %+v, want %+v", got, want)
		}
	})
	t.Run("Seq stays at a clear that leaves no record", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=error")
		write7501(s, "state=clear")
		want := ProgramStatus{State: ProgramStateIdle, Progress: -1, Seq: 2}
		if got := s.ProgramStatus(); got != want {
			t.Errorf("after the clear: ProgramStatus() = %+v, want %+v", got, want)
		}
		s.Write([]byte("\x1bc"))
		if got := s.ProgramStatus(); got != want {
			t.Errorf("after RIS: ProgramStatus() = %+v, want %+v: Seq never falls", got, want)
		}
	})
	t.Run("Seq moves to the report that changed which blocked record shows", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=blocked:kind=question:id=a")
		write7501(s, "state=blocked:kind=permission:id=b")
		write7501(s, "state=clear:id=b")
		want := ProgramStatus{State: ProgramStateBlocked, Kind: "question", Progress: -1, Seq: 3}
		if got := s.ProgramStatus(); got != want {
			t.Errorf("after clearing b: ProgramStatus() = %+v, want %+v", got, want)
		}
	})
	t.Run("Seq holds when a progress update reorders blocked records", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=blocked:kind=question:id=a")
		write7501(s, "state=blocked:kind=permission:id=b")
		write7501(s, "state=blocked:kind=question:id=a:progress=5")
		want := ProgramStatus{State: ProgramStateBlocked, Kind: "question", Progress: 5, Seq: 2}
		if got := s.ProgramStatus(); got != want {
			t.Errorf("ProgramStatus() = %+v, want %+v", got, want)
		}
	})
	t.Run("app comes from the nearest ancestor that has one", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=working:app=root")
		write7501(s, "state=working:id=a:app=mid")
		write7501(s, "state=blocked:id=a/b/c")
		if got := s.ProgramStatus(); got.App != "mid" {
			t.Errorf("App = %q, want mid", got.App)
		}
	})
}

func TestOSC7501Probe(t *testing.T) {
	for _, term := range []string{"\x1b\\", "\a"} {
		t.Run(fmt.Sprintf("%q", term), func(t *testing.T) {
			s := New(24, 80)
			s.Write([]byte("\x1b]7501;?" + term))
			want := "\x1b]7501;?:states=idle,working,done,blocked,error:kinds=permission,question,auth\x1b\\"
			if got := string(s.TakeResponse()); got != want {
				t.Errorf("reply = %q, want %q", got, want)
			}
			if s.ProgramReported() || s.ProgramReportSeq() != 0 {
				t.Errorf("the query counted as a report: seen = %v seq = %d", s.ProgramReported(), s.ProgramReportSeq())
			}
		})
	}
	t.Run("an echoed reply is neither a query nor a report", func(t *testing.T) {
		s := New(24, 80)
		s.Write([]byte("\x1b]7501;?\a"))
		s.Write(s.TakeResponse())
		if got := s.TakeResponse(); len(got) != 0 {
			t.Errorf("the echoed reply was answered again with %q", got)
		}
		if s.ProgramReported() {
			t.Error("the echoed reply was accepted as a report")
		}
	})
	t.Run("a report writes nothing back", func(t *testing.T) {
		s := New(24, 80)
		write7501(s, "state=blocked:kind=question:id=a:msg="+b64("x"))
		if got := s.TakeResponse(); len(got) != 0 {
			t.Errorf("a report wrote %q back to the program", got)
		}
	})
}

func TestOSC7501SharesTheNotificationCounter(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;first\a"))
	write7501(s, "state=working")
	s.Write([]byte("\x1b]9;second\a"))
	write7501(s, "state=done")
	if s.NotificationSeq != 3 || s.ProgramReportSeq() != 4 {
		t.Errorf("NotificationSeq = %d ProgramReportSeq = %d, want 3 and 4 from one counter",
			s.NotificationSeq, s.ProgramReportSeq())
	}
	s.Write([]byte("\x1bc"))
	if got := s.ProgramReportSeq(); got != 4 {
		t.Errorf("RIS moved ProgramReportSeq 4 -> %d; it must never fall back", got)
	}
	s.Write([]byte("\x1b]9;third\a"))
	if s.NotificationSeq != 5 {
		t.Errorf("NotificationSeq after RIS = %d, want 5, above every earlier report", s.NotificationSeq)
	}
}

// TestOSC7501RecordSurvival is the record half of docs/sessions.md "Record
// lifecycle": a record of each state, at the root or below it, against each
// event that drops or replaces records. It reads the record set directly,
// because the terminal layer cannot see a child that is not blocked.
func TestOSC7501RecordSurvival(t *testing.T) {
	positions := []struct{ name, id string }{{"root", ""}, {"child", "a/b"}}
	states := []struct {
		name, body string
		rec        programRecord
	}{
		{"idle", "state=idle:app=s", programRecord{state: ProgramStateIdle, app: "s", progress: -1}},
		{"working", "state=working:app=s:progress=10", programRecord{state: ProgramStateWorking, app: "s", progress: 10}},
		{"blocked", "state=blocked:app=s:progress=10", programRecord{state: ProgramStateBlocked, app: "s", progress: 10}},
		{"blocked question", "state=blocked:kind=question:app=s", programRecord{state: ProgramStateBlocked, kind: "question", app: "s", progress: -1}},
		{"done", "state=done:app=s", programRecord{state: ProgramStateDone, app: "s", progress: -1}},
		{"error", "state=error:app=s", programRecord{state: ProgramStateError, app: "s", progress: -1}},
	}
	finished := []string{ProgramStateIdle, ProgramStateDone, ProgramStateError}
	newer := programRecord{state: ProgramStateWorking, progress: 77}
	events := []struct {
		act      func(s *Screen, id string)
		name     string
		keeps    []string // the states that survive; ignored when replaced
		replaced bool     // the record becomes newer
	}{
		{name: "OSC 133 A", keeps: finished, act: func(s *Screen, _ string) { s.Write([]byte("\x1b]133;A\x1b\\")) }},
		{name: "process exit", keeps: finished, act: func(s *Screen, _ string) { s.ProgramExited() }},
		{name: "RIS", act: func(s *Screen, _ string) { s.Write([]byte("\x1bc")) }},
		{name: "a newer report for the same id", replaced: true, act: func(s *Screen, id string) {
			body := "state=working:progress=77"
			if id != "" {
				body += ":id=" + id
			}
			write7501(s, body)
		}},
	}
	for _, pos := range positions {
		for _, st := range states {
			for _, ev := range events {
				t.Run(pos.name+" "+st.name+" "+ev.name, func(t *testing.T) {
					s := New(24, 80)
					body := st.body
					if pos.id != "" {
						body += ":id=" + pos.id
					}
					write7501(s, body)
					ev.act(s, pos.id)
					want := map[string]programRecord{}
					switch {
					case ev.replaced:
						want[pos.id] = newer
					case slices.Contains(ev.keeps, st.rec.state):
						want[pos.id] = st.rec
					}
					got := make(map[string]programRecord, len(s.program.records))
					for id, r := range s.program.records {
						r.touched = 0
						got[id] = r
					}
					if !maps.Equal(got, want) {
						t.Errorf("records = %+v, want %+v", got, want)
					}
				})
			}
		}
	}
}
