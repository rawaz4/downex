package widgets_test

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
)

// rig is a window of widgets driven headless: kittest's loop over hostcaptest's host (popups,
// fields of every kind, tooltips).
type rig struct {
	t     *testing.T
	clock *kittest.Clock
	loop  *kittest.Loop
	host  *hostcaptest.Host
	win   *kit.Window
	root  *widgets.Scope
	// keysSeen are the window's shortcuts that reached OnKey (a dialog's Escape, say).
	keysSeen []kit.KeyEvent
}

var light = &widgets.Choice{Choice: tokens.ChoiceLight}

// newRig shows children in a window of size (1190 × 752 when zero), light theme, 100%.
func newRig(t *testing.T, size kit.Size, children ...kit.Widget) *rig {
	t.Helper()
	if size == (kit.Size{}) {
		size = kit.Size{W: 1190, H: 752}
	}
	r := &rig{t: t, clock: kittest.NewClock()}
	r.root = widgets.NewScope(light, children...)
	r.root.Style = kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(20)), RowGap: kit.Px(12)}
	r.win = kit.NewWindow(r.root, kit.WindowOptions{
		Title: "test", Palette: func(env kit.Environment) kit.Palette { return light.Tokens(env).Palette() },
		Now: r.clock.Now, Scheduler: r.clock,
		OnKey: func(e kit.KeyEvent) bool {
			r.keysSeen = append(r.keysSeen, e)
			return false
		},
	})
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}}
	r.loop, r.host = hostcaptest.NewLoop(r.win, r.clock, size, env)
	r.settle()
	return r
}

// frameNow runs what l's root posted and, when it asked for one, a frame at the clock's time,
// without moving the clock (kittest's Step moves it a refresh on).
func frameNow(l *kittest.Loop) {
	h := l.Host
	h.RunPosted()
	if h.FrameWanted || !h.Invalid.Empty() {
		h.FrameWanted = false
		l.Frames++
		l.Root.Frame(l.Clock.Now())
		if !h.Invalid.Empty() {
			dirty := h.Invalid
			h.Invalid = kit.Rect{}
			l.Paints++
			l.Painted = dirty
			l.Root.Paint(l.Canvas, dirty)
		}
	}
	h.RunPosted()
}

// settle runs the owner's and the popups' frames until nothing is asked for, the clock
// standing still (a running animation asks for frames until wait moves the clock on).
func (r *rig) settle() {
	for range 20 {
		frameNow(r.loop)
		for _, p := range r.host.Open() {
			frameNow(p.Loop)
		}
		idle := r.loop.Idle()
		for _, p := range r.host.Open() {
			idle = idle && p.Loop.Idle()
		}
		if idle {
			return
		}
	}
}

// key presses k (down and up) with mods, as the host sends it; a key the root did not use
// that types a character sends that character too.
func (r *rig) key(k kit.Key, mods kit.Modifiers) bool {
	used := r.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: mods})
	if !used && mods&(kit.ModCtrl|kit.ModAlt) == 0 {
		if ch := charOf(k, mods); ch != "" {
			r.loop.Send(kit.TextEvent{Text: ch})
		}
	}
	r.loop.Send(kit.KeyEvent{Action: kit.KeyUp, Key: k, Mods: mods})
	r.settle()
	return used
}

func charOf(k kit.Key, mods kit.Modifiers) string {
	switch {
	case k >= kit.Key0 && k <= kit.Key9:
		return string(rune('0' + k - kit.Key0))
	case k >= kit.KeyA && k <= kit.KeyZ:
		if mods&kit.ModShift != 0 {
			return string(rune('A' + k - kit.KeyA))
		}
		return string(rune('a' + k - kit.KeyA))
	case k == kit.KeySpace:
		return " "
	}
	return ""
}

// typeText types s character by character (letters and digits).
func (r *rig) typeText(s string) {
	for _, c := range s {
		var k kit.Key
		switch {
		case c >= 'a' && c <= 'z':
			k = kit.KeyA + kit.Key(c-'a')
		case c >= '0' && c <= '9':
			k = kit.Key0 + kit.Key(c-'0')
		default:
			r.loop.Send(kit.TextEvent{Text: string(c)})
			continue
		}
		r.key(k, 0)
	}
}

// click clicks the middle of e (window coordinates of its own window).
func (r *rig) click(e *kit.Element) {
	r.clickAt(e.WindowBounds().Center())
}

func (r *rig) clickAt(p kit.Point) {
	r.loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	r.settle()
}

// press stands in for a mouse button going down at p outside every popup (the popups hear
// it first, then the owner's root gets it).
func (r *rig) press(p kit.Point) {
	r.host.Press(p)
	r.loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	r.loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	r.settle()
}

// wait moves the clock on by d in 1 ms steps, running what comes due at its time (a timer's
// work is a post, which may start timers of its own) and a frame every 16 ms, as a display
// would.
func (r *rig) wait(d time.Duration) {
	var since time.Duration
	for d > 0 {
		step := min(d, time.Millisecond)
		r.clock.Advance(step)
		d -= step
		since += step
		r.host.RunPosted()
		for _, p := range r.host.Open() {
			p.Host.RunPosted()
		}
		if since >= 16*time.Millisecond || d == 0 {
			since = 0
			r.settle()
		}
	}
}

// popupClick clicks the middle of e, an element of the top popup's window.
func (r *rig) popupClick(e *kit.Element) {
	p := r.host.Top()
	if p == nil {
		r.t.Fatal("no popup is open")
	}
	p.Click(e.WindowBounds().Center())
	r.settle()
}

// node is e's semantic node in its window's tree.
func node(t *testing.T, e *kit.Element) kit.SemanticNode {
	t.Helper()
	w := e.Window()
	if w == nil {
		t.Fatalf("%T is not in a window", e.Widget())
	}
	n, ok := w.Semantics().Node(e.NodeID())
	if !ok {
		t.Fatalf("%T has no semantic node", e.Widget())
	}
	return n
}

// find is the first element under e (e included) that ok accepts, in tree order.
func find(e *kit.Element, ok func(*kit.Element) bool) *kit.Element {
	if ok(e) {
		return e
	}
	for _, c := range e.Children() {
		if f := find(c, ok); f != nil {
			return f
		}
	}
	return nil
}

// findAll is every element under e that ok accepts, in tree order.
func findAll(e *kit.Element, ok func(*kit.Element) bool) []*kit.Element {
	var out []*kit.Element
	var walk func(*kit.Element)
	walk = func(x *kit.Element) {
		if ok(x) {
			out = append(out, x)
		}
		for _, c := range x.Children() {
			walk(c)
		}
	}
	walk(e)
	return out
}

// byRole finds the elements of a role in a window, with their nodes.
func byRole(t *testing.T, w *kit.Window, role kit.Role) []kit.SemanticNode {
	t.Helper()
	tree := w.Semantics()
	var out []kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok {
			return
		}
		if n.Role == role {
			out = append(out, n)
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	return out
}

// rootOf is a popup window's root element (its content).
func rootOf(w *kit.Window) *kit.Element { return w.Content().Base() }
