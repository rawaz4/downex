//go:build windows

package sdkheader

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Binding describes a binding package for Check: its Go types are the COM interfaces named
// Prefix + type name ("RenderTarget" in package d2d is "ID2D1RenderTarget"), except those in
// Names, which map a Go type to its interface explicitly.
type Binding struct {
	Dir     string            // the package's source folder
	Prefix  string            // "ID2D1", "IDWrite", "IWIC"
	Names   map[string]string // Go type -> interface, for types outside the prefix rule
	Headers []string          // the headers that declare the interfaces
}

// Finding is one thing Check verified, for the test's log.
type Finding struct {
	Where string // "RenderTarget.FillRectangle"
	What  string // "slot 17 = ID2D1RenderTarget::FillRectangle"
}

// Check reads every non-test Go file of b.Dir and verifies against the headers:
//
//   - each method whose body calls X.Method(n) uses n = the header's slot of the method of the
//     same name in the receiver's interface (and the literal is the only slot it reads), and
//     passes syscall.SyscallN(X.Method(n), ...) as many words as the header's declaration
//     takes (Set.Arity: the interface pointer, a hidden pointer for a structure returned by
//     value, one per parameter);
//   - each struct type that embeds another binding type embeds an ancestor of its interface;
//   - each package-level IID_<Interface> = com.MustGUID("...") is the interface's uuid, and
//     each CLSID_<X> or GUID_<X> = com.MustGUID("...") is DEFINE_GUID(CLSID_<X>/GUID_<X>).
//
// It returns what it verified, and the mismatches as errors.
func Check(sdk string, b Binding) ([]Finding, []error) {
	set, err := Parse(sdk, b.Headers...)
	if err != nil {
		return nil, []error{err}
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(b.Dir)
	if err != nil {
		return nil, []error{err}
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(b.Dir, n), nil, 0)
		if err != nil {
			return nil, []error{err}
		}
		files = append(files, f)
	}
	iface := func(goType string) string {
		if n, ok := b.Names[goType]; ok {
			return n
		}
		return b.Prefix + goType
	}
	var found []Finding
	var errs []error
	types := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, s := range gd.Specs {
					ts := s.(*ast.TypeSpec)
					if _, ok := ts.Type.(*ast.StructType); ok {
						types[ts.Name.Name] = true
					}
				}
			}
		}
	}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) != 1 || d.Body == nil {
					continue
				}
				recv := receiverType(d.Recv.List[0].Type)
				var slots []int
				var bad []string
				words := -1 // the words passed to SyscallN after the method's address
				ast.Inspect(d.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if isSyscallN(call) && len(call.Args) > 0 {
						if m, ok := call.Args[0].(*ast.CallExpr); ok {
							if sel, ok := m.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Method" {
								words = len(call.Args) - 1
							}
						}
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Method" || len(call.Args) != 1 {
						return true
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok || lit.Kind != token.INT {
						bad = append(bad, fmt.Sprintf("%s.%s: Method's slot is not an integer literal", recv, d.Name.Name))
						return true
					}
					v, _ := strconv.Atoi(lit.Value)
					slots = append(slots, v)
					return true
				})
				for _, m := range bad {
					errs = append(errs, fmt.Errorf("%s", m))
				}
				if len(slots) == 0 {
					continue
				}
				where := recv + "." + d.Name.Name
				if len(slots) > 1 {
					errs = append(errs, fmt.Errorf("%s reads %d slots; a wrapper calls one method", where, len(slots)))
					continue
				}
				name := iface(recv)
				want, err := set.Slot(name, d.Name.Name)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %v", where, err))
					continue
				}
				if want != slots[0] {
					errs = append(errs, fmt.Errorf("%s uses slot %d, but %s::%s is slot %d in %s", where, slots[0], name, d.Name.Name, want, fileOf(set, name)))
					continue
				}
				arity, err := set.Arity(name, d.Name.Name)
				switch {
				case err != nil:
					errs = append(errs, fmt.Errorf("%s: %v", where, err))
					continue
				case words < 0:
					errs = append(errs, fmt.Errorf("%s does not call syscall.SyscallN(X.Method(%d), ...): its arguments cannot be counted", where, want))
					continue
				case words != arity:
					errs = append(errs, fmt.Errorf("%s passes %d words, but %s::%s takes %d (the interface pointer, a hidden return pointer for a structure, one per parameter)",
						where, words, name, d.Name.Name, arity))
					continue
				}
				found = append(found, Finding{where, fmt.Sprintf("slot %d = %s::%s, %d words", want, name, d.Name.Name, arity)})
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, s := range d.Specs {
						ts := s.(*ast.TypeSpec)
						st, ok := ts.Type.(*ast.StructType)
						if !ok {
							continue
						}
						for _, fld := range st.Fields.List {
							if len(fld.Names) != 0 {
								continue
							}
							base := embeddedName(fld.Type)
							if base == "" {
								continue
							}
							var baseIface string
							switch {
							case base == "com.Object":
								baseIface = "IUnknown"
							case types[base]:
								baseIface = iface(base)
							default:
								continue
							}
							name := iface(ts.Name.Name)
							chain, err := set.Chain(name)
							if err != nil {
								errs = append(errs, fmt.Errorf("type %s: %v", ts.Name.Name, err))
								continue
							}
							ok := false
							for _, it := range chain[:len(chain)-1] {
								if it.Name == baseIface {
									ok = true
								}
							}
							if !ok {
								errs = append(errs, fmt.Errorf("type %s embeds %s, but %s is not a base of %s", ts.Name.Name, base, baseIface, name))
								continue
							}
							found = append(found, Finding{"type " + ts.Name.Name, fmt.Sprintf("%s derives from %s", name, baseIface)})
						}
					}
				case token.VAR:
					for _, s := range d.Specs {
						vs := s.(*ast.ValueSpec)
						for i, id := range vs.Names {
							if i >= len(vs.Values) {
								continue
							}
							lit := mustGUIDArg(vs.Values[i])
							if lit == "" {
								continue
							}
							got := strings.ToLower(lit)
							var want string
							var ok bool
							switch {
							case strings.HasPrefix(id.Name, "IID_"):
								want, ok = set.IID(strings.TrimPrefix(id.Name, "IID_"))
							case strings.HasPrefix(id.Name, "CLSID_"), strings.HasPrefix(id.Name, "GUID_"):
								want, ok = set.GUID(id.Name)
							default:
								errs = append(errs, fmt.Errorf("var %s: a GUID's name starts with IID_, CLSID_ or GUID_", id.Name))
								continue
							}
							if !ok {
								errs = append(errs, fmt.Errorf("var %s: not declared in the headers read", id.Name))
								continue
							}
							if got != want {
								errs = append(errs, fmt.Errorf("var %s = %s, headers say %s", id.Name, got, want))
								continue
							}
							found = append(found, Finding{"var " + id.Name, want})
						}
					}
				}
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Where < found[j].Where })
	return found, errs
}

// isSyscallN reports a call of syscall.SyscallN.
func isSyscallN(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "SyscallN" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "syscall"
}

func receiverType(e ast.Expr) string {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func embeddedName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			return x.Name + "." + t.Sel.Name
		}
	}
	return ""
}

// mustGUIDArg is the string of a com.MustGUID("...") call, or "".
func mustGUIDArg(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "MustGUID" {
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "MustGUID" {
			return ""
		}
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

func fileOf(s *Set, name string) string {
	if it, ok := s.Interface(name); ok {
		return it.File
	}
	return "?"
}
