package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// The contract additions the views base asked for (stage 2): Window.Direction, RaiseLayer and
// Layers, LayoutFollower, and the exported Scrollbar over a ScrollTarget.

func TestWindowDirection(t *testing.T) {
	for _, d := range []kit.Direction{kit.DirAuto, kit.DirLTR, kit.DirRTL} {
		w, _ := open(t, box(kit.Style{}), kit.Size{W: 100, H: 100}, env100, func(o *kit.WindowOptions) { o.Direction = d })
		if got := w.Direction(); got != d {
			t.Errorf("Direction() = %v, want %v", got, d)
		}
	}
}

func TestRaiseLayerKeepsAttachment(t *testing.T) {
	content := box(kit.Style{})
	w, loop := open(t, content, kit.Size{W: 200, H: 200}, env100)
	a := &attachCounter{}
	b := box(kit.Style{})
	w.PushLayer(a)
	w.PushLayer(b)
	loop.Step()
	if got := w.Layers(); len(got) != 2 || got[0] != kit.Widget(a) || got[1] != kit.Widget(b) {
		t.Fatalf("Layers() = %v, want [a b]", got)
	}
	w.RaiseLayer(a)
	loop.Step()
	if got := w.Layers(); len(got) != 2 || got[0] != kit.Widget(b) || got[1] != kit.Widget(a) {
		t.Fatalf("after RaiseLayer(a): %v, want [b a]", got)
	}
	if a.attached != 1 || a.detached != 0 {
		t.Errorf("RaiseLayer re-attached the layer: attached %d, detached %d", a.attached, a.detached)
	}
	// The content and a widget that is not a layer never move.
	w.RaiseLayer(content)
	w.RaiseLayer(box(kit.Style{}))
	if got := w.Layers(); len(got) != 2 || got[1] != kit.Widget(a) {
		t.Errorf("RaiseLayer moved what is not a layer: %v", got)
	}
	if w.Content() != kit.Widget(content) {
		t.Errorf("the content changed")
	}
}

type attachCounter struct {
	kit.Element
	attached, detached int
}

func (a *attachCounter) Attached() { a.attached++ }
func (a *attachCounter) Detached() { a.detached++ }

// follower records every LaidOut call and its box at that time.
type follower struct {
	kit.Element
	calls int
	at    kit.Rect
}

func (f *follower) LaidOut() { f.calls++; f.at = f.WindowBounds() }

func TestLayoutFollowerHearsEveryPass(t *testing.T) {
	f := &follower{}
	f.Style = kit.Style{Width: kit.Px(40), Height: kit.Px(20)}
	spacer := leaf(10, 30)
	root := box(kit.Style{Display: kit.DisplayBlock}, spacer, f)
	_, loop := open(t, root, kit.Size{W: 200, H: 200}, env100)
	if f.calls == 0 {
		t.Fatal("no LaidOut after the first layout")
	}
	if !approxRect(f.at, kit.R(0, 30, 40, 50)) {
		t.Errorf("box at LaidOut = %v, want (0,30)-(40,50)", f.at)
	}
	// A pass that lays out only the spacer moves the follower: it hears it.
	before := f.calls
	spacer.Style.Height = kit.Px(60)
	spacer.Relayout()
	loop.Step()
	if f.calls == before {
		t.Error("no LaidOut after a pass that moved the follower")
	}
	if !approxRect(f.at, kit.R(0, 60, 40, 80)) {
		t.Errorf("box at LaidOut = %v, want (0,60)-(40,80)", f.at)
	}
	// Detached, it is no longer told.
	root.Remove(f)
	loop.Step()
	n := f.calls
	spacer.Style.Height = kit.Px(10)
	spacer.Relayout()
	loop.Step()
	if f.calls != n {
		t.Errorf("a detached follower heard %d more passes", f.calls-n)
	}
}

// pane is a ScrollTarget of its own: 1000 DIPs of content in its height.
type pane struct {
	kit.Element
	bar    *kit.Scrollbar
	offset kit.DIP
}

func (p *pane) ScrollRange() (offset, content, view kit.DIP) {
	return p.offset, 1000, p.Bounds().Height()
}
func (p *pane) ScrollTo(o kit.DIP) {
	p.offset = min(max(o, 0), 1000-p.Bounds().Height())
	p.bar.Invalidate()
}
func (p *pane) PageStep() kit.DIP                               { return p.Bounds().Height() * p.bar.Metrics.Page }
func (p *pane) ContentWidths(*kit.LayoutContext) (a, b kit.DIP) { return 0, 0 }
func (p *pane) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP {
	return 0
}
func (p *pane) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := p.bar.Metrics.Thickness
	ctx.Place(p.bar, kit.R(box.Max.X-t, box.Min.Y, box.Max.X, box.Max.Y), t)
}

func TestNewScrollbarOverATarget(t *testing.T) {
	p := &pane{}
	p.bar = kit.NewScrollbar(p)
	p.Style = kit.Style{Width: kit.Px(200), Height: kit.Px(200)}
	p.Add(p.bar)
	_, loop := open(t, box(kit.Style{}, p), kit.Size{W: 200, H: 200}, env100)
	thumb, ok := p.bar.ThumbRect()
	if !ok {
		t.Fatal("no thumb while the content overflows")
	}
	// track 200 - 2*18 = 164; thumb round(164*200/1000) = 33 (FluentScroll)
	if thumb.Height() != 33 {
		t.Errorf("thumb length %v, want 33", thumb.Height())
	}
	// A press on the down arrow scrolls by a line (40 DIPs).
	at := kit.Pt(192, 195)
	loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft})
	loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft})
	if p.offset != 40 {
		t.Errorf("offset after a down-arrow click = %v, want 40", p.offset)
	}
	if p.bar.Dragging() {
		t.Error("dragging after an arrow click")
	}
}
