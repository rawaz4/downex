package kit

import (
	"math"
	"time"
)

// ScrollMetrics are a scroll bar's measurements and steps, in DIPs.
type ScrollMetrics struct {
	// Thickness is the bar's width: the gutter a scrolled list leaves for it.
	Thickness DIP
	// Button is the length of the arrow button at each end.
	Button DIP
	// Thumb is the thumb's thickness; the thumb is a pill centred in the bar.
	Thumb DIP
	// MinThumb is the thumb's shortest length.
	MinThumb DIP
	// Line is how far an arrow click (or an arrow key) scrolls.
	Line DIP
	// Page is the share of the visible length that a click on the track (or Page Up and
	// Page Down) scrolls.
	Page float32
	// RepeatDelay and RepeatEvery are how a held arrow or track press repeats.
	RepeatDelay, RepeatEvery time.Duration

	// [mac scroll] The macOS bars' own (MacLegacyScroll, MacOverlayScroll; zero in
	// FluentScroll, whose bars ignore them). Inset is the track's inset at both ends and the
	// thumb's from the bar's outer edge; a bar with an Inset has no arrows and its thumb is
	// not centred. PageOverlap makes a page at least the view less it (PageFor).
	Inset, PageOverlap DIP
	// Overlay: the bar lies over the content's edge, Width wide, and shows only while the
	// content scrolls (ScrollReveal): HideDelay after the last scroll it fades out over
	// FadeOut. While the pointer is over it the thumb grows to HoverThumb over Grow.
	Overlay                  bool
	Width, HoverThumb        DIP
	HideDelay, FadeOut, Grow time.Duration
	// Smooth is how long a page from a track click takes to scroll (0: at once).
	Smooth time.Duration
	// [/mac scroll]
}

// FluentScroll are the metrics of the scroll bar the WebView window shows: Chromium's
// Fluent scroll bar with app.css's scrollbar-color, measured in headless Edge 156 (the
// WebView2 runtime's build) on 2026-11-03: a 15 DIP gutter (19 px at 125%), 18 DIP arrow
// buttons, an 8 DIP pill thumb no shorter than 17 DIPs, 40 DIPs an arrow click, 87.5% of
// the visible height a track click. The repeat timing is Chromium's scroll bar timer
// (a held arrow scrolled 513 DIPs in 830 ms, as 13 steps of 40 do with a 250 ms delay
// and a 50 ms interval).
var FluentScroll = ScrollMetrics{
	Thickness:   15,
	Button:      18,
	Thumb:       8,
	MinThumb:    17,
	Line:        40,
	Page:        0.875,
	RepeatDelay: 250 * time.Millisecond,
	RepeatEvery: 50 * time.Millisecond,
}

// ScrollTarget is what a scroll bar scrolls: a List, or a widget's scroll pane.
type ScrollTarget interface {
	// ScrollRange is the scroll offset, the content's length and the visible length.
	ScrollRange() (offset, content, view DIP)
	// ScrollTo scrolls to offset (the target clamps it).
	ScrollTo(offset DIP)
	// PageStep is how far a click on the track scrolls.
	PageStep() DIP
}

// Scrollbar is a vertical scroll bar drawn by the kit over its target's edge: arrow
// buttons at both ends and a pill thumb on a transparent track, in the palette's
// ScrollThumb colours. Its owner places it (as a child laid out over the gutter) and hides
// it while the content fits; the bar takes its own pointer input (the thumb drags, an arrow
// or the track held repeats).
type Scrollbar struct {
	Element
	Metrics ScrollMetrics

	target   ScrollTarget
	hot      bool // the pointer is over the thumb
	dragging bool
	dragFrom Point
	dragOff  DIP
	repeat   *Timer
	nextAt   time.Time // when the next repeat is due
	pressDir int       // -1 or 1 while an arrow or the track is held
	pressOn  int       // 1: an arrow, 2: the track
	arrows   [2]Path
	arrowRes Resources

	// [mac scroll] (scrollmac.go)
	style    ScrollBarStyle
	reveal   ScrollReveal
	smooth   *Tween
	smoothTo DIP
	// [/mac scroll]
}

// NewScrollbar makes a bar for t with FluentScroll's metrics.
func NewScrollbar(t ScrollTarget) *Scrollbar {
	b := &Scrollbar{Metrics: FluentScroll, target: t}
	b.self = b
	return b
}

// geometry is the track (between the buttons), the thumb's offset along it and its
// length, in the bar's coordinates.
func (b *Scrollbar) geometry() (trackTop, trackLen, thumbTop, thumbLen DIP, ok bool) {
	if b.Metrics.macStyle() { // [mac scroll]
		return b.macGeometry()
	}
	off, content, view := b.target.ScrollRange()
	h := b.bounds.Height()
	trackTop = b.Metrics.Button
	trackLen = h - 2*b.Metrics.Button
	if content <= view || trackLen <= 0 {
		return trackTop, max(trackLen, 0), 0, 0, false
	}
	thumbLen = min(max(b.Metrics.MinThumb, DIP(math.Round(float64(trackLen*view/content)))), trackLen)
	maxOff := content - view
	thumbTop = trackTop + DIP(math.Floor(float64((trackLen-thumbLen)*clampDIP(off, 0, maxOff)/maxOff)))
	return trackTop, trackLen, thumbTop, thumbLen, true
}

// Dragging reports whether the thumb is being dragged; Hot whether the pointer is over it.
func (b *Scrollbar) Dragging() bool { return b.dragging }
func (b *Scrollbar) Hot() bool      { return b.hot }

// ThumbRect is the thumb in the bar's coordinates; ok is false while the content fits.
func (b *Scrollbar) ThumbRect() (Rect, bool) { return b.thumbRect() }

func (b *Scrollbar) thumbRect() (Rect, bool) {
	if b.Metrics.macStyle() { // [mac scroll]
		return b.macThumbRect()
	}
	_, _, top, n, ok := b.geometry()
	if !ok {
		return Rect{}, false
	}
	x := (b.bounds.Width() - b.Metrics.Thumb) / 2
	return R(x, top, x+b.Metrics.Thumb, top+n), true
}

// ContentWidths, ContentHeight and LayoutContent make the bar a Layouter with no content
// of its own: its owner places it.
func (b *Scrollbar) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }
func (b *Scrollbar) ContentHeight(*LayoutContext, DIP) DIP       { return 0 }
func (b *Scrollbar) LayoutContent(*LayoutContext, Rect)          {}

// Paint draws the arrows and the thumb.
func (b *Scrollbar) Paint(p *PaintContext) {
	if b.Metrics.macStyle() { // [mac scroll]
		b.paintMac(p)
		return
	}
	_, _, _, _, ok := b.geometry()
	if !ok {
		return
	}
	c := p.Palette.ScrollThumb
	if thumb, ok := b.thumbRect(); ok {
		tc := c
		if (b.hot || b.dragging) && p.Palette.ScrollThumbActive.A > 0 {
			tc = p.Palette.ScrollThumbActive
		}
		p.Canvas.FillRoundedRect(RoundRect(thumb.Add(p.Origin), b.Metrics.Thumb/2), tc)
	}
	b.paintArrows(p, c)
}

// paintArrows draws the two arrow glyphs: rounded triangles 8 DIPs wide and 6 high, 6
// DIPs from the ends (measured at 400% in headless Edge: an inner triangle with corners
// rounded by 1 DIP).
func (b *Scrollbar) paintArrows(p *PaintContext, c Color) {
	res := b.res()
	if res == nil || c.A <= 0 {
		return
	}
	if b.arrowRes != res {
		b.releaseArrows()
		b.arrowRes = res
	}
	cx := b.bounds.Width() / 2
	h := b.bounds.Height()
	for i, up := range [2]bool{true, false} {
		if b.arrows[i] == nil {
			var d PathData
			tip, base := DIP(7), DIP(11)
			if !up {
				tip, base = -7, -11
			}
			d.MoveTo(Pt(0, tip))
			d.LineTo(Pt(3, base))
			d.LineTo(Pt(-3, base))
			d.Close()
			path, err := res.NewPath(d)
			if err != nil {
				return
			}
			b.arrows[i] = path
		}
		at := Pt(cx, 0)
		if !up {
			at = Pt(cx, h)
		}
		p.Canvas.PushTransform(Translation(p.Origin.X+at.X, p.Origin.Y+at.Y))
		p.Canvas.FillPath(b.arrows[i], c)
		p.Canvas.StrokePath(b.arrows[i], Stroke{Width: 2, Join: JoinRound}, c)
		p.Canvas.Pop()
	}
}

func (b *Scrollbar) res() Resources {
	if b.win == nil || b.win.host == nil {
		return nil
	}
	return b.win.host.Resources()
}

func (b *Scrollbar) releaseArrows() {
	for i, a := range b.arrows {
		if a != nil {
			a.Release()
			b.arrows[i] = nil
		}
	}
}

// Detached releases the arrow paths and stops a repeat.
func (b *Scrollbar) Detached() {
	b.macDetached() // [mac scroll]
	b.releaseArrows()
	b.arrowRes = nil
	b.stopRepeat()
	b.dragging, b.hot = false, false
}

// HandlePointer is PointerHandler's: dragging the thumb, and holding an arrow or the track.
func (b *Scrollbar) HandlePointer(in *PointerInput) bool {
	if b.Metrics.macStyle() { // [mac scroll]
		return b.macPointer(in)
	}
	_, _, top, n, ok := b.geometry()
	if !ok {
		return in.Action != PointerMove
	}
	y := in.Local.Y
	thumb, _ := b.thumbRect()
	switch in.Action {
	case PointerMove, PointerEnter:
		if b.dragging {
			_, trackLen, _, _, _ := b.geometry()
			_, content, view := b.target.ScrollRange()
			if free := trackLen - n; free > 0 {
				b.target.ScrollTo(b.dragOff + (in.Pos.Y-b.dragFrom.Y)*(content-view)/free)
			}
			return true
		}
		b.setHot(thumb.Contains(in.Local))
		return true
	case PointerLeave:
		b.setHot(false)
		return false
	case PointerDown:
		if in.Button != ButtonLeft {
			return true
		}
		w := b.win
		switch {
		case thumb.Contains(in.Local):
			off, _, _ := b.target.ScrollRange()
			b.dragging, b.dragFrom, b.dragOff = true, in.Pos, off
			b.Invalidate()
			w.Capture(&b.Element)
		case y < b.Metrics.Button:
			b.press(-1, 1)
		case y >= b.bounds.Height()-b.Metrics.Button:
			b.press(1, 1)
		case y < top:
			b.press(-1, 2)
		case y >= top+n:
			b.press(1, 2)
		}
		if b.pressDir != 0 {
			w.Capture(&b.Element)
		}
		return true
	case PointerUp, PointerCancel:
		if b.dragging {
			b.dragging = false
			b.Invalidate()
		}
		b.stopRepeat()
		b.setHot(in.Action == PointerUp && thumb.Contains(in.Local))
		return true
	}
	return false
}

func (b *Scrollbar) setHot(hot bool) {
	if hot != b.hot {
		b.hot = hot
		b.Invalidate()
	}
}

// press steps once in dir (an arrow: on 1, the track: on 2) and repeats while held.
func (b *Scrollbar) press(dir, on int) {
	b.pressDir, b.pressOn = dir, on
	b.step()
	b.nextAt = b.win.Now().Add(b.Metrics.RepeatDelay)
	b.repeat = b.win.After(b.Metrics.RepeatDelay, b.repeatStep)
}

// repeatStep steps again; the repeats keep their own beat (every RepeatEvery from the
// first), however late each one runs.
func (b *Scrollbar) repeatStep() {
	if b.pressDir == 0 || b.win == nil {
		return
	}
	if !b.step() {
		b.stopRepeat()
		return
	}
	b.nextAt = b.nextAt.Add(b.Metrics.RepeatEvery)
	b.repeat = b.win.After(max(b.nextAt.Sub(b.win.Now()), 0), b.repeatStep)
}

// step scrolls once and reports whether holding on should go on.
func (b *Scrollbar) step() bool {
	if b.Metrics.macStyle() && b.pressOn == 2 { // [mac scroll]
		return b.macStep()
	}
	off, content, view := b.target.ScrollRange()
	if b.pressOn == 1 {
		b.target.ScrollTo(off + DIP(b.pressDir)*b.Metrics.Line)
	} else {
		// The track stops once the thumb reaches the pointer.
		_, _, top, n, ok := b.geometry()
		y := b.WindowToLocal(b.win.PointerPos()).Y
		if !ok || (b.pressDir < 0 && y >= top) || (b.pressDir > 0 && y < top+n) {
			return false
		}
		b.target.ScrollTo(off + DIP(b.pressDir)*b.target.PageStep())
	}
	now, _, _ := b.target.ScrollRange()
	return now != off && now > 0 && now < content-view
}

func (b *Scrollbar) stopRepeat() {
	b.pressDir, b.pressOn = 0, 0
	if b.repeat != nil {
		b.repeat.Stop()
		b.repeat = nil
	}
}
