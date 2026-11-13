package kit_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"downex/ui/kit"
)

// The layout cases of testdata/layout-cases.json, laid out by Chromium (the WebView
// window's engine) into testdata/layout-chromium.json by testdata/chromium-layout.mjs.

type layoutCase struct {
	Name string     `json:"name"`
	W    float32    `json:"w"`
	H    float32    `json:"h"`
	Tree layoutNode `json:"tree"`
}

type layoutNode struct {
	S     map[string]any `json:"s"`
	K     []layoutNode   `json:"k"`
	Words []float32      `json:"words"`
	WH    float32        `json:"h"`
}

type chromiumCase struct {
	Name  string       `json:"name"`
	Rects [][4]float64 `json:"rects"`
}

func readJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// length is a case's CSS value as a kit Length: a number of px, or "auto", "N%", "Nrem".
func length(t *testing.T, v any) kit.Length {
	switch v := v.(type) {
	case float64:
		return kit.Px(float32(v))
	case string:
		switch {
		case v == "auto":
			return kit.Auto
		case strings.HasSuffix(v, "rem"):
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "rem"), 32)
			if err != nil {
				t.Fatalf("length %q", v)
			}
			return kit.Rem(float32(f))
		case strings.HasSuffix(v, "%"):
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 32)
			if err != nil {
				t.Fatalf("length %q", v)
			}
			return kit.Pct(float32(f))
		}
	}
	t.Fatalf("length %v", v)
	return kit.Length{}
}

func edges(t *testing.T, v any) kit.Edges {
	a, ok := v.([]any)
	if !ok || len(a) != 4 {
		t.Fatalf("edges %v", v)
	}
	// CSS order: top right bottom left; the cases are left to right.
	return kit.Edges{Top: length(t, a[0]), End: length(t, a[1]), Bottom: length(t, a[2]), Start: length(t, a[3])}
}

func align(t *testing.T, v string) kit.Align {
	switch v {
	case "flex-start", "start":
		return kit.AlignStart
	case "flex-end", "end":
		return kit.AlignEnd
	case "center":
		return kit.AlignCenter
	case "baseline":
		return kit.AlignBaseline
	case "stretch":
		return kit.AlignStretch
	case "space-between":
		return kit.AlignSpaceBetween
	case "space-around":
		return kit.AlignSpaceAround
	case "space-evenly":
		return kit.AlignSpaceEvenly
	}
	t.Fatalf("align %q", v)
	return 0
}

func flexOf(t *testing.T, v string) kit.Flex {
	if v == "none" {
		return kit.FlexNone
	}
	f := strings.Fields(v)
	if len(f) != 3 {
		t.Fatalf("flex %q: give grow, shrink and basis", v)
	}
	g, _ := strconv.ParseFloat(f[0], 32)
	s, _ := strconv.ParseFloat(f[1], 32)
	var basis kit.Length
	switch b := f[2]; {
	case b == "auto":
	case strings.HasSuffix(b, "px"):
		n, _ := strconv.ParseFloat(strings.TrimSuffix(b, "px"), 32)
		basis = kit.Px(float32(n))
	default:
		basis = length(t, b)
	}
	return kit.FlexOf(float32(g), float32(s), basis)
}

// build makes the case's tree, collecting its elements in tree order.
func build(t *testing.T, n layoutNode, out *[]*kit.Element) *kit.Element {
	e := &kit.Element{}
	*out = append(*out, e)
	if n.Words != nil {
		e.Style = kit.Style{Display: kit.DisplayFlex, Wrap: true}
		for _, w := range n.Words {
			c := &kit.Element{Style: kit.Style{Flex: kit.FlexNone, Width: kit.Px(w), Height: kit.Px(n.WH)}}
			*out = append(*out, c)
			e.Add(c)
		}
		return e
	}
	st := &e.Style
	for k, v := range n.S {
		switch k {
		case "display":
			if v == "flex" {
				st.Display = kit.DisplayFlex
			}
		case "flexDirection":
			switch v {
			case "column":
				st.Axis = kit.AxisColumn
			case "row-reverse":
				st.Reverse = true
			case "column-reverse":
				st.Axis, st.Reverse = kit.AxisColumn, true
			}
		case "flexWrap":
			st.Wrap = v == "wrap"
		case "justifyContent":
			st.Justify = map[string]kit.Justify{
				"flex-start": kit.JustifyStart, "flex-end": kit.JustifyEnd, "center": kit.JustifyCenter,
				"space-between": kit.JustifySpaceBetween, "space-around": kit.JustifySpaceAround,
				"space-evenly": kit.JustifySpaceEvenly,
			}[v.(string)]
		case "alignItems":
			st.AlignItems = align(t, v.(string))
		case "alignSelf":
			st.AlignSelf = align(t, v.(string))
		case "alignContent":
			st.AlignContent = align(t, v.(string))
		case "flex":
			st.Flex = flexOf(t, v.(string))
		case "width":
			st.Width = length(t, v)
		case "height":
			st.Height = length(t, v)
		case "minWidth":
			st.MinWidth = length(t, v)
		case "maxWidth":
			st.MaxWidth = length(t, v)
		case "minHeight":
			st.MinHeight = length(t, v)
		case "maxHeight":
			st.MaxHeight = length(t, v)
		case "margin":
			st.Margin = edges(t, v)
		case "padding":
			st.Padding = edges(t, v)
		case "border":
			st.Border = edges(t, v)
		case "columnGap":
			st.ColumnGap = length(t, v)
		case "rowGap":
			st.RowGap = length(t, v)
		case "overflow":
			if v == "hidden" {
				st.Overflow = kit.OverflowHidden
			}
		default:
			t.Fatalf("property %q", k)
		}
	}
	for _, k := range n.K {
		e.Add(build(t, k, out))
	}
	return e
}

// TestLayoutMatchesChromium lays out every case and compares each box with the one
// Chromium laid out, to Chromium's own precision (LayoutUnit, 1/64 px) and a little.
func TestLayoutMatchesChromium(t *testing.T) {
	var cases []layoutCase
	readJSON(t, "layout-cases.json", &cases)
	var ref struct {
		Engine string         `json:"engine"`
		Cases  []chromiumCase `json:"cases"`
	}
	readJSON(t, "layout-chromium.json", &ref)
	if len(ref.Cases) != len(cases) {
		t.Fatalf("%d cases, %d laid out by Chromium: run testdata/chromium-layout.mjs", len(cases), len(ref.Cases))
	}
	t.Logf("reference engine: %s", ref.Engine)
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if ref.Cases[i].Name != c.Name {
				t.Fatalf("case %d is %q in the reference", i, ref.Cases[i].Name)
			}
			var els []*kit.Element
			root := build(t, c.Tree, &els)
			wrap := &kit.Element{} // the case's block, which the window lays out at its size
			wrap.Add(root)
			_, _ = open(t, wrap, kit.Size{W: c.W, H: c.H}, env100)
			want := ref.Cases[i].Rects
			if len(want) != len(els) {
				t.Fatalf("%d boxes, %d in the reference", len(els), len(want))
			}
			for j, e := range els {
				got := e.WindowBounds()
				w := want[j]
				exp := kit.R(float32(w[0]), float32(w[1]), float32(w[0]+w[2]), float32(w[1]+w[3]))
				if !closeRect(got, exp, 0.05) {
					t.Errorf("box %d: %v, Chromium %v", j, fmtRect(got), fmtRect(exp))
				}
			}
		})
	}
}

func closeRect(a, b kit.Rect, tol float64) bool {
	d := func(x, y float32) bool { return math.Abs(float64(x-y)) <= tol }
	return d(a.Min.X, b.Min.X) && d(a.Min.Y, b.Min.Y) && d(a.Max.X, b.Max.X) && d(a.Max.Y, b.Max.Y)
}

func fmtRect(r kit.Rect) string {
	return fmt.Sprintf("(%.3f,%.3f %.3fx%.3f)", r.Min.X, r.Min.Y, r.Width(), r.Height())
}
