package widgets

import (
	"sync"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
)

// cssEase is CSS's ease, cubic-bezier(0.25, 0.1, 0.25, 1): app.css's colour transitions.
var cssEase = kit.CubicBezier(0.25, 0.1, 0.25, 1)

// transition is a CSS transition of a widget's look between two of its states (rest, hover,
// press...): Paint mixes the colours the theme gives the state it comes from and the one it
// goes to by the eased share of the time gone. The colours are read at paint time, so a theme
// change repaints in the new theme at once. A change while one runs starts from the nearer
// end.
type transition struct {
	e        *kit.Element
	from, to uint8
	share    float32 // of the way from `from` to `to`, eased
	dur      time.Duration
	anim     *transitionAnim
	running  bool
	set      bool
}

type transitionAnim struct {
	t     *transition
	start time.Time
	dead  bool // a newer change replaced it
}

// Step is kit.Animation's.
func (a *transitionAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	t := a.t
	if a.start.IsZero() {
		a.start = now
	}
	s := float32(1)
	if t.dur > 0 {
		s = float32(now.Sub(a.start)) / float32(t.dur)
	}
	if s >= 1 {
		a.Finish()
		return false
	}
	t.share = cssEase(max(s, 0))
	t.e.Invalidate()
	return true
}

// Finish is kit.Animation's.
func (a *transitionAnim) Finish() {
	if a.dead {
		return
	}
	a.dead = true
	t := a.t
	t.running, t.share, t.from = false, 1, t.to
	t.e.Invalidate()
}

// stop drops the running animation where it is.
func (t *transition) stop() {
	if t.anim != nil {
		t.anim.dead = true
		t.anim = nil
	}
	t.running = false
}

// moveTo moves the look to state over dur (at once before the first paint, without motion, or
// for dur 0).
func (t *transition) moveTo(e *kit.Element, state uint8, dur time.Duration) {
	t.e = e
	if t.set && state == t.to {
		return
	}
	w := e.Window()
	if !t.set || w == nil || dur <= 0 || !w.MotionOK() {
		t.jump(e, state)
		return
	}
	if t.running && t.share < 0.5 {
		t.to = t.from // the nearer end
	}
	t.stop()
	t.from, t.to, t.share, t.dur = t.to, state, 0, dur
	t.running = true
	t.anim = &transitionAnim{t: t}
	w.Animate(t.anim)
	e.Invalidate()
}

// jump shows state at once.
func (t *transition) jump(e *kit.Element, state uint8) {
	t.e = e
	t.stop()
	t.set = true
	t.from, t.to, t.share = state, state, 1
	e.Invalidate()
}

// state is the state the look rests in (or goes to).
func (t *transition) state() uint8 { return t.to }

// mix is the colour now, from the colours of the two states.
func (t *transition) mix(from, to kit.Color) kit.Color {
	if !t.running || t.share >= 1 || from == to {
		return to
	}
	return kit.Mix(to, t.share, from)
}

// amountOf is how much of state s the look shows now, from 0 to 1.
func (t *transition) amountOf(s uint8) float32 {
	to, from := t.to == s, t.fromState() == s
	switch {
	case to && from:
		return 1
	case to:
		return t.share
	case from:
		return 1 - t.share
	}
	return 0
}

// fromState is the state the look comes from (its colours are mixed with to's).
func (t *transition) fromState() uint8 {
	if t.running {
		return t.from
	}
	return t.to
}

// iconSets are the icon sets of the kit's resources (one per backend, so one per process).
var iconSets struct {
	sync.Mutex
	m map[kit.Resources]*icons.Set
}

// iconsFor is the icon set that draws with res.
func iconsFor(res kit.Resources) *icons.Set {
	iconSets.Lock()
	defer iconSets.Unlock()
	if iconSets.m == nil {
		iconSets.m = map[kit.Resources]*icons.Set{}
	}
	s := iconSets.m[res]
	if s == nil {
		s = icons.NewSet(res)
		iconSets.m[res] = s
	}
	return s
}

// iconOf is the line icon named as Icon.svelte names it ("queue", "chevron-down"); "" or an
// unknown name is none.
func iconOf(name string) (icons.Name, bool) {
	if name == "" {
		return 0, false
	}
	return icons.ByName(name)
}

// Icon is a decorative line icon (Icon.svelte) at its size, in a colour its owner gives (its
// currentColor). It is hidden from assistive technology.
type Icon struct {
	kit.Element
	Name  icons.Name
	size  kit.DIP
	color func(t *tokens.Theme) kit.Color
	// Opacity is the icon's opacity (a disabled button's icon: 0.7); 0 is 1.
	Opacity float32
}

// NewIcon makes an icon of a size (px) whose colour color gives in the theme.
func NewIcon(name icons.Name, size kit.DIP, color func(t *tokens.Theme) kit.Color) *Icon {
	ic := &Icon{Name: name, size: size, color: color}
	ic.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	ic.PassThrough = true
	ic.Semantics.Hidden = true
	return ic
}

// SetColor changes where the icon's colour comes from.
func (ic *Icon) SetColor(color func(t *tokens.Theme) kit.Color) {
	ic.color = color
	ic.Invalidate()
}

// SetName changes the icon.
func (ic *Icon) SetName(n icons.Name) {
	if n != ic.Name {
		ic.Name = n
		ic.Invalidate()
	}
}

// Paint is kit.Painter's: the icon at the element's top-left, snapped to device pixels as an
// <svg> box is.
func (ic *Icon) Paint(p *kit.PaintContext) {
	w := ic.Window()
	if w == nil || w.Host() == nil || ic.color == nil {
		return
	}
	col := ic.color(tokensOf(&ic.Element))
	if ic.Opacity > 0 && ic.Opacity < 1 {
		col = col.WithAlpha(col.A * ic.Opacity)
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	iconsFor(w.Host().Resources()).DrawIcon(p.Canvas, ic.Name, at, ic.size, col)
}

// box is a plain element with a style, for layout.
func box(st kit.Style, children ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: st}
	e.Add(children...)
	return e
}

// drawShadows draws CSS box-shadows (the first on top) under a rounded box.
func drawShadows(c kit.Canvas, r kit.RRect, sh []kit.Shadow) {
	for i := len(sh) - 1; i >= 0; i-- {
		if s := sh[i]; !s.Inset && s.Color.A > 0 {
			c.DrawShadow(r, s)
		}
	}
}

// drawInsetShadows draws the inset ones of CSS box-shadows (the first on top) inside r, the
// padding box, over the background as CSS paints them: the primary button's --primary-lift
// highlight, inset 0 1px 0 rgb(255 255 255 / 0.18), which Edge (the 0.9.2 references) and
// WebKit both draw.
func drawInsetShadows(c kit.Canvas, r kit.RRect, sh []kit.Shadow) {
	for i := len(sh) - 1; i >= 0; i-- {
		if s := sh[i]; s.Inset && s.Color.A > 0 {
			c.DrawShadow(r, s)
		}
	}
}

// fieldFocusShadows are app.css's focused field: box-shadow 0 0 0 1px var(--focus), 0 0 0
// 4px var(--accent-soft) (the danger pair while invalid). CSS paints the first on top, so the
// 4 px spread goes first.
func drawFieldFocus(c kit.Canvas, r kit.RRect, ring, soft kit.Color) {
	c.DrawShadow(r, kit.Shadow{Spread: 4, Color: soft})
	c.DrawShadow(r, kit.Shadow{Spread: 1, Color: ring})
}

// strokeInside strokes a w wide border inside the rounded box r (device-snapped already).
func strokeInside(c kit.Canvas, r kit.RRect, w kit.DIP, col kit.Color) {
	if w <= 0 || col.A <= 0 {
		return
	}
	c.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, col)
}

// snapBox is the element's border box in window coordinates, snapped to device pixels, with
// radius rad.
func snapBox(p *kit.PaintContext, rad kit.DIP) kit.RRect {
	return kit.RRect{Rect: p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), Radii: kit.UniformRadii(rad)}.Normalized()
}

// ReleaseIcons releases the icon paths the widgets made with res (call it before res closes).
func ReleaseIcons(res kit.Resources) {
	iconSets.Lock()
	defer iconSets.Unlock()
	if s := iconSets.m[res]; s != nil {
		s.Release()
		delete(iconSets.m, res)
	}
}
