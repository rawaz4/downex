package queues

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestStringsAreTodays: every string the package's code holds that a user can read (on screen,
// in a tooltip, or through UI Automation) is in today's QueueDialog.svelte or
// ScheduleEditor.svelte byte for byte (a string joined around a name is checked by its parts).
// The texts made by ui/core (schedule summaries, notes, actions' labels, status texts) are
// that package's, checked against queues.ts and display.ts there.
func TestStringsAreTodays(t *testing.T) {
	src := filepath.Join("..", "..", "frontend", "src", "lib", "components")
	var today strings.Builder
	for _, f := range []string{"QueueDialog.svelte", "ScheduleEditor.svelte"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if err != nil {
			t.Skipf("today's sources: %v", err)
		}
		today.Write(b)
	}
	svelte := today.String()
	// Values that are not text: icon names, the actions' option values (api.ts's PostAction).
	notText := map[string]bool{
		"add": true, "remove": true, "resume": true, "power": true, "chevron-up": true, "chevron-down": true,
		"none": true, "quit": true, "sleep": true, "hibernate": true, "shutdown": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fs := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fs, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		imports := map[*ast.BasicLit]bool{}
		for _, im := range f.Imports {
			imports[im.Path] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || imports[lit] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.ContainsFunc(s, unicode.IsLetter) || notText[s] {
				return true
			}
			part := strings.TrimSpace(s)
			checked++
			if !strings.Contains(svelte, part) {
				t.Errorf("%s: %q is not in today's QueueDialog.svelte or ScheduleEditor.svelte", fs.Position(lit.Pos()), s)
			}
			return true
		})
	}
	t.Logf("%d strings checked", checked)
}
