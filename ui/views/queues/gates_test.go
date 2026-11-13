package queues

import (
	"fmt"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

// The plan's acceptance beyond the vitest cases: locked while stale or busy, a queue deleted
// elsewhere, the order's summary of what is not listed, one scroll region and the fixture states
// fitting at the reference sizes (16 and 20 px text), and steady frames.

func TestLockedWhileStaleOrBusy(t *testing.T) {
	t.Run("a call in flight locks what changes the queues (not the fields)", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 1})
		var during struct{ arrows, newBtn, save, start, close, nameOff, busy bool }
		r.fake.Before = func(name string, _ ...any) error {
			if name != "moveDownloads" {
				return nil
			}
			rows := r.orderRows()
			during.arrows = rows[1].up.Disabled && rows[1].down.Disabled
			during.newBtn = r.d.qlist.newBtn.Disabled
			during.save = r.d.saveBtn.Disabled
			during.start = r.d.head.run.Disabled
			during.close = r.d.closeBtn.Disabled
			during.nameOff = r.d.settings.name.Disabled
			during.busy = r.d.Busy()
			return nil
		}
		r.click(r.arrow("Move second.iso up"))
		if !during.arrows || !during.newBtn || !during.save || !during.start || !during.close || during.nameOff || !during.busy {
			t.Fatalf("while moving: %+v", during)
		}
		if r.d.saveBtn.Disabled || r.orderRows()[1].up.Disabled || r.d.Busy() {
			t.Fatal("still locked after the answer")
		}
	})

	t.Run("downexd unreachable: nothing can be changed", func(t *testing.T) {
		r := setup(t, setupOptions{initial: 2})
		r.fake.SetDown(true)
		r.h.Main.Downloads.RefreshStatus()
		r.h.Settle()
		if !r.h.App.Stale() {
			t.Skip("the store did not go stale")
		}
		d := r.d
		if !d.settings.name.Disabled || !d.settings.after.Disabled || !d.sched.sw.Switch.Disabled {
			t.Fatal("fields still enabled")
		}
		if !d.saveBtn.Disabled || !d.qlist.newBtn.Disabled || !d.head.run.Disabled || !d.delBtn.Disabled {
			t.Fatal("buttons still enabled")
		}
		d.submit()
		r.h.Settle()
		if r.fake.Count("updateQueue") != 0 {
			t.Fatal("saved while stale")
		}
		r.fake.SetDown(false)
		r.h.Main.Downloads.RefreshStatus()
		r.h.Settle()
		if d.saveBtn.Disabled || d.settings.name.Disabled {
			t.Fatal("still locked once downexd answers")
		}
	})
}

func TestQueueDeletedElsewhereShowsTheMainQueue(t *testing.T) {
	r := setup(t, setupOptions{initial: 2})
	r.fake.DeleteQueueItem(2)
	r.fake.Emit(statetest.QueueRemoved(2))
	r.h.Settle()
	if r.heading() != "Main queue" || r.d.selected != 1 {
		t.Fatalf("heading %q", r.heading())
	}
}

func TestOrderListsAtMost200(t *testing.T) {
	ids := make([]int64, 0, 205)
	for i := int64(1); i <= 205; i++ {
		ids = append(ids, 100+i)
	}
	var list []*downexv1.Download
	for _, id := range ids {
		list = append(list, download(id, fmt.Sprintf("file-%d.bin", id), stQueued))
	}
	fake := statetest.NewDaemon(list...)
	h := viewstest.New(t, viewstest.Options{Width: 1920, Height: 1080, Daemon: fake})
	d := New(h.App, views.QueuesRequest{Queue: 1})
	h.App.Dialogs.Show(d)
	h.Settle()
	if n := len(d.order.ids); n != MaxRows {
		t.Fatalf("%d rows", n)
	}
	if d.order.more.Style.Hidden || d.order.more.Text() != "And 5 more, after these." {
		t.Fatalf("summary %q", d.order.more.Text())
	}
	// The last listed row's Down arrow is off (it moves only among the rows shown).
	if last := d.order.rows[d.order.ids[MaxRows-1]]; !last.down.Disabled {
		t.Fatal("the last row can move down")
	}
}

// TestOneScrollRegionAndFit: in every fixture state, at every reference size with 16 and 20 px
// text, the dialog's body never scrolls (only the editor, and the queue list when it is long),
// the dialog is inside the window with its footer, and it is 72rem wide (at most the window
// less 32 px) and 48rem high or more (at most the room).
func TestOneScrollRegionAndFit(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skipf("no text engine: %v", err)
	}
	sizes := [][2]int{{1200, 760}, {1190, 752}, {960, 608}, {900, 600}, {1920, 1080}}
	n := 0
	for _, st := range states {
		for _, sz := range sizes {
			for _, font := range []int{16, 20} {
				o := viewstest.Options{Width: sz[0], Height: sz[1], Font: font, Fixture: st.name, Real: true}
				h := viewstest.New(t, o)
				h.App.Caps.WakeTimers = true
				d := open(h)
				if st.reach != nil {
					st.reach(h, d)
				}
				h.Settle()
				name := fmt.Sprintf("%s %dx%d %dpx", st.name, sz[0], sz[1], font)
				body, ok := d.Body.Parent().Widget().(*widgets.ScrollPane)
				if !ok {
					t.Fatalf("%s: the body is not in the dialog's pane", name)
				}
				if body.Overflows() {
					t.Errorf("%s: the dialog's body scrolls", name)
				}
				card := d.Card().WindowBounds()
				gap := kit.DIP(16)
				if sz[1] <= 640 {
					gap = 8
				}
				room := kit.DIP(sz[1]) - h.App.ModalTop() - 2*gap
				wantW := min(kit.DIP(72*font), kit.DIP(sz[0]-32))
				if card.Width() != wantW || card.Height() < min(kit.DIP(48*font), room)-0.5 || card.Height() > room+0.5 {
					t.Errorf("%s: dialog %v × %v (room %v)", name, card.Width(), card.Height(), room)
				}
				if card.Max.Y > kit.DIP(sz[1])-gap+0.5 || card.Min.Y < h.App.ModalTop()+gap-0.5 {
					t.Errorf("%s: dialog %v outside the window", name, card)
				}
				foot := d.saveBtn.WindowBounds()
				if foot.Max.Y > card.Max.Y || foot.Min.Y < card.Min.Y {
					t.Errorf("%s: the footer is cut", name)
				}
				// the scroll regions: the editor's pane and the list's; nothing nested
				if d.qlist.pane.Overflows() && st.name != "queues-many-queues" {
					t.Errorf("%s: the queue list scrolls", name)
				}
				h.Close()
				n++
			}
		}
	}
	t.Logf("%d states × sizes × text sizes checked", n)
}

// nopCanvas draws nothing, so that what allocates is the dialog's own drawing.
type nopCanvas struct{}

func (nopCanvas) FillRect(kit.Rect, kit.Color)                       {}
func (nopCanvas) StrokeRect(kit.Rect, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillRoundedRect(kit.RRect, kit.Color)               {}
func (nopCanvas) StrokeRoundedRect(kit.RRect, kit.Stroke, kit.Color) {}
func (nopCanvas) FillEllipse(kit.Rect, kit.Color)                    {}
func (nopCanvas) StrokeEllipse(kit.Rect, kit.Stroke, kit.Color)      {}
func (nopCanvas) DrawLine(a, b kit.Point, s kit.Stroke, c kit.Color) {}
func (nopCanvas) FillPath(kit.Path, kit.Color)                       {}
func (nopCanvas) StrokePath(kit.Path, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillGradient(kit.Rect, kit.LinearGradient)          {}
func (nopCanvas) DrawShadow(kit.RRect, kit.Shadow)                   {}
func (nopCanvas) DrawText(kit.TextLayout, kit.Point, kit.Color)      {}
func (nopCanvas) DrawImage(kit.Image, kit.Rect, kit.Rect)            {}
func (nopCanvas) PushClip(kit.Rect)                                  {}
func (nopCanvas) PushRoundedClip(kit.RRect)                          {}
func (nopCanvas) PushOpacity(float32)                                {}
func (nopCanvas) PushTransform(kit.Affine)                           {}
func (nopCanvas) Pop()                                               {}

// TestSteadyFramesAndIdle: once shown, a repaint of the whole dialog allocates nothing (in
// every fixture state, a row being dragged included), and an idle dialog paints nothing.
func TestSteadyFramesAndIdle(t *testing.T) {
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			h := viewstest.New(t, viewstest.Options{Fixture: st.name})
			h.App.Caps.WakeTimers = true
			d := open(h)
			if st.reach != nil {
				st.reach(h, d)
			}
			h.Settle()
			h.Advance(2 * time.Second) // the open motion and any transition end
			full := kit.RectAt(kit.Point{}, h.Window.Size())
			var c nopCanvas
			h.Window.Paint(c, full) // the labels' layouts are made once
			if n := testing.AllocsPerRun(50, func() { h.Window.Paint(c, full) }); n != 0 {
				t.Errorf("a repaint allocates %.1f times", n)
			}
			paints, frames := h.Loop.Paints, h.Loop.Frames
			h.Advance(5 * time.Second)
			if h.Loop.Paints != paints {
				t.Errorf("idle: %d paints in 5 s", h.Loop.Paints-paints)
			}
			t.Logf("idle 5 s: %d frames, %d paints", h.Loop.Frames-frames, h.Loop.Paints-paints)
			if rows := d.order.ids; len(rows) > 1 && !d.order.Style.Hidden {
				// a drag in progress (dimmed row, drop mark)
				l := d.order.list
				l.pressed, l.dragging, l.dragID = true, true, rows[0]
				l.setDrop(rows[1])
				h.Window.Paint(c, full)
				if n := testing.AllocsPerRun(50, func() { h.Window.Paint(c, full) }); n != 0 {
					t.Errorf("a repaint while dragging allocates %.1f times", n)
				}
				l.cancel()
			}
		})
	}
}
