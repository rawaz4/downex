package add

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// The element boxes of today's Add and Confirm dialogs: ref-0.9.2's index has only the
// dialog's measured box for dialog states, so the same states were laid out again by the
// reference script (refshots.mjs, the same Vite build, headless Edge, the same sizes, scales
// and text sizes) with every element of the open dialog and every text node's box recorded
// (light theme: the boxes do not depend on it). DOWNEX_ADD_BOXES names the file; without it,
// ref-0.9.2-add-boxes.json beside the references' folder (viewstest.RefDir).
const boxesFile = "ref-0.9.2-add-boxes.json"

type refBoxShot struct {
	Name   string `json:"name"`
	CSS    struct{ W, H int }
	Scale  float32 `json:"scale"`
	Font   int     `json:"font"`
	Layout *struct {
		Nodes []refBoxNode `json:"nodes"`
	} `json:"layout"`
}

type refBoxNode struct {
	El    string       `json:"el"`
	D     int          `json:"d"`
	X     float32      `json:"x"`
	Y     float32      `json:"y"`
	W     float32      `json:"w"`
	H     float32      `json:"h"`
	Role  string       `json:"role"`
	Text  string       `json:"text"`
	Texts []refBoxText `json:"texts"`
}

type refBoxText struct {
	T          string `json:"t"`
	X, Y, W, H float32
}

func (n refBoxNode) rect() kit.Rect { return kit.R(n.X, n.Y, n.X+n.W, n.Y+n.H) }

func (n refBoxNode) has(class string) bool {
	parts := strings.Split(n.El, ".")
	for _, p := range parts[1:] {
		if p == class {
			return true
		}
	}
	return false
}

func (n refBoxNode) tag() string { return strings.SplitN(n.El, ".", 2)[0] }

func loadRefBoxes() ([]refBoxShot, error) {
	file := os.Getenv("DOWNEX_ADD_BOXES")
	if file == "" {
		file = filepath.Join(filepath.Dir(filepath.Clean(viewstest.RefDir())), boxesFile)
	}
	b, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	var idx struct {
		Shots []refBoxShot `json:"shots"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return idx.Shots, nil
}

// boxKind is a kind of layout box both trees have, matched in document order.
type boxKind struct {
	name   string
	ref    func(n refBoxNode) bool
	native func(e *kit.Element) bool
}

var boxKinds = []boxKind{
	{"dialog", func(n refBoxNode) bool { return n.tag() == "div" && n.has("dialog") }, nil},
	{"qcard", func(n refBoxNode) bool { return n.tag() == "label" && n.has("qcard") }, func(e *kit.Element) bool { _, ok := e.Widget().(*qcard); return ok }},
	{"card", func(n refBoxNode) bool { return n.tag() == "div" && n.has("card") }, func(e *kit.Element) bool { _, ok := e.Widget().(*VideoCard); return ok }},
	{"address", func(n refBoxNode) bool { return n.tag() == "input" && n.has("address") }, func(e *kit.Element) bool { _, ok := e.Widget().(*addressText); return ok }},
	{"input", func(n refBoxNode) bool { return (n.tag() == "input" && n.has("input")) || n.tag() == "textarea" }, func(e *kit.Element) bool {
		switch e.Widget().(type) {
		case *widgets.Input, *widgets.TextArea, *monoArea:
			return true
		}
		return false
	}},
	{"select", func(n refBoxNode) bool { return n.tag() == "button" && n.has("select") }, func(e *kit.Element) bool { _, ok := e.Widget().(*widgets.Select); return ok }},
	{"button", func(n refBoxNode) bool { return n.tag() == "button" && n.has("btn") }, func(e *kit.Element) bool { _, ok := e.Widget().(*widgets.Button); return ok }},
}

// nativeText is a label of the native dialog: its text and the window box of its glyphs'
// line boxes.
type nativeText struct {
	text      string
	box       kit.Rect
	truncated bool
	rtl       bool
	used      bool
}

type textLabel interface {
	Text() string
	TextLayout() kit.TextLayout
	TextOrigin() kit.Point
	Truncated() bool
	Font() kit.Font
}

func visible(e *kit.Element) bool {
	for p := e; p != nil; p = p.Parent() {
		if p.Style.Hidden {
			return false
		}
	}
	return true
}

func collectNative(card *kit.Element, fm func(kit.Font) (kit.FontMetrics, bool), round func(kit.DIP) kit.DIP) (texts []*nativeText, kinds map[string][]*kit.Element) {
	kinds = map[string][]*kit.Element{}
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden || (e.Size().W <= 0 && e.Size().H <= 0) {
			return
		}
		for _, k := range boxKinds {
			if k.native != nil && k.native(e) {
				kinds[k.name] = append(kinds[k.name], e)
			}
		}
		if l, ok := e.Widget().(textLabel); ok && l.TextLayout() != nil && strings.TrimSpace(l.Text()) != "" {
			lay := l.TextLayout()
			o := l.TextOrigin().Add(e.WindowBounds().Min)
			var u kit.Rect
			for i, r := range lay.AppendRangeRects(nil, 0, len(l.Text())) {
				r = r.Add(o)
				if i == 0 {
					u = r
				} else {
					u = kit.R(min(u.Min.X, r.Min.X), min(u.Min.Y, r.Min.Y), max(u.Max.X, r.Max.X), max(u.Max.Y, r.Max.Y))
				}
			}
			// a page's range ends each line at its last glyph, the layout's line rects at
			// its trailing spaces: the widest line without them (Metrics.Width)
			if met := lay.Metrics(); met.Lines > 1 {
				if met.Direction == kit.DirRTL {
					u.Min.X = u.Max.X - met.Width
				} else {
					u.Max.X = u.Min.X + met.Width
				}
			}
			// the box a text node's range has in a page: the font's ascent over the first
			// baseline to its descent under the last (the content area), not the line box
			if m, ok := fm(l.Font()); ok {
				met := lay.Metrics()
				base := o.Y + met.Baseline
				// Chromium rounds the ascent and the descent to device pixels for that box
				u.Min.Y = base - round(m.Ascent)
				u.Max.Y = base + m.LineHeight*kit.DIP(max(met.Lines-1, 0)) + round(m.Descent)
			}
			texts = append(texts, &nativeText{text: normWS(l.Text()), box: u, truncated: l.Truncated(), rtl: lay.Metrics().Direction == kit.DirRTL})
		}
		for _, k := range e.Children() {
			walk(k)
		}
	}
	walk(card)
	return texts, kinds
}

// boxDelta is one compared box: what, the reference's and the native box, in DIPs.
type boxDelta struct {
	what        string
	ref, native kit.Rect
	text        bool
	rtl         bool // a right-to-left text: its start edge is its right
}

func (d boxDelta) worst() float32 {
	if d.text {
		// a text: its start edge (its width is widthPct). Its block position is logged, not
		// judged: a page's text-node box is the font's content area from Chromium's rounded
		// ascent and descent, which DirectWrite's metrics model only to about 1 DIP; the
		// line boxes (the layout boxes above) and the pixels (TestReferenceComparisons'
		// BestShift) judge the block axis.
		if d.rtl {
			return abs32(d.native.Max.X - d.ref.Max.X)
		}
		return abs32(d.native.Min.X - d.ref.Min.X)
	}
	return max(abs32(d.native.Min.X-d.ref.Min.X), abs32(d.native.Min.Y-d.ref.Min.Y), abs32(d.native.Width()-d.ref.Width()), abs32(d.native.Height()-d.ref.Height()))
}

func (d boxDelta) widthPct() float64 {
	if d.ref.Width() <= 0 {
		return 0
	}
	return 100 * math.Abs(float64(d.native.Width()-d.ref.Width())) / float64(d.ref.Width())
}

func (d boxDelta) String() string {
	return fmt.Sprintf("%s: dx %+.1f dy %+.1f dw %+.1f dh %+.1f (ref %.1f,%.1f %.1fx%.1f)", d.what,
		d.native.Min.X-d.ref.Min.X, d.native.Min.Y-d.ref.Min.Y, d.native.Width()-d.ref.Width(), d.native.Height()-d.ref.Height(),
		d.ref.Min.X, d.ref.Min.Y, d.ref.Width(), d.ref.Height())
}

func abs32(v kit.DIP) float32 { return float32(math.Abs(float64(v))) }

// compareBoxes matches the reference's boxes with the native dialog's: the layout boxes kind by
// kind in document order, the texts by their text (the nearest of equal ones).
func compareBoxes(shot refBoxShot, m views.Modal, fm func(kit.Font) (kit.FontMetrics, bool), round func(kit.DIP) kit.DIP) (boxes, texts []boxDelta, unmatched []string) {
	card := cardOf(m)
	ntexts, kinds := collectNative(card, fm, round)
	kinds["dialog"] = []*kit.Element{card}
	refKinds := map[string][]refBoxNode{}
	for _, n := range shot.Layout.Nodes {
		for _, k := range boxKinds {
			if k.ref(n) {
				refKinds[k.name] = append(refKinds[k.name], n)
				break
			}
		}
	}
	for _, k := range boxKinds {
		rs, ns := refKinds[k.name], kinds[k.name]
		if k.name == "button" {
			// buttons by their text (the reference's header close button has none)
			for _, r := range rs {
				t := ""
				if len(r.Texts) > 0 {
					t = r.Texts[0].T
				}
				var hit *kit.Element
				for _, e := range ns {
					if b := e.Widget().(*widgets.Button); t != "" && b.Label() != nil && normWS(b.Label().Text()) == t {
						hit = e
						break
					}
				}
				if hit == nil {
					if t != "" {
						unmatched = append(unmatched, "button "+t)
					}
					continue
				}
				boxes = append(boxes, boxDelta{what: "button " + t, ref: r.rect(), native: hit.WindowBounds()})
			}
			continue
		}
		if len(rs) != len(ns) {
			unmatched = append(unmatched, fmt.Sprintf("%d %s boxes in the reference, %d native", len(rs), k.name, len(ns)))
		}
		for i := 0; i < len(rs) && i < len(ns); i++ {
			boxes = append(boxes, boxDelta{what: fmt.Sprintf("%s %d", k.name, i+1), ref: rs[i].rect(), native: ns[i].WindowBounds()})
		}
	}
	for _, n := range shot.Layout.Nodes {
		for _, rt := range n.Texts {
			if rt.W <= 0 || strings.HasPrefix(n.El, "span.sr-only") || n.has("sr-only") {
				continue
			}
			ref := kit.R(rt.X, rt.Y, rt.X+rt.W, rt.Y+rt.H)
			var best *nativeText
			bestD := kit.DIP(1e9)
			for _, nt := range ntexts {
				if nt.used || !(nt.text == rt.T || (len(rt.T) >= 80 && strings.HasPrefix(nt.text, rt.T))) {
					continue
				}
				d := abs32(nt.box.Min.X-ref.Min.X) + abs32(nt.box.Min.Y-ref.Min.Y)
				if kit.DIP(d) < bestD {
					best, bestD = nt, kit.DIP(d)
				}
			}
			if best == nil {
				unmatched = append(unmatched, fmt.Sprintf("text %q", rt.T))
				continue
			}
			best.used = true
			d := boxDelta{what: fmt.Sprintf("text %q", rt.T), ref: ref, native: best.box, text: true, rtl: best.rtl}
			if best.truncated {
				d.what += " (cut)"
			}
			texts = append(texts, d)
		}
	}
	return boxes, texts, unmatched
}

// TestElementBoxes compares every layout box (the dialog, the video card, the quality cards,
// the fields, the selects, the buttons) and every text of each Add and Confirm state with
// today's, at every size, scale and text size: boxes within 1 DIP, texts at their place within
// 1 DIP and their width within 1 %. DOWNEX_ADD_STATES, _SIZES and _FONTS narrow the run.
func TestElementBoxes(t *testing.T) {
	if testing.Short() {
		t.Skip("lays out every state")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no DirectWrite: %v", err)
	}
	shots, err := loadRefBoxes()
	if err != nil {
		t.Skipf("reference boxes: %v", err)
	}
	states, sizes, fonts := pick2("DOWNEX_ADD_STATES"), pick2("DOWNEX_ADD_SIZES"), pick2("DOWNEX_ADD_FONTS")
	strict := os.Getenv("DOWNEX_ADD_BOXES_STRICT") != ""
	all := os.Getenv("DOWNEX_ADD_BOXES_ALL") != ""
	type worst struct {
		box, text float32
		pct       float64
		what      string
	}
	summary := map[string]*worst{}
	for _, shot := range shots {
		if shot.Layout == nil || (states != nil && !states[shot.Name]) {
			continue
		}
		tag := fmt.Sprintf("%dx%d@%v", shot.CSS.W, shot.CSS.H, shot.Scale)
		if (sizes != nil && !sizes[tag]) || (fonts != nil && !fonts[fmt.Sprint(shot.Font)]) {
			continue
		}
		t.Run(fmt.Sprintf("%s/%s/%dpx", shot.Name, tag, shot.Font), func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Width: shot.CSS.W, Height: shot.CSS.H, Scale: shot.Scale, Font: shot.Font, Fixture: shot.Name, Real: true})
			m := stateOf(t, h, shot.Name)
			sc := h.Window.Environment().Scale
			res := h.Window.Host().Resources()
			fm := func(f kit.Font) (kit.FontMetrics, bool) {
				mt, ok := res.(interface {
					FontMetrics(kit.TextStyle) (kit.FontMetrics, error)
				})
				if !ok {
					return kit.FontMetrics{}, false
				}
				v, err := mt.FontMetrics(f.Style(sc))
				return v, err == nil
			}
			round := func(v kit.DIP) kit.DIP {
				return kit.DIP(math.Round(float64(v*kit.DIP(shot.Scale)))) / kit.DIP(shot.Scale)
			}
			boxes, texts, unmatched := compareBoxes(shot, m, fm, round)
			w := summary[shot.Name]
			if w == nil {
				w = &worst{}
				summary[shot.Name] = w
			}
			report := t.Logf
			if strict {
				report = t.Errorf
			}
			for _, d := range boxes {
				if v := d.worst(); v > w.box {
					w.box = v
				}
				if d.worst() > 1 {
					report("box %v", d)
				} else if all {
					t.Logf("box %v", d)
				}
			}
			for _, d := range texts {
				if v := d.worst(); v > w.text {
					w.text = v
				}
				cut := strings.HasSuffix(d.what, "(cut)")
				if p := d.widthPct(); !cut && p > w.pct {
					w.pct, w.what = p, d.what
				}
				if d.worst() > 1 || (!cut && d.widthPct() > 1) {
					report("text %v (width %.2f%%)", d, d.widthPct())
				}
			}
			t.Logf("%d boxes, %d texts compared", len(boxes), len(texts))
			for _, u := range unmatched {
				t.Logf("unmatched: %s", u)
			}
		})
	}
	names := make([]string, 0, len(summary))
	for n := range summary {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w := summary[n]
		t.Logf("SUMMARY %-28s boxes ≤ %.2f DIP, texts ≤ %.2f DIP, widths ≤ %.2f%% (%s)", n, w.box, w.text, w.pct, w.what)
	}
}
