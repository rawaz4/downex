package frontend

import (
	"bytes"
	"fmt"
	"go/format"
	"slices"
	"strconv"
	"strings"
)

// Kind is what a token's value is.
type Kind int

// The token kinds.
const (
	KindColor    Kind = iota + 1 // a colour: a Theme field
	KindShadows                  // a box-shadow list: a Theme field
	KindLength                   // px or rem
	KindDuration                 // ms
	KindEasing                   // cubic-bezier()
	KindFont                     // a font-family list
	KindNumber                   // a bare number (a font weight)
)

// Token is one custom property of app.css's :root.
type Token struct {
	Name string // "--chrome"
	Kind Kind
	// Light and Dark are the values as written (Dark is "" when the dark theme does not
	// set it, Light when only the dark theme does).
	Light, Dark string
	Line        int
}

// Override is a :root value that applies under a condition: a width media query, or the
// compact density.
type Override struct {
	Token  string
	Query  string // the @media query, "" for the density
	Length Length
}

// Tokens are app.css's tokens.
type Tokens struct {
	List       []*Token
	byName     map[string]*Token
	Media      []Override
	Compact    []Override
	Forced     []Rule
	SchemeDark bool // the dark blocks set color-scheme: dark
}

// The markers of app.css's legacy block, which the native window does not mirror.
const (
	legacyStart = "---- legacy aliases: remove after the redesign ----"
	legacyEnd   = "---- end of the legacy aliases ----"
)

// The selectors of the theme blocks.
const (
	selRoot        = ":root"
	selDarkSystem  = ":root:not([data-theme='light'])"
	selDarkManual  = ":root[data-theme='dark']"
	selCompact     = ":root[data-density='compact']"
	mediaDark      = "(prefers-color-scheme: dark)"
	mediaForced    = "(forced-colors: active)"
	mediaNoMotion  = "(prefers-reduced-motion: reduce)"
	mediaWidthPart = "-width:"
)

// cutLegacy blanks app.css's legacy block (keeping its newlines), or fails when only one of
// its markers is there.
func cutLegacy(src string) (string, error) {
	start := strings.Index(src, legacyStart)
	end := strings.Index(src, legacyEnd)
	switch {
	case start < 0 && end < 0:
		return src, nil
	case start < 0 || end < 0 || end < start:
		return "", fmt.Errorf("app.css: the legacy block's markers %q and %q do not pair up", legacyStart, legacyEnd)
	}
	// From the line of the start marker to the end of the end marker's line.
	from := strings.LastIndexByte(src[:start], '\n') + 1
	to := end + strings.IndexByte(src[end:], '\n')
	b := []byte(src)
	for i := from; i < to; i++ {
		if b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b), nil
}

// ReadTokens reads app.css's tokens: the :root values, the dark theme (whose two copies,
// for the system's preference and for the manual choice, must agree), the width media
// overrides, the compact density, and the forced-colours rules.
func ReadTokens(appCSS string) (*Tokens, error) {
	src, err := cutLegacy(appCSS)
	if err != nil {
		return nil, err
	}
	if src, err = StripComments(src); err != nil {
		return nil, fmt.Errorf("app.css: %w", err)
	}
	rules, err := ParseCSS(src)
	if err != nil {
		return nil, fmt.Errorf("app.css: %w", err)
	}
	t := &Tokens{byName: map[string]*Token{}}
	var darkSystem, darkManual []Decl
	var darkSystemSeen, darkManualSeen bool
	for _, r := range rules {
		root := len(r.Selectors) == 1
		switch {
		case r.Media == "" && root && r.Selectors[0] == selRoot:
			for _, d := range r.Decls {
				if !strings.HasPrefix(d.Prop, "--") {
					continue // color-scheme: light
				}
				if t.byName[d.Prop] != nil {
					return nil, fmt.Errorf("app.css:%d: %s set twice in :root", d.Line, d.Prop)
				}
				tok := &Token{Name: d.Prop, Light: d.Value, Line: d.Line}
				t.List = append(t.List, tok)
				t.byName[d.Prop] = tok
			}
		case r.Media == mediaDark && root && r.Selectors[0] == selDarkSystem:
			darkSystem, darkSystemSeen = append(darkSystem, r.Decls...), true
		case r.Media == "" && root && r.Selectors[0] == selDarkManual:
			darkManual, darkManualSeen = append(darkManual, r.Decls...), true
		case r.Media == "" && root && r.Selectors[0] == selCompact:
			for _, d := range r.Decls {
				l, err := ParseLength(d.Value)
				if err != nil {
					return nil, fmt.Errorf("app.css:%d: compact %s: %w", d.Line, d.Prop, err)
				}
				t.Compact = append(t.Compact, Override{Token: d.Prop, Length: l})
			}
		case strings.Contains(r.Media, mediaWidthPart) && root && r.Selectors[0] == selRoot:
			for _, d := range r.Decls {
				l, err := ParseLength(d.Value)
				if err != nil {
					return nil, fmt.Errorf("app.css:%d: %s under @media %s: %w", d.Line, d.Prop, r.Media, err)
				}
				t.Media = append(t.Media, Override{Token: d.Prop, Query: r.Media, Length: l})
			}
		case r.Media == mediaForced:
			t.Forced = append(t.Forced, r)
		case slices.ContainsFunc(r.Selectors, func(s string) bool {
			return strings.HasPrefix(s, ":root") && !strings.ContainsAny(s, " >+~")
		}) && slices.ContainsFunc(r.Decls, func(d Decl) bool { return strings.HasPrefix(d.Prop, "--") }):
			// A block that sets tokens under a condition the generator does not know.
			return nil, fmt.Errorf("app.css:%d: tokens under a condition the generator does not know: @media %q %s", r.Line, r.Media, strings.Join(r.Selectors, ", "))
		}
	}
	if len(t.List) == 0 {
		return nil, fmt.Errorf("app.css: no :root tokens")
	}
	if !darkSystemSeen || !darkManualSeen {
		return nil, fmt.Errorf("app.css: the dark theme needs both %s under %s and %s", selDarkSystem, mediaDark, selDarkManual)
	}
	if len(darkSystem) != len(darkManual) {
		return nil, fmt.Errorf("app.css: the two dark blocks differ: %d and %d declarations", len(darkSystem), len(darkManual))
	}
	for i, d := range darkSystem {
		m := darkManual[i]
		if d.Prop != m.Prop || d.Value != m.Value {
			return nil, fmt.Errorf("app.css:%d and :%d: the two dark blocks differ: %s: %s against %s: %s", d.Line, m.Line, d.Prop, d.Value, m.Prop, m.Value)
		}
		if d.Prop == "color-scheme" {
			t.SchemeDark = d.Value == "dark"
			continue
		}
		if !strings.HasPrefix(d.Prop, "--") {
			return nil, fmt.Errorf("app.css:%d: %s in a dark block", d.Line, d.Prop)
		}
		tok := t.byName[d.Prop]
		if tok == nil { // only the dark theme sets it (--brand-ring)
			tok = &Token{Name: d.Prop, Line: d.Line}
			t.List = append(t.List, tok)
			t.byName[d.Prop] = tok
		}
		tok.Dark = d.Value
	}
	for _, o := range append(append([]Override{}, t.Media...), t.Compact...) {
		tok := t.byName[o.Token]
		if tok == nil {
			return nil, fmt.Errorf("app.css: override of unknown token %s", o.Token)
		}
	}
	for _, tok := range t.List {
		if err := t.classify(tok); err != nil {
			return nil, fmt.Errorf("app.css:%d: %s: %w", tok.Line, tok.Name, err)
		}
	}
	return t, nil
}

// resolve follows var() references in theme dark (or light) to a value.
func (t *Tokens) resolve(v string, dark bool, seen int) (string, error) {
	if seen > 16 {
		return "", fmt.Errorf("var() loop")
	}
	name, fallback, ok := VarRef(v)
	if !ok {
		return v, nil
	}
	tok := t.byName[name]
	if tok == nil {
		if fallback != "" {
			return t.resolve(fallback, dark, seen+1)
		}
		return "", fmt.Errorf("var(%s) of an unknown token", name)
	}
	next := tok.Light
	if dark && tok.Dark != "" {
		next = tok.Dark
	}
	if next == "" {
		if fallback != "" {
			return t.resolve(fallback, dark, seen+1)
		}
		return "", nil // set only in the other theme
	}
	return t.resolve(next, dark, seen+1)
}

func (t *Tokens) classify(tok *Token) error {
	v := tok.Light
	dark := false
	if v == "" {
		v, dark = tok.Dark, true
	}
	r, err := t.resolve(v, dark, 0)
	if err != nil {
		return err
	}
	switch {
	case r == "none" || strings.Contains(r, "px ") && (strings.Contains(r, "rgb") || strings.Contains(r, "#")):
		tok.Kind = KindShadows
	case strings.HasPrefix(r, "#") || strings.HasPrefix(r, "rgb") || r == "transparent":
		tok.Kind = KindColor
	case strings.HasPrefix(r, "cubic-bezier("):
		tok.Kind = KindEasing
	case strings.HasSuffix(r, "ms"):
		tok.Kind = KindDuration
	case strings.HasSuffix(r, "rem") || strings.HasSuffix(r, "px"):
		tok.Kind = KindLength
	case strings.Contains(r, ",") || strings.ContainsAny(r, "'\""):
		tok.Kind = KindFont
	default:
		if _, err := strconv.ParseFloat(r, 64); err != nil {
			return fmt.Errorf("value %q of no known kind", r)
		}
		tok.Kind = KindNumber
	}
	if tok.Dark != "" && tok.Kind != KindColor && tok.Kind != KindShadows {
		return fmt.Errorf("a %v token that the dark theme changes; only colours and shadows may", tok.Kind)
	}
	// Both themes must parse as the kind.
	for _, dark := range []bool{false, true} {
		if _, err := t.value(tok, dark); err != nil {
			return err
		}
	}
	return nil
}

func (k Kind) String() string {
	return [...]string{"?", "colour", "shadow", "length", "duration", "easing", "font", "number"}[k]
}

// value is tok's value in a theme as Go source.
func (t *Tokens) value(tok *Token, dark bool) (string, error) {
	raw := tok.Light
	if dark && tok.Dark != "" {
		raw = tok.Dark
	}
	r, err := t.resolve(raw, dark, 0)
	if err != nil {
		return "", err
	}
	switch tok.Kind {
	case KindColor:
		if r == "" {
			return "kit.Color{}", nil
		}
		c, err := ParseColor(r)
		if err != nil {
			return "", err
		}
		return goColor(c), nil
	case KindShadows:
		if r == "" {
			return "nil", nil
		}
		list, err := ParseShadows(r)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "nil", nil
		}
		var b strings.Builder
		b.WriteString("[]kit.Shadow{")
		for i, s := range list {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("{")
			var parts []string
			if s.X != 0 || s.Y != 0 {
				parts = append(parts, fmt.Sprintf("Offset: kit.Pt(%s, %s)", num(s.X), num(s.Y)))
			}
			if s.Blur != 0 {
				parts = append(parts, "Blur: "+num(s.Blur))
			}
			if s.Spread != 0 {
				parts = append(parts, "Spread: "+num(s.Spread))
			}
			parts = append(parts, "Color: "+goColor(s.Color))
			if s.Inset {
				parts = append(parts, "Inset: true")
			}
			b.WriteString(strings.Join(parts, ", "))
			b.WriteString("}")
		}
		b.WriteString("}")
		return b.String(), nil
	case KindLength:
		l, err := ParseLength(r)
		if err != nil {
			return "", err
		}
		return goLength(l), nil
	case KindDuration:
		ms, err := ParseDuration(r)
		if err != nil {
			return "", err
		}
		if ms != float64(int64(ms)) {
			return fmt.Sprintf("time.Duration(%s * float64(time.Millisecond))", num(ms)), nil
		}
		return fmt.Sprintf("%d * time.Millisecond", int64(ms)), nil
	case KindEasing:
		b, err := ParseCubicBezier(r)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Bezier{%s, %s, %s, %s}", num(b[0]), num(b[1]), num(b[2]), num(b[3])), nil
	case KindFont:
		return strconv.Quote(r), nil
	case KindNumber:
		v, err := strconv.ParseFloat(r, 64)
		if err != nil {
			return "", err
		}
		return num(v), nil
	}
	return "", fmt.Errorf("unknown kind")
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func goColor(c Color) string {
	switch {
	case c.A == 0 && c.Hex == -1 && c.Bytes == [3]uint8{}:
		return "kit.Color{}"
	case c.Hex >= 0:
		return fmt.Sprintf("kit.RGB(0x%06x)", c.Hex)
	}
	return fmt.Sprintf("kit.RGBA(%d, %d, %d, %s)", c.Bytes[0], c.Bytes[1], c.Bytes[2], num(c.A))
}

func goLength(l Length) string {
	if l.Unit == "rem" {
		return fmt.Sprintf("kit.Rem(%s)", num(l.V))
	}
	return fmt.Sprintf("kit.Px(%s)", num(l.V))
}

// goQuery turns a width media query, "(min-width: 721px) and (max-width: 999px)", into a
// kit.Query literal.
func goQuery(q string) (string, error) {
	var parts []string
	for _, cond := range strings.Split(q, " and ") {
		cond = strings.TrimSpace(cond)
		in, ok := strings.CutPrefix(cond, "(")
		if !ok || !strings.HasSuffix(in, ")") {
			return "", fmt.Errorf("media condition %q", cond)
		}
		prop, val, ok := strings.Cut(strings.TrimSuffix(in, ")"), ":")
		if !ok {
			return "", fmt.Errorf("media condition %q", cond)
		}
		l, err := ParseLength(strings.TrimSpace(val))
		if err != nil {
			return "", err
		}
		switch strings.TrimSpace(prop) {
		case "min-width":
			parts = append(parts, "MinWidth: "+goLength(l))
		case "max-width":
			parts = append(parts, "MaxWidth: "+goLength(l))
		default:
			return "", fmt.Errorf("media feature %q", prop)
		}
	}
	return "kit.Query{" + strings.Join(parts, ", ") + "}", nil
}

// Generate writes the tokens package's generated file.
func (t *Tokens) Generate() ([]byte, error) {
	var b bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	p("// Code generated by go generate (ui/kit/internal/gen from ui/frontend/src/app.css); DO NOT EDIT.\n\n")
	p("package tokens\n\nimport (\n\t\"time\"\n\n\t\"downex/ui/kit\"\n)\n\n")
	p("var _ = time.Millisecond\n\n")

	p("// Theme is app.css's colours and shadows in one theme: Light, Dark, or Forced.\n")
	p("type Theme struct {\n")
	p("\t// Dark is the theme's color-scheme.\n\tDark bool\n")
	p("\t// forced marks the forced-colours theme (Forced).\n\tforced bool\n\n")
	for _, tok := range t.List {
		if tok.Kind != KindColor && tok.Kind != KindShadows {
			continue
		}
		typ := "kit.Color"
		if tok.Kind == KindShadows {
			typ = "[]kit.Shadow"
		}
		p("\t%s %s // %s\n", GoName(tok.Name), typ, tok.Name)
	}
	p("}\n\n")

	for _, dark := range []bool{false, true} {
		name, doc := "Light", "app.css's :root values, the default theme."
		if dark {
			name, doc = "Dark", "app.css's dark theme (prefers-color-scheme: dark, or data-theme='dark')."
		}
		p("// %s is %s\n", name, doc)
		p("var %s = Theme{\n", name)
		if dark && t.SchemeDark {
			p("\tDark: true,\n")
		}
		for _, tok := range t.List {
			if tok.Kind != KindColor && tok.Kind != KindShadows {
				continue
			}
			v, err := t.value(tok, dark)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", tok.Name, err)
			}
			if v == "nil" || v == "kit.Color{}" {
				continue
			}
			p("\t%s: %s,\n", GoName(tok.Name), v)
		}
		p("}\n\n")
	}

	p("// colorTokens and shadowTokens name every colour and shadow field by its token.\n")
	p("var colorTokens = [...]struct {\n\tname  string\n\tfield func(*Theme) *kit.Color\n}{\n")
	for _, tok := range t.List {
		if tok.Kind == KindColor {
			p("\t{%q, func(t *Theme) *kit.Color { return &t.%s }},\n", tok.Name, GoName(tok.Name))
		}
	}
	p("}\n\n")
	p("var shadowTokens = [...]struct {\n\tname  string\n\tfield func(*Theme) *[]kit.Shadow\n}{\n")
	for _, tok := range t.List {
		if tok.Kind == KindShadows {
			p("\t{%q, func(t *Theme) *[]kit.Shadow { return &t.%s }},\n", tok.Name, GoName(tok.Name))
		}
	}
	p("}\n\n")

	groups := []struct {
		kind       Kind
		decl, what string
	}{
		{KindFont, "const", "The font-family lists."},
		{KindNumber, "const", "The numbers."},
		{KindLength, "var", "The lengths: type sizes, spacing, radii and window metrics."},
		{KindDuration, "const", "The durations."},
		{KindEasing, "var", "The easing curves."},
	}
	for _, g := range groups {
		p("// %s\n%s (\n", g.what, g.decl)
		for _, tok := range t.List {
			if tok.Kind != g.kind {
				continue
			}
			v, err := t.value(tok, false)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", tok.Name, err)
			}
			if g.kind == KindFont {
				p("\t%s = %s // %s\n", GoName(tok.Name), v, tok.Name)
				continue
			}
			p("\t%s = %s // %s: %s\n", GoName(tok.Name), v, tok.Name, tok.Light)
		}
		p(")\n\n")
	}

	p("// MediaOverrides are the :root lengths app.css changes inside width media queries.\n")
	p("var MediaOverrides = []Override{\n")
	for _, o := range t.Media {
		q, err := goQuery(o.Query)
		if err != nil {
			return nil, fmt.Errorf("@media %s: %w", o.Query, err)
		}
		p("\t{Token: %q, When: %s, Length: %s},\n", o.Token, q, goLength(o.Length))
	}
	p("}\n\n")
	p("// CompactOverrides are the lengths of the compact density (data-density='compact').\n")
	p("var CompactOverrides = []Override{\n")
	for _, o := range t.Compact {
		p("\t{Token: %q, Length: %s},\n", o.Token, goLength(o.Length))
	}
	p("}\n\n")

	p("// ForcedRules are app.css's rules under @media (forced-colors: active), as written.\n")
	p("var ForcedRules = []Rule{\n")
	for _, r := range t.Forced {
		p("\t{Selectors: []string{")
		for i, s := range r.Selectors {
			if i > 0 {
				p(", ")
			}
			p("%q", s)
		}
		p("}, Decls: []Decl{")
		for i, d := range r.Decls {
			if i > 0 {
				p(", ")
			}
			p("{%q, %q}", d.Prop, d.Value)
		}
		p("}},\n")
	}
	p("}\n")

	out, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt of the generated tokens: %w\n%s", err, b.Bytes())
	}
	return out, nil
}

// Token is the named token, or nil.
func (t *Tokens) Token(name string) *Token { return t.byName[name] }
