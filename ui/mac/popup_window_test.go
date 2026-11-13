//go:build darwin && cgo

package mac

// [popups]

import (
	"errors"
	"math"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

const (
	evLeftDown  = 1
	evLeftUp    = 2
	evRightDown = 3
	evRightUp   = 4
	evMoved     = 5
	evOtherDown = 25
)

// keepDismiss records d, unless it is the application's deactivation: whether a test
// process may stay active is the system's choice, and it can come and go during a test.
func keepDismiss(ds []kit.Dismiss, d kit.Dismiss) []kit.Dismiss {
	if d.Reason == kit.DismissDeactivate {
		return ds
	}
	return append(ds, d)
}

func TestPopupMenuWindow(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(220, 220, 220, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 400, H: 300})
	pr := &recRoot{box: kit.R(10, 10, 190, 140)}
	var dismissals []kit.Dismiss
	var p *popupWindow
	mainDo(func() {
		pp, err := w.OpenPopup(pr, kit.PopupOptions{Kind: kit.PopupMenu, Bounds: kit.R(50, 50, 250, 200),
			Hit: kit.R(10, 10, 190, 140), Name: "menu", OnDismiss: func(d kit.Dismiss) { dismissals = keepDismiss(dismissals, d) }})
		if err != nil {
			t.Fatalf("OpenPopup: %v", err)
		}
		p = pp.(*popupWindow)
	})
	pump(200 * time.Millisecond)
	mainDo(func() {
		if pr.host == nil || pr.frames == 0 || p.paints == 0 || !p.shown {
			t.Fatalf("the popup did not attach and paint: host %v frames %d paints %d shown %v", pr.host != nil, pr.frames, p.paints, p.shown)
		}
		if p.Size() != (kit.Size{W: 200, H: 150}) {
			t.Errorf("size %v", p.Size())
		}
		if p.Environment() != w.Environment() {
			t.Errorf("environment %+v, owner's %+v", p.Environment(), w.Environment())
		}
		// Placed at the bounds: the owner's content origin + (50, 50), y flipped.
		x, y, pw, ph := p.screenFrame()
		o := w.screenToContent(x, y+ph)
		if math.Abs(float64(o.X)-50) > 0.01 || math.Abs(float64(o.Y)-50) > 0.01 || pw != 200 || ph != 150 {
			t.Errorf("panel at owner %v size %v×%v, want (50, 50) 200×150", o, pw, ph)
		}
	})

	// A press over the popup's hit area is the popup's, and dismisses nothing.
	mainDo(func() {
		p.postMouse(evLeftDown, kit.Pt(100, 60), 1)
		p.postMouse(evLeftUp, kit.Pt(100, 60), 1)
	})
	pump(150 * time.Millisecond)
	mainDo(func() {
		downs := pr.pointers(kit.PointerDown)
		if len(downs) != 1 || downs[0].Pos != kit.Pt(100, 60) || downs[0].Clicks != 1 || downs[0].Button != kit.ButtonLeft {
			t.Errorf("popup downs %+v", downs)
		}
		if len(pr.pointers(kit.PointerUp)) != 1 {
			t.Errorf("popup ups %+v", pr.pointers(kit.PointerUp))
		}
		if len(dismissals) != 0 {
			t.Errorf("a press on the popup dismissed it: %+v", dismissals)
		}
	})

	// A press on the shadow goes to what is under the popup: the owner, which takes it (the
	// right button: a test process cannot make its window key, and AppKit keeps a left press
	// on a window that is not key for activating it).
	mainDo(func() { p.postMouse(evRightDown, kit.Pt(4, 4), 1); p.postMouse(evRightUp, kit.Pt(4, 4), 1) })
	pump(150 * time.Millisecond)
	mainDo(func() {
		if len(pr.pointers(kit.PointerDown)) != 1 {
			t.Errorf("the shadow took a press: %+v", pr.pointers(kit.PointerDown))
		}
		od := owner.pointers(kit.PointerDown)
		if len(od) != 1 || od[0].Pos != kit.Pt(54, 54) {
			t.Errorf("owner downs %+v, want one at (54, 54)", od)
		}
		if len(dismissals) != 1 || dismissals[0].Reason != kit.DismissPress || !dismissals[0].Inside || dismissals[0].Pos != kit.Pt(54, 54) {
			t.Errorf("dismissals %+v", dismissals)
		}
	})

	// A press on the owner elsewhere: DismissPress inside, and the owner gets it.
	mainDo(func() { w.postMouse(evRightDown, kit.Pt(300, 250), 1) })
	pump(150 * time.Millisecond)
	mainDo(func() {
		if len(dismissals) != 2 || dismissals[1].Pos != kit.Pt(300, 250) || !dismissals[1].Inside {
			t.Errorf("dismissals %+v", dismissals)
		}
		if n := len(owner.pointers(kit.PointerDown)); n != 2 {
			t.Errorf("owner downs %d, want 2", n)
		}
	})

	// Deliver hands keys to the popup's root at once.
	mainDo(func() {
		if p.Deliver(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyArrowDown}) {
			t.Error("Deliver reported a key the root did not use")
		}
		if pr.count(func(e kit.Event) bool { k, ok := e.(kit.KeyEvent); return ok && k.Key == kit.KeyArrowDown }) != 1 {
			t.Error("the popup's root did not get the delivered key")
		}
	})

	// SetBounds with a new size: a SizeEvent; Resize grows from the top-left corner.
	mainDo(func() {
		p.SetBounds(kit.R(50, 50, 300, 250))
		if p.Size() != (kit.Size{W: 250, H: 200}) || p.target.w != int(250*p.ppd) {
			t.Errorf("after SetBounds: size %v target %d px", p.Size(), p.target.w)
		}
		if pr.count(func(e kit.Event) bool { _, ok := e.(kit.SizeEvent); return ok }) != 1 {
			t.Error("no SizeEvent")
		}
		p.SetHit(kit.R(0, 0, 250, 200))
	})
	pump(100 * time.Millisecond)

	// Close inside a call into the popup's root waits for the call; Detach comes once.
	mainDo(func() {
		p.call(func() {
			p.Close()
			if p.closed {
				t.Error("Close inside a call closed at once")
			}
		})
		if !p.closed || pr.detach != 1 {
			t.Errorf("closed %v detach %d", p.closed, pr.detach)
		}
		p.Close()
		if pr.detach != 1 {
			t.Error("Close is not idempotent")
		}
		if err := p.Post(func() {}); !errors.Is(err, kit.ErrClosed) {
			t.Errorf("Post after Close: %v", err)
		}
		if len(popupsOf[w]) != 0 {
			t.Errorf("the owner still lists %d popups", len(popupsOf[w]))
		}
	})
	pump(100 * time.Millisecond)
}

func TestPopupOwnerEvents(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(220, 220, 220, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 400, H: 300})
	open := func() (*popupWindow, *recRoot, *[]kit.Dismiss) {
		pr := &recRoot{box: kit.R(8, 8, 108, 108)}
		var ds []kit.Dismiss
		var p *popupWindow
		mainDo(func() {
			pp, err := w.OpenPopup(pr, kit.PopupOptions{Bounds: kit.R(20, 20, 136, 136), Hit: kit.R(8, 8, 108, 108),
				OnDismiss: func(d kit.Dismiss) { ds = keepDismiss(ds, d) }})
			if err != nil {
				t.Fatal(err)
			}
			p = pp.(*popupWindow)
		})
		pump(100 * time.Millisecond)
		return p, pr, &ds
	}

	// Posts run in order, outside other calls.
	p, pr, ds := open()
	var order []int
	for i := range 3 {
		if err := p.Post(func() { order = append(order, i) }); err != nil {
			t.Fatal(err)
		}
	}
	pump(100 * time.Millisecond)
	if len(order) != 3 || order[0] != 0 || order[2] != 2 {
		t.Errorf("posts ran %v", order)
	}

	// A trackpad's wheel events that scroll nothing (fingers resting, the gesture's end or
	// cancel) dismiss nothing; a wheel that scrolls does, and the owner moving does.
	mainDo(func() {
		for _, ph := range []int{popuptest.PhaseMayBegin, popuptest.PhaseBegan, popuptest.PhaseEnded, popuptest.PhaseCancelled} {
			w.postScrollPhase(kit.Pt(350, 250), ph)
		}
	})
	pump(150 * time.Millisecond)
	mainDo(func() {
		if len(*ds) != 0 {
			t.Errorf("wheel events without a delta dismissed: %+v", *ds)
		}
	})
	mainDo(func() { w.postScroll(kit.Pt(350, 250), -1) })
	pump(150 * time.Millisecond)
	mainDo(func() { w.ownerEvent(3) })
	mainDo(func() {
		var got []kit.DismissReason
		for _, d := range *ds {
			got = append(got, d.Reason)
		}
		if len(got) != 2 || got[0] != kit.DismissWheel || got[1] != kit.DismissMove {
			t.Errorf("dismissals %v, want wheel then move", got)
		}
	})

	// A real owner move dismisses (NSWindowDidMoveNotification).
	mainDo(func() {
		*ds = nil
		w.moveBy(10)
	})
	pump(150 * time.Millisecond)
	mainDo(func() {
		if len(*ds) == 0 || (*ds)[0].Reason != kit.DismissMove {
			t.Errorf("owner moved: dismissals %+v", *ds)
		}
	})

	// The owner closing: DismissClosed, then the host closes the popup.
	mainDo(func() {
		*ds = nil
		w.Close()
	})
	pump(150 * time.Millisecond)
	mainDo(func() {
		if len(*ds) != 1 || (*ds)[0].Reason != kit.DismissClosed {
			t.Errorf("owner closed: dismissals %+v", *ds)
		}
		if !p.closed || pr.detach != 1 {
			t.Errorf("popup closed %v detach %d", p.closed, pr.detach)
		}
		if len(popupsOf) != 0 || len(tipsOf) != 0 {
			t.Errorf("left behind: %d popup owners, %d tooltips", len(popupsOf), len(tipsOf))
		}
	})
}

// closeInAttach is a popup root that closes itself in Attach.
type closeInAttach struct {
	recRoot
}

func (r *closeInAttach) Attach(h kit.Host) {
	r.host = h
	h.(kit.Popup).Close()
}

// A root that closes its popup inside Attach gets no Frame: Detach comes next.
func TestPopupClosedInsideAttach(t *testing.T) {
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(220, 220, 220, 1)}, kit.Size{W: 300, H: 200})
	r := &closeInAttach{}
	mainDo(func() {
		_, err := w.OpenPopup(r, kit.PopupOptions{Bounds: kit.R(10, 10, 110, 110)})
		if !errors.Is(err, kit.ErrClosed) {
			t.Errorf("OpenPopup: %v, want ErrClosed", err)
		}
		if r.frames != 0 || r.detach != 1 {
			t.Errorf("frames %d detach %d, want 0 and 1", r.frames, r.detach)
		}
		if len(popupsOf[w]) != 0 {
			t.Errorf("%d popups listed", len(popupsOf[w]))
		}
	})
	pump(100 * time.Millisecond)
}

// A closed window's view that AppKit still holds (an event it queued, a mouseExited of a
// window closed under the pointer) reports to no window, once the window's handle is gone:
// no panic, no call into the closed root.
func TestClosedWindowViewReportsNothing(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(220, 220, 220, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 300, H: 200})
	var view unsafe.Pointer
	mainDo(func() {
		view = popuptest.HoldContentView(w.ns)
		w.Close()
	})
	pump(200 * time.Millisecond) // the window's release and its handle's deletion
	mainDo(func() {
		n := len(owner.events)
		popuptest.PokeView(view)
		popuptest.ReleaseObject(view)
		if len(owner.events) != n {
			t.Errorf("the closed window's root heard %d events", len(owner.events)-n)
		}
	})
}

// closeOnLeave is a popup root that closes its popup when the pointer leaves it, as a
// hover card would.
type closeOnLeave struct {
	recRoot
}

func (r *closeOnLeave) Event(e kit.Event) bool {
	r.recRoot.Event(e)
	if pe, ok := e.(kit.PointerEvent); ok && pe.Action == kit.PointerLeave {
		r.host.(kit.Popup).Close()
	}
	return false
}

// A popup that closes itself when the pointer leaves it, told so while the application's
// monitor goes over the open panels: the others are still handled, and nothing breaks.
func TestPopupClosesOnLeaveDuringTheMonitor(t *testing.T) {
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(220, 220, 220, 1)}, kit.Size{W: 400, H: 300})
	var ps []*popupWindow
	var roots []*closeOnLeave
	mainDo(func() {
		for i := range 2 {
			r := &closeOnLeave{recRoot{box: kit.R(0, 0, 80, 80)}}
			x := float32(20 + 120*i)
			pp, err := w.OpenPopup(r, kit.PopupOptions{Bounds: kit.R(x, 20, x+80, 100)})
			if err != nil {
				t.Fatal(err)
			}
			ps = append(ps, pp.(*popupWindow))
			roots = append(roots, r)
		}
	})
	pump(150 * time.Millisecond)
	// The pointer over both (each takes it, as over two overlapping panels), then away from
	// both: the monitor tells each it left, and the first closes at once.
	mainDo(func() {
		for _, p := range ps {
			popuptest.SetIgnoresMouse(p.ns, false)
			p.inside = true
		}
		w.postMouse(evMoved, kit.Pt(300, 250), 0)
	})
	pump(200 * time.Millisecond)
	mainDo(func() {
		for i, p := range ps {
			if !p.closed || roots[i].detach != 1 {
				t.Errorf("popup %d: closed %v detach %d", i, p.closed, roots[i].detach)
			}
		}
		if len(popupsOf[w]) != 0 {
			t.Errorf("%d popups left", len(popupsOf[w]))
		}
	})
}

// The middle button over a popup's shadow is not the popup's (in the page the shadow takes
// no events): the popup hears nothing of it, and it dismisses the popup as a press on the
// window beneath does. (AppKit cannot hand it to the window beneath; the wheel over the
// shadow is handled alike, but a test cannot post a wheel event to a panel.)
func TestPopupShadowPassesOtherButtons(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(220, 220, 220, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 400, H: 300})
	pr := &recRoot{box: kit.R(10, 10, 190, 140)}
	var ds []kit.Dismiss
	var p *popupWindow
	mainDo(func() {
		pp, err := w.OpenPopup(pr, kit.PopupOptions{Bounds: kit.R(50, 50, 250, 200), Hit: kit.R(10, 10, 190, 140),
			OnDismiss: func(d kit.Dismiss) { ds = keepDismiss(ds, d) }})
		if err != nil {
			t.Fatal(err)
		}
		p = pp.(*popupWindow)
	})
	pump(150 * time.Millisecond)
	mainDo(func() {
		at := kit.Pt(4, 4).Add(p.shift)
		popuptest.PostMouse(p.ns, evOtherDown, float64(at.X), float64(at.Y), 1)
	})
	pump(200 * time.Millisecond)
	mainDo(func() {
		if n := len(pr.pointers(kit.PointerDown)); n != 0 {
			t.Errorf("the popup took %d presses on its shadow", n)
		}
		if len(ds) != 1 || ds[0].Reason != kit.DismissPress || !ds[0].Inside || ds[0].Pos != kit.Pt(54, 54) {
			t.Errorf("dismissals %+v, want a press inside the owner at (54, 54)", ds)
		}
	})
}
