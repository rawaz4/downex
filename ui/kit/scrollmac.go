package kit

import (
	"math"
	"time"
)

// [mac scroll] The macOS scroll bars. Today's Mac window is WebKit (WKWebView), whose scroll
// bars are AppKit's (NSScrollerImp) in the style the system asks for (System Settings ›
// Appearance › Show scroll bars, and NSScroller.preferredScrollerStyle: overlay with a
// trackpad, legacy with a mouse), with app.css's scrollbar-color (html { scrollbar-color:
// var(--line-strong) transparent }) as the knob's colour and a transparent track. A host
// that draws such bars says so in Environment.ScrollBars; Windows leaves it zero, the Fluent
// bars of FluentScroll, and nothing of this file runs there.

// ScrollBarStyle is the kind of scroll bar the system asks for (Environment.ScrollBars).
type ScrollBarStyle uint8

// The scroll bar styles.
const (
	// ScrollBarsFluent is Chromium's Fluent bar (FluentScroll), the WebView2 window's: the
	// zero value, so a host that says nothing gets it.
	ScrollBarsFluent ScrollBarStyle = iota
	// ScrollBarsMacOverlay is macOS's overlay scroller (MacOverlayScroll): no gutter, a
	// thumb over the content's edge that shows while the content scrolls and fades out,
	// and grows while the pointer is over it.
	ScrollBarsMacOverlay
	// ScrollBarsMacLegacy is macOS's legacy scroller (MacLegacyScroll): a gutter that is
	// always there while the content overflows, and a thumb without arrows.
	ScrollBarsMacLegacy
)

// MacLegacyScroll are the metrics of WebKit's legacy scroller on macOS 26 with app.css's
// scrollbar-color, measured on WKWebView snapshots (2026-11-09): a 17 pt gutter (the
// element's offsetWidth - clientWidth), an 11 pt pill thumb 3 pt from both of the gutter's
// sides, a track inset 3 pt at both ends, a thumb of round(track × view ÷ content) and 20
// pt at least (NSScrollerImp's knobMinLength), no arrows, no visible track (the track colour
// is transparent). A click on the track pages by max(80% of the view, the view less 40)
// (WebKit's ScrollableArea::pageStep: 260 of 300), animated in about 180 ms; held, it pages
// again after half a second, then about every 140 ms until the thumb reaches the pointer.
var MacLegacyScroll = ScrollMetrics{
	Thickness:   17,
	Thumb:       11,
	MinThumb:    20,
	Line:        40,
	Page:        0.8,
	PageOverlap: 40,
	Inset:       3,
	RepeatDelay: 500 * time.Millisecond,
	RepeatEvery: 140 * time.Millisecond,
	Smooth:      180 * time.Millisecond,
}

// MacOverlayScroll are the metrics of WebKit's overlay scroller on macOS 26 with app.css's
// scrollbar-color, measured on WKWebView snapshots (2026-11-09): no gutter; a 6 pt pill
// thumb 3 pt from the content's edge, in the knob colour at full opacity, that appears at
// once when the content scrolls (by the wheel, a key or a script), stays 500 ms after the
// last scroll and fades out linearly over 250 ms; a track inset 3 pt at both ends and a
// thumb of 26 pt at least (NSScrollerImp's knobMinLength). While it shows, the bar takes
// the pointer over its 14 pt (the expanded track box) and the thumb grows to 11 pt
// (NSScrollerImp's expanded track width; WebKit's synthetic events do not reach AppKit's
// rollover tracking, so the growth itself was not measured: 150 ms is AppKit's look, by
// eye). While it is hidden the pointer goes through to the content (a click there reaches
// the page and scrolls nothing). Track clicks are the legacy bar's.
var MacOverlayScroll = ScrollMetrics{
	Thickness:   0,
	Width:       14,
	Thumb:       6,
	HoverThumb:  11,
	MinThumb:    26,
	Line:        40,
	Page:        0.8,
	PageOverlap: 40,
	Inset:       3,
	Overlay:     true,
	HideDelay:   500 * time.Millisecond,
	FadeOut:     250 * time.Millisecond,
	Grow:        150 * time.Millisecond,
	RepeatDelay: 500 * time.Millisecond,
	RepeatEvery: 140 * time.Millisecond,
	Smooth:      180 * time.Millisecond,
}

// ScrollMetricsFor is the metrics of a style's bars.
func ScrollMetricsFor(s ScrollBarStyle) ScrollMetrics {
	switch s {
	case ScrollBarsMacOverlay:
		return MacOverlayScroll
	case ScrollBarsMacLegacy:
		return MacLegacyScroll
	}
	return FluentScroll
}

// PageFor is how far a page scrolls in a view of length view: Page of it, and with a
// PageOverlap at least the view less the overlap (WebKit's pageStep).
func (m ScrollMetrics) PageFor(view DIP) DIP {
	p := view * m.Page
	if m.PageOverlap > 0 {
		p = max(p, view-m.PageOverlap)
	}
	return p
}

// macStyle reports whether m are a macOS bar's (inset track, no arrows).
func (m ScrollMetrics) macStyle() bool { return m.Inset > 0 }

// BarWidth is the width an owner lays the bar out at: the gutter, or an overlay bar's own
// width over the content.
func (m ScrollMetrics) BarWidth() DIP {
	if m.Overlay {
		return m.Width
	}
	return m.Thickness
}

// ScrollThumbSpan is a macOS bar's thumb along a track of length (the bar's length): its
// start and length, snapped to s's device pixels; ok is false while the content fits.
func ScrollThumbSpan(m ScrollMetrics, length, off, content, view DIP, s Scale) (start, n DIP, ok bool) {
	trackLen := length - 2*m.Inset
	if content <= view || trackLen <= 0 {
		return 0, 0, false
	}
	n = min(max(m.MinThumb, DIP(math.Round(float64(trackLen*view/content)))), trackLen)
	maxOff := content - view
	start = m.Inset + s.Snap((trackLen-n)*clampDIP(off, 0, maxOff)/maxOff)
	return start, n, true
}

// ScrollReveal is an overlay bar's visibility (MacOverlayScroll): it shows at once when the
// offset changes, HideDelay after the last change it fades out over FadeOut, and while the
// pointer is over it, or it is held, it stays and the thumb grows (Expansion). Its owner
// tells it the offset at every paint (Offset) and the pointer (SetHover, SetHold); El is
// invalidated as it changes and made PassThrough while the bar is hidden.
type ScrollReveal struct {
	El *Element

	m      ScrollMetrics
	alpha  float32
	grow   float32
	hover  bool
	hold   bool
	seen   bool
	last   DIP
	hide   *Timer
	fade   *Tween
	growTw *Tween
	w      *Window
}

// Alpha is the bar's opacity now (0: hidden).
func (r *ScrollReveal) Alpha() float32 { return r.alpha }

// Expansion is how far the thumb has grown, 0 to 1.
func (r *ScrollReveal) Expansion() float32 { return r.grow }

// Offset tells the reveal the offset now; a change from the last one shows the bar. The first
// call only remembers it.
func (r *ScrollReveal) Offset(w *Window, m ScrollMetrics, off DIP) {
	r.m = m
	if !r.seen {
		r.seen, r.last = true, off
		r.passThrough()
		return
	}
	if off == r.last {
		return
	}
	r.last = off
	r.Show(w)
}

// Show shows the bar now and starts the wait before it hides.
func (r *ScrollReveal) Show(w *Window) {
	if w == nil {
		return
	}
	r.w = w
	r.stopFade()
	if r.alpha != 1 {
		r.alpha = 1
		r.invalidate()
	}
	r.passThrough()
	r.restartHide()
}

// SetHover tells that the pointer is over the bar (on) or left it.
func (r *ScrollReveal) SetHover(w *Window, on bool) {
	if on == r.hover {
		return
	}
	r.hover = on
	r.changed(w)
}

// SetHold tells that the thumb is dragged or the track held (on), or let go.
func (r *ScrollReveal) SetHold(w *Window, on bool) {
	if on == r.hold {
		return
	}
	r.hold = on
	r.changed(w)
}

func (r *ScrollReveal) changed(w *Window) {
	if w == nil {
		return
	}
	r.w = w
	if r.hover || r.hold {
		if r.alpha > 0 { // a hidden bar takes no pointer: only one that shows can stay
			r.stopFade()
			r.alpha = 1
			r.stopHide()
			r.growTo(1)
			r.invalidate()
		}
		return
	}
	if r.alpha > 0 {
		r.restartHide()
	}
}

// Reset hides the bar at once and forgets the offset (the bar left its window).
func (r *ScrollReveal) Reset() {
	r.stopHide()
	r.stopFade()
	if r.growTw != nil && r.w != nil {
		r.w.stopAnimation(r.growTw)
	}
	r.growTw = nil
	r.alpha, r.grow, r.hover, r.hold, r.seen = 0, 0, false, false, false
}

func (r *ScrollReveal) restartHide() {
	r.stopHide()
	if r.hover || r.hold || r.w == nil {
		return
	}
	r.hide = r.w.After(r.m.HideDelay, r.startFade)
}

func (r *ScrollReveal) stopHide() {
	if r.hide != nil {
		r.hide.Stop()
		r.hide = nil
	}
}

func (r *ScrollReveal) startFade() {
	r.hide = nil
	if r.w == nil || r.hover || r.hold {
		return
	}
	r.stopFade()
	r.fade = &Tween{From: r.alpha, To: 0, Duration: r.m.FadeOut, Ease: Linear, Set: func(v float32) {
		r.alpha = v
		if v <= 0 {
			r.fade = nil
			r.grow = 0 // it hides collapsed
			r.passThrough()
		}
		r.invalidate()
	}}
	r.w.Animate(r.fade)
}

func (r *ScrollReveal) stopFade() {
	if r.fade != nil && r.w != nil {
		r.w.stopAnimation(r.fade)
	}
	r.fade = nil
}

func (r *ScrollReveal) growTo(v float32) {
	if r.grow == v || r.w == nil {
		return
	}
	if r.growTw != nil {
		r.w.stopAnimation(r.growTw)
	}
	r.growTw = &Tween{From: r.grow, To: v, Duration: r.m.Grow, Ease: Linear, Set: func(g float32) {
		r.grow = g
		r.invalidate()
	}}
	r.w.Animate(r.growTw)
}

func (r *ScrollReveal) passThrough() {
	if r.El != nil {
		r.El.PassThrough = r.alpha <= 0 && !r.hold
	}
}

func (r *ScrollReveal) invalidate() {
	if r.El != nil {
		r.El.Invalidate()
	}
}

// ---- Scrollbar's macOS side ----------------------------------------------------------------

// follow takes the environment's style: the bar's metrics become the style's.
func (b *Scrollbar) follow(env Environment) {
	if env.ScrollBars == b.style {
		return
	}
	b.style = env.ScrollBars
	b.Metrics = ScrollMetricsFor(env.ScrollBars)
	b.stopSmooth()
	b.reveal.Reset()
	b.reveal.El, b.reveal.m = &b.Element, b.Metrics
	b.PassThrough = b.Metrics.Overlay // an overlay bar is hidden until the content scrolls
	b.Invalidate()
}

// Attached is Attacher's: the bar takes its window's scroll bar style.
func (b *Scrollbar) Attached() {
	if b.win != nil {
		b.follow(b.win.env)
	}
}

// HoverChanged is HoverHandler's: an overlay bar stays and grows while the pointer is
// over it.
func (b *Scrollbar) HoverChanged(on bool) {
	if !b.Metrics.Overlay {
		return
	}
	b.reveal.SetHover(b.win, on)
}

// Revealed is an overlay bar's opacity now (1 for the other bars).
func (b *Scrollbar) Revealed() float32 {
	if !b.Metrics.Overlay {
		return 1
	}
	return b.reveal.Alpha()
}

// Expansion is how far an overlay bar's thumb has grown, 0 to 1.
func (b *Scrollbar) Expansion() float32 { return b.reveal.Expansion() }

func (b *Scrollbar) scale() Scale {
	if b.win != nil {
		return b.win.scale()
	}
	return Scale{PixelsPerDIP: 1, Text: 1}
}

// macGeometry is geometry for a macOS bar: the track inset at both ends, no arrows.
func (b *Scrollbar) macGeometry() (trackTop, trackLen, thumbTop, thumbLen DIP, ok bool) {
	off, _, _ := b.target.ScrollRange()
	return b.spanAt(off)
}

// macThumbThickness is the thumb's thickness now (an overlay thumb grows on hover).
func (b *Scrollbar) macThumbThickness() DIP {
	m := b.Metrics
	if !m.Overlay || m.HoverThumb <= m.Thumb {
		return m.Thumb
	}
	return m.Thumb + (m.HoverThumb-m.Thumb)*b.reveal.Expansion()
}

// macThumbRect is the thumb Inset from the bar's outer edge (the right, the left in a
// right-to-left window, where the bar is at the left).
func (b *Scrollbar) macThumbRect() (Rect, bool) {
	_, _, top, n, ok := b.macGeometry()
	if !ok {
		return Rect{}, false
	}
	t := b.macThumbThickness()
	x := b.bounds.Width() - b.Metrics.Inset - t
	if b.win != nil && b.win.rtl() {
		x = b.Metrics.Inset
	}
	return R(x, top, x+t, top+n), true
}

// paintMac draws a macOS bar: the thumb only, in ScrollThumb (the knob colour; the track is
// transparent), faded with an overlay bar.
func (b *Scrollbar) paintMac(p *PaintContext) {
	off, _, _ := b.target.ScrollRange()
	if b.Metrics.Overlay {
		b.reveal.El = &b.Element
		b.reveal.Offset(b.win, b.Metrics, off)
	}
	thumb, ok := b.macThumbRect()
	if !ok {
		return
	}
	c := p.Palette.ScrollThumb
	if b.Metrics.Overlay {
		c.A *= b.reveal.Alpha()
	}
	if c.A <= 0 {
		return
	}
	p.Canvas.FillRoundedRect(RoundRect(thumb.Add(p.Origin), thumb.Width()/2), c)
}

// macPointer is HandlePointer for a macOS bar: the thumb drags; a press on the track pages
// toward the pointer (animated) and repeats while held, or, with "Jump to the spot that's
// clicked" (Environment.ScrollClickJumps; Option inverts it, as WebKit's
// ScrollbarThemeMac::shouldCenterOnThumb does), centres the thumb on the pointer and drags it.
func (b *Scrollbar) macPointer(in *PointerInput) bool {
	_, trackLen, top, n, ok := b.macGeometry()
	if !ok {
		return in.Action != PointerMove
	}
	thumb, _ := b.macThumbRect()
	// The whole width of the bar is the thumb's along its length, as AppKit hit-tests it.
	onThumb := in.Local.Y >= top && in.Local.Y < top+n
	switch in.Action {
	case PointerMove, PointerEnter:
		if b.dragging {
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
		b.stopSmooth()
		jump := w.env.ScrollClickJumps != (in.Mods&ModAlt != 0)
		switch {
		case onThumb:
			off, _, _ := b.target.ScrollRange()
			b.dragging, b.dragFrom, b.dragOff = true, in.Pos, off
		case jump:
			_, content, view := b.target.ScrollRange()
			if free := trackLen - n; free > 0 {
				b.target.ScrollTo((in.Local.Y - b.Metrics.Inset - n/2) * (content - view) / free)
			}
			off, _, _ := b.target.ScrollRange()
			b.dragging, b.dragFrom, b.dragOff = true, in.Pos, off
		case in.Local.Y < top:
			b.press(-1, 2)
		default:
			b.press(1, 2)
		}
		if b.dragging {
			b.Invalidate()
		}
		if b.dragging || b.pressDir != 0 {
			w.Capture(&b.Element)
			if b.Metrics.Overlay {
				b.reveal.SetHold(w, true)
			}
		}
		return true
	case PointerUp, PointerCancel:
		if b.dragging {
			b.dragging = false
			b.Invalidate()
		}
		b.stopRepeat()
		if b.Metrics.Overlay {
			b.reveal.SetHold(b.win, false)
		}
		b.setHot(in.Action == PointerUp && thumb.Contains(in.Local))
		return true
	}
	return false
}

// macStep is step for a macOS bar's track: a page toward the pointer, animated from where
// the last page was going, until the thumb reaches the pointer.
func (b *Scrollbar) macStep() bool {
	off, content, view := b.target.ScrollRange()
	goal := off
	if b.smooth != nil {
		goal = b.smoothTo
	}
	_, _, top, n, ok := b.spanAt(goal)
	y := b.WindowToLocal(b.win.PointerPos()).Y
	if !ok || (b.pressDir < 0 && y >= top) || (b.pressDir > 0 && y < top+n) {
		return false
	}
	next := clampDIP(goal+DIP(b.pressDir)*b.target.PageStep(), 0, content-view)
	if next == goal {
		return false
	}
	b.smoothScroll(next)
	return next > 0 && next < content-view
}

// spanAt is the track and the thumb as they would be at offset off.
func (b *Scrollbar) spanAt(off DIP) (trackTop, trackLen, thumbTop, thumbLen DIP, ok bool) {
	_, content, view := b.target.ScrollRange()
	m := b.Metrics
	trackTop, trackLen = m.Inset, max(b.bounds.Height()-2*m.Inset, 0)
	thumbTop, thumbLen, ok = ScrollThumbSpan(m, b.bounds.Height(), off, content, view, b.scale())
	return trackTop, trackLen, thumbTop, thumbLen, ok
}

// macEase is the curve of an animated page (CSS ease; WebKit's smooth scroll of a track
// click measured 180 ms, slow at both ends).
var macEase = CubicBezier(0.25, 0.1, 0.25, 1)

// smoothScroll scrolls to goal over Metrics.Smooth (at once without motion).
func (b *Scrollbar) smoothScroll(goal DIP) {
	b.stopSmooth()
	off, _, _ := b.target.ScrollRange()
	if b.Metrics.Smooth <= 0 || b.win == nil || !b.win.MotionOK() {
		b.target.ScrollTo(goal)
		return
	}
	b.smoothTo = goal
	tw := &Tween{From: float32(off), To: float32(goal), Duration: b.Metrics.Smooth, Ease: macEase}
	tw.Set = func(v float32) {
		b.target.ScrollTo(DIP(v))
		if v == float32(goal) && b.smooth == tw {
			b.smooth = nil
		}
	}
	b.smooth = tw
	b.win.Animate(tw)
}

func (b *Scrollbar) stopSmooth() {
	if b.smooth != nil && b.win != nil {
		b.win.stopAnimation(b.smooth)
	}
	b.smooth = nil
}

// macDetached forgets an overlay bar's state and stops a page in progress.
func (b *Scrollbar) macDetached() {
	b.stopSmooth()
	b.reveal.Reset()
	if b.Metrics.Overlay {
		b.PassThrough = true
	}
}

// [/mac scroll]
