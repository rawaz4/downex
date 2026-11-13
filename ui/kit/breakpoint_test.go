package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// TestMediaBreakpoints checks the page header's window rules (Toolbar.svelte): labels
// hidden under 1180 px ((max-width: 1179.98px)), chips under 960 px.
func TestMediaBreakpoints(t *testing.T) {
	label := kit.NewLabel("Resume", font13, kit.TextOptions{})
	label.Style.Breakpoints = []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Px(1179.98)}, Apply: func(s *kit.Style) { s.Hidden = true }}}
	chip := box(kit.Style{Width: kit.Px(90), Height: kit.Px(22)})
	chip.Style.Breakpoints = []kit.Breakpoint{{When: kit.Query{MaxWidth: kit.Px(959.98)}, Apply: func(s *kit.Style) { s.Hidden = true }}}
	icon := box(kit.Style{Width: kit.Px(16), Height: kit.Px(16), Flex: kit.FlexNone})
	header := box(kit.Style{Display: kit.DisplayFlex, ColumnGap: kit.Px(8), AlignItems: kit.AlignCenter}, chip, icon, label)
	w, loop := open(t, box(kit.Style{}, header), kit.Size{W: 1200, H: 760}, env100)
	shown := func(e *kit.Element) bool { return !e.Bounds().Empty() && w.ElementAt(e.WindowBounds().Center()) != nil }
	if !shown(&label.Element) || !shown(chip) {
		t.Fatal("at 1200 px the label and the chip show")
	}
	loop.Send(kit.SizeEvent{Size: kit.Size{W: 1100, H: 760}})
	loop.Settle(5)
	if lbl := w.ElementAt(kit.Pt(90+8+16+8+5, 10)); lbl == &label.Element {
		t.Error("at 1100 px the label still shows")
	}
	if !shown(chip) {
		t.Error("at 1100 px the chip shows")
	}
	if got := icon.Bounds().Min.X; got != 98 {
		t.Errorf("icon at %v, want after the chip", got)
	}
	loop.Send(kit.SizeEvent{Size: kit.Size{W: 900, H: 760}})
	loop.Settle(5)
	if got := icon.Bounds().Min.X; got != 0 {
		t.Errorf("at 900 px the chip hides and the icon comes first, at %v", got)
	}
	loop.Send(kit.SizeEvent{Size: kit.Size{W: 1366, H: 760}})
	loop.Settle(5)
	if !shown(&label.Element) || icon.Bounds().Min.X != 98 {
		t.Error("back at 1366 px everything shows again")
	}
}

// TestContainerBreakpoints checks a container query in rem: a dialog's two-column layout
// (@container dialog (min-width: 42rem)) holds at 16 px text in a 44rem dialog and not at
// 20 px text in a window where the dialog cannot be 44rem wide.
func TestContainerBreakpoints(t *testing.T) {
	a, b := box(kit.Style{Height: kit.Px(20), Flex: kit.Flex1}), box(kit.Style{Height: kit.Px(20), Flex: kit.Flex1})
	body := box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}, a, b)
	body.Style.Breakpoints = []kit.Breakpoint{{
		When:  kit.Query{MinWidth: kit.Rem(42)},
		Apply: func(s *kit.Style) { s.Axis = kit.AxisRow },
	}}
	dialog := box(kit.Style{Width: kit.Rem(44), MaxWidth: kit.Px(900 - 32), Container: true}, body)
	_, loop := open(t, box(kit.Style{}, dialog), kit.Size{W: 900, H: 600}, env100)
	if b.Bounds().Min.X == 0 {
		t.Errorf("at 16 px text the 704 DIP dialog puts the parts side by side: %v", b.Bounds())
	}
	env := env100
	env.Scale.Text = 1.25
	loop.Send(kit.EnvironmentEvent{Env: env})
	loop.Settle(5)
	// 44rem = 880 DIPs, held to 868 by the window; 42rem = 840 > 868? no: 868 ≥ 840.
	if b.Bounds().Min.X == 0 {
		t.Errorf("at 20 px text the dialog is 868 wide, at least 42rem (840): side by side, got %v", b.Bounds())
	}
	// The dialog narrows (in the app, min(44rem, 100vw - 32px) in an 860 DIP window).
	dialog.Style.MaxWidth = kit.Px(860 - 32)
	dialog.Relayout()
	loop.Settle(5)
	if b.Bounds().Min.X != 0 || b.Bounds().Min.Y != 20 {
		t.Errorf("a dialog of 828 DIPs is under 42rem at 20 px text: stacked, got %v", b.Bounds())
	}
}

// TestRTLMirrors checks the right-to-left layout: a row runs from the right, logical
// padding swaps sides, and a list's scroll bar is at the left.
func TestRTLMirrors(t *testing.T) {
	first, second := leaf(50, 10), leaf(60, 10)
	row := box(kit.Style{Display: kit.DisplayFlex, Padding: kit.Edges{Start: kit.Px(20)}, ColumnGap: kit.Px(4)}, first, second)
	_, _ = open(t, box(kit.Style{}, row), kit.Size{W: 400, H: 100}, env100, func(o *kit.WindowOptions) { o.Direction = kit.DirRTL })
	if got := first.Bounds(); got != kit.R(330, 0, 380, 10) {
		t.Errorf("first item %v, want at the right edge inside the start padding", got)
	}
	if got := second.Bounds(); got != kit.R(266, 0, 326, 10) {
		t.Errorf("second item %v", got)
	}
	f := newGridRTL(t)
	if got := f.list.Scrollbar().WindowBounds(); got.Min.X != 0 {
		t.Errorf("RTL scroll bar at %v, want the left edge", got)
	}
	if got := f.list.RowWidget(1).Base().WindowBounds(); got.Min.X != 15 {
		t.Errorf("RTL row at %v, want after the gutter", got)
	}
}

func newGridRTL(t *testing.T) *gridFixture {
	f := &gridFixture{m: newDownloads(100)}
	f.sel = newSelection(f.m)
	var l *kit.List
	l = kit.NewList(f.m, newDownloadRow(f.m, &l), comfortable)
	l.Style = kit.Style{Flex: kit.Flex1}
	f.list = l
	f.w, f.loop = open(t, box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn}, l), kit.Size{W: 600, H: 400}, env100,
		func(o *kit.WindowOptions) { o.Direction = kit.DirRTL })
	return f
}
