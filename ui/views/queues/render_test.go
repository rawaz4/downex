package queues

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// The parity gate: every ref-0.9.2 shot of the Queues dialog (7 states, 5 window sizes, scale
// 1 and 1.25, 16 and 20 px text, light and dark: 280 shots) rendered offscreen with the scene in
// the same state, and compared: the dialog's box with the index's measured box, the editor's
// scroll region (client and content heights) and the queue list's with the index's vscroll,
// and the dialog's pixels (reference | native | difference, written with DOWNEX_VIEWS_WRITE=1 as
// native-s2-queues-<state>[-20px]-<theme>-<width>-<scale>.png), each part's best-matching shift
// and the inked width of its texts.

// states are the fixture states of this dialog and how today's steps reach them.
var states = []struct {
	name  string
	reach func(h *viewstest.Harness, d *Dialog)
}{
	{"queues-main", nil},
	{"queues-night", func(h *viewstest.Harness, d *Dialog) { clickRow(h, d, "Night") }},
	{"queues-night-once", func(h *viewstest.Harness, d *Dialog) {
		clickRow(h, d, "Night")
		chooseSegment(h, d, "Once")
	}},
	{"queues-new", func(h *viewstest.Harness, d *Dialog) { h.Click(&d.qlist.newBtn.Element) }},
	{"queues-many-queues", nil},
	{"queues-night-many", func(h *viewstest.Harness, d *Dialog) { clickRow(h, d, "Night") }},
	{"queues-main-many", nil},
}

func clickRow(h *viewstest.Harness, d *Dialog, name string) {
	h.T.Helper()
	for _, r := range d.qlist.rows {
		if r.name.Text() == name {
			h.Click(&r.Element)
			return
		}
	}
	h.T.Fatalf("no queue row %q", name)
}

// chooseSegment chooses a segment of the schedule's Every week | Once control as today's step
// does (a DOM click on its label, which does not scroll it into view): UI Automation's Select.
func chooseSegment(h *viewstest.Harness, d *Dialog, label string) {
	h.T.Helper()
	e := segment(d, label)
	if e == nil {
		h.T.Fatalf("no segment %q", label)
	}
	if err := h.Window.Semantics().Perform(kit.ActionRequest{Node: e.NodeID(), Action: kit.ActionSelect}); err != nil {
		h.T.Fatalf("select %q: %v", label, err)
	}
	h.Settle()
}

// segment is a segment of the schedule's Every week | Once control.
func segment(d *Dialog, label string) *kit.Element {
	for _, c := range d.sched.mode.Track().Children() {
		if strings.Contains(textOf(c), label) {
			return c
		}
	}
	return nil
}

// textOf is the text inside an element (its labels', in order).
func textOf(e *kit.Element) string {
	var b []string
	var walk func(*kit.Element)
	walk = func(e *kit.Element) {
		if e.Style.Hidden {
			return
		}
		if l, ok := e.Widget().(interface{ TextContent() string }); ok {
			if s := l.TextContent(); s != "" {
				b = append(b, s)
			}
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(e)
	return strings.Join(b, " ")
}

// open shows the dialog as App.svelte's openQueues does (Manage queues…: the main queue).
func open(h *viewstest.Harness) *Dialog {
	d := New(h.App, views.QueuesRequest{})
	h.App.Dialogs.Show(d)
	h.Settle()
	return d
}

// vscroll is a scroll region the index measured in a shot.
type vscroll struct {
	El      string  `json:"el"`
	By      float64 `json:"by"`
	Client  float64 `json:"client"`
	Content float64 `json:"content"`
}

var (
	scrollOnce sync.Once
	scrolls    map[string][]vscroll
	loadMs     map[string]float64
)

// msSinceLoad is when the index's shot was taken after the reference page loaded.
func msSinceLoad(t *testing.T, file string) float64 {
	scrollsOf(t, file)
	return loadMs[file]
}

// scrollsOf is the index's vscroll of a shot's file.
func scrollsOf(t *testing.T, file string) []vscroll {
	scrollOnce.Do(func() {
		scrolls, loadMs = map[string][]vscroll{}, map[string]float64{}
		b, err := os.ReadFile(filepath.Join(viewstest.RefDir(), "index.json"))
		if err != nil {
			return
		}
		b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf}) // the index's UTF-8 BOM
		var idx struct {
			Shots []struct {
				File    string  `json:"file"`
				Ms      float64 `json:"msSinceLoad"`
				Measure *struct {
					VScroll []vscroll `json:"vscroll"`
				} `json:"measure"`
			} `json:"shots"`
		}
		if err := json.Unmarshal(b, &idx); err != nil {
			t.Logf("index: %v", err)
			return
		}
		for _, s := range idx.Shots {
			loadMs[s.File] = s.Ms
			if s.Measure != nil {
				scrolls[s.File] = s.Measure.VScroll
			}
		}
	})
	return scrolls[file]
}

// parity is one shot's measures.
type parity struct {
	shot                       string
	box                        viewstest.Delta
	edClient, edContent        [2]float64 // reference, native
	listClient, listContent    [2]float64
	listScrolls, listScrollRef bool
	mean, differing            float64
	arrival                    bool
	regions                    []regionShift
	texts                      []textWidth
	// noticeMean is the mean difference where the demo's arrival notice shows.
	noticeMean float64
}

// regionShift is the best-matching shift of a part of the dialog (device pixels in the
// image, reported in DIPs): how far the native part is drawn from where the reference has it.
type regionShift struct {
	name   string
	dx, dy float64 // DIP
	best   float64 // mean difference there (0-255)
	// covered: the reference's arrival notice is over the part (not measured).
	covered bool
}

// textWidth is a text's inked width in the reference and natively (DIPs), measured in the
// text's native box widened by 3 DIPs at each side.
type textWidth struct {
	name     string
	ref, nat float64
}

func (w textWidth) pct() float64 {
	if w.ref == 0 {
		return 0
	}
	return 100 * (w.nat - w.ref) / w.ref
}

// gray is an image's luminance (0-255), one value per pixel.
type gray struct {
	w, h int
	v    []float32
}

func grayOf(img image.Image) gray {
	b := img.Bounds()
	g := gray{w: b.Dx(), h: b.Dy(), v: make([]float32, b.Dx()*b.Dy())}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			cr, cg, cb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			g.v[y*g.w+x] = 0.2126*float32(cr>>8) + 0.7152*float32(cg>>8) + 0.0722*float32(cb>>8)
		}
	}
	return g
}

func (g gray) at(x, y int) (float32, bool) {
	if x < 0 || y < 0 || x >= g.w || y >= g.h {
		return 0, false
	}
	return g.v[y*g.w+x], true
}

// shiftOf is viewstest.BestShift on luminance arrays: the shift (device pixels, within ±m) of
// the native image that best matches the reference in r.
func shiftOf(ref, nat gray, r image.Rectangle, m int) (dx, dy int, best float64) {
	measure := func(sx, sy int) float64 {
		var sum float64
		n := 0
		for y := r.Min.Y + m; y < r.Max.Y-m; y++ {
			for x := r.Min.X + m; x < r.Max.X-m; x++ {
				a, ok1 := ref.at(x, y)
				b, ok2 := nat.at(x+sx, y+sy)
				if !ok1 || !ok2 {
					continue
				}
				sum += math.Abs(float64(a - b))
				n++
			}
		}
		if n == 0 {
			return math.Inf(1)
		}
		return sum / float64(n)
	}
	best = measure(0, 0)
	for sy := -m; sy <= m; sy++ {
		for sx := -m; sx <= m; sx++ {
			if v := measure(sx, sy); v < best-0.01 {
				best, dx, dy = v, sx, sy
			}
		}
	}
	return dx, dy, best
}

// inkSpan is the horizontal extent (device pixels) of the pixels in r whose luminance differs
// from r's start-top corner by more than 48 (text on its ground).
func inkSpan(g gray, r image.Rectangle) (lo, hi int, ok bool) {
	r = r.Intersect(image.Rect(0, 0, g.w, g.h))
	if r.Empty() {
		return 0, 0, false
	}
	bg, _ := g.at(r.Min.X, r.Min.Y)
	lo, hi = r.Max.X, r.Min.X-1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if v, _ := g.at(x, y); math.Abs(float64(v-bg)) > 48 {
				lo, hi = min(lo, x), max(hi, x)
			}
		}
	}
	return lo, hi, hi >= lo
}

func TestParityWithReferences(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no offscreen renderer: %v", err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skipf("references: %v", err)
	}
	only := os.Getenv("DOWNEX_QUEUES_ONLY") // e.g. "queues-night/1200/1/16/light"
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	var results []parity
	sizes := [][2]int{{1200, 760}, {1190, 752}, {960, 608}, {900, 600}, {1920, 1080}}
	// Under the race detector (a pixel comparison, not a concurrency test: 280 renders took
	// over 10 minutes) each state runs in raceCombos of its 40 combinations, a different pair
	// per state, unless DOWNEX_QUEUES_ONLY narrows the run or DOWNEX_VIEWS_FULL=1 asks for all.
	sample := raceOn && only == "" && os.Getenv("DOWNEX_VIEWS_FULL") == ""
	for si, st := range states {
		combo := -1
		for _, sz := range sizes {
			for _, scale := range []float32{1, 1.25} {
				for _, font := range []int{16, 20} {
					for _, theme := range []string{"light", "dark"} {
						combo++
						key := fmt.Sprintf("%s/%d/%v/%d/%s", st.name, sz[0], scale, font, theme)
						if only != "" && !strings.HasPrefix(key, only) {
							continue
						}
						if sample && !raceSampled(si, combo) {
							continue
						}
						o := viewstest.Options{Width: sz[0], Height: sz[1], Scale: scale, Font: font, Theme: theme, Fixture: st.name, Real: true}
						shot, ok := refs.For(o)
						if !ok {
							t.Errorf("%s: no reference", key)
							continue
						}
						// The reference demo's timer ran: in some shots a browser hand-off had
						// arrived (one more download waiting in the main queue, "running 14", and
						// its notice at the window's bottom-end corner). Those are told by the
						// notice (the plain render differs there) or, when the editor scrolls,
						// by its content height (one more row), and rendered with the arrival.
						p, ok := renderState(t, refs, shot, o, st.reach, false, false)
						if ok {
							arrival := p.noticeMean > 12
							if p.edClient[0] >= 0 && !arrival {
								q, ok2 := renderState(t, refs, shot, o, st.reach, true, false)
								arrival = ok2 && math.Abs(q.edContent[1]-q.edContent[0])+1 < math.Abs(p.edContent[1]-p.edContent[0])
							}
							p, ok = renderState(t, refs, shot, o, st.reach, arrival, write)
						}
						if ok {
							p.shot = key
							results = append(results, p)
						}
					}
				}
			}
		}
	}
	report(t, results)
}

// raceCombos is how many of a state's 40 combinations (size, scale, text size, theme) run
// under the race detector.
const raceCombos = 2

// raceSampled reports whether combination combo (0 to 39) of state number si runs under the
// race detector: raceCombos of them, spread over the 40 and turning with the state, so that the
// states together cover every size, scale, text size and theme.
func raceSampled(si, combo int) bool {
	const all, step = 40, 40 / raceCombos
	return (combo+all-(si*7)%all)%step == 0
}

func renderState(t *testing.T, refs *viewstest.Refs, shot *viewstest.Shot, o viewstest.Options, reach func(*viewstest.Harness, *Dialog), arrival, write bool) (parity, bool) {
	p := parity{arrival: arrival}
	h := viewstest.New(t, o)
	defer h.Close()
	h.App.Caps.WakeTimers = true // the reference demo's capabilities (Windows)
	if arrival {
		if _, err := h.Mock.BrowserHandoff(nil); err != nil {
			t.Errorf("%s: hand-off: %v", shot.File, err)
		}
		h.Mock.Tick(1) // its events
		h.Settle()
	}
	d := open(h)
	if reach != nil {
		reach(h, d)
	}
	h.Settle()
	img, err := h.Render()
	if err != nil {
		t.Errorf("%s: render: %v", shot.File, err)
		return p, false
	}
	ref, err := refs.Image(shot)
	if err != nil {
		t.Errorf("%s: %v", shot.File, err)
		return p, false
	}
	card := d.Card().WindowBounds()
	p.box = viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})[0]
	_, content, view := d.shell.pane.ScrollRange()
	for _, v := range scrollsOf(t, shot.File) {
		switch v.El {
		case "div.editor":
			p.edClient[0], p.edContent[0] = v.Client, v.Content
		case "ul.":
			p.listClient[0], p.listContent[0], p.listScrollRef = v.Client, v.Content, true
		}
	}
	if p.edClient[0] == 0 { // not scrolled: the editor's client is the content
		p.edClient[0], p.edContent[0] = -1, -1
	}
	p.edClient[1], p.edContent[1] = float64(view), float64(content)
	_, lc, lv := d.qlist.pane.ScrollRange()
	p.listClient[1], p.listContent[1], p.listScrolls = float64(lv), float64(lc), d.qlist.pane.Overflows()
	c := viewstest.Compare(ref, img, p.box.Ref, card.Min, o.Scale)
	p.mean, p.differing = c.Mean, c.Differing
	// The parts of the dialog, each matched alone (the dialog's box is the index's; inside it
	// the index has no boxes, so the parts are measured on the images).
	gr, gn := grayOf(ref), grayOf(img)
	px := func(v kit.DIP) int { return int(math.Round(float64(v * kit.DIP(o.Scale)))) }
	// The demo's arrival notice (views/app's, not drawn here) covers the window's bottom-end
	// corner of such a reference: what is under it is not measured.
	notice := kit.R(kit.DIP(o.Width)-400, kit.DIP(o.Height)-180, kit.DIP(o.Width), kit.DIP(o.Height))
	{
		n := notice.Intersect(card)
		_, _, p.noticeMean = shiftOf(gr, gn, image.Rect(px(n.Min.X), px(n.Min.Y), px(n.Max.X), px(n.Max.Y)), 0)
	}
	for _, pr := range regionProbes(d) {
		if pr.e.Style.Hidden || pr.e.Window() == nil {
			continue
		}
		b := pr.e.WindowBounds().Intersect(card)
		if d.shell.pane.Element.Contains(pr.e) {
			b = b.Intersect(d.shell.pane.WindowBounds()) // what is in view
		}
		if b.Height() < 16 || b.Width() < 16 {
			continue
		}
		r := image.Rect(px(b.Min.X), px(b.Min.Y), px(b.Max.X), px(b.Max.Y))
		dx, dy, best := shiftOf(gr, gn, r, 4)
		covered := arrival && !b.Intersect(notice).Empty()
		p.regions = append(p.regions, regionShift{name: pr.name, dx: float64(dx) / float64(o.Scale), dy: float64(dy) / float64(o.Scale), best: best, covered: covered})
	}
	for _, pr := range textProbes(d) {
		if pr.e.Style.Hidden || pr.e.Window() == nil {
			continue
		}
		b := pr.e.WindowBounds()
		// only texts wholly in view (the editor's pane clips what is scrolled away)
		view := card
		if d.shell.pane.Element.Contains(pr.e) {
			view = d.shell.pane.WindowBounds()
		}
		if b.Empty() || b.Max.Y > view.Max.Y || b.Min.Y < view.Min.Y || arrival && !b.Intersect(notice).Empty() {
			continue
		}
		r := image.Rect(px(b.Min.X-3), px(b.Min.Y), px(b.Max.X+3), px(b.Max.Y))
		rl, rh, ok1 := inkSpan(gr, r)
		nl, nh, ok2 := inkSpan(gn, r)
		if !ok1 || !ok2 {
			continue
		}
		s := float64(o.Scale)
		p.texts = append(p.texts, textWidth{name: pr.name, ref: float64(rh-rl+1) / s, nat: float64(nh-nl+1) / s})
	}
	if write {
		state := strings.TrimPrefix(shot.Name, "queues-")
		if o.Font == 20 {
			state += "-20px"
		}
		name := viewstest.NativeName("queues", state, o.Theme, o.Width, float64(o.Scale))
		if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), name), c.Sheet); err != nil {
			t.Error(err)
		}
	}
	return p, true
}

type probe struct {
	name string
	e    *kit.Element
}

// regionProbes are the dialog's parts: the list column, the editor's sections and cards, the
// footer's buttons.
func regionProbes(d *Dialog) []probe {
	ps := []probe{
		{"list", &d.qlist.Element},
		{"new-btn", &d.qlist.newBtn.Element},
		{"head", &d.head.Element},
		{"settings-card", &d.settings.grid.Element},
		{"schedule-card", &d.sched.card.Element},
		{"order", &d.order.Element},
		{"close-btn", &d.closeBtn.Element},
		{"save-btn", &d.saveBtn.Element},
		{"delete", d.delGroup},
	}
	for i, r := range d.qlist.rows {
		ps = append(ps, probe{fmt.Sprintf("queue-row-%d", i+1), &r.Element})
	}
	return ps
}

// textProbes are texts whose inked width is compared.
func textProbes(d *Dialog) []probe {
	ps := []probe{
		{"h3", &d.head.h3.Element},
		{"summary", &d.head.summary.Element},
		{"new-btn", &d.qlist.newBtn.Label().Element},
		{"save-btn", &d.saveBtn.Label().Element},
		{"close-btn", &d.closeBtn.Label().Element},
		{"order-hint", &d.order.hint.Element},
	}
	for i, r := range d.qlist.rows {
		ps = append(ps, probe{fmt.Sprintf("queue-name-%d", i+1), &r.name.Element})
	}
	n := 0
	for _, id := range d.order.ids {
		if n == 3 {
			break
		}
		r := d.order.rows[id]
		ps = append(ps, probe{fmt.Sprintf("order-name-%d", n+1), &r.name.Element}, probe{fmt.Sprintf("order-state-%d", n+1), &r.text.Element})
		n++
	}
	return ps
}

func report(t *testing.T, rs []parity) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].shot < rs[j].shot })
	var worstBox, worstEd float64
	for _, r := range rs {
		ed := "fits (ref fits)"
		switch {
		case r.edClient[0] < 0 && r.edContent[1] > r.edClient[1]+0.5:
			ed = fmt.Sprintf("SCROLLS by %.1f, ref fits", r.edContent[1]-r.edClient[1])
		case r.edClient[0] >= 0:
			dc, dn := r.edClient[1]-r.edClient[0], r.edContent[1]-r.edContent[0]
			ed = fmt.Sprintf("client %.0f/%.1f (%+.1f) content %.0f/%.1f (%+.1f)", r.edClient[0], r.edClient[1], dc, r.edContent[0], r.edContent[1], dn)
			worstEd = math.Max(worstEd, math.Max(math.Abs(dc), math.Abs(dn)))
		}
		list := ""
		if r.listScrollRef || r.listScrolls {
			list = fmt.Sprintf(" list client %.0f/%.1f content %.0f/%.1f", r.listClient[0], r.listClient[1], r.listContent[0], r.listContent[1])
		}
		worstBox = math.Max(worstBox, float64(r.box.Max()))
		note := fmt.Sprintf(" (notice area %.1f)", r.noticeMean)
		if r.arrival {
			note = fmt.Sprintf(" (with the demo's arrival; notice area %.1f)", r.noticeMean)
		}
		t.Logf("%-40s box %v | editor %s%s | mean %.2f, %.1f%% differ%s", r.shot, r.box, ed, list, r.mean, 100*r.differing, note)
		var parts []string
		for _, g := range r.regions {
			if g.covered {
				parts = append(parts, g.name+" (under the reference's notice)")
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %+.1f,%+.1f", g.name, g.dx, g.dy))
			k := g.name
			if strings.HasPrefix(k, "queue-row-") {
				k = "queue-row"
			}
			w := worstRegion[k]
			w.n++
			w.dx, w.dy = math.Max(w.dx, math.Abs(g.dx)), math.Max(w.dy, math.Abs(g.dy))
			if math.Abs(g.dx) > 1 || math.Abs(g.dy) > 1 {
				w.over++
				w.at = append(w.at, r.shot)
			}
			worstRegion[k] = w
		}
		var texts []string
		for _, x := range r.texts {
			texts = append(texts, fmt.Sprintf("%s %.1f/%.1f (%+.1f%%)", x.name, x.ref, x.nat, x.pct()))
			k := x.name
			if i := strings.LastIndexByte(k, '-'); i > 0 && k[i+1] >= '0' && k[i+1] <= '9' {
				k = k[:i]
			}
			w := worstText[k]
			w.n++
			if math.Abs(x.pct()) > math.Abs(w.pct) {
				w.pct, w.at = x.pct(), r.shot
			}
			if math.Abs(x.nat-x.ref) > math.Max(1, 0.01*x.ref) {
				w.over++
			}
			worstText[k] = w
		}
		t.Logf("    parts (DIP): %s", strings.Join(parts, "; "))
		t.Logf("    text widths ref/native (DIP): %s", strings.Join(texts, "; "))
	}
	t.Logf("%d shots: worst dialog box delta %.2f DIP, worst editor scroll delta %.2f DIP", len(rs), worstBox, worstEd)
	keys := make([]string, 0, len(worstRegion))
	for k := range worstRegion {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := worstRegion[k]
		t.Logf("part %-14s in %3d shots: worst |dx| %.1f |dy| %.1f DIP; %d shots over 1 DIP %v", k, w.n, w.dx, w.dy, w.over, w.at)
	}
	keys = keys[:0]
	for k := range worstText {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		w := worstText[k]
		t.Logf("text %-12s in %3d measures: worst width delta %+.2f%% (%s); %d over max(1 DIP, 1%%)", k, w.n, w.pct, w.at, w.over)
	}
}

var (
	worstRegion = map[string]struct {
		n, over int
		dx, dy  float64
		at      []string
	}{}
	worstText = map[string]struct {
		n, over int
		pct     float64
		at      string
	}{}
)
