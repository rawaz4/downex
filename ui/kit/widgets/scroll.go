package widgets

import (
	"math"

	"downex/ui/kit"
)

// ScrollPane is an overflow: auto box that scrolls its content vertically: a dialog's body,
// a Select's list, the details pane, a Settings page. Its scroll bar is kit's (the WebView
// window's: Chromium's Fluent bar with app.css's scrollbar-color, kit.FluentScroll's
// measurements): a 15 DIP gutter at the inline end that is there only while the content
// overflows, 18 DIP arrow buttons and an 8 DIP pill thumb. The wheel scrolls it (100 DIPs a
// notch), the thumb drags, an arrow or the track held repeats; Focusable makes it a scroll
// region the keyboard reaches while it overflows (arrows 40 DIPs, Page Up and Down 87.5%, Home
// and End). It is a kit.ScrollShower, so kit.Window.ScrollIntoView and UI Automation's
// ScrollItem reach what it holds.
type ScrollPane struct {
	kit.Element
	Content *kit.Element
	// Focusable makes the pane take the focus with Tab while its content overflows.
	Focusable bool
	// OnScroll hears every change of the offset.
	OnScroll func(offset kit.DIP)

	bar      *kit.Scrollbar
	offset   kit.DIP
	contentH kit.DIP
	viewH    kit.DIP
}

var (
	_ kit.ScrollTarget = (*ScrollPane)(nil)
	_ kit.ScrollShower = (*ScrollPane)(nil)
)

// NewScrollPane makes a pane around content (a block box of its own when it is a widget's
// element).
func NewScrollPane(content kit.Widget) *ScrollPane {
	s := &ScrollPane{}
	s.Style = kit.Style{Overflow: kit.OverflowHidden, MinHeight: kit.Px(0)}
	s.Content = content.Base()
	s.bar = kit.NewScrollbar(s)
	s.bar.Style.Hidden = true
	s.Add(content, s.bar)
	return s
}

// Scrollbar is the pane's scroll bar.
func (s *ScrollPane) Scrollbar() *kit.Scrollbar { return s.bar }

// Offset is how far the content is scrolled.
func (s *ScrollPane) Offset() kit.DIP { return s.offset }

// Overflows reports whether the content is taller than the pane.
func (s *ScrollPane) Overflows() bool { return s.contentH > s.viewH+0.5 }

func (s *ScrollPane) maxOffset() kit.DIP { return kit.DIP(math.Max(0, float64(s.contentH-s.viewH))) }

// ScrollRange is kit.ScrollTarget's: the offset, the content's height and the pane's.
func (s *ScrollPane) ScrollRange() (offset, content, view kit.DIP) {
	return s.offset, s.contentH, s.viewH
}

// PageStep is kit.ScrollTarget's: 87.5% of the pane's height.
func (s *ScrollPane) PageStep() kit.DIP { return s.bar.Metrics.PageFor(s.viewH) } // [mac scroll] PageFor

// ScrollTo scrolls to offset (clamped).
func (s *ScrollPane) ScrollTo(offset kit.DIP) {
	offset = kit.DIP(math.Round(float64(min(max(offset, 0), s.maxOffset()))))
	if offset == s.offset {
		return
	}
	s.offset = offset
	s.RelayoutContent()
	s.Invalidate()
	s.bar.Invalidate()
	if s.OnScroll != nil {
		s.OnScroll(offset)
	}
}

// Show scrolls as little as it takes to bring e (inside the content) into view, as
// scrollIntoView({block: 'nearest'}) does.
func (s *ScrollPane) Show(e *kit.Element) {
	if e == nil || !s.Content.Contains(e) {
		return
	}
	r := e.WindowBounds()
	top := r.Min.Y - s.Content.WindowBounds().Min.Y // in the content
	bottom := top + r.Height()
	switch {
	case top < s.offset:
		s.ScrollTo(top)
	case bottom > s.offset+s.viewH:
		s.ScrollTo(bottom - s.viewH)
	}
}

// ScrollToShow is kit.ScrollShower's: Show.
func (s *ScrollPane) ScrollToShow(e *kit.Element) { s.Show(e) }

// ContentWidths is kit.Layouter's: the content's, and the gutter while it overflows.
func (s *ScrollPane) ContentWidths(ctx *kit.LayoutContext) (min, max kit.DIP) {
	min, max = ctx.IntrinsicWidths(s.Content)
	if s.Overflows() {
		t := s.bar.Metrics.Thickness
		min, max = min+t, max+t
	}
	return min, max
}

// ContentHeight is kit.Layouter's: the content's whole height (a maximum on the pane cuts it).
func (s *ScrollPane) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(s.Content, w, w)
}

// LayoutContent is kit.Layouter's: the content at its whole height, scrolled, beside the
// gutter when it overflows, and the bar over the gutter.
func (s *ScrollPane) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	t := s.bar.Metrics.Thickness
	s.viewH = box.Height()
	cw := box.Width()
	h := ctx.HeightFor(s.Content, cw, cw)
	overflow := h > s.viewH+0.5
	if overflow {
		cw = max(cw-t, 0)
		h = ctx.HeightFor(s.Content, cw, cw)
	}
	if h != s.contentH {
		s.bar.Invalidate() // the thumb's length changed
	}
	s.contentH = h
	s.offset = min(s.offset, s.maxOffset())
	x := box.Min.X
	if ctx.RTL && overflow {
		x += t
	}
	ctx.Place(s.Content, kit.R(x, box.Min.Y-s.offset, x+cw, box.Min.Y-s.offset+h), cw)
	s.bar.Style.Hidden = !overflow
	bt := s.bar.Metrics.BarWidth() // [mac scroll] an overlay bar lies over the content
	bx := box.Max.X - bt
	if ctx.RTL {
		bx = box.Min.X
	}
	ctx.Place(s.bar, kit.R(bx, box.Min.Y, bx+bt, box.Max.Y), bt)
	s.Focus = kit.FocusNone
	if s.Focusable && overflow {
		s.Focus = kit.FocusTab
	}
}

// HandleWheel is kit.WheelHandler's: the wheel scrolls the pane while it can move.
func (s *ScrollPane) HandleWheel(in *kit.WheelInput) bool {
	// [mac webkit] WebKit scrolls a Shift + vertical wheel vertically (macOS turns a
	// notched one sideways before it arrives).
	if !s.Overflows() || in.Mods&kit.ModShift != 0 && !s.webKit() {
		return false
	}
	d := in.Delta.Y
	if in.Mode == kit.WheelPages {
		d *= s.PageStep()
	}
	before := s.offset
	s.ScrollTo(s.offset + d)
	return s.offset != before || (d < 0 && before > 0) || (d > 0 && before < s.maxOffset())
}

// [mac webkit] webKit: the window draws for WebKit (kit.Environment.WebKit).
func (s *ScrollPane) webKit() bool {
	w := s.Window()
	return w != nil && w.Environment().WebKit
}

// HandleKey is kit.KeyHandler's: a focused scroll region scrolls with the keys.
func (s *ScrollPane) HandleKey(e kit.KeyEvent) bool {
	if !s.Focused() || e.Action != kit.KeyDown || e.Mods&(kit.ModCtrl|kit.ModAlt|kit.ModMeta) != 0 {
		return false
	}
	switch e.Key {
	case kit.KeyArrowDown:
		s.ScrollTo(s.offset + s.bar.Metrics.Line)
	case kit.KeyArrowUp:
		s.ScrollTo(s.offset - s.bar.Metrics.Line)
	case kit.KeyPageDown, kit.KeySpace:
		s.ScrollTo(s.offset + s.PageStep())
	case kit.KeyPageUp:
		s.ScrollTo(s.offset - s.PageStep())
	case kit.KeyHome:
		s.ScrollTo(0)
	case kit.KeyEnd:
		s.ScrollTo(s.maxOffset())
	default:
		return false
	}
	return true
}

// FillSemantics is kit.SemanticFiller's: a scroll region names nothing itself.
func (s *ScrollPane) FillSemantics(n *kit.SemanticNode) {
	if s.Overflows() {
		n.Range = kit.RangeValue{Valid: true, Min: 0, Max: float64(s.maxOffset()), Value: float64(s.offset)}
	}
}
