package shell

import (
	"sync"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// The fonts the shell's components set (app.css and their own <style>), with Chromium's
// font-optical-sizing: auto.
var (
	// fontBody is body's font: 400 var(--type-body) / 1.4 var(--font-ui), tabular figures.
	fontBody = views.FontBody
	// fontWord is TitleBar's .word: 650 0.9375rem / 1 var(--font-display), -0.015em (the
	// font shorthand resets the body's tabular figures).
	fontWord = kit.Font{Family: tokens.FontDisplay, Size: kit.Rem(0.9375), Weight: 650, LineHeight: 1, LetterSpacing: -0.015, AutoOptical: true}
	// fontKbd is app.css's kbd: 500 0.6875rem/1 var(--font-ui).
	fontKbd = kit.Font{Family: tokens.FontUI, Size: kit.Rem(0.6875), Weight: 500, LineHeight: 1, AutoOptical: true}
)

// uiFont is the UI font at a size and weight with body's line height and tabular figures
// (an element of the page inherits body's font and sets only its size and weight).
func uiFont(size kit.Length, weight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: 1.4, Tabular: true, AutoOptical: true}
}

// iconSets are the icon sets of the kit's resources (one per backend, so one per process),
// for the brand mark, the file tiles and the status marks the shell draws itself (line icons
// are widgets.Icon's).
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

// ReleaseIcons frees the paths the shell made with res; call it before closing res (with
// widgets.ReleaseIcons).
func ReleaseIcons(res kit.Resources) {
	iconSets.Lock()
	s := iconSets.m[res]
	delete(iconSets.m, res)
	iconSets.Unlock()
	if s != nil {
		s.Release()
	}
}

// setOf is the icon set of e's window, nil outside a window.
func setOf(e *kit.Element) *icons.Set {
	w := e.Window()
	if w == nil || w.Host() == nil {
		return nil
	}
	return iconsFor(w.Host().Resources())
}

// iconBox is a decorative element that draws one line icon at its size, in a colour the
// owner gives (its currentColor).
type iconBox struct {
	kit.Element
	name  icons.Name
	size  kit.DIP
	color func(p *kit.PaintContext) kit.Color
}

func newIconBox(name icons.Name, size kit.DIP, color func(p *kit.PaintContext) kit.Color) *iconBox {
	b := &iconBox{name: name, size: size, color: color}
	b.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	b.PassThrough = true
	b.Semantics.Hidden = true
	return b
}

// Paint is kit.Painter's: the icon at the element's top-left, snapped to device pixels as
// an <svg> box is.
func (b *iconBox) Paint(p *kit.PaintContext) {
	set := setOf(&b.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set.DrawIcon(p.Canvas, b.name, at, b.size, b.color(p))
}

// tileBox is FileIcon.svelte: a file kind's tile at its size.
type tileBox struct {
	kit.Element
	kind icons.TileKind
	size kit.DIP
}

func newTile(kind icons.TileKind, size kit.DIP) *tileBox {
	t := &tileBox{kind: kind, size: size}
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	t.PassThrough = true
	t.Semantics.Hidden = true
	return t
}

// Paint is kit.Painter's.
func (t *tileBox) Paint(p *kit.PaintContext) {
	set := setOf(&t.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set.DrawTile(p.Canvas, t.kind, at, t.size, views.TokensOf(&t.Element))
}

// markBox is StatusMark.svelte: a status's mark at its size.
type markBox struct {
	kit.Element
	kind icons.MarkKind
	size kit.DIP
}

func newMark(kind icons.MarkKind, size kit.DIP) *markBox {
	m := &markBox{kind: kind, size: size}
	m.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	m.PassThrough = true
	m.Semantics.Hidden = true
	return m
}

// Paint is kit.Painter's.
func (m *markBox) Paint(p *kit.PaintContext) {
	set := setOf(&m.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set.DrawMark(p.Canvas, m.kind, at, m.size, views.TokensOf(&m.Element))
}

// dot is app.css's .dot: a 7 px circle in a colour its owner gives.
type dot struct {
	kit.Element
	color func(t *tokens.Theme) kit.Color
}

// dotSize is .dot's inline-size and block-size.
const dotSize kit.DIP = 7

func newDot(color func(t *tokens.Theme) kit.Color) *dot {
	d := &dot{color: color}
	d.Style = kit.Style{Width: kit.Px(dotSize), Height: kit.Px(dotSize), Flex: kit.FlexNone}
	d.PassThrough = true
	d.Semantics.Hidden = true
	return d
}

// Paint is kit.Painter's.
func (d *dot) Paint(p *kit.PaintContext) {
	r := views.SnapBox(p, dotSize/2)
	p.Canvas.FillRoundedRect(r, d.color(views.TokensOf(&d.Element)))
}

// dimmer is an opacity that follows a target as a CSS opacity transition does (transition:
// opacity var(--dur-2) ease): the title bar's search and buttons step back to 0.45 while a
// modal dialog is open.
type dimmer struct {
	owner    *TitleBar
	cur      float32
	from, to float32
	anim     *dimAnim
}

type dimAnim struct {
	d     *dimmer
	start time.Time
	dead  bool
}

func (d *dimmer) init(owner *TitleBar) { d.owner, d.cur, d.to = owner, 1, 1 }

// Step is kit.Animation's.
func (a *dimAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	t := float32(now.Sub(a.start)) / float32(tokens.Dur2)
	if t >= 1 {
		a.d.show(a.d.to)
		return false
	}
	e := cssEase(max(t, 0))
	a.d.show(a.d.from + (a.d.to-a.d.from)*e)
	return true
}

// Finish is kit.Animation's.
func (a *dimAnim) Finish() {
	if !a.dead {
		a.d.show(a.d.to)
	}
}

func (d *dimmer) show(v float32) {
	if v == d.cur {
		return
	}
	d.cur = v
	d.owner.dimSearch.Invalidate()
	d.owner.barBtns.Invalidate()
	// The search's native field is a child window, which draws at full opacity over the
	// faded box (a white rectangle and an undimmed placeholder in the light theme): while the
	// search is faded at all, the kit draws its text in its place, in the faded layer, and the
	// native field comes back once the search is fully shown again.
	d.owner.field.SetStandIn(v < 1)
}

func (d *dimmer) moveTo(w *kit.Window, target float32) {
	if target == d.to {
		return
	}
	if d.anim != nil {
		d.anim.dead = true
		d.anim = nil
	}
	d.to = target
	if w == nil {
		d.show(target)
		return
	}
	d.from = d.cur
	d.anim = &dimAnim{d: d}
	w.Animate(d.anim)
}

// dimmed is a part of the title bar that steps back while a modal is open (.titlebar.modal
// .search, .titlebar.modal .bar-btns { opacity: 0.45 }).
type dimmed struct {
	kit.Element
	dim    *dimmer
	pushed bool
}

// Paint is kit.Painter's: the opacity over the part (popped in PaintOver).
func (d *dimmed) Paint(p *kit.PaintContext) {
	d.pushed = d.dim.cur < 1
	if d.pushed {
		p.Canvas.PushOpacity(d.dim.cur)
	}
}

// PaintOver is kit.OverPainter's.
func (d *dimmed) PaintOver(p *kit.PaintContext) {
	if d.pushed {
		p.Canvas.Pop()
		d.pushed = false
	}
}

// modalOpacity is the dimmed parts' opacity while a modal is open.
const modalOpacity = 0.45

// Part is an element of a component and the reference index's selector for it ("tag.class"
// and its index among the matches), for box comparisons with the 0.9.2 references.
type Part struct {
	Sel string
	Nth int
	El  *kit.Element
}
