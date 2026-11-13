package downloads

import (
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// composed are this package's strings that today's components compose from pieces, so that
// the literal is not in their source although what the user reads is the same, byte for byte.
// There are none: every string the grid and the pane show is a literal of today's sources.
var composed = map[string]string{}

// reworded are this package's strings that the native window words differently from today's
// components on purpose, each with its reason (the WebView window keeps its own words).
var reworded = map[string]string{
	emptyExtension: "Settings → Integration now has Firefox's steps too (release 2026-11)",
}

// TestStringsAreToday: every user-facing string of this package is in today's components and
// modules (ui/frontend/src, tests excluded) as it stands, or composed there from the same
// pieces (composed), or reworded on purpose (reworded). A string is user-facing when it has two letters in a row and is not an
// identifier, a key, an import path or an error value (errors.New, fmt.Errorf: logged by the
// composition, never shown).
func TestStringsAreToday(t *testing.T) {
	root := filepath.Join("..", "..", "frontend", "src")
	var today strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(p, ".test.ts") || !(strings.HasSuffix(p, ".svelte") || strings.HasSuffix(p, ".ts")) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		today.Write(b)
		today.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Skipf("today's sources: %v", err)
	}
	ws := regexp.MustCompile(`\s+`)
	src := ws.ReplaceAllString(html.UnescapeString(today.String()), " ")
	ident := regexp.MustCompile(`^[a-z0-9_.\-/:]+$`)
	letters := regexp.MustCompile(`[A-Za-z]{2}`)

	files, _ := filepath.Glob("*.go")
	fs := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fs, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if _, ok := n.(*ast.ImportSpec); ok {
				return false
			}
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && (pkg.Name == "errors" && sel.Sel.Name == "New" || pkg.Name == "fmt" && sel.Sel.Name == "Errorf") {
						return false
					}
				}
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !letters.MatchString(s) {
				return true
			}
			if !strings.Contains(s, " ") && ident.MatchString(strings.ToLower(s)) && !unicode.IsUpper([]rune(s)[0]) {
				return true
			}
			checked++
			norm := strings.TrimSpace(ws.ReplaceAllString(s, " "))
			if strings.Contains(src, norm) {
				return true
			}
			if why, ok := composed[s]; ok {
				t.Logf("composed: %q (%s)", s, why)
				return true
			}
			if why, ok := reworded[s]; ok {
				t.Logf("reworded: %q (%s)", s, why)
				return true
			}
			t.Errorf("%s: %q is not in today's sources", fs.Position(lit.Pos()), s)
			return true
		})
	}
	t.Logf("%d user-facing strings checked", checked)
}
