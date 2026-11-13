//go:build windows

package uia

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"downex/ui/win/internal/com"
	"downex/ui/win/internal/sdkheader"
)

func sdkOrSkip(t *testing.T) string {
	t.Helper()
	dir, ok := sdkheader.Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\<version>\um folder)`, dir)
	}
	return dir
}

// TestVtablesMatchHeader checks every vtable this package builds against UIAutomationCore.h:
// the interface's methods, IUnknown's first, in the header's slot order, and for each
// method as many machine words as the declaration takes (the interface pointer, one per
// parameter) as the Go function receives; and each interface's id.
func TestVtablesMatchHeader(t *testing.T) {
	sdk := sdkOrSkip(t)
	set, err := sdkheader.Parse(sdk, "UIAutomationCore.h")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for k, it := range ifaces {
		all, err := set.AllMethods(it.name)
		if err != nil {
			t.Error(err)
			continue
		}
		got := []string{"QueryInterface", "AddRef", "Release"}
		for _, m := range it.methods {
			got = append(got, m.name)
		}
		if !slices.Equal(got, all) {
			t.Errorf("%s: vtable\n%v\nheader\n%v", it.name, got, all)
			continue
		}
		for _, m := range it.methods {
			words, err := set.Arity(it.name, m.name)
			if err != nil {
				t.Errorf("%s::%s: %v", it.name, m.name, err)
				continue
			}
			ft := reflect.TypeOf(m.fn)
			if ft.Kind() != reflect.Func || ft.NumOut() != 1 || ft.Out(0).Kind() != reflect.Uintptr {
				t.Errorf("%s::%s: %v is not a callback", it.name, m.name, ft)
				continue
			}
			for i := 0; i < ft.NumIn(); i++ {
				if ft.In(i).Kind() != reflect.Uintptr {
					t.Errorf("%s::%s: argument %d is %v, not a machine word", it.name, m.name, i, ft.In(i))
				}
			}
			if ft.NumIn() != words {
				t.Errorf("%s::%s: the Go function takes %d words, the header %d", it.name, m.name, ft.NumIn(), words)
			}
			checked++
		}
		iid, ok := set.IID(it.name)
		if !ok || iid != com.GUIDString(*iids[k]) {
			t.Errorf("%s: IID %s, header %s", it.name, com.GUIDString(*iids[k]), iid)
		}
	}
	if checked < 50 {
		t.Errorf("only %d methods checked", checked)
	}
	t.Logf("%d interfaces, %d methods checked against UIAutomationCore.h", len(ifaces), checked)

	// The IID_ variables, through the bindings' own checker.
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{Dir: ".", Prefix: "I", Headers: []string{"UIAutomationCore.h"}})
	for _, err := range errs {
		t.Error(err)
	}
	if len(found) < len(ifaces) {
		t.Errorf("sdkheader.Check verified %d ids, want %d at least", len(found), len(ifaces))
	}
}

// The thunked methods take doubles: their declarations must say so, at the argument
// positions the thunks move (thunk_amd64.s).
func TestThunkedMethodsTakeDoubles(t *testing.T) {
	sdk := sdkOrSkip(t)
	raw, err := os.ReadFile(filepath.Join(sdk, "UIAutomationCore.h"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, c := range []struct{ iface, decl string }{
		{"IRawElementProviderFragmentRoot", `ElementProviderFromPoint\(\s*/\*\s*\[in\]\s*\*/\s*double x,\s*/\*\s*\[in\]\s*\*/\s*double y,`},
		{"IRangeValueProvider", `SetValue\(\s*/\*\s*\[in\]\s*\*/\s*double val\)`},
	} {
		at := strings.Index(src, c.iface+" : public IUnknown")
		if at < 0 {
			t.Fatalf("%s not found", c.iface)
		}
		if !regexp.MustCompile(c.decl).MatchString(src[at : at+2000]) {
			t.Errorf("%s: the declaration %q is not there", c.iface, c.decl)
		}
	}
	n := 0
	for _, it := range ifaces {
		for _, m := range it.methods {
			if m.thunk {
				n++
			}
		}
	}
	if n != 2 {
		t.Errorf("%d thunked methods, want 2", n)
	}
}

// TestConstantsMatchHeaders checks every constant of consts.go against the headers: the
// "const long" ids of UIAutomationClient.h, the enums of UIAutomationCore.h and the
// #defines of UIAutomationCoreApi.h.
func TestConstantsMatchHeaders(t *testing.T) {
	sdk := sdkOrSkip(t)
	want := map[string]int64{}
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(sdk, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, m := range regexp.MustCompile(`const long (\w+)\s*=\s*(-?\d+);`).FindAllStringSubmatch(read("UIAutomationClient.h"), -1) {
		v, _ := strconv.ParseInt(m[2], 10, 64)
		want[m[1]] = v
	}
	// Enum members: "Name = 0x2," or "Name = ( Prev + 1 ) ," in declaration order.
	core := read("UIAutomationCore.h")
	for _, body := range regexp.MustCompile(`(?s)\benum \w+\s*\{(.*?)\}`).FindAllStringSubmatch(core, -1) {
		prev := int64(-1)
		for _, m := range regexp.MustCompile(`(\w+)\s*=\s*([^,]+)`).FindAllStringSubmatch(body[1], -1) {
			expr := strings.TrimSpace(m[2])
			var v int64
			if p := regexp.MustCompile(`^\(\s*\w+\s*\+\s*1\s*\)$`).MatchString(expr); p {
				v = prev + 1
			} else if x, err := strconv.ParseInt(expr, 0, 64); err == nil {
				v = x
			} else {
				continue
			}
			want[m[1]] = v
			prev = v
		}
	}
	for _, m := range regexp.MustCompile(`#define (\w+)\s+(-?(?:0x[0-9A-Fa-f]+|\d+))\b`).FindAllStringSubmatch(read("UIAutomationCoreApi.h"), -1) {
		v, err := strconv.ParseInt(m[2], 0, 64)
		if err == nil {
			want[m[1]] = v
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "consts.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, id := range vs.Names {
				got, ok := literal(vs.Values[i])
				if !ok {
					t.Errorf("%s: not a literal", id.Name)
					continue
				}
				h, ok := want[id.Name]
				switch {
				case !ok:
					t.Errorf("%s is not declared in the headers read", id.Name)
				case h != got:
					t.Errorf("%s = %d, the header says %d", id.Name, got, h)
				default:
					n++
				}
			}
		}
	}
	if n < 100 {
		t.Errorf("only %d constants checked", n)
	}
	t.Logf("%d constants checked", n)
}

// literal is the value of an integer literal, perhaps negated.
func literal(e ast.Expr) (int64, bool) {
	neg := false
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.SUB {
		neg, e = true, u.X
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	v, err := strconv.ParseInt(lit.Value, 0, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}
