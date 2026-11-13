package widgets_test

// [mac scroll] A ScrollPane under the macOS scroll bar styles (kit/scrollmac.go).

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The overlay style gives the gutter back to the content and lays the bar over its edge; the
// legacy style keeps a 17 DIP gutter; both page by max(80%, view − 40); and the pane follows
// the style live.
func TestScrollPaneMacStyles(t *testing.T) {
	col, rows := tall(20) // 800 DIPs
	pane := widgets.NewScrollPane(col)
	pane.Style.Height = kit.Px(200)
	r := newRig(t, kit.Size{W: 400, H: 400}, pane)
	base := kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}}
	check := func(name string, bar, gutter kit.DIP, page kit.DIP) {
		t.Helper()
		pb, bb := pane.WindowBounds(), pane.Scrollbar().WindowBounds()
		if bb.Width() != bar || bb.Max.X != pb.Max.X || bb.Height() != pb.Height() {
			t.Errorf("%s: bar at %v in a pane at %v, want %v wide at the end", name, bb, pb, bar)
		}
		if w := rows[0].WindowBounds().Width(); w != pb.Width()-gutter {
			t.Errorf("%s: content %v wide, want %v", name, w, pb.Width()-gutter)
		}
		if got := pane.PageStep(); got != page {
			t.Errorf("%s: page %v, want %v", name, got, page)
		}
	}
	check("Fluent", 15, 15, 175)

	env := base
	env.ScrollBars = kit.ScrollBarsMacOverlay
	r.loop.Send(kit.EnvironmentEvent{Env: env})
	r.settle()
	check("overlay", 14, 0, 160)
	if !pane.Scrollbar().PassThrough {
		t.Error("overlay: the bar takes the pointer before anything scrolled")
	}
	// A scroll shows it.
	pane.ScrollTo(100)
	r.settle()
	if pane.Scrollbar().Revealed() != 1 {
		t.Errorf("overlay: alpha %v after a scroll", pane.Scrollbar().Revealed())
	}

	env.ScrollBars = kit.ScrollBarsMacLegacy
	r.loop.Send(kit.EnvironmentEvent{Env: env})
	r.settle()
	check("legacy", 17, 17, 160)

	r.loop.Send(kit.EnvironmentEvent{Env: base})
	r.settle()
	check("Fluent again", 15, 15, 175)
}

// intModel is n rows keyed by their index; plainRow shows nothing.
type intModel struct{ n int }

func (m *intModel) Len() int              { return m.n }
func (m *intModel) Key(i int) kit.ListKey { return kit.ListKey(i + 1) }
func (m *intModel) Index(k kit.ListKey) (int, bool) {
	return int(k) - 1, k >= 1 && int(k) <= m.n
}

type plainRow struct{ kit.Element }

func (r *plainRow) BindRow(int, kit.ListKey) {}

// A List under the overlay style lays its rows the whole width.
func TestListOverlayRowsFullWidth(t *testing.T) {
	model := &intModel{n: 100}
	l := kit.NewList(model, func() kit.ListRow { return &plainRow{} }, func(kit.Scale) kit.DIP { return 30 })
	l.Style = kit.Style{Width: kit.Px(300), Height: kit.Px(200)}
	r := newRig(t, kit.Size{W: 400, H: 400}, l)
	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, ScrollBars: kit.ScrollBarsMacOverlay}
	r.loop.Send(kit.EnvironmentEvent{Env: env})
	r.settle()
	first, _ := l.Realised()
	row := l.RowWidget(model.Key(first))
	if row == nil {
		t.Fatal("no row realised")
	}
	if w := row.Base().WindowBounds().Width(); w != 300 {
		t.Errorf("overlay: row %v wide, want the list's 300", w)
	}
	if b := l.Scrollbar().Bounds(); b.Width() != 14 {
		t.Errorf("overlay: list bar %v wide, want 14", b.Width())
	}
	env.ScrollBars = kit.ScrollBarsMacLegacy
	r.loop.Send(kit.EnvironmentEvent{Env: env})
	r.settle()
	if w := l.RowWidget(model.Key(first)).Base().WindowBounds().Width(); w != 283 {
		t.Errorf("legacy: row %v wide, want 300 - 17", w)
	}
}

// [mac webkit] A Shift + vertical wheel scrolls a pane in WebKit (today's Mac window: macOS
// turns a notched wheel sideways before it arrives, and a trackpad's vertical swipe with
// Shift held scrolls vertically); Chromium leaves it to the sideways scroller.
func TestScrollPaneShiftWheelInWebKit(t *testing.T) {
	col, _ := tall(20)
	pane := widgets.NewScrollPane(col)
	pane.Style.Height = kit.Px(200)
	r := newRig(t, kit.Size{W: 400, H: 400}, pane)
	shift := &kit.WheelInput{WheelEvent: kit.WheelEvent{Delta: kit.Pt(0, 30), Mods: kit.ModShift, Mode: kit.WheelPrecise}}
	if pane.HandleWheel(shift) {
		t.Error("Chromium: a Shift wheel scrolled the pane")
	}
	r.loop.Send(kit.EnvironmentEvent{Env: kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, WebKit: true}})
	r.settle()
	if !pane.HandleWheel(shift) || pane.Offset() != 30 {
		t.Errorf("WebKit: a Shift wheel left the pane at %v, want 30", pane.Offset())
	}
}
