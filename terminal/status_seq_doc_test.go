package terminal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// statusSeqDoc returns SessionInfo.StatusSeq's doc comment as one line.
func statusSeqDoc(t *testing.T) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "session_manager.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse session_manager.go: %v", err)
	}
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "SessionInfo" {
			return true
		}
		for _, field := range ts.Type.(*ast.StructType).Fields.List {
			if len(field.Names) == 1 && field.Names[0].Name == "StatusSeq" {
				doc = field.Doc.Text()
			}
		}
		return false
	})
	if doc == "" {
		t.Fatal("SessionInfo.StatusSeq has no doc comment")
	}
	return strings.Join(strings.Fields(doc), " ")
}

// tsDocWords returns src's words with each line's leading TSDoc "*" removed.
func tsDocWords(src string) string {
	var words []string
	for line := range strings.SplitSeq(src, "\n") {
		words = append(words, strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "*"))...)
	}
	return strings.Join(words, " ")
}

// The StatusSeq rule has one owner, SessionInfo.StatusSeq's doc, and the
// other copies quote it whole.
func TestStatusSeqDocIsQuotedWordForWord(t *testing.T) {
	rule, ok := strings.CutPrefix(statusSeqDoc(t), "StatusSeq ")
	if !ok {
		t.Fatal(`SessionInfo.StatusSeq's doc does not start with "StatusSeq "`)
	}
	// A flag, not a func field: a func-valued field here makes golangci-lint's
	// gosec taint analysis report G703 at os.ReadFile calls in other files.
	copies := []struct {
		path, lead string
		ts         bool
	}{
		{"../docs/sessions.md", "- `statusSeq` ", false},
		{"../web/src/status-stream.ts", "/** ", true},
	}
	for _, c := range copies {
		src, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		words := strings.Join(strings.Fields(string(src)), " ")
		if c.ts {
			words = tsDocWords(string(src))
		}
		if !strings.Contains(words, c.lead+rule) {
			t.Errorf("%s does not quote SessionInfo.StatusSeq's doc after %q. Want:\n%s", c.path, c.lead, rule)
		}
	}
}
