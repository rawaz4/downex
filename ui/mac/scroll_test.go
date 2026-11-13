//go:build darwin && cgo

package mac

// [mac scroll] The window tells the kit the system's scroll bar style and follows it live.

import (
	"testing"
	"time"

	"downex/ui/kit"
)

func envOf(r *recRoot) []kit.ScrollBarStyle {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []kit.ScrollBarStyle
	for _, e := range r.events {
		if ev, ok := e.(kit.EnvironmentEvent); ok {
			out = append(out, ev.Env.ScrollBars)
		}
	}
	return out
}

func TestScrollBarStyleFollowsSystem(t *testing.T) {
	root := &recRoot{ground: kit.RGBA(240, 240, 240, 1)}
	w := openTestWindow(t, root, kit.Size{W: 300, H: 200})
	t.Cleanup(func() { mainDo(func() { scrollTestStyle(kit.ScrollBarsFluent) }) })

	var sys kit.ScrollBarStyle
	var env kit.Environment
	mainDo(func() { sys, _ = scrollEnvironment(); env = w.Environment() })
	if env.ScrollBars != sys || sys == kit.ScrollBarsFluent {
		t.Fatalf("window's style %v, the system's %v (never Fluent on the Mac)", env.ScrollBars, sys)
	}
	t.Logf("this Mac's scroll bars: %v (1 overlay, 2 legacy)", sys)

	// Each change of the style (AppKit's notice; the style read stands in for the system's)
	// reaches the root as an EnvironmentEvent.
	for _, c := range []struct {
		setting kit.ScrollBarStyle
		want    kit.ScrollBarStyle
	}{{kit.ScrollBarsMacLegacy, kit.ScrollBarsMacLegacy}, {kit.ScrollBarsMacOverlay, kit.ScrollBarsMacOverlay}, {kit.ScrollBarsMacLegacy, kit.ScrollBarsMacLegacy}} {
		mainDo(func() { scrollTestStyle(c.setting) })
		ok := pumpUntil(2*time.Second, func() bool { return w.Environment().ScrollBars == c.want })
		if !ok {
			t.Fatalf("setting %d: window's style %v, want %v", c.setting, w.Environment().ScrollBars, c.want)
		}
		if got := envOf(root); len(got) == 0 || got[len(got)-1] != c.want {
			t.Errorf("setting %d: environment events %v, want the last %v", c.setting, got, c.want)
		}
	}
}

// The window draws the overlay bar the kit asks for: hidden until the content scrolls, then
// the knob colour 3 pt from the edge, and gone after the fade (a real kit window on a real
// NSWindow, read back from its pixels).
func TestOverlayBarOnTheWindow(t *testing.T) {
	mainDo(func() { scrollTestStyle(kit.ScrollBarsMacOverlay) })
	t.Cleanup(func() { mainDo(func() { scrollTestStyle(kit.ScrollBarsFluent) }) })
	p := &scrollProbe{}
	p.bar = kit.NewScrollbar(p)
	p.Style = kit.Style{Width: kit.Pct(100), Height: kit.Pct(100)}
	p.Add(p.bar)
	knob := kit.RGB(0xc9d1cd)
	kw := kit.NewWindow(p, kit.WindowOptions{Title: "scroll test", Palette: func(kit.Environment) kit.Palette {
		return kit.Palette{Ground: kit.RGB(0xffffff), ScrollThumb: knob}
	}})
	w := openTestWindow(t, kw, kit.Size{W: 300, H: 300})
	pump(200 * time.Millisecond)
	var m kit.ScrollMetrics
	mainDo(func() { m = p.bar.Metrics })
	if m != kit.MacOverlayScroll {
		t.Fatalf("the bar's metrics %+v, want MacOverlayScroll", m)
	}
	px := func(x, y float32) [4]uint8 {
		var c [4]uint8
		mainDo(func() { c = winPixel(w, x, y) })
		return c
	}
	// 3000 of content in 300: thumb 294 × 0.1 = 29 long from 3, 6 wide ending 3 from the edge.
	if c := px(294, 10); c[1] != 0xff {
		t.Errorf("before any scroll, the bar's place is %v, want the white ground", c)
	}
	mainDo(func() { p.scrollTo(10) })
	ok := pumpUntil(time.Second, func() bool { c := winPixel(w, 294, 10); return c[1] == 0xd1 })
	if !ok {
		t.Errorf("after a scroll, the thumb's centre is %v, want the knob colour", px(294, 10))
	}
	if c := px(290, 10); c[1] != 0xff {
		t.Errorf("left of the 6 pt thumb %v, want the ground", c)
	}
	if !pumpUntil(2*time.Second, func() bool { c := winPixel(w, 294, 10); return c[1] == 0xff }) {
		t.Errorf("a second after the scroll the thumb is still there: %v", px(294, 10))
	}

	// The legacy style, live: an 11 pt thumb in a 17 pt gutter, there without scrolling.
	mainDo(func() { scrollTestStyle(kit.ScrollBarsMacLegacy) })
	if !pumpUntil(time.Second, func() bool { c := winPixel(w, 291, 10); return c[1] == 0xd1 }) {
		t.Errorf("legacy: the thumb's centre is %v, want the knob colour", px(291, 10))
	}
	if c := px(285, 10); c[1] != 0xff {
		t.Errorf("legacy: left of the 11 pt thumb %v, want the ground", c)
	}
	mainDo(func() {
		if b := p.bar.Bounds(); b.Width() != 17 {
			t.Errorf("legacy: bar %v wide, want 17", b.Width())
		}
	})
}

// winPixel is the window's last frame at (x, y) in points, as RGBA (main thread).
func winPixel(w *Window, x, y float32) [4]uint8 {
	img, err := (&Offscreen{t: w.target}).RGBA()
	if err != nil {
		return [4]uint8{}
	}
	c := img.RGBAAt(int(x*w.ppd), int(y*w.ppd))
	return [4]uint8{c.R, c.G, c.B, c.A}
}

// scrollProbe is 3000 points of content in the window's height with a bar at the end.
type scrollProbe struct {
	kit.Element
	bar    *kit.Scrollbar
	offset kit.DIP
}

func (p *scrollProbe) ScrollRange() (offset, content, view kit.DIP) {
	return p.offset, 3000, p.Bounds().Height()
}
func (p *scrollProbe) ScrollTo(o kit.DIP) { p.scrollTo(o) }
func (p *scrollProbe) scrollTo(o kit.DIP) {
	o = min(max(o, 0), 3000-p.Bounds().Height())
	if o != p.offset {
		p.offset = o
		p.Invalidate()
	}
}
func (p *scrollProbe) PageStep() kit.DIP                               { return p.bar.Metrics.PageFor(p.Bounds().Height()) }
func (p *scrollProbe) ContentWidths(*kit.LayoutContext) (a, b kit.DIP) { return 0, 0 }
func (p *scrollProbe) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP {
	return 0
}
func (p *scrollProbe) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := p.bar.Metrics.BarWidth()
	ctx.Place(p.bar, kit.R(box.Max.X-t, box.Min.Y, box.Max.X, box.Max.Y), t)
}
func (p *scrollProbe) Paint(pc *kit.PaintContext) {
	pc.Canvas.FillRect(kit.RectAt(pc.Origin, p.Bounds().Size()), pc.Palette.Ground)
}
