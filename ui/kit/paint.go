package kit

// Palette holds the colours the kit itself draws with; everything else is drawn by the
// widgets in their theme's colours. The views set it from the theme's tokens
// (Window.SetPalette), in both themes and in forced colours.
type Palette struct {
	// Ground is painted under everything (the window's --chrome).
	Ground Color
	// Text is the colour of labels that have none of their own (body's color: --ink).
	Text Color
	// Focus is the focus ring (--focus).
	Focus Color
	// ScrollThumb is a scroll bar's thumb and arrows (app.css's scrollbar-color thumb,
	// --line-strong).
	ScrollThumb Color
	// ScrollThumbActive is the thumb under the pointer or dragged. WebView2 derives it from
	// the thumb colour; with app.css's it measures #959b98 in the light theme and #656f6a in
	// the dark one (headless Edge 156, 2026-11-03).
	ScrollThumbActive Color
}

// PaintContext is what a widget draws with during Window.Paint. Its drawing methods take
// coordinates in the element's own space (its border box's top-left is 0, 0) and snap the
// edges of boxes to device pixels, as Chromium does when it paints CSS boxes.
type PaintContext struct {
	// Canvas is the frame's canvas, in window coordinates.
	Canvas Canvas
	Scale  Scale
	Env    Environment
	// Palette is the window's palette.
	Palette Palette
	// Origin is the element's border box's top-left in window coordinates.
	Origin Point
	// Dirty is the area being painted, in window coordinates.
	Dirty Rect

	e *Element
	w *Window
}

// Element is the element being painted.
func (p *PaintContext) Element() *Element { return p.e }

// Size is the element's border-box size.
func (p *PaintContext) Size() Size { return p.e.bounds.Size() }

// Window maps a rectangle in the element's space to window coordinates.
func (p *PaintContext) Window(r Rect) Rect { return r.Add(p.Origin) }

// Visible reports whether any of r (element space) is in the dirty area.
func (p *PaintContext) Visible(r Rect) bool { return !r.Add(p.Origin).Intersect(p.Dirty).Empty() }

// FillRect fills r (element space), its edges snapped to device pixels.
func (p *PaintContext) FillRect(r Rect, c Color) {
	if c.A <= 0 {
		return
	}
	p.Canvas.FillRect(p.Scale.SnapRect(r.Add(p.Origin)), c)
}

// FillRoundedRect fills r (element space), its edges snapped.
func (p *PaintContext) FillRoundedRect(r RRect, c Color) {
	if c.A <= 0 {
		return
	}
	r.Rect = p.Scale.SnapRect(r.Rect.Add(p.Origin))
	if _, ok := r.Uniform(); ok && r.Radii.TopLeft <= 0 {
		p.Canvas.FillRect(r.Rect, c)
		return
	}
	p.Canvas.FillRoundedRect(r.Normalized(), c)
}

// Border strokes a CSS border w wide inside the rounded box r (element space): its outer
// edge snapped, its width snapped as CSS snaps border widths.
func (p *PaintContext) Border(r RRect, w DIP, c Color) {
	w = p.Scale.SnapWidth(w)
	if w <= 0 || c.A <= 0 {
		return
	}
	r.Rect = p.Scale.SnapRect(r.Rect.Add(p.Origin))
	r = r.Normalized()
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), Stroke{Width: w}, c)
}

// DrawText draws l with its box's top-left at at (element space).
func (p *PaintContext) DrawText(l TextLayout, at Point, c Color) {
	if l == nil || c.A <= 0 {
		return
	}
	p.Canvas.DrawText(l, at.Add(p.Origin), c)
}

// DrawLine strokes a line between two points of the element's space.
func (p *PaintContext) DrawLine(a, b Point, s Stroke, c Color) {
	p.Canvas.DrawLine(a.Add(p.Origin), b.Add(p.Origin), s, c)
}

// FocusRing draws the focus ring around r (element space) as ring describes it, in the
// palette's focus colour: CSS's outline, whose corners follow the box's radii.
func (p *PaintContext) FocusRing(r RRect, ring Ring) {
	if ring.None || p.Palette.Focus.A <= 0 {
		return
	}
	offset, width := ring.metrics(p.Scale)
	width = p.Scale.SnapWidth(width)
	if width <= 0 {
		return
	}
	r.Rect = p.Scale.SnapRect(r.Rect.Add(p.Origin))
	edge := r.Normalized().Inset(-offset)
	p.Canvas.StrokeRoundedRect(edge.Inset(-width/2), Stroke{Width: width}, p.Palette.Focus)
}

// radii are e's corner radii in effect.
func (w *Window) radii(e *Element, st *Style) Radii {
	return st.Radius.resolve(w.scale(), e.bounds.Width(), w.rtl())
}

// paintBox draws e's box: outer shadows, background, inset shadows and border.
func (w *Window) paintBox(p *PaintContext, st *Style) {
	e := p.e
	if st.Background.A <= 0 && st.BorderColor.A <= 0 && len(st.Shadows) == 0 {
		return
	}
	box := RRect{Rect: p.Scale.SnapRect(e.bounds.Size().rect().Add(p.Origin)), Radii: w.radii(e, st)}.Normalized()
	for i := len(st.Shadows) - 1; i >= 0; i-- {
		if s := st.Shadows[i]; !s.Inset && s.Color.A > 0 {
			p.Canvas.DrawShadow(box, s)
		}
	}
	if st.Background.A > 0 {
		if r, ok := box.Uniform(); ok && r <= 0 {
			p.Canvas.FillRect(box.Rect, st.Background)
		} else {
			p.Canvas.FillRoundedRect(box, st.Background)
		}
	}
	for i := len(st.Shadows) - 1; i >= 0; i-- {
		if s := st.Shadows[i]; s.Inset && s.Color.A > 0 {
			p.Canvas.DrawShadow(box, s)
		}
	}
	if st.BorderColor.A <= 0 {
		return
	}
	b := e.border
	s := p.Scale
	t, r, bt, l := s.SnapWidth(b.Top), s.SnapWidth(b.Right), s.SnapWidth(b.Bottom), s.SnapWidth(b.Left)
	if t == r && t == bt && t == l {
		if t > 0 {
			p.Canvas.StrokeRoundedRect(box.Inset(t/2), Stroke{Width: t}, st.BorderColor)
		}
		return
	}
	// Sides of different widths (border-block, border-inline-start): straight sides.
	x0, y0, x1, y1 := box.Rect.Min.X, box.Rect.Min.Y, box.Rect.Max.X, box.Rect.Max.Y
	c := st.BorderColor
	if t > 0 {
		p.Canvas.FillRect(R(x0, y0, x1, y0+t), c)
	}
	if bt > 0 {
		p.Canvas.FillRect(R(x0, y1-bt, x1, y1), c)
	}
	if l > 0 {
		p.Canvas.FillRect(R(x0, y0+t, x0+l, y1-bt), c)
	}
	if r > 0 {
		p.Canvas.FillRect(R(x1-r, y0+t, x1, y1-bt), c)
	}
}

func (s Size) rect() Rect { return Rect{Max: Point{s.W, s.H}} }

// overflow is how far e's drawing may reach outside its border box, on each side: its
// outer shadows, its focus ring, and a label's glyphs past its line boxes.
func (w *Window) overflow(e *Element) Insets {
	st := e.used
	if st == nil {
		st = &e.Style
	}
	var d DIP
	for _, s := range st.Shadows {
		if s.Inset {
			continue
		}
		dx, dy := s.Offset.X, s.Offset.Y
		d = max(d, abs32(dx)+s.Blur+s.Spread, abs32(dy)+s.Blur+s.Spread)
	}
	if !e.Ring.None && e.Focus != FocusNone {
		o, wd := e.Ring.metrics(w.scale())
		d = max(d, o+wd)
	}
	o := UniformInsets(d)
	if l, ok := e.self.(*Label); ok {
		ink := l.inkOverflow()
		o = Insets{Top: max(o.Top, ink.Top), Right: max(o.Right, ink.Right), Bottom: max(o.Bottom, ink.Bottom), Left: max(o.Left, ink.Left)}
	}
	// what an element paints outside its box by itself
	if p, ok := e.self.(PaintOverflower); ok {
		ink := p.PaintOverflow()
		o = Insets{Top: max(o.Top, ink.Top), Right: max(o.Right, ink.Right), Bottom: max(o.Bottom, ink.Bottom), Left: max(o.Left, ink.Left)}
	}
	return o
}

// PaintOverflower is an element that paints outside its border box by itself (a shadow its
// Paint draws, a rise it translates by), not through its style: how far on each side. It
// counts, as a style's shadows do, in the area repainted when the element moves, changes size
// or is invalidated, and in the paint's culling. Both backends keep a window's pixels between
// frames and repaint only what was invalidated, so without it a shrinking card left its old
// shadow behind (the command palette, on macOS and on Windows).
type PaintOverflower interface {
	PaintOverflow() Insets
}

// ShadowsOverflow is how far outer shadows reach outside their box on any side as the
// backends draw them (ui/internal/gfx: a Gaussian of σ = blur/2 cut at 3σ, so 1.5 × the
// blur, not CSS's nominal blur).
func ShadowsOverflow(ss []Shadow) DIP {
	var d DIP
	for _, s := range ss {
		if s.Inset {
			continue
		}
		b := s.Blur * 3 / 2
		d = max(d, abs32(s.Offset.X)+b+s.Spread, abs32(s.Offset.Y)+b+s.Spread)
	}
	return d
}

// outset is r grown by o on each side and by extra all round.
func outset(r Rect, o Insets, extra DIP) Rect {
	return r.Inset(Insets{Top: -o.Top - extra, Right: -o.Right - extra, Bottom: -o.Bottom - extra, Left: -o.Left - extra})
}

// visualRect is the area e may draw in, in window coordinates.
func (w *Window) visualRect(e *Element) Rect {
	r := e.WindowBounds()
	if o := w.overflow(e); o != (Insets{}) {
		r = outset(r, o, 1)
	}
	return r
}
