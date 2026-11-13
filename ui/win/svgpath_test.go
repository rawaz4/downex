//go:build windows

package win

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"downex/ui/kit"
)

const components = "../frontend/src/lib/components"

// svelteIconPaths reads every path data string of the Studio icons: Icon.svelte's ICONS
// (with ${PAGE} resolved and the computed gear), and the d attributes of FileIcon.svelte
// and StatusMark.svelte, each with its component's grid size.
func svelteIconPaths(t *testing.T) map[string]struct {
	d    string
	grid float32
} {
	t.Helper()
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(components, name))
		if err != nil {
			t.Skipf("the Svelte components are not next to ui/win: %v", err)
		}
		return string(b)
	}
	out := map[string]struct {
		d    string
		grid float32
	}{}
	icon := read("Icon.svelte")
	page := regexp.MustCompile(`const PAGE = '([^']+)'`).FindStringSubmatch(icon)
	if page == nil {
		t.Fatal("Icon.svelte: no PAGE")
	}
	block := icon[strings.Index(icon, "export const ICONS = {"):]
	block = block[:strings.Index(block, "} as const;")]
	entry := regexp.MustCompile("(?m)^\\s*('?[\\w-]+'?):\\s*(?:\\n\\s*)?(?:'([^']*)'|`([^`]*)`|gear\\(\\))")
	for _, m := range entry.FindAllStringSubmatch(block, -1) {
		name := strings.Trim(m[1], "'")
		d := m[2]
		if m[3] != "" {
			d = strings.ReplaceAll(m[3], "${PAGE}", page[1])
		}
		if strings.HasSuffix(m[0], "gear()") {
			d = gearPath()
		}
		out["Icon "+name] = struct {
			d    string
			grid float32
		}{d, 24}
	}
	attr := regexp.MustCompile(`\sd="([^"]+)"`)
	for file, grid := range map[string]float32{"FileIcon.svelte": 32, "StatusMark.svelte": 14} {
		for i, m := range attr.FindAllStringSubmatch(read(file), -1) {
			out[fmt.Sprintf("%s #%d", file, i)] = struct {
				d    string
				grid float32
			}{m[1], grid}
		}
	}
	return out
}

// gearPath is Icon.svelte's gear(): eight teeth and the hub, with JavaScript's toFixed(2).
func gearPath() string {
	var b strings.Builder
	const teeth, outer, inner = 8, 9.2, 7.3
	n := 0
	for k := 0; k < teeth; k++ {
		a := float64(k) / teeth * math.Pi * 2
		w := math.Pi * 2 / teeth
		for _, s := range [][2]float64{{a - w*0.5, inner}, {a - w*0.22, inner}, {a - w*0.14, outer}, {a + w*0.14, outer}, {a + w*0.22, inner}} {
			op := "L"
			if n == 0 {
				op = "M"
			}
			fmt.Fprintf(&b, "%s%.2f %.2f", op, 12+s[1]*math.Cos(s[0]), 12+s[1]*math.Sin(s[0]))
			n++
		}
	}
	b.WriteString("ZM12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6z")
	return b.String()
}

// Every path of Icon.svelte, FileIcon.svelte and StatusMark.svelte becomes a geometry that
// lies on its component's grid.
func TestSvelteIconPaths(t *testing.T) {
	paths := svelteIconPaths(t)
	if len(paths) < 70 {
		t.Fatalf("found only %d paths in the components", len(paths))
	}
	r := newRes(t)
	for name, p := range paths {
		got, err := r.NewSVGPath(p.d, kit.FillNonZero)
		if err != nil {
			t.Errorf("%s %q: %v", name, p.d, err)
			continue
		}
		b := got.(*path).Bounds()
		if b.Width() <= 0 && b.Height() <= 0 && !strings.Contains(p.d, "h.01") && !strings.Contains(p.d, "v.1") && !strings.Contains(p.d, "v.25") {
			t.Errorf("%s: no extent", name)
		}
		if b.Min.X < -1 || b.Min.Y < -1 || b.Max.X > p.grid+1 || b.Max.Y > p.grid+1 {
			t.Errorf("%s: bounds %v outside the %v grid", name, b, p.grid)
		}
		got.Release()
	}
	t.Logf("%d icon paths built", len(paths))
	// BrandMark.svelte's barPath, square and round-ended.
	for _, d := range []string{"M0 5h12v2h-12z", "M2 3.5h10.5a2 2 0 0 1 0 4h-10.5z", "M1 2h5a1.5 1.5 0 0 1 0 3h-5z"} {
		p, err := r.NewSVGPath(d, kit.FillNonZero)
		if err != nil {
			t.Errorf("BrandMark bar %q: %v", d, err)
			continue
		}
		p.Release()
	}
}

func TestSVGGrammar(t *testing.T) {
	pt := kit.Pt
	op := func(v kit.PathVerb, p ...kit.Point) kit.PathOp {
		o := kit.PathOp{Verb: v}
		copy(o.P[:], p)
		return o
	}
	for _, c := range []struct {
		d    string
		want []kit.PathOp
	}{
		{"M1 2L3 4", []kit.PathOp{op(kit.PathMove, pt(1, 2)), op(kit.PathLine, pt(3, 4))}},
		{"m1 2 3 4", []kit.PathOp{op(kit.PathMove, pt(1, 2)), op(kit.PathLine, pt(4, 6))}}, // implicit relative lineto
		{"M.75.75h-.5V1.5", []kit.PathOp{op(kit.PathMove, pt(.75, .75)), op(kit.PathLine, pt(.25, .75)), op(kit.PathLine, pt(.25, 1.5))}},
		{"M1-2 3e-1,4E1", []kit.PathOp{op(kit.PathMove, pt(1, -2)), op(kit.PathLine, pt(.3, 40))}},
		{"M0 0C1 1 2 2 3 3S5 5 6 6", []kit.PathOp{op(kit.PathMove, pt(0, 0)), op(kit.PathCubic, pt(1, 1), pt(2, 2), pt(3, 3)),
			op(kit.PathCubic, pt(4, 4), pt(5, 5), pt(6, 6))}},
		{"M0 0Q1 1 2 0T4 0", []kit.PathOp{op(kit.PathMove, pt(0, 0)), op(kit.PathQuad, pt(1, 1), pt(2, 0)), op(kit.PathQuad, pt(3, -1), pt(4, 0))}},
		{"M0 0s1 1 2 2", []kit.PathOp{op(kit.PathMove, pt(0, 0)), op(kit.PathCubic, pt(0, 0), pt(1, 1), pt(2, 2))}}, // S without a C before it
		{"M1 1h2v2zl1 0", []kit.PathOp{op(kit.PathMove, pt(1, 1)), op(kit.PathLine, pt(3, 1)), op(kit.PathLine, pt(3, 3)), {Verb: kit.PathClose},
			op(kit.PathLine, pt(2, 1))}}, // after Z, relative to the subpath's start
		{"M0 0a1 1 0 01 2 2", []kit.PathOp{op(kit.PathMove, pt(0, 0)), {Verb: kit.PathArc, P: [3]kit.Point{pt(2, 2)}, Radii: kit.Size{W: 1, H: 1}, Sweep: true}}},
		{"M0 0a1,1 30 1,0 2,2", []kit.PathOp{op(kit.PathMove, pt(0, 0)), {Verb: kit.PathArc, P: [3]kit.Point{pt(2, 2)}, Radii: kit.Size{W: 1, H: 1}, Rotation: 30, Large: true}}},
		{"", nil},
		{"  \n ", nil},
	} {
		got, err := parseSVGPath(c.d, nil)
		if err != nil {
			t.Errorf("%q: %v", c.d, err)
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%q:\n got %v\nwant %v", c.d, got, c.want)
		}
	}
	for d, at := range map[string]string{
		"L1 2":         "offset 0",
		"M1":           "offset 2",
		"M1 2 Z 3":     "offset 7",
		"M1 2X":        "offset 4",
		"M0 0a1 1 0 2": "offset 11",
		"M0 0 1,,2":    "offset 7",
		"M1e999 0":     `offset 1: bad number "1e999"`, // the message names the number
	} {
		_, err := parseSVGPath(d, nil)
		if !errors.Is(err, kit.ErrInvalid) || !strings.Contains(err.Error(), at) {
			t.Errorf("%q: %v, want ErrInvalid at %s", d, err, at)
		}
	}
	r := newRes(t)
	if _, err := r.NewSVGPath("M1 2 Q", kit.FillNonZero); !errors.Is(err, kit.ErrInvalid) {
		t.Errorf("NewSVGPath of bad data: %v", err)
	}
}

// NewPath takes absolute PathData; its arcs follow SVG's out-of-range rules.
func TestNewPath(t *testing.T) {
	r := newRes(t)
	var d kit.PathData
	d.MoveTo(kit.Pt(0, 0))
	d.ArcTo(kit.Size{W: 1, H: 1}, 0, false, true, kit.Pt(10, 0)) // radii too small: a half circle of radius 5
	d.Close()
	p, err := r.NewPath(d)
	if err != nil {
		t.Fatal(err)
	}
	if b := p.(*path).Bounds(); math.Abs(float64(b.Height()-5)) > 0.05 || math.Abs(float64(b.Width()-10)) > 0.05 {
		t.Errorf("scaled-up arc bounds %v, want 10 × 5", b)
	}
	p.Release()
	d.Reset()
	d.MoveTo(kit.Pt(0, 0))
	d.ArcTo(kit.Size{W: 0, H: 4}, 0, false, false, kit.Pt(10, 0)) // a zero radius: a straight line
	d.LineTo(kit.Pt(10, 0))
	p, err = r.NewPath(d)
	if err != nil {
		t.Fatal(err)
	}
	if b := p.(*path).Bounds(); b.Height() > 0.01 {
		t.Errorf("zero-radius arc bounds %v, want a line", b)
	}
	p.Release()
	for _, bad := range []kit.PathData{
		{Ops: []kit.PathOp{{Verb: kit.PathMove, P: [3]kit.Point{{X: float32(math.NaN())}}}}},
		{Ops: []kit.PathOp{{Verb: kit.PathLine, P: [3]kit.Point{{X: float32(math.Inf(1))}}}}},
		{Ops: []kit.PathOp{{Verb: 0}}},
		{Ops: []kit.PathOp{{Verb: kit.PathArc, Radii: kit.Size{W: float32(math.NaN())}}}},
	} {
		if _, err := r.NewPath(bad); !errors.Is(err, kit.ErrInvalid) {
			t.Errorf("NewPath(%v): %v, want ErrInvalid", bad, err)
		}
	}
	// Even-odd leaves the hole of a ring empty; nonzero fills it when both turn the same way.
	ring := "M0 0h10v10h-10zM3 3h4v4h-4z"
	for rule, filled := range map[kit.FillRule]bool{kit.FillNonZero: true, kit.FillEvenOdd: false} {
		p, err := r.NewSVGPath(ring, rule)
		if err != nil {
			t.Fatal(err)
		}
		in, _ := p.(*path).geo.FillContainsPoint(5, 5, nil, 0.01)
		if in != filled {
			t.Errorf("rule %v: centre filled = %v", rule, in)
		}
		p.Release()
	}
}
