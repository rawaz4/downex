package downloads

import (
	"math"
	"time"

	"downex/ui/kit"
)

// The grid scrolls sideways only when even the essential columns do not fit (its rows'
// min-inline-size): the rows and the header move by the table's scrollX, and this bar is the
// grid's horizontal scroll bar, Chromium's Fluent bar along the bottom (the vertical one's
// metrics turned: a 15 DIP gutter, 18 DIP arrows, an 8 DIP pill thumb of 17 DIPs at least).
type hscroll struct {
	kit.Element
	t        *Table
	arrows   [2]kit.Path
	res      kit.Resources
	hot      bool
	dragging bool
	dragFrom kit.DIP
	dragOff  kit.DIP
	pressDir int
	pressOn  int
	repeat   *kit.Timer
	nextAt   time.Time
	reveal   kit.ScrollReveal // [mac scroll] an overlay bar's visibility
}

func newHScroll(t *Table) *hscroll {
	h := &hscroll{t: t}
	h.Semantics.Role = kit.RoleScrollBar
	h.Semantics.Orientation = kit.OrientationHorizontal
	h.Style.Hidden = true
	return h
}

// [mac scroll] the window's style: FluentScroll on Windows, a macOS bar on the Mac.
func (h *hscroll) metrics() kit.ScrollMetrics {
	if w := h.Window(); w != nil {
		return kit.ScrollMetricsFor(w.Environment().ScrollBars)
	}
	return kit.FluentScroll
}

// ContentWidths is kit.Layouter's.
func (h *hscroll) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (h *hscroll) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's.
func (h *hscroll) LayoutContent(*kit.LayoutContext, kit.Rect) {}

func (h *hscroll) geometry() (trackX, trackLen, thumbX, thumbLen kit.DIP, ok bool) {
	m := h.metrics()
	off, content, view := h.t.scrollRangeX()
	w := h.Size().W
	if m.Inset > 0 { // [mac scroll] a macOS bar: the track inset at both ends, no arrows
		thumbX, thumbLen, ok = kit.ScrollThumbSpan(m, w, off, content, view, h.scale())
		return m.Inset, max(w-2*m.Inset, 0), thumbX, thumbLen, ok
	}
	trackX, trackLen = m.Button, w-2*m.Button
	if content <= view || trackLen <= 0 {
		return trackX, max(trackLen, 0), 0, 0, false
	}
	thumbLen = min(max(m.MinThumb, kit.DIP(math.Round(float64(trackLen*view/content)))), trackLen)
	maxOff := content - view
	f := float64((trackLen - thumbLen) * min(max(off, 0), maxOff) / maxOff)
	thumbX = trackX + kit.DIP(math.Floor(f))
	return trackX, trackLen, thumbX, thumbLen, true
}

func (h *hscroll) thumbRect() (kit.Rect, bool) {
	_, _, x, n, ok := h.geometry()
	if !ok {
		return kit.Rect{}, false
	}
	m := h.metrics()
	if m.Inset > 0 { // [mac scroll] Inset above the bottom edge; an overlay thumb grows on hover
		t := m.Thumb
		if m.Overlay && m.HoverThumb > t {
			t += (m.HoverThumb - t) * h.reveal.Expansion()
		}
		y := h.Size().H - m.Inset - t
		return kit.R(x, y, x+n, y+t), true
	}
	y := (h.Size().H - m.Thumb) / 2
	return kit.R(x, y, x+n, y+m.Thumb), true
}

// [mac scroll]
func (h *hscroll) scale() kit.Scale {
	if w := h.Window(); w != nil {
		return w.Environment().Scale
	}
	return kit.Scale{PixelsPerDIP: 1, Text: 1}
}

// paintMac draws a macOS bar: the thumb alone in the knob colour, faded with an overlay bar.
func (h *hscroll) paintMac(p *kit.PaintContext, m kit.ScrollMetrics) {
	off, _, _ := h.t.scrollRangeX()
	c := p.Palette.ScrollThumb
	if m.Overlay {
		h.reveal.El = &h.Element
		h.reveal.Offset(h.Window(), m, off)
		c.A *= h.reveal.Alpha()
	}
	th, ok := h.thumbRect()
	if !ok || c.A <= 0 {
		return
	}
	p.Canvas.FillRoundedRect(kit.RoundRect(th.Add(p.Origin), th.Height()/2), c)
}

// [/mac scroll]

// Paint is kit.Painter's: the thumb and the two arrows in the palette's scroll colours.
func (h *hscroll) Paint(p *kit.PaintContext) {
	if _, _, _, _, ok := h.geometry(); !ok {
		return
	}
	if m := h.metrics(); m.Inset > 0 { // [mac scroll]
		h.paintMac(p, m)
		return
	}
	c := p.Palette.ScrollThumb
	if th, ok := h.thumbRect(); ok {
		tc := c
		if (h.hot || h.dragging) && p.Palette.ScrollThumbActive.A > 0 {
			tc = p.Palette.ScrollThumbActive
		}
		p.Canvas.FillRoundedRect(kit.RoundRect(th.Add(p.Origin), h.metrics().Thumb/2), tc)
	}
	w := h.Window()
	if w == nil || w.Host() == nil || c.A <= 0 {
		return
	}
	res := w.Host().Resources()
	if res != h.res {
		h.release()
		h.res = res
	}
	cy := h.Size().H / 2
	for i, left := range [2]bool{true, false} {
		if h.arrows[i] == nil {
			var d kit.PathData
			tip, base := kit.DIP(7), kit.DIP(11)
			if !left {
				tip, base = -7, -11
			}
			d.MoveTo(kit.Pt(tip, 0))
			d.LineTo(kit.Pt(base, 3))
			d.LineTo(kit.Pt(base, -3))
			d.Close()
			path, err := res.NewPath(d)
			if err != nil {
				return
			}
			h.arrows[i] = path
		}
		at := kit.Pt(0, cy)
		if !left {
			at = kit.Pt(h.Size().W, cy)
		}
		p.Canvas.PushTransform(kit.Translation(p.Origin.X+at.X, p.Origin.Y+at.Y))
		p.Canvas.FillPath(h.arrows[i], c)
		p.Canvas.StrokePath(h.arrows[i], kit.Stroke{Width: 2, Join: kit.JoinRound}, c)
		p.Canvas.Pop()
	}
}

func (h *hscroll) release() {
	for i, a := range h.arrows {
		if a != nil {
			a.Release()
			h.arrows[i] = nil
		}
	}
}

// Detached is kit.Detacher's.
func (h *hscroll) Detached() {
	h.reveal.Reset() // [mac scroll]
	h.release()
	h.res = nil
	h.stopRepeat()
	h.dragging, h.hot = false, false
}

// HandlePointer is kit.PointerHandler's: the thumb drags; an arrow or the track held repeats.
func (h *hscroll) HandlePointer(in *kit.PointerInput) bool {
	_, trackLen, x0, n, ok := h.geometry()
	if !ok {
		return in.Action != kit.PointerMove
	}
	m := h.metrics()
	x := in.Local.X
	th, _ := h.thumbRect()
	switch in.Action {
	case kit.PointerMove:
		if h.dragging {
			_, content, view := h.t.scrollRangeX()
			if free := trackLen - n; free > 0 {
				h.t.scrollToX(h.dragOff + (in.Pos.X-h.dragFrom)*(content-view)/free)
			}
			return true
		}
		h.setHot(th.Contains(in.Local))
		return true
	case kit.PointerDown:
		if in.Button != kit.ButtonLeft {
			return true
		}
		w := h.Window()
		if m.Inset > 0 && w != nil { // [mac scroll] no arrows; Option or the setting jumps
			if m.Overlay {
				h.reveal.SetHold(w, true)
			}
			jump := w.Environment().ScrollClickJumps != (in.Mods&kit.ModAlt != 0)
			if jump && !(x >= x0 && x < x0+n) {
				_, content, view := h.t.scrollRangeX()
				if free := trackLen - n; free > 0 {
					h.t.scrollToX((x - m.Inset - n/2) * (content - view) / free)
				}
				_, _, x0, n, _ = h.geometry()
			}
		}
		switch {
		case th.Contains(in.Local) || (m.Inset > 0 && x >= x0 && x < x0+n): // [mac scroll] the bar's whole height
			off, _, _ := h.t.scrollRangeX()
			h.dragging, h.dragFrom, h.dragOff = true, in.Pos.X, off
			h.Invalidate()
		case m.Inset > 0 && x < x0: // [mac scroll] the track (no arrows)
			h.press(-1, 2)
		case m.Inset > 0:
			h.press(1, 2)
		case x < m.Button:
			h.press(-1, 1)
		case x >= h.Size().W-m.Button:
			h.press(1, 1)
		case x < x0:
			h.press(-1, 2)
		case x >= x0+n:
			h.press(1, 2)
		}
		if w != nil {
			w.Capture(&h.Element)
		}
		return true
	case kit.PointerUp, kit.PointerCancel:
		if h.dragging {
			h.dragging = false
			h.Invalidate()
		}
		if w := h.Window(); w != nil {
			w.ReleaseCapture()
		}
		h.stopRepeat()
		if m.Overlay { // [mac scroll]
			h.reveal.SetHold(h.Window(), false)
		}
		h.setHot(in.Action == kit.PointerUp && th.Contains(in.Local))
		return true
	}
	return false
}

// HoverChanged is kit.HoverHandler's.
func (h *hscroll) HoverChanged(on bool) {
	if m := h.metrics(); m.Overlay { // [mac scroll] an overlay bar stays and grows under the pointer
		h.reveal.SetHover(h.Window(), on)
	}
	if !on {
		h.setHot(false)
	}
}

func (h *hscroll) setHot(hot bool) {
	if hot != h.hot {
		h.hot = hot
		h.Invalidate()
	}
}

func (h *hscroll) press(dir, on int) {
	w := h.Window()
	if w == nil {
		return
	}
	h.pressDir, h.pressOn = dir, on
	h.step()
	m := h.metrics()
	h.nextAt = w.Now().Add(m.RepeatDelay)
	h.repeat = w.After(m.RepeatDelay, h.repeatStep)
}

func (h *hscroll) repeatStep() {
	w := h.Window()
	if h.pressDir == 0 || w == nil {
		return
	}
	if !h.step() {
		h.stopRepeat()
		return
	}
	h.nextAt = h.nextAt.Add(h.metrics().RepeatEvery)
	h.repeat = w.After(max(h.nextAt.Sub(w.Now()), 0), h.repeatStep)
}

func (h *hscroll) step() bool {
	off, content, view := h.t.scrollRangeX()
	if h.pressOn == 1 {
		h.t.scrollToX(off + kit.DIP(h.pressDir)*h.metrics().Line)
	} else {
		_, _, x0, n, ok := h.geometry()
		w := h.Window()
		if !ok || w == nil {
			return false
		}
		px := h.WindowToLocal(w.PointerPos()).X
		if (h.pressDir < 0 && px >= x0) || (h.pressDir > 0 && px < x0+n) {
			return false
		}
		h.t.scrollToX(off + kit.DIP(h.pressDir)*h.metrics().PageFor(view)) // [mac scroll] PageFor
	}
	now, _, _ := h.t.scrollRangeX()
	return now != off && now > 0 && now < content-view
}

func (h *hscroll) stopRepeat() {
	h.pressDir, h.pressOn = 0, 0
	if h.repeat != nil {
		h.repeat.Stop()
		h.repeat = nil
	}
}

// ---- the table's side of it ------------------------------------------------------------------

// rowsWidth is the rows' width: the grid's, or the columns' least (--min-inline) when that is
// more, and at least what the tracks take.
func (t *Table) rowsWidth(view kit.DIP) kit.DIP {
	need := kit.DIP(math.Max(t.layout.MinInline, rowPad+t.layout.Content))
	return max(view, need)
}

// scrollRangeX is the sideways offset, the rows' width and the visible width.
func (t *Table) scrollRangeX() (off, content, view kit.DIP) {
	view = t.viewW
	return t.scrollX, t.rowsWidth(view), view
}

// scrollToX scrolls sideways to off (clamped).
func (t *Table) scrollToX(off kit.DIP) {
	_, content, view := t.scrollRangeX()
	off = min(max(off, 0), max(content-view, 0))
	if off == t.scrollX {
		return
	}
	t.scrollX = off
	t.columnsMoved()
}

// columnsMoved lays the header and the rows out again where the sideways offset puts them.
func (t *Table) columnsMoved() {
	t.header.RelayoutContent()
	first, last := t.Realised()
	last = min(last, t.model.Len())
	for i := first; i < last; i++ {
		if r, ok := t.RowWidget(t.model.Key(i)).(*row); ok {
			r.RelayoutContent()
		}
	}
	t.dl.grid.relayoutGhosts()
	t.dl.grid.hbar.Invalidate()
	t.Invalidate()
}

// HandleWheel is kit.WheelHandler's: Shift and the wheel's sideways turns scroll sideways while
// the columns overflow; the rest is the list's.
func (t *Table) HandleWheel(in *kit.WheelInput) bool {
	_, content, view := t.scrollRangeX()
	if content > view {
		d := in.Delta.X
		// [mac webkit] macOS has already turned Shift + a wheel sideways (deltaX), and
		// WebKit scrolls a Shift + vertical swipe vertically: no swap there.
		if in.Mods&kit.ModShift != 0 && !t.Window().Environment().WebKit {
			d = in.Delta.Y
		}
		if d != 0 {
			if in.Mode == kit.WheelPages {
				d *= kit.ScrollMetricsFor(t.Window().Environment().ScrollBars).PageFor(view) // [mac scroll]
			}
			t.scrollToX(t.scrollX + d)
			return true
		}
	}
	return t.List.HandleWheel(in)
}
