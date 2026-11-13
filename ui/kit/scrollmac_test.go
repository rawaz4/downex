package kit_test

// [mac scroll] The macOS scroll bars (scrollmac.go), against the numbers measured on WKWebView
// snapshots on macOS 26, and the Fluent bars left as they were.

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

var (
	envLegacy  = kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, ScrollBars: kit.ScrollBarsMacLegacy}
	envOverlay = kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, ScrollBars: kit.ScrollBarsMacOverlay}
)

// macPane is a ScrollTarget of its own, 1000 DIPs of content, laid out as the kit's owners
// lay a bar out: a gutter of Thickness, the bar BarWidth wide at the end.
type macPane struct {
	kit.Element
	bar    *kit.Scrollbar
	offset kit.DIP
	down   int // presses that reached the pane itself
}

func (p *macPane) ScrollRange() (offset, content, view kit.DIP) {
	return p.offset, 1000, p.Bounds().Height()
}
func (p *macPane) ScrollTo(o kit.DIP) {
	o = min(max(o, 0), 1000-p.Bounds().Height())
	if o != p.offset {
		p.offset = o
		p.Invalidate()
	}
}
func (p *macPane) PageStep() kit.DIP                               { return p.bar.Metrics.PageFor(p.Bounds().Height()) }
func (p *macPane) ContentWidths(*kit.LayoutContext) (a, b kit.DIP) { return 0, 0 }
func (p *macPane) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP {
	return 0
}
func (p *macPane) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := p.bar.Metrics.BarWidth()
	ctx.Place(p.bar, kit.R(box.Max.X-t, box.Min.Y, box.Max.X, box.Max.Y), t)
}
func (p *macPane) HandlePointer(in *kit.PointerInput) bool {
	if in.Action == kit.PointerDown {
		p.down++
	}
	return true
}

func openMacPane(t *testing.T, env kit.Environment, tune ...func(*kit.WindowOptions)) (*macPane, *kittest.Loop) {
	t.Helper()
	p := &macPane{}
	p.bar = kit.NewScrollbar(p)
	p.Style = kit.Style{Width: kit.Px(200), Height: kit.Px(200)}
	p.Add(p.bar)
	_, loop := open(t, box(kit.Style{}, p), kit.Size{W: 200, H: 200}, env, tune...)
	return p, loop
}

// thumbOps are the rounded rectangles the bar drew in the last paint.
func thumbOps(loop *kittest.Loop) []kittest.Op {
	var out []kittest.Op
	for _, o := range loop.Canvas.Ops {
		if o.Kind == "FillRoundedRect" {
			out = append(out, o)
		}
	}
	return out
}

func paintAll(loop *kittest.Loop) {
	loop.Canvas.Ops = nil
	loop.Host.Invalidate(kit.R(0, 0, 200, 200))
	loop.Step()
}

func click(loop *kittest.Loop, at kit.Point, mods kit.Modifiers) {
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Mods: mods})
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft, Mods: mods})
}

// The Fluent bars are untouched: the zero environment gives FluentScroll's metrics, arrows and
// all, and a page of 87.5% of the view.
func TestFluentScrollUntouched(t *testing.T) {
	if kit.ScrollMetricsFor(kit.ScrollBarsFluent) != kit.FluentScroll {
		t.Fatal("ScrollMetricsFor(ScrollBarsFluent) is not FluentScroll")
	}
	want := kit.ScrollMetrics{Thickness: 15, Button: 18, Thumb: 8, MinThumb: 17, Line: 40, Page: 0.875,
		RepeatDelay: 250 * time.Millisecond, RepeatEvery: 50 * time.Millisecond}
	if kit.FluentScroll != want {
		t.Errorf("FluentScroll = %+v, want %+v", kit.FluentScroll, want)
	}
	if got := kit.FluentScroll.PageFor(200); got != 175 {
		t.Errorf("Fluent page of 200 = %v, want 175", got)
	}
	if got := kit.FluentScroll.BarWidth(); got != 15 {
		t.Errorf("Fluent bar width %v, want 15", got)
	}
	p, loop := openMacPane(t, env100)
	if p.bar.Metrics != kit.FluentScroll {
		t.Errorf("bar metrics in the zero environment: %+v", p.bar.Metrics)
	}
	paintAll(loop)
	paths := 0
	for _, o := range loop.Canvas.Ops {
		if o.Kind == "FillPath" {
			paths++
		}
	}
	if paths != 2 {
		t.Errorf("Fluent bar drew %d arrow paths, want 2", paths)
	}
	if p.bar.PassThrough || p.bar.Revealed() != 1 {
		t.Error("a Fluent bar is see-through or not shown")
	}
}

// The legacy bar: a 17 DIP gutter, an 11 DIP pill 3 from the edges, the track inset 3 at both
// ends, round(track × view ÷ content) at least 20, no arrows, the knob colour always.
func TestMacLegacyBar(t *testing.T) {
	p, loop := openMacPane(t, envLegacy)
	if p.bar.Metrics != kit.MacLegacyScroll {
		t.Fatalf("metrics %+v, want MacLegacyScroll", p.bar.Metrics)
	}
	if b := p.bar.Bounds(); b.Width() != 17 {
		t.Errorf("bar width %v, want the 17 DIP gutter", b.Width())
	}
	th, ok := p.bar.ThumbRect()
	// track 200 - 6 = 194; thumb round(194 × 200 ÷ 1000) = 39
	if want := kit.R(3, 3, 14, 42); !ok || th != want {
		t.Errorf("thumb %v, want %v", th, want)
	}
	p.ScrollTo(400) // 3 + round(155 × 400 ÷ 800) = 81
	if th, _ := p.bar.ThumbRect(); th.Min.Y != 81 {
		t.Errorf("thumb top at 400: %v, want 81", th.Min.Y)
	}
	paintAll(loop)
	ops := thumbOps(loop)
	if len(ops) != 1 || ops[0].Color != testPalette.ScrollThumb || ops[0].RRect != kit.RoundRect(kit.R(186, 81, 197, 120), 5.5) {
		t.Fatalf("thumb drawn as %+v, want one 5.5 DIP pill in ScrollThumb", ops)
	}
	for _, o := range loop.Canvas.Ops {
		if o.Kind == "FillPath" || o.Kind == "StrokePath" {
			t.Errorf("a macOS bar drew an arrow (%s)", o.Kind)
		}
	}
	// The minimum length.
	if s, n, ok := kit.ScrollThumbSpan(kit.MacLegacyScroll, 200, 0, 100000, 200, env100.Scale); !ok || s != 3 || n != 20 {
		t.Errorf("long content: thumb at %v, %v long, want 3, 20", s, n)
	}
	// Hovering and dragging keep the colour (WebKit's knob colour has no hover state).
	at := kit.Pt(190, 100)
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft})
	if !p.bar.Dragging() {
		t.Fatal("a press on the thumb does not drag")
	}
	loop.Canvas.Ops = nil
	loop.Step()
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 1 || ops[0].Color != testPalette.ScrollThumb {
		t.Errorf("dragged thumb drawn as %+v", ops)
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(190, 131), Buttons: kit.ButtonLeft})
	// 31 DIPs of a free track of 155 move 800 × 31 ÷ 155 = 160
	if p.offset != 560 {
		t.Errorf("offset after a 31 DIP drag = %v, want 560", p.offset)
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(190, 131), Button: kit.ButtonLeft})
}

// A press on the track pages by max(80%, view − 40) toward the pointer, animated over 180 ms;
// held, it pages again after 500 ms, every 140 ms, until the thumb reaches the pointer.
func TestMacTrackPaging(t *testing.T) {
	p, loop := openMacPane(t, envLegacy)
	if got := p.PageStep(); got != 160 {
		t.Fatalf("page of a 200 DIP view %v, want 160", got)
	}
	if got := kit.MacLegacyScroll.PageFor(300); got != 260 {
		t.Errorf("page of a 300 DIP view %v, want 260 (WebKit's)", got)
	}
	click(loop, kit.Pt(190, 150), 0)
	if p.offset != 0 {
		t.Errorf("the page jumped (offset %v at once)", p.offset)
	}
	loop.Step()
	loop.Step()
	loop.Step()
	if p.offset <= 0 || p.offset >= 160 {
		t.Errorf("offset %v 50 ms into the page, want between 0 and 160", p.offset)
	}
	loop.Run(200 * time.Millisecond)
	if p.offset != 160 {
		t.Errorf("offset after the page %v, want 160", p.offset)
	}

	// Held: the thumb walks down to the pointer and stops there.
	p.ScrollTo(0)
	at := kit.Pt(190, 190)
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft})
	loop.Run(400 * time.Millisecond)
	if p.offset != 160 {
		t.Errorf("offset %v before the repeat delay, want one page (160)", p.offset)
	}
	loop.Run(2 * time.Second)
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft})
	if th, _ := p.bar.ThumbRect(); p.offset != 800 || th.Max.Y < 190 {
		t.Errorf("held to the end: offset %v, thumb %v", p.offset, th)
	}

	// Without motion the page is at once.
	p2, loop2 := openMacPane(t, kit.Environment{Scale: env100.Scale, ScrollBars: kit.ScrollBarsMacLegacy, ReducedMotion: true})
	click(loop2, kit.Pt(190, 150), 0)
	if p2.offset != 160 {
		t.Errorf("reduced motion: offset %v right after the click, want 160", p2.offset)
	}
}

// "Jump to the spot that's clicked" centres the thumb on the pointer and drags it; Option
// inverts the setting, as in WebKit.
func TestMacTrackJumps(t *testing.T) {
	env := envLegacy
	env.ScrollClickJumps = true
	p, loop := openMacPane(t, env)
	at := kit.Pt(190, 120)
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft})
	// (120 - 3 - 39 ÷ 2) × 800 ÷ 155 = 503.2
	if th, _ := p.bar.ThumbRect(); p.offset < 503 || p.offset > 504 || !p.bar.Dragging() {
		t.Errorf("jump: offset %v, thumb %v, dragging %v", p.offset, th, p.bar.Dragging())
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft})
	// Option pages instead.
	p.ScrollTo(0)
	click(loop, kit.Pt(190, 150), kit.ModAlt)
	loop.Run(300 * time.Millisecond)
	if p.offset != 160 {
		t.Errorf("Option-click with jumping on: offset %v, want a page (160)", p.offset)
	}
	// And Option jumps when the setting pages.
	p2, loop2 := openMacPane(t, envLegacy)
	loop2.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop2.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Mods: kit.ModAlt})
	if p2.offset < 503 || p2.offset > 504 {
		t.Errorf("Option-click with paging on: offset %v, want the jump (503)", p2.offset)
	}
}

// The overlay bar: no gutter; a 6 DIP thumb 3 from the edge that shows when the content
// scrolls, stays 500 ms, fades out over 250 ms; the pointer goes through it while hidden.
func TestMacOverlayBar(t *testing.T) {
	p, loop := openMacPane(t, envOverlay)
	if p.bar.Metrics != kit.MacOverlayScroll || p.bar.Metrics.Thickness != 0 {
		t.Fatalf("metrics %+v, want MacOverlayScroll", p.bar.Metrics)
	}
	if b := p.bar.Bounds(); b.Width() != 14 || b.Max.X != 200 {
		t.Errorf("bar at %v, want 14 DIPs over the edge", b)
	}
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 0 || p.bar.Revealed() != 0 {
		t.Errorf("a bar that never scrolled drew %v (alpha %v)", ops, p.bar.Revealed())
	}
	if !p.bar.PassThrough {
		t.Error("the hidden bar takes the pointer")
	}
	// A press where the hidden bar is reaches the content and scrolls nothing.
	click(loop, kit.Pt(195, 150), 0)
	loop.Run(300 * time.Millisecond)
	if p.offset != 0 || p.down != 1 {
		t.Errorf("a press on the hidden bar: offset %v, presses that reached the pane %d", p.offset, p.down)
	}

	// Scrolling shows it at once.
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(100, 100)})
	p.ScrollTo(100)
	loop.Canvas.Ops = nil
	loop.Step()
	ops := thumbOps(loop)
	// track 194, thumb 39 at 3 + round(155 × 100 ÷ 800) = 22, x = 14 - 3 - 6 = 5
	if len(ops) != 1 || ops[0].Color != testPalette.ScrollThumb || ops[0].RRect.Rect != kit.R(191, 22, 197, 61) {
		t.Fatalf("thumb after a scroll: %+v, want a 6 DIP pill at (191, 22, 197, 61)", ops)
	}
	if p.bar.PassThrough {
		t.Error("the shown bar lets the pointer through")
	}
	loop.Run(450 * time.Millisecond)
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 1 || ops[0].Color.A != 1 {
		t.Errorf("thumb 450 ms after the scroll: %+v, want it fully shown", ops)
	}
	loop.Run(175 * time.Millisecond) // 625 ms: halfway through the fade
	if a := p.bar.Revealed(); a < 0.3 || a > 0.7 {
		t.Errorf("alpha 125 ms into the fade %v, want about 0.5", a)
	}
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 1 || ops[0].Color.A >= 1 || ops[0].Color.A <= 0 {
		t.Errorf("thumb in the fade: %+v", ops)
	}
	loop.Run(200 * time.Millisecond)
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 0 || p.bar.Revealed() != 0 || !p.bar.PassThrough {
		t.Errorf("after the fade: drew %v, alpha %v, pass-through %v", ops, p.bar.Revealed(), p.bar.PassThrough)
	}
	if !loop.Settle(30) {
		t.Error("the window keeps working once the bar is hidden")
	}
}

// Over a shown overlay bar the pointer keeps it and grows the thumb to 11 DIPs; it drags; it
// fades out once the pointer leaves.
func TestMacOverlayHover(t *testing.T) {
	p, loop := openMacPane(t, envOverlay)
	p.ScrollTo(100)
	loop.Step()
	at := kit.Pt(195, 40) // over the thumb (22..61)
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	loop.Run(2 * time.Second)
	if p.bar.Revealed() != 1 || p.bar.Expansion() != 1 {
		t.Fatalf("hovered for 2 s: alpha %v, expansion %v", p.bar.Revealed(), p.bar.Expansion())
	}
	paintAll(loop)
	if ops := thumbOps(loop); len(ops) != 1 || ops[0].RRect.Rect.Width() != 11 || ops[0].RRect.Rect.Max.X != 197 {
		t.Errorf("hovered thumb %+v, want 11 DIPs wide, 3 from the edge", ops)
	}
	// It drags (the whole width of the bar along the thumb).
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft})
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(195, 71), Buttons: kit.ButtonLeft})
	loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: kit.Pt(60, 71), Buttons: kit.ButtonLeft})
	if p.offset != 260 || !p.bar.Dragging() {
		t.Errorf("dragged 31 DIPs: offset %v (want 260), dragging %v", p.offset, p.bar.Dragging())
	}
	loop.Run(time.Second)
	if p.bar.Revealed() != 1 {
		t.Error("the bar faded while its thumb was dragged")
	}
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: kit.Pt(60, 71), Button: kit.ButtonLeft})
	loop.Run(900 * time.Millisecond)
	if p.bar.Revealed() != 0 || p.bar.Expansion() != 0 {
		t.Errorf("after the pointer left: alpha %v, expansion %v; want hidden and collapsed", p.bar.Revealed(), p.bar.Expansion())
	}
}

// The style follows the environment live, both ways.
func TestScrollStyleFollowsEnvironment(t *testing.T) {
	p, loop := openMacPane(t, env100)
	loop.Send(kit.EnvironmentEvent{Env: envOverlay})
	loop.Step()
	if p.bar.Metrics != kit.MacOverlayScroll || p.bar.Bounds().Width() != 14 {
		t.Errorf("after the overlay style: %+v, bar %v", p.bar.Metrics, p.bar.Bounds())
	}
	loop.Send(kit.EnvironmentEvent{Env: envLegacy})
	loop.Step()
	if p.bar.Metrics != kit.MacLegacyScroll || p.bar.Bounds().Width() != 17 || p.bar.PassThrough {
		t.Errorf("after the legacy style: %+v, bar %v, pass-through %v", p.bar.Metrics, p.bar.Bounds(), p.bar.PassThrough)
	}
	loop.Send(kit.EnvironmentEvent{Env: env100})
	loop.Step()
	if p.bar.Metrics != kit.FluentScroll || p.bar.Bounds().Width() != 15 {
		t.Errorf("back to Fluent: %+v, bar %v", p.bar.Metrics, p.bar.Bounds())
	}
}
