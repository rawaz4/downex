package frontend

import (
	"bytes"
	"fmt"
	"go/format"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The functions of the components that the generator and package icons port by hand. A
// drift test compares their source, white space collapsed, with these; when one changes,
// port the change and copy the new source here.
const (
	// GearSource is Icon.svelte's gear(), which gearPath ports.
	GearSource = `function gear(): string { const teeth = 8; const outer = 9.2; const inner = 7.3; const pts: string[] = []; for (let k = 0; k < teeth; k++) { const a = (k / teeth) * Math.PI * 2; const w = (Math.PI * 2) / teeth; const seq: [number, number][] = [ [a - w * 0.5, inner], [a - w * 0.22, inner], [a - w * 0.14, outer], [a + w * 0.14, outer], [a + w * 0.22, inner], ]; for (const [angle, r] of seq) { pts.push(` + "`${pts.length === 0 ? 'M' : 'L'}${(12 + r * Math.cos(angle)).toFixed(2)} ${(12 + r * Math.sin(angle)).toFixed(2)}`" + `); } } return ` + "`${pts.join('')}ZM12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6z`" + `; }`
)

// GenerateTokens reads app.css in the frontend folder src and writes package tokens' file.
func GenerateTokens(src string) ([]byte, error) {
	b, err := readSource(src, "app.css")
	if err != nil {
		return nil, err
	}
	t, err := ReadTokens(b)
	if err != nil {
		return nil, err
	}
	return t.Generate()
}

func readSource(src, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

// LineIcon is one icon of Icon.svelte.
type LineIcon struct {
	Name   string
	D      string
	Stroke float64
	Filled bool
}

// TileShape is one shape of a file tile or a status mark: path data on the component's
// grid and how it is painted. Paints name a token ("--c-video") or a tile role ("hue",
// "deep", "glyph"); "" is none.
type TileShape struct {
	D                        string
	Fill, Stroke             string
	FillOpacity, Opacity     float64 // 1 when not set
	StrokeWidth              float64
	RoundCap, RoundJoin      bool
	Rotate, RotateX, RotateY float64 // rotate(deg cx cy); 0 when none
}

// Tile is one kind of FileIcon.svelte; Small is the variant at SMALL_TILE_PX and below
// (nil when the kind has none).
type Tile struct {
	Kind         string
	Shapes       []TileShape
	Small        []TileShape
	Hue, Deep    string // the tokens of the kind's hue and deep tone
	SmallMax     int
	ViewBox      [4]float64
	GlyphToken   string
	ForcedAdjust string
}

// Mark is one kind of StatusMark.svelte: its colour token (currentColor), its shapes.
type Mark struct {
	Kind   string
	Color  string
	Shapes []TileShape
}

// Icons are what the three components draw.
type Icons struct {
	Lines     []LineIcon
	Tiles     []Tile
	Marks     []Mark
	TileBox   [4]float64
	SmallTile int
	MarkBox   [4]float64
	// ForcedMark are StatusMark's forced-colours paints: the mark's colour and the glyph's.
	ForcedMark, ForcedMarkOn string
}

// ReadIcons reads Icon.svelte, FileIcon.svelte and StatusMark.svelte from the frontend's
// components folder.
func ReadIcons(src string) (*Icons, error) {
	ic := &Icons{}
	dir := "lib/components/"
	iconSrc, err := readSource(src, dir+"Icon.svelte")
	if err != nil {
		return nil, err
	}
	if ic.Lines, err = readLineIcons(iconSrc); err != nil {
		return nil, fmt.Errorf("Icon.svelte: %w", err)
	}
	tileSrc, err := readSource(src, dir+"FileIcon.svelte")
	if err != nil {
		return nil, err
	}
	if err = readTiles(tileSrc, ic); err != nil {
		return nil, fmt.Errorf("FileIcon.svelte: %w", err)
	}
	markSrc, err := readSource(src, dir+"StatusMark.svelte")
	if err != nil {
		return nil, err
	}
	if err = readMarks(markSrc, ic); err != nil {
		return nil, fmt.Errorf("StatusMark.svelte: %w", err)
	}
	return ic, nil
}

// GenerateIcons reads the icon components in the frontend folder src and writes package
// icons' file.
func GenerateIcons(src string) ([]byte, error) {
	ic, err := ReadIcons(src)
	if err != nil {
		return nil, err
	}
	return ic.Generate()
}

// ---- Icon.svelte ------------------------------------------------------------------------------

func readLineIcons(src string) ([]LineIcon, error) {
	script, err := moduleScript(src)
	if err != nil {
		return nil, err
	}
	toks, err := tsTokens(script)
	if err != nil {
		return nil, err
	}
	consts := map[string]string{}
	for i := 0; i+4 < len(toks); i++ {
		if toks[i].text == "const" && toks[i+1].kind == 'i' && toks[i+2].text == "=" && toks[i+3].kind == 's' && toks[i+4].text == ";" {
			consts[toks[i+1].text] = toks[i+3].text
		}
	}
	at := func(seq ...string) int {
		for i := 0; i+len(seq) <= len(toks); i++ {
			ok := true
			for j, s := range seq {
				if toks[i+j].text != s {
					ok = false
					break
				}
			}
			if ok {
				return i + len(seq)
			}
		}
		return -1
	}
	i := at("export", "const", "ICONS", "=", "{")
	if i < 0 {
		return nil, fmt.Errorf("export const ICONS = { not found")
	}
	var icons []LineIcon
	seen := map[string]bool{}
	for {
		if i >= len(toks) {
			return nil, fmt.Errorf("ICONS never ends")
		}
		if toks[i].text == "}" {
			break
		}
		key := toks[i]
		if key.kind != 'i' && key.kind != 's' || i+2 >= len(toks) || toks[i+1].text != ":" {
			return nil, fmt.Errorf("line %d: ICONS entry %q", lineOf(script, key.at), key.text)
		}
		v := toks[i+2]
		var d string
		i += 3
		switch {
		case v.kind == 's':
			d = v.text
		case v.kind == 't':
			d, err = expandTemplate(v.text, consts)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s: %w", lineOf(script, v.at), key.text, err)
			}
		case v.kind == 'i' && v.text == "gear" && i+1 < len(toks) && toks[i].text == "(" && toks[i+1].text == ")":
			fn, err := functionSource(script, "gear")
			if err != nil {
				return nil, err
			}
			if fn != GearSource {
				return nil, fmt.Errorf("gear() changed: port it to frontend.gearPath and copy its source into GearSource:\n%s", fn)
			}
			d = GearPath()
			i += 2
		default:
			return nil, fmt.Errorf("line %d: %s: a value the generator cannot read (%q)", lineOf(script, v.at), key.text, v.text)
		}
		if seen[key.text] {
			return nil, fmt.Errorf("icon %s twice", key.text)
		}
		seen[key.text] = true
		icons = append(icons, LineIcon{Name: key.text, D: d})
		if toks[i].text == "," {
			i++
		}
	}
	if j := at("}", "as", "const", ";"); j < 0 {
		return nil, fmt.Errorf("ICONS is not `as const`")
	}

	// FILLED and STROKE, and the default stroke width from the markup.
	filled := map[string]bool{}
	if j := at("new", "Set", "<", "IconName", ">", "(", "["); j >= 0 {
		for ; j < len(toks) && toks[j].text != "]"; j++ {
			if toks[j].kind == 's' {
				filled[toks[j].text] = true
			}
		}
	} else {
		return nil, fmt.Errorf("FILLED's new Set<IconName>([...]) not found")
	}
	strokes := map[string]float64{}
	j := at("const", "STROKE")
	if j < 0 {
		return nil, fmt.Errorf("const STROKE not found")
	}
	for ; j < len(toks) && toks[j].text != "{"; j++ {
	}
	for j++; j+2 < len(toks) && toks[j].text != "}"; j++ {
		if (toks[j].kind == 'i' || toks[j].kind == 's') && toks[j+1].text == ":" && toks[j+2].kind == 'n' {
			v, err := strconv.ParseFloat(toks[j+2].text, 64)
			if err != nil {
				return nil, err
			}
			strokes[toks[j].text] = v
			j += 2
		}
	}
	nodes, err := Markup(src)
	if err != nil {
		return nil, err
	}
	svg := find(nodes, "svg")
	if svg == nil {
		return nil, fmt.Errorf("no <svg> in the markup")
	}
	def, err := checkIconSVG(svg)
	if err != nil {
		return nil, err
	}
	for k := range filled {
		if !seen[k] {
			return nil, fmt.Errorf("FILLED names unknown icon %s", k)
		}
	}
	for k := range strokes {
		if !seen[k] {
			return nil, fmt.Errorf("STROKE names unknown icon %s", k)
		}
	}
	for n := range icons {
		icons[n].Filled = filled[icons[n].Name]
		icons[n].Stroke = def
		if w, ok := strokes[icons[n].Name]; ok {
			icons[n].Stroke = w
		}
	}
	return icons, nil
}

// checkIconSVG checks the attributes Icon.svelte draws every icon with and returns the
// default stroke width.
func checkIconSVG(svg *Node) (float64, error) {
	want := map[string]string{
		"viewBox":         "0 0 24 24",
		"stroke":          "currentColor",
		"stroke-linecap":  "round",
		"stroke-linejoin": "round",
		"fill":            "{filled ? 'currentColor' : 'none'}",
	}
	for k, v := range want {
		if svg.Attrs[k] != v {
			return 0, fmt.Errorf("line %d: <svg %s=%q>, the generator knows %q", svg.Line, k, svg.Attrs[k], v)
		}
	}
	sw := svg.Attrs["stroke-width"]
	in, ok := strings.CutPrefix(sw, "{STROKE[name] ?? ")
	if !ok || !strings.HasSuffix(in, "}") {
		return 0, fmt.Errorf("line %d: <svg stroke-width=%q>, the generator knows {STROKE[name] ?? N}", svg.Line, sw)
	}
	return strconv.ParseFloat(strings.TrimSuffix(in, "}"), 64)
}

func expandTemplate(body string, consts map[string]string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(body, "${")
		if i < 0 {
			b.WriteString(body)
			return b.String(), nil
		}
		b.WriteString(body[:i])
		end := strings.IndexByte(body[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("template ${ never closed")
		}
		name := body[i+2 : i+end]
		v, ok := consts[name]
		if !ok {
			return "", fmt.Errorf("template uses %s, which is not a string constant", name)
		}
		b.WriteString(v)
		body = body[i+end+1:]
	}
}

func find(nodes []*Node, tag string) *Node {
	for _, n := range nodes {
		if n.Tag == tag {
			return n
		}
	}
	return nil
}

// GearPath is Icon.svelte's gear(): an eight-tooth gear with its hub, its points written as
// JavaScript's toFixed(2) writes them.
func GearPath() string {
	const teeth = 8
	const outer, inner = 9.2, 7.3
	var pts []string
	for k := 0; k < teeth; k++ {
		a := float64(k) / teeth * math.Pi * 2
		w := (math.Pi * 2) / teeth
		seq := [...][2]float64{
			{a - w*0.5, inner},
			{a - w*0.22, inner},
			{a - w*0.14, outer},
			{a + w*0.14, outer},
			{a + w*0.22, inner},
		}
		for _, s := range seq {
			cmd := "L"
			if len(pts) == 0 {
				cmd = "M"
			}
			pts = append(pts, cmd+ToFixed(12+s[1]*math.Cos(s[0]), 2)+" "+ToFixed(12+s[1]*math.Sin(s[0]), 2))
		}
	}
	return strings.Join(pts, "") + "ZM12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6z"
}

// ToFixed is JavaScript's Number.prototype.toFixed(digits) for finite v: the decimal of
// that many digits nearest to v's exact value, the larger magnitude on a tie.
func ToFixed(v float64, digits int) string {
	neg := v < 0
	r := new(big.Rat).SetFloat64(math.Abs(v))
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil))
	r.Mul(r, scale)
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Quo(r.Num(), r.Denom()) // floor, r being positive
	s := n.String()
	for len(s) <= digits {
		s = "0" + s
	}
	out := s
	if digits > 0 {
		out = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	if neg {
		out = "-" + out
	}
	return out
}

// ---- FileIcon.svelte --------------------------------------------------------------------------

func readTiles(src string, ic *Icons) error {
	script, err := moduleScript(src)
	if err != nil {
		return err
	}
	toks, err := tsTokens(script)
	if err != nil {
		return err
	}
	var kinds []string
	var viewBox string
	small := -1
	for i := 0; i+3 < len(toks); i++ {
		switch {
		case toks[i].text == "TILE_KINDS" && toks[i+1].text == ":":
			for j := i; j < len(toks) && toks[j].text != ";"; j++ {
				if toks[j].kind == 's' {
					kinds = append(kinds, toks[j].text)
				}
			}
		case toks[i].text == "TILE_VIEWBOX" && toks[i+1].text == "=" && toks[i+2].kind == 's':
			viewBox = toks[i+2].text
		case toks[i].text == "SMALL_TILE_PX" && toks[i+1].text == "=" && toks[i+2].kind == 'n':
			small, _ = strconv.Atoi(toks[i+2].text)
		}
	}
	if len(kinds) == 0 || viewBox == "" || small <= 0 {
		return fmt.Errorf("TILE_KINDS, TILE_VIEWBOX or SMALL_TILE_PX not found")
	}
	vb, err := parseViewBox(viewBox)
	if err != nil {
		return err
	}
	ic.TileBox, ic.SmallTile = vb, small
	nodes, err := Markup(src)
	if err != nil {
		return err
	}
	svg := find(nodes, "svg")
	if svg == nil {
		return fmt.Errorf("no <svg>")
	}
	for k, v := range map[string]string{
		"viewBox":     "{TILE_VIEWBOX}",
		"style:--hue": "var(--c-{shown})", "style:--deep": "var(--c-{shown}-deep)",
	} {
		if svg.Attrs[k] != v {
			return fmt.Errorf("line %d: <svg %s=%q>, the generator knows %q", svg.Line, k, svg.Attrs[k], v)
		}
	}
	if len(svg.Children) != 1 || svg.Children[0].Tag != "#if" {
		return fmt.Errorf("line %d: the tiles are not one {#if shown === ...} block", svg.Line)
	}
	covered := map[string]bool{}
	var elseKids []*Node
	for _, br := range svg.Children[0].Branches {
		if br.Cond == "" {
			elseKids = br.Children
			continue
		}
		kind, ok := quotedAfter(br.Cond, "shown === ")
		if !ok || !slices.Contains(kinds, kind) {
			return fmt.Errorf("tile condition %q", br.Cond)
		}
		t, err := tileOf(kind, br.Children, small)
		if err != nil {
			return fmt.Errorf("tile %s: %w", kind, err)
		}
		covered[kind] = true
		ic.Tiles = append(ic.Tiles, t)
	}
	// The {:else} arm draws the one kind no condition names (other).
	var rest []string
	for _, k := range kinds {
		if !covered[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) != 1 || elseKids == nil {
		return fmt.Errorf("the {:else} tile stands for %v; the generator wants exactly one kind", rest)
	}
	t, err := tileOf(rest[0], elseKids, small)
	if err != nil {
		return fmt.Errorf("tile %s: %w", rest[0], err)
	}
	ic.Tiles = append(ic.Tiles, t)
	// In TILE_KINDS order.
	slices.SortFunc(ic.Tiles, func(a, b Tile) int { return slices.Index(kinds, a.Kind) - slices.Index(kinds, b.Kind) })
	return nil
}

func quotedAfter(cond, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(cond, prefix)
	if !ok || len(rest) < 2 || rest[0] != '\'' || rest[len(rest)-1] != '\'' {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}

func parseViewBox(s string) ([4]float64, error) {
	var vb [4]float64
	f := strings.Fields(s)
	if len(f) != 4 {
		return vb, fmt.Errorf("viewBox %q", s)
	}
	for i, x := range f {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return vb, fmt.Errorf("viewBox %q: %w", s, err)
		}
		vb[i] = v
	}
	return vb, nil
}

func tileOf(kind string, kids []*Node, small int) (Tile, error) {
	t := Tile{Kind: kind, Hue: "--c-" + kind, Deep: "--c-" + kind + "-deep", GlyphToken: "--tile-glyph", SmallMax: small}
	var walk func(kids []*Node, rot [3]float64, dst *[]TileShape, sizeVariant string) error
	walk = func(kids []*Node, rot [3]float64, dst *[]TileShape, sizeVariant string) error {
		for _, n := range kids {
			switch n.Tag {
			case "#if":
				if sizeVariant != "" || len(n.Branches) != 2 || n.Branches[0].Cond != "size <= SMALL_TILE_PX" || n.Branches[1].Cond != "" {
					return fmt.Errorf("line %d: a size condition other than {#if size <= SMALL_TILE_PX}{:else}", n.Line)
				}
				if t.Small != nil {
					return fmt.Errorf("line %d: two size conditions", n.Line)
				}
				// The shapes before the block are in both variants.
				t.Small = slices.Clone(*dst)
				if err := walk(n.Branches[0].Children, rot, &t.Small, "small"); err != nil {
					return err
				}
				if err := walk(n.Branches[1].Children, rot, dst, "large"); err != nil {
					return err
				}
			case "g":
				r, err := parseRotate(n.Attrs["transform"])
				if err != nil {
					return fmt.Errorf("line %d: %w", n.Line, err)
				}
				if len(n.Attrs) != 1 {
					return fmt.Errorf("line %d: <g> with attributes other than transform", n.Line)
				}
				if err := walk(n.Children, r, dst, sizeVariant); err != nil {
					return err
				}
			case "rect", "circle", "path":
				s, err := shapeOf(n, tilePaint)
				if err != nil {
					return fmt.Errorf("line %d: %w", n.Line, err)
				}
				s.Rotate, s.RotateX, s.RotateY = rot[0], rot[1], rot[2]
				*dst = append(*dst, s)
				if t.Small != nil && sizeVariant == "" {
					t.Small = append(t.Small, s) // after the size block: in both variants
				}
			default:
				return fmt.Errorf("line %d: <%s> in a tile", n.Line, n.Tag)
			}
		}
		return nil
	}
	if err := walk(kids, [3]float64{}, &t.Shapes, ""); err != nil {
		return t, err
	}
	return t, nil
}

func parseRotate(s string) ([3]float64, error) {
	in, ok := strings.CutPrefix(s, "rotate(")
	if !ok || !strings.HasSuffix(in, ")") {
		return [3]float64{}, fmt.Errorf("transform %q: the generator reads rotate(deg cx cy) only", s)
	}
	f := strings.Fields(strings.ReplaceAll(strings.TrimSuffix(in, ")"), ",", " "))
	if len(f) != 3 {
		return [3]float64{}, fmt.Errorf("transform %q: want rotate(deg cx cy)", s)
	}
	var r [3]float64
	for i, x := range f {
		v, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return r, fmt.Errorf("transform %q: %w", s, err)
		}
		r[i] = v
	}
	return r, nil
}

// tilePaint reads a tile's fill or stroke: var(--hue), var(--deep), var(--tile-glyph) or none.
func tilePaint(v string) (string, error) {
	switch v {
	case "var(--hue)":
		return "hue", nil
	case "var(--deep)":
		return "deep", nil
	case "var(--tile-glyph)":
		return "glyph", nil
	case "none":
		return "", nil
	}
	return "", fmt.Errorf("paint %q", v)
}

// shapeOf reads a rect, circle or path into path data and paint, with paint reading the
// fill and stroke attributes. An element with no fill attribute fills in black, SVG's
// default, which the components never rely on: it fails.
func shapeOf(n *Node, paint func(string) (string, error)) (TileShape, error) {
	s := TileShape{FillOpacity: 1, Opacity: 1, StrokeWidth: 1}
	num := func(name string) (float64, error) {
		v, ok := n.Attrs[name]
		if !ok {
			return 0, fmt.Errorf("<%s> without %s", n.Tag, name)
		}
		return strconv.ParseFloat(v, 64)
	}
	var err error
	switch n.Tag {
	case "rect":
		var x, y, w, h, rx float64
		for _, p := range []struct {
			v *float64
			n string
		}{{&x, "x"}, {&y, "y"}, {&w, "width"}, {&h, "height"}} {
			if *p.v, err = num(p.n); err != nil {
				return s, err
			}
		}
		if _, ok := n.Attrs["rx"]; ok {
			if rx, err = num("rx"); err != nil {
				return s, err
			}
		}
		s.D = RectPath(x, y, w, h, rx)
	case "circle":
		var cx, cy, r float64
		if cx, err = num("cx"); err != nil {
			return s, err
		}
		if cy, err = num("cy"); err != nil {
			return s, err
		}
		if r, err = num("r"); err != nil {
			return s, err
		}
		s.D = CirclePath(cx, cy, r)
	case "path":
		s.D = n.Attrs["d"]
		if s.D == "" {
			return s, fmt.Errorf("<path> without d")
		}
	}
	known := map[string]bool{"x": true, "y": true, "width": true, "height": true, "rx": true, "cx": true, "cy": true, "r": true, "d": true, "class": true, "pathLength": true}
	for k, v := range n.Attrs {
		switch k {
		case "fill":
			if s.Fill, err = paint(v); err != nil {
				return s, err
			}
		case "stroke":
			if s.Stroke, err = paint(v); err != nil {
				return s, err
			}
		case "stroke-width":
			if s.StrokeWidth, err = strconv.ParseFloat(v, 64); err != nil {
				return s, err
			}
		case "stroke-linecap":
			s.RoundCap = v == "round"
			if v != "round" {
				return s, fmt.Errorf("stroke-linecap %q", v)
			}
		case "stroke-linejoin":
			s.RoundJoin = v == "round"
			if v != "round" {
				return s, fmt.Errorf("stroke-linejoin %q", v)
			}
		case "fill-opacity":
			if s.FillOpacity, err = strconv.ParseFloat(v, 64); err != nil {
				return s, err
			}
		case "opacity":
			if s.Opacity, err = strconv.ParseFloat(v, 64); err != nil {
				return s, err
			}
		default:
			if !known[k] {
				return s, fmt.Errorf("attribute %s the generator does not read", k)
			}
		}
	}
	return s, nil
}

// RectPath is an SVG rect (rx, ry = rx, clamped to half the sides as SVG clamps it) as path
// data, drawn clockwise from the top-left corner's end, as SVG's rect is.
func RectPath(x, y, w, h, rx float64) string {
	rx = math.Min(math.Max(rx, 0), math.Min(w/2, h/2))
	f := pathNum
	if rx == 0 {
		return fmt.Sprintf("M%s %sH%sV%sH%sZ", f(x), f(y), f(x+w), f(y+h), f(x))
	}
	r := f(rx)
	return fmt.Sprintf("M%s %sH%sA%s %s 0 0 1 %s %sV%sA%s %s 0 0 1 %s %sH%sA%s %s 0 0 1 %s %sV%sA%s %s 0 0 1 %s %sZ",
		f(x+rx), f(y), f(x+w-rx), r, r, f(x+w), f(y+rx), f(y+h-rx), r, r, f(x+w-rx), f(y+h),
		f(x+rx), r, r, f(x), f(y+h-rx), f(y+rx), r, r, f(x+rx), f(y))
}

// pathNum writes a coordinate worked out from a component's numbers: rounded to a millionth
// of a unit, so that 7 - 6.2 is 0.8 rather than 0.7999999999999998.
func pathNum(v float64) string { return strconv.FormatFloat(math.Round(v*1e6)/1e6, 'g', -1, 64) }

// CirclePath is an SVG circle as path data: two half arcs from its leftmost point.
func CirclePath(cx, cy, r float64) string {
	f := pathNum
	return fmt.Sprintf("M%s %sA%s %s 0 1 0 %s %sA%s %s 0 1 0 %s %sZ", f(cx-r), f(cy), f(r), f(r), f(cx+r), f(cy), f(r), f(r), f(cx-r), f(cy))
}

// ---- StatusMark.svelte ------------------------------------------------------------------------

func readMarks(src string, ic *Icons) error {
	script, err := moduleScript(src)
	if err != nil {
		return err
	}
	toks, err := tsTokens(script)
	if err != nil {
		return err
	}
	var kinds []string
	for i := 0; i+2 < len(toks); i++ {
		if toks[i].text == "type" && toks[i+1].text == "MarkKind" && toks[i+2].text == "=" {
			for j := i + 3; j < len(toks) && toks[j].text != ";"; j++ {
				if toks[j].kind == 's' {
					kinds = append(kinds, toks[j].text)
				}
			}
		}
	}
	if len(kinds) == 0 {
		return fmt.Errorf("type MarkKind not found")
	}
	css, _, err := SvelteStyle(src)
	if err != nil {
		return err
	}
	rules, err := ParseCSS(css)
	if err != nil {
		return fmt.Errorf("<style>: %w", err)
	}
	nodes, err := Markup(src)
	if err != nil {
		return err
	}
	svg := find(nodes, "svg")
	if svg == nil {
		return fmt.Errorf("no <svg>")
	}
	if svg.Attrs["viewBox"] != "0 0 14 14" || svg.Attrs["class"] != "mark mark-{kind}" {
		return fmt.Errorf("line %d: <svg viewBox=%q class=%q>, the generator knows 0 0 14 14 and mark mark-{kind}", svg.Line, svg.Attrs["viewBox"], svg.Attrs["class"])
	}
	ic.MarkBox = [4]float64{0, 0, 14, 14}
	for _, r := range rules {
		if r.Media != "(forced-colors: active)" {
			continue
		}
		if r.HasSelector(".mark") {
			ic.ForcedMark, _ = r.Get("color")
		}
		if r.HasSelector(".on") {
			ic.ForcedMarkOn, _ = r.Get("stroke")
		}
	}
	if len(svg.Children) != 1 || svg.Children[0].Tag != "#if" {
		return fmt.Errorf("the marks are not one {#if kind === ...} block")
	}
	covered := map[string]bool{}
	var elseKids []*Node
	type arm struct {
		kind string
		kids []*Node
	}
	var arms []arm
	for _, br := range svg.Children[0].Branches {
		if br.Cond == "" {
			elseKids = br.Children
			continue
		}
		kind, ok := quotedAfter(br.Cond, "kind === ")
		if !ok || !slices.Contains(kinds, kind) {
			return fmt.Errorf("mark condition %q", br.Cond)
		}
		covered[kind] = true
		arms = append(arms, arm{kind, br.Children})
	}
	var rest []string
	for _, k := range kinds {
		if !covered[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) != 1 || elseKids == nil {
		return fmt.Errorf("the {:else} mark stands for %v; the generator wants exactly one kind", rest)
	}
	arms = append(arms, arm{rest[0], elseKids})
	for _, a := range arms {
		m := Mark{Kind: a.kind}
		colour, err := cascade(rules, []string{"mark", "mark-" + a.kind}, nil, "color")
		if err != nil {
			return fmt.Errorf("mark %s: %w", a.kind, err)
		}
		if m.Color, err = tokenPaint(colour, ""); err != nil {
			return fmt.Errorf("mark %s colour: %w", a.kind, err)
		}
		for _, n := range a.kids {
			s, err := markShape(n, rules, a.kind, m.Color)
			if err != nil {
				return fmt.Errorf("mark %s, line %d: %w", a.kind, n.Line, err)
			}
			m.Shapes = append(m.Shapes, s)
		}
		ic.Marks = append(ic.Marks, m)
	}
	slices.SortFunc(ic.Marks, func(a, b Mark) int { return slices.Index(kinds, a.Kind) - slices.Index(kinds, b.Kind) })
	return nil
}

// cascade is the value of prop for an element with classes inside the <svg> of svgClasses,
// from the component's rules outside media queries: the most specific matching rule wins,
// the later one on a tie. Selectors are one class (.on) or a class of the <svg> and one of
// the element (.mark-downloading .on); others (.morph ...) never match.
func cascade(rules []Rule, svgClasses, classes []string, prop string) (string, error) {
	best, bestSpec := "", -1
	for _, r := range rules {
		if r.Media != "" {
			continue
		}
		v, ok := r.Get(prop)
		if !ok {
			continue
		}
		for _, sel := range r.Selectors {
			parts := strings.Fields(sel)
			spec := -1
			switch {
			case len(parts) == 1 && strings.HasPrefix(parts[0], ".") && strings.Count(parts[0], ".") == 1:
				c := parts[0][1:]
				if classes == nil && slices.Contains(svgClasses, c) || classes != nil && slices.Contains(classes, c) {
					spec = 1
				}
			case len(parts) == 2 && classes != nil && strings.Count(parts[0], ".") == 1 && strings.Count(parts[1], ".") == 1:
				if slices.Contains(svgClasses, parts[0][1:]) && slices.Contains(classes, parts[1][1:]) {
					spec = 2
				}
			}
			if spec >= bestSpec && spec >= 0 {
				best, bestSpec = v, spec
			}
		}
	}
	if bestSpec < 0 {
		return "", nil
	}
	return best, nil
}

// tokenPaint reads a paint that names a token: var(--x) or var(--x, fallback); currentColor
// is cur (the mark's colour); none is "".
func tokenPaint(v, cur string) (string, error) {
	switch v {
	case "none", "":
		return "", nil
	case "currentColor":
		if cur == "" {
			return "", fmt.Errorf("currentColor without a colour")
		}
		return cur, nil
	}
	name, _, ok := VarRef(v)
	if !ok {
		return "", fmt.Errorf("paint %q does not name a token", v)
	}
	return name, nil
}

func markShape(n *Node, rules []Rule, kind, colour string) (TileShape, error) {
	if n.Tag != "rect" && n.Tag != "circle" && n.Tag != "path" {
		return TileShape{}, fmt.Errorf("<%s> in a mark", n.Tag)
	}
	classes := strings.Fields(n.Attrs["class"])
	if len(classes) == 0 {
		return TileShape{}, fmt.Errorf("<%s> without a class", n.Tag)
	}
	s, err := shapeOf(n, func(string) (string, error) { return "", fmt.Errorf("a mark's paints come from its classes") })
	if err != nil {
		return s, err
	}
	svgClasses := []string{"mark", "mark-" + kind}
	get := func(prop string) (string, error) { return cascade(rules, svgClasses, classes, prop) }
	fill, err := get("fill")
	if err != nil {
		return s, err
	}
	if s.Fill, err = tokenPaint(fill, colour); err != nil {
		return s, err
	}
	stroke, err := get("stroke")
	if err != nil {
		return s, err
	}
	if s.Stroke, err = tokenPaint(stroke, colour); err != nil {
		return s, err
	}
	// A CSS rule beats the presentation attribute; without either the width stays 1.
	if w, _ := get("stroke-width"); w != "" {
		if s.StrokeWidth, err = strconv.ParseFloat(w, 64); err != nil {
			return s, err
		}
	}
	if c, _ := get("stroke-linecap"); c != "" {
		if c != "round" {
			return s, fmt.Errorf("stroke-linecap %q", c)
		}
		s.RoundCap = true
	}
	if j, _ := get("stroke-linejoin"); j != "" {
		if j != "round" {
			return s, fmt.Errorf("stroke-linejoin %q", j)
		}
		s.RoundJoin = true
	}
	if s.Fill == "" && s.Stroke == "" {
		return s, fmt.Errorf("a shape that paints nothing")
	}
	if s.Fill != "" && s.Stroke != "" {
		return s, fmt.Errorf("a mark shape that fills and strokes: the generated table holds one paint")
	}
	if fill == "" && n.Tag != "path" && s.Stroke == "" {
		return s, fmt.Errorf("<%s> fills in SVG's default black", n.Tag)
	}
	return s, nil
}

// ---- generation -------------------------------------------------------------------------------

// Generate writes package icons' generated file.
func (ic *Icons) Generate() ([]byte, error) {
	var b bytes.Buffer
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	f := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	p("// Code generated by go generate (ui/kit/internal/gen from ui/frontend/src/lib/components: Icon.svelte, FileIcon.svelte, StatusMark.svelte); DO NOT EDIT.\n\n")
	p("package icons\n\nimport (\n\t\"downex/ui/kit\"\n\t\"downex/ui/kit/tokens\"\n)\n\n")

	p("// The line icons of Icon.svelte, drawn on a 24×24 grid with round caps and joins.\n")
	p("const (\n")
	for i, ic := range ic.Lines {
		if i == 0 {
			p("\t%s Name = iota // %s\n", GoName(ic.Name), ic.Name)
			continue
		}
		p("\t%s // %s\n", GoName(ic.Name), ic.Name)
	}
	p("\tnumIcons\n)\n\n")
	p("var lineIcons = [numIcons]lineIcon{\n")
	for _, ic := range ic.Lines {
		p("\t%s: {name: %q, d: %q, stroke: %s", GoName(ic.Name), ic.Name, ic.D, f(ic.Stroke))
		if ic.Filled {
			p(", filled: true")
		}
		p("},\n")
	}
	p("}\n\n")

	p("// The file kinds of FileIcon.svelte, in TILE_KINDS order.\nconst (\n")
	for i, t := range ic.Tiles {
		if i == 0 {
			p("\tTile%s TileKind = iota // %s\n", GoName(t.Kind), t.Kind)
			continue
		}
		p("\tTile%s // %s\n", GoName(t.Kind), t.Kind)
	}
	p("\tnumTiles\n)\n\n")
	p("// tileBox is FileIcon's viewBox; smallTile is SMALL_TILE_PX.\n")
	p("var tileBox = [4]float32{%s, %s, %s, %s}\n\nconst smallTile = %d\n\n", f(ic.TileBox[0]), f(ic.TileBox[1]), f(ic.TileBox[2]), f(ic.TileBox[3]), ic.SmallTile)
	writeShapes := func(list []TileShape, paint func(string) string) {
		p("[]shape{\n")
		for _, s := range list {
			p("\t\t\t{d: %q", s.D)
			if s.Fill != "" {
				p(", fill: %s", paint(s.Fill))
			}
			if s.Stroke != "" {
				p(", stroke: %s, width: %s", paint(s.Stroke), f(s.StrokeWidth))
			}
			if s.RoundCap {
				p(", roundCap: true")
			}
			if s.RoundJoin {
				p(", roundJoin: true")
			}
			if s.FillOpacity != 1 {
				p(", fillOpacity: %s", f(s.FillOpacity))
			}
			if s.Opacity != 1 {
				p(", opacity: %s", f(s.Opacity))
			}
			if s.Rotate != 0 {
				p(", rotate: [3]float32{%s, %s, %s}", f(s.Rotate), f(s.RotateX), f(s.RotateY))
			}
			p("},\n")
		}
		p("\t\t}")
	}
	tilePaintGo := func(s string) string { return "paint" + strings.ToUpper(s[:1]) + s[1:] }
	p("var tiles = [numTiles]tile{\n")
	for _, t := range ic.Tiles {
		p("\tTile%s: {\n\t\thue: func(t *tokens.Theme) kit.Color { return t.%s },\n\t\tdeep: func(t *tokens.Theme) kit.Color { return t.%s },\n", GoName(t.Kind), GoName(t.Hue), GoName(t.Deep))
		p("\t\tshapes: ")
		writeShapes(t.Shapes, tilePaintGo)
		p(",\n")
		if t.Small != nil {
			p("\t\tsmall: ")
			writeShapes(t.Small, tilePaintGo)
			p(",\n")
		}
		p("\t},\n")
	}
	p("}\n\n")

	p("// The status marks of StatusMark.svelte, in MarkKind order, on a 14×14 grid.\nconst (\n")
	for i, m := range ic.Marks {
		if i == 0 {
			p("\tMark%s MarkKind = iota // %s\n", GoName(m.Kind), m.Kind)
			continue
		}
		p("\tMark%s // %s\n", GoName(m.Kind), m.Kind)
	}
	p("\tnumMarks\n)\n\n")
	tokenGo := func(name string) string {
		return fmt.Sprintf("func(t *tokens.Theme) kit.Color { return t.%s }", GoName(name))
	}
	p("var marks = [numMarks]mark{\n")
	for _, m := range ic.Marks {
		p("\tMark%s: {\n\t\tcolor: %s,\n\t\tshapes: ", GoName(m.Kind), tokenGo(m.Color))
		list := make([]TileShape, len(m.Shapes))
		copy(list, m.Shapes)
		p("[]markShape{\n")
		for _, s := range list {
			p("\t\t\t{shape: shape{d: %q", s.D)
			if s.Stroke != "" {
				p(", width: %s", f(s.StrokeWidth))
			}
			if s.RoundCap {
				p(", roundCap: true")
			}
			if s.RoundJoin {
				p(", roundJoin: true")
			}
			p("}")
			if s.Fill != "" {
				p(", fill: %s, onTone: %v", tokenGo(s.Fill), s.Fill != m.Color)
			}
			if s.Stroke != "" {
				p(", stroke: %s, onTone: %v", tokenGo(s.Stroke), s.Stroke != m.Color)
			}
			p("},\n")
		}
		p("\t\t},\n\t},\n")
	}
	p("}\n\n")
	p("// markForced and markForcedOn are StatusMark's forced-colours paints (%s, %s).\n", ic.ForcedMark, ic.ForcedMarkOn)
	p("const (\n\tmarkForced = %q\n\tmarkForcedOn = %q\n)\n", ic.ForcedMark, ic.ForcedMarkOn)

	out, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gofmt of the generated icons: %w\n%s", err, b.Bytes())
	}
	return out, nil
}
