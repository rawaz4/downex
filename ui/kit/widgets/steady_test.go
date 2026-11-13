package widgets_test

import (
	"math"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// nopCanvas draws nothing and records nothing, so that what allocates is the widgets' own.
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

// TestSteadyFramesAllocateNothing: once laid out, a repaint of every kind of widget, and a
// frame of a running hatch (an unknown size downloading), allocate nothing.
func TestSteadyFramesAllocateNothing(t *testing.T) {
	bar := widgets.NewProgressBar("Progress", false)
	hatch := widgets.NewProgressBar("Progress", false)
	seg := widgets.NewSegmentBar(widgets.SegLg)
	tabs := widgets.NewTabs([]widgets.TabOption{{Value: "a", Label: "Status"}, {Value: "b", Label: "Speed limit", Dot: true}}, "a", widgets.TabsOptions{AriaLabel: "View"}, nil)
	seg2 := widgets.NewSegmentedControl([]widgets.SegmentOption{{Value: "a", Label: "Files"}, {Value: "b", Label: "Video or stream"}}, "a", widgets.SegmentedOptions{AriaLabel: "Mode"}, nil)
	pane := widgets.NewScrollPane(column(4, widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil), widgets.NewButton("Save", "add", widgets.Primary, widgets.SizeMd, nil),
		widgets.NewIconButton("more", 16, "More", false, nil), widgets.NewChip("3 downloading", widgets.Live, true, "", nil), widgets.NewPill("Stopped", widgets.Plain),
		widgets.NewCheckbox("Stop at", true, nil), widgets.NewSwitchRow("Wake the computer", "While Downex runs", true, nil),
		widgets.NewSelect([]widgets.SelectOption{{Value: "a", Label: "Keep running"}}, "a", widgets.SelectOptions{Label: "When done", Icon: "power"}, nil),
		widgets.NewInput("x", widgets.InputOptions{Name: "Name"}, nil)))
	pane.Style.MaxHeight = kit.Px(200)
	r := newRig(t, kit.Size{W: 600, H: 600}, bar, hatch, seg, tabs, seg2, pane)
	bar.Set(0.45, widgets.BarDownloading)
	hatch.Set(math.NaN(), widgets.BarDownloading)
	seg.Set([]widgets.SegCell{{Fraction: 1}, {Fraction: 0.5, Live: true}, {Indeterminate: true, Live: true}}, widgets.BarDownloading)
	r.wait(time.Second)
	full := kit.RectAt(kit.Point{}, r.win.Size())
	var c nopCanvas
	if n := testing.AllocsPerRun(50, func() { r.win.Paint(c, full) }); n != 0 {
		t.Errorf("a repaint allocates %.1f times", n)
	}
	now := r.clock.Now()
	if n := testing.AllocsPerRun(50, func() {
		now = now.Add(16 * time.Millisecond)
		r.win.Frame(now)
		r.win.Paint(c, full)
	}); n != 0 {
		t.Errorf("a frame of the running hatch allocates %.1f times", n)
	}
}

func column(gap kit.DIP, ws ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(gap)}}
	e.Add(ws...)
	return e
}

// A focused text area repaints without allocating (before, each repaint posted its field's
// Reclaim as a method value: 2 allocations a frame, in the add lane's URL list); its field
// takes the system's focus back once each time the window is activated again.
func TestFocusedTextAreaRepaintsWithoutAllocating(t *testing.T) {
	area := widgets.NewTextArea("https://example.com/a.iso", 4, widgets.InputOptions{Name: "URLs"}, nil)
	r := newRig(t, kit.Size{W: 500, H: 300}, area)
	if len(r.host.Fields) != 1 {
		t.Fatalf("%d native fields, want 1", len(r.host.Fields))
	}
	f := r.host.Fields[0]
	r.win.Focus(area)
	r.settle()
	full := kit.RectAt(kit.Point{}, r.win.Size())
	var c nopCanvas
	r.win.Paint(c, full)
	if n := testing.AllocsPerRun(50, func() { r.win.Paint(c, full) }); n != 0 {
		t.Errorf("a repaint of the focused text area allocates %.1f times", n)
	}
	r.settle()
	before := f.Reclaims
	r.loop.Send(kit.FocusEvent{In: false})
	r.loop.Send(kit.FocusEvent{In: true})
	r.settle()
	if got := f.Reclaims - before; got != 1 {
		t.Errorf("the window activated again: the field reclaimed the focus %d times, want 1", got)
	}
	r.win.Paint(c, full)
	r.settle()
	if got := f.Reclaims - before; got != 1 {
		t.Errorf("a repaint reclaimed the focus (%d reclaims)", got)
	}
}
