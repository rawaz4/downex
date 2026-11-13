//go:build windows

// Package sdkheader reads COM interface declarations out of the Windows SDK's C++ headers
// (d2d1.h, dwrite.h, wincodec.h, objidl.h and their siblings), so that tests can check the
// hand-written vtable slots and GUIDs of ui/win's bindings against Microsoft's own
// declarations. It is used by tests only; nothing in the product imports it.
//
// Two declaration styles are read: the DirectX headers' DX_DECLARE_INTERFACE /
// DWRITE_DECLARE_INTERFACE with STDMETHOD(...) PURE methods, and MIDL's MIDL_INTERFACE with
// "virtual ... STDMETHODCALLTYPE Name(...) = 0" methods. Non-virtual inline overloads (the
// headers' COM_DECLSPEC_NOTHROW helpers) are not vtable entries and are skipped. MSVC puts
// virtual overloads of one name declared in one interface together, in reverse order, at the
// first one's place; such names are reported as overloaded and have no single slot.
package sdkheader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultDir is the SDK include folder the bindings were written against.
const DefaultDir = `C:\Program Files (x86)\Windows Kits\10\Include\10.0.26100.0\um`

// Dir is the SDK's um include folder: $DOWNEX_SDK_INCLUDE when set, else DefaultDir. It
// reports false when the folder does not exist, so tests can skip with a clear message.
func Dir() (string, bool) {
	dir := os.Getenv("DOWNEX_SDK_INCLUDE")
	if dir == "" {
		dir = DefaultDir
	}
	st, err := os.Stat(dir)
	return dir, err == nil && st.IsDir()
}

// Interface is one COM interface as a header declares it.
type Interface struct {
	Name string
	Base string // "" for IUnknown
	IID  string // lower case, as in the header's uuid string
	File string
	// Methods are the interface's own vtable entries in slot order, after IUnknown's or
	// its base's.
	Methods []string
	// Overloaded holds the names declared more than once in this interface.
	Overloaded map[string]bool
	// Conditional reports a preprocessor directive inside the declaration: its method list
	// may depend on macros, so Slot refuses it.
	Conditional bool
	// Params is the number of parameters each method declares, without the interface
	// pointer; Returns is its return type as written ("HRESULT", "D2D1_SIZE_F").
	Params  map[string]int
	Returns map[string]string
}

// Set is the interfaces of some headers by name, with IUnknown built in.
type Set struct {
	ifaces map[string]*Interface
	guids  map[string]string // DEFINE_GUID name -> lower-case guid string
	enums  map[string]bool   // the enum types the headers declare
}

var (
	reDX   = regexp.MustCompile(`interface\s+(?:DX_DECLARE_INTERFACE|DWRITE_DECLARE_INTERFACE)\s*\(\s*"([0-9A-Fa-f-]{36})"\s*\)\s*(\w+)\s*:\s*public\s+(\w+)\s*\{`)
	reMIDL = regexp.MustCompile(`MIDL_INTERFACE\s*\(\s*"([0-9A-Fa-f-]{36})"\s*\)\s*(\w+)\s*:\s*public\s+(\w+)\s*\{`)
	reSTD  = regexp.MustCompile(`\bSTDMETHOD\s*\(\s*(\w+)\s*\)|\bSTDMETHOD_\s*\(\s*[^,()]+?\s*,\s*(\w+)\s*\)`)
	reVirt = regexp.MustCompile(`\bvirtual\s+[^;{}()]*?\bSTDMETHODCALLTYPE\s+(\w+)\s*\(`)
	reGUID = regexp.MustCompile(`DEFINE_GUID\s*\(\s*(\w+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,` +
		`\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,` +
		`\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*,\s*0x([0-9A-Fa-f]+)\s*\)`)
	reCVtbl = regexp.MustCompile(`typedef\s+struct\s+(\w+)Vtbl\s*\{`)
	reCMeth = regexp.MustCompile(`\(\s*STDMETHODCALLTYPE\s*\*\s*(\w+)\s*\)\s*\(`)
	reEnum  = regexp.MustCompile(`\benum\s+(?:class\s+)?(\w+)`)
	reRet   = regexp.MustCompile(`\bvirtual\s+(.*?)\s*\bSTDMETHODCALLTYPE\b`)
)

// Parse reads the named headers of dir.
func Parse(dir string, files ...string) (*Set, error) {
	s := &Set{
		ifaces: map[string]*Interface{
			"IUnknown": {Name: "IUnknown", IID: "00000000-0000-0000-c000-000000000046", File: "unknwn.h",
				Methods: []string{"QueryInterface", "AddRef", "Release"},
				Params:  map[string]int{"QueryInterface": 2, "AddRef": 0, "Release": 0},
				Returns: map[string]string{"QueryInterface": "HRESULT", "AddRef": "ULONG", "Release": "ULONG"}},
		},
		guids: map[string]string{},
		enums: map[string]bool{},
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return nil, err
		}
		if err := s.parseFile(f, stripComments(string(raw))); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	return s, nil
}

func (s *Set) parseFile(name, src string) error {
	for _, re := range []*regexp.Regexp{reDX, reMIDL} {
		for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
			iid, iname, base := src[m[2]:m[3]], src[m[4]:m[5]], src[m[6]:m[7]]
			open := m[1] - 1 // the '{'
			end, err := matchBrace(src, open)
			if err != nil {
				return fmt.Errorf("interface %s: %w", iname, err)
			}
			body := src[open+1 : end]
			it := &Interface{Name: iname, Base: base, IID: strings.ToLower(iid), File: name,
				Params: map[string]int{}, Returns: map[string]string{}}
			it.Conditional = hasDirective(body)
			var decl []string
			for _, mm := range reSTD.FindAllStringSubmatchIndex(body, -1) {
				n, ret := "", "HRESULT"
				if mm[2] >= 0 {
					n = body[mm[2]:mm[3]]
				} else {
					n = body[mm[4]:mm[5]]
					// STDMETHOD_(type, Name): the type is what lies between the '(' and the ','.
					m := body[mm[0]:mm[1]]
					ret = strings.TrimSpace(m[strings.Index(m, "(")+1 : strings.LastIndex(m, ",")])
				}
				decl = append(decl, n)
				it.Params[n] = paramCount(body, mm[1])
				it.Returns[n] = ret
			}
			for _, mm := range reVirt.FindAllStringSubmatchIndex(body, -1) {
				n := body[mm[2]:mm[3]]
				decl = append(decl, n)
				it.Params[n] = paramCount(body, mm[1]-1)
				ret := "HRESULT"
				if r := reRet.FindStringSubmatch(body[mm[0]:mm[1]]); r != nil {
					ret = strings.TrimSpace(r[1])
				}
				it.Returns[n] = ret
			}
			it.Methods, it.Overloaded = msvcOrder(decl)
			if prev, ok := s.ifaces[iname]; ok && prev.File != name {
				return fmt.Errorf("interface %s declared in %s and %s", iname, prev.File, name)
			}
			s.ifaces[iname] = it
		}
	}
	for _, m := range reGUID.FindAllStringSubmatch(src, -1) {
		s.guids[m[1]] = guidString(m[2:])
	}
	for _, m := range reEnum.FindAllStringSubmatch(src, -1) {
		s.enums[m[1]] = true
	}
	return nil
}

// paramCount is the number of parameters in the list whose '(' is at or after at in src:
// the top-level commas plus one, none for "()", "(void)" and the DirectX headers' "(THIS)"
// ("THIS_" before a list is the interface pointer, which is not counted).
func paramCount(src string, at int) int {
	open := strings.IndexByte(src[at:], '(')
	if open < 0 {
		return -1
	}
	open += at
	depth, n, empty := 0, 1, true
	for i := open; i < len(src); i++ {
		switch c := src[i]; c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				list := strings.TrimSpace(src[open+1 : i])
				list = strings.TrimSpace(strings.TrimPrefix(list, "THIS_"))
				if empty || list == "" || list == "void" || list == "THIS" {
					return 0
				}
				return n
			}
		case ',':
			if depth == 1 {
				n++
			}
		default:
			if depth == 1 && c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				empty = false
			}
		}
	}
	return -1
}

// scalarTypes are the return types that come back in RAX (or XMM0); any other value type
// is a structure, which an MSVC member function returns through a hidden pointer.
var scalarTypes = map[string]bool{
	"void": true, "HRESULT": true, "BOOL": true, "ULONG": true, "UINT": true, "UINT32": true, "INT32": true,
	"UINT64": true, "INT": true, "DWORD": true, "FLOAT": true, "float": true, "WCHAR": true, "BYTE": true,
	"UINT16": true, "INT16": true, "LONG": true, "BOOLEAN": true, "D2D1_TAG": true,
}

// Arity is the number of machine words a call of method through iface passes after the
// function: the interface pointer, a hidden pointer for a structure returned by value
// (MSVC's x64 rule for member functions), and one per parameter (a structure larger than
// 8 bytes passed by value travels as a pointer, one word too).
func (s *Set) Arity(iface, method string) (int, error) {
	chain, err := s.Chain(iface)
	if err != nil {
		return 0, err
	}
	var in *Interface
	for _, it := range chain {
		if _, ok := it.Params[method]; ok {
			in = it
		}
	}
	if in == nil {
		return 0, fmt.Errorf("%s has no method %s", iface, method)
	}
	n := in.Params[method]
	if n < 0 {
		return 0, fmt.Errorf("%s::%s: its parameter list could not be read", in.Name, method)
	}
	words := 1 + n
	ret := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Returns[method]), "CONST "))
	if !scalarTypes[ret] && !s.enums[ret] && !strings.Contains(ret, "*") {
		words++
	}
	return words, nil
}

// msvcOrder is the vtable order MSVC gives the virtual methods declared in one class:
// declaration order, except that overloads of one name sit together at the first one's
// place, the last declared first.
func msvcOrder(decl []string) ([]string, map[string]bool) {
	count := map[string]int{}
	for _, n := range decl {
		count[n]++
	}
	over := map[string]bool{}
	var out []string
	done := map[string]bool{}
	for _, n := range decl {
		if count[n] == 1 {
			out = append(out, n)
			continue
		}
		over[n] = true
		if done[n] {
			continue
		}
		done[n] = true
		for i := 0; i < count[n]; i++ {
			out = append(out, n)
		}
	}
	return out, over
}

// Interface is the named interface, or false.
func (s *Set) Interface(name string) (*Interface, bool) {
	it, ok := s.ifaces[name]
	return it, ok
}

// Chain is the interface and its bases from IUnknown down, or an error naming the missing
// declaration.
func (s *Set) Chain(name string) ([]*Interface, error) {
	var chain []*Interface
	for n := name; n != ""; {
		it, ok := s.ifaces[n]
		if !ok {
			return nil, fmt.Errorf("interface %s is not declared in the headers read (base of %s)", n, name)
		}
		chain = append([]*Interface{it}, chain...)
		if len(chain) > 64 {
			return nil, fmt.Errorf("interface %s: inheritance loop", name)
		}
		n = it.Base
	}
	return chain, nil
}

// AllMethods is every vtable entry of the interface, IUnknown's first.
func (s *Set) AllMethods(name string) ([]string, error) {
	chain, err := s.Chain(name)
	if err != nil {
		return nil, err
	}
	var all []string
	for _, it := range chain {
		all = append(all, it.Methods...)
	}
	return all, nil
}

// ErrOverloaded: the method has several overloads in its interface, so its name alone does
// not give a slot.
var ErrOverloaded = errors.New("overloaded method")

// Slot is the vtable index of method as iface sees it: the slot of the method in the most
// derived interface of iface's chain that declares it.
func (s *Set) Slot(iface, method string) (int, error) {
	chain, err := s.Chain(iface)
	if err != nil {
		return 0, err
	}
	base := 0
	found := -1
	var in *Interface
	for _, it := range chain {
		for i, m := range it.Methods {
			if m == method {
				found, in = base+i, it
			}
		}
		base += len(it.Methods)
	}
	switch {
	case found < 0:
		return 0, fmt.Errorf("%s has no method %s", iface, method)
	case in.Overloaded[method]:
		return 0, fmt.Errorf("%s::%s: %w", in.Name, method, ErrOverloaded)
	case in.Conditional:
		return 0, fmt.Errorf("%s (%s) has preprocessor directives inside its declaration", in.Name, in.File)
	}
	// A conditional base would shift every later slot.
	for _, it := range chain {
		if it == in {
			break
		}
		if it.Conditional {
			return 0, fmt.Errorf("%s, a base of %s, has preprocessor directives inside its declaration", it.Name, iface)
		}
	}
	return found, nil
}

// IID is the interface's uuid, lower case.
func (s *Set) IID(iface string) (string, bool) {
	it, ok := s.ifaces[iface]
	if !ok {
		return "", false
	}
	return it.IID, true
}

// GUID is the value of a DEFINE_GUID name (a CLSID or a pixel format), lower case.
func (s *Set) GUID(name string) (string, bool) {
	g, ok := s.guids[name]
	return g, ok
}

// CVtable is the method list of the C declaration "typedef struct <name>Vtbl" that MIDL
// headers carry next to the C++ one, for checking the parser against it.
func CVtable(dir, file, name string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return nil, err
	}
	src := stripComments(string(raw))
	for _, m := range reCVtbl.FindAllStringSubmatchIndex(src, -1) {
		if src[m[2]:m[3]] != name {
			continue
		}
		end, err := matchBrace(src, m[1]-1)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, mm := range reCMeth.FindAllStringSubmatch(src[m[1]:end], -1) {
			out = append(out, mm[1])
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: no C vtable for %s", file, name)
}

// stripComments blanks // and /* */ comments, keeping string literals and line breaks.
func stripComments(src string) string {
	b := []byte(src)
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '"':
			for i++; i < len(b) && b[i] != '"' && b[i] != '\n'; i++ {
				if b[i] == '\\' {
					i++
				}
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			b[i], b[i+1] = ' ', ' '
			for i += 2; i < len(b); i++ {
				if b[i] == '*' && i+1 < len(b) && b[i+1] == '/' {
					b[i], b[i+1] = ' ', ' '
					i++
					break
				}
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return string(b)
}

// matchBrace is the index of the '}' that closes the '{' at open.
func matchBrace(src string, open int) (int, error) {
	if open < 0 || open >= len(src) || src[open] != '{' {
		return 0, errors.New("no opening brace")
	}
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, errors.New("unbalanced braces")
}

func hasDirective(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			return true
		}
	}
	return false
}

// guidString formats DEFINE_GUID's eleven hex parts as a uuid string.
func guidString(p []string) string {
	n := make([]uint64, len(p))
	for i, s := range p {
		n[i], _ = strconv.ParseUint(s, 16, 64)
	}
	return fmt.Sprintf("%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		n[0], n[1], n[2], n[3], n[4], n[5], n[6], n[7], n[8], n[9], n[10])
}
