package queues

import (
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// The dialog's own fonts (QueueDialog.svelte's CSS over app.css's body font).
var (
	// fontQueue is .queue: var(--type-body) 500 (600 when chosen).
	fontQueue = uiFont(tokens.TypeBody, 500)
	// fontNote is .qnote: var(--type-meta) 400.
	fontNote = uiFont(tokens.TypeMeta, 400)
	// fontSmall is var(--type-small) 400 (.qcount, .q-hint, .pos, .suffix, the day chips' size).
	fontSmall = uiFont(tokens.TypeSmall, 400)
	// fontCell is var(--type-cell) 400 (.dstate, .empty, .foot-msg).
	fontCell = uiFont(tokens.TypeCell, 400)
	// fontLabel is .field > label: 600 var(--type-cell).
	fontLabel = uiFont(tokens.TypeCell, 600)
	// fontQLabel is .q-label: var(--type-body) 500.
	fontQLabel = uiFont(tokens.TypeBody, 500)
	// fontTitle is .q-title: 650 var(--type-lead) / 1.3 var(--font-display).
	fontTitle = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeLead, Weight: 650, LineHeight: 1.3, AutoOptical: true}
	// fontDay is .chip.day: var(--type-small) 500, line-height 1 (600 when pressed).
	fontDay = kit.Font{Family: tokens.FontUI, Size: tokens.TypeSmall, Weight: 500, LineHeight: 1, Tabular: true, AutoOptical: true}
)

// uiFont is the UI font at a size and weight with body's line height and figures.
func uiFont(size kit.Length, weight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: 1.4, Tabular: true, AutoOptical: true}
}

func withWeight(f kit.Font, w float32) kit.Font {
	f.Weight = w
	return f
}

// box is a plain element with a style.
func box(st kit.Style, children ...kit.Widget) *kit.Element {
	e := &kit.Element{Style: st}
	e.Add(children...)
	return e
}

// cssEase is CSS's ease (.queue's background transition).
var cssEase = kit.CubicBezier(0.25, 0.1, 0.25, 1)

// iconHolder keeps the dialog's icon set: the tiles', marks' and grips' paths, made from the
// window's resources on first use and released when the dialog leaves its window (paths belong
// to whoever made them).
type iconHolder struct {
	res kit.Resources
	set *icons.Set
}

func (h *iconHolder) of(w *kit.Window) *icons.Set {
	if w == nil || w.Host() == nil {
		return nil
	}
	res := w.Host().Resources()
	if h.set == nil || h.res != res {
		h.release()
		h.res, h.set = res, icons.NewSet(res)
	}
	return h.set
}

func (h *iconHolder) release() {
	if h.set != nil {
		h.set.Release()
		h.set, h.res = nil, nil
	}
}

// tile is FileIcon.svelte: a file kind's tile, decorative (the name is beside it).
type tile struct {
	kit.Element
	icons *iconHolder
	kind  icons.TileKind
	size  kit.DIP
}

func newTile(h *iconHolder, size kit.DIP) *tile {
	t := &tile{icons: h, size: size, kind: icons.TileOther}
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	t.Semantics.Hidden = true
	t.PassThrough = true
	return t
}

// tileOf is the tile of a download's kind (categories' kinds; anything else is other).
func tileOf(d *downexv1.Download) icons.TileKind {
	switch categories.KindOf(d) {
	case categories.Video:
		return icons.TileVideo
	case categories.Audio:
		return icons.TileAudio
	case categories.Archives:
		return icons.TileArchives
	case categories.Documents:
		return icons.TileDocuments
	case categories.Programs:
		return icons.TilePrograms
	}
	return icons.TileOther
}

func (t *tile) set(k icons.TileKind) {
	if k != t.kind {
		t.kind = k
		t.Invalidate()
	}
}

// Paint is kit.Painter's: the tile at its box, on device pixels as an <svg> box is.
func (t *tile) Paint(p *kit.PaintContext) {
	s := t.icons.of(t.Window())
	if s == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	s.DrawTile(p.Canvas, t.kind, at, t.size, views.TokensOf(&t.Element))
}

// mark is StatusMark.svelte: a status's mark (its own colour and shape).
type mark struct {
	kit.Element
	icons *iconHolder
	kind  icons.MarkKind
	size  kit.DIP
}

func newMark(h *iconHolder, size kit.DIP) *mark {
	m := &mark{icons: h, size: size, kind: icons.MarkQueued}
	m.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	m.Semantics.Hidden = true
	m.PassThrough = true
	return m
}

// markOf is StatusMark.svelte's markOfState.
func markOf(s downexv1.DownloadState) icons.MarkKind {
	switch s {
	case downexv1.DownloadState_DOWNLOAD_STATE_DOWNLOADING, downexv1.DownloadState_DOWNLOAD_STATE_PROBING:
		return icons.MarkDownloading
	case downexv1.DownloadState_DOWNLOAD_STATE_PAUSED:
		return icons.MarkPaused
	case downexv1.DownloadState_DOWNLOAD_STATE_FAILED:
		return icons.MarkFailed
	case downexv1.DownloadState_DOWNLOAD_STATE_COMPLETED:
		return icons.MarkCompleted
	case downexv1.DownloadState_DOWNLOAD_STATE_AWAITING_CONFIRMATION:
		return icons.MarkAwaiting
	case downexv1.DownloadState_DOWNLOAD_STATE_CANCELED:
		return icons.MarkCanceled
	}
	return icons.MarkQueued
}

func (m *mark) set(k icons.MarkKind) {
	if k != m.kind {
		m.kind = k
		m.Invalidate()
	}
}

// Paint is kit.Painter's.
func (m *mark) Paint(p *kit.PaintContext) {
	s := m.icons.of(m.Window())
	if s == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	s.DrawMark(p.Canvas, m.kind, at, m.size, views.TokensOf(&m.Element))
}

// grip is the order row's .grip: a 12 px column with the 8 × 14 drag handle (six dots: an SVG
// path of zero-length segments stroked 2.2 wide with round caps) in --faint.
type grip struct{ kit.Element }

func newGrip() *grip {
	g := &grip{}
	g.Style = kit.Style{Width: kit.Px(12), Height: kit.Px(14), Flex: kit.FlexNone}
	g.Semantics.Hidden = true
	g.PassThrough = true
	return g
}

// gripDots are the dots' centres on the 8 × 14 grid.
var gripDots = [6]kit.Point{{X: 2, Y: 2}, {X: 6, Y: 2}, {X: 2, Y: 7}, {X: 6, Y: 7}, {X: 2, Y: 12}, {X: 6, Y: 12}}

// Paint is kit.Painter's: the 8 px wide svg is centred in the 12 px column.
func (g *grip) Paint(p *kit.PaintContext) {
	c := views.TokensOf(&g.Element).Faint
	sz := p.Size()
	ox := p.Origin.X + (sz.W-8)/2
	oy := p.Origin.Y + (sz.H-14)/2
	const r = 1.1
	for _, d := range gripDots {
		x, y := ox+d.X+0.005, oy+d.Y
		p.Canvas.FillEllipse(kit.R(x-r, y-r, x+r, y+r), c)
	}
}

// dot is app.css's .dot: a 7 px circle in the text colour of what it is in (a running pill).
type dot struct {
	kit.Element
	ink views.Ink
}

func newDot(ink views.Ink) *dot {
	d := &dot{ink: ink}
	d.Style = kit.Style{Width: kit.Px(7), Height: kit.Px(7), Flex: kit.FlexNone}
	d.Semantics.Hidden = true
	return d
}

// Paint is kit.Painter's.
func (d *dot) Paint(p *kit.PaintContext) {
	p.Canvas.FillEllipse(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), d.ink(views.TokensOf(&d.Element)))
}

// chText is a one-line text at least n ch wide (CSS min-inline-size: Nch; the width of the
// font's "0"), its lines at the end (text-align: end): the queue's count and an order row's
// position.
type chText struct {
	*views.Text
	n     float32
	zero  kit.DIP
	scale kit.Scale
}

func newChText(s string, f kit.Font, ink views.Ink, n float32) *chText {
	t := &chText{Text: views.NewText(s, f, ink, kit.TextOptions{Align: kit.TextAlignEnd}), n: n}
	t.Style.Flex = kit.FlexNone
	return t
}

// ContentWidths is kit.Layouter's: the text's, and at least n zeros.
func (t *chText) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	lo, hi = t.Text.ContentWidths(ctx)
	if t.zero == 0 || t.scale != ctx.Scale {
		t.scale, t.zero = ctx.Scale, 0
		if l, err := ctx.Text.LayoutText("0", t.Font().Style(ctx.Scale), kit.TextOptions{}); err == nil {
			t.zero = l.Metrics().Width
			l.Release()
		}
	}
	w := t.zero * kit.DIP(t.n)
	return max(lo, w), max(hi, w)
}

// fade is a CSS transition of one colour share (0 to 1) over a duration with CSS's ease: the
// queue rows' background (transition: background-color var(--dur-2) ease).
type fade struct {
	e     *kit.Element
	v     float32
	tween *kit.Tween
}

func (f *fade) to(e *kit.Element, target float32, d time.Duration) {
	f.e = e
	w := e.Window()
	if f.tween != nil && w != nil {
		w.StopAnimation(f.tween)
	}
	f.tween = nil
	if w == nil || !w.MotionOK() || d <= 0 || f.v == target {
		f.v = target
		e.Invalidate()
		return
	}
	f.tween = &kit.Tween{From: f.v, To: target, Duration: d, Ease: cssEase, Set: func(v float32) {
		f.v = v
		e.Invalidate()
	}}
	w.Animate(f.tween)
}

func (f *fade) jump(target float32) {
	if f.tween != nil && f.e != nil && f.e.Window() != nil {
		f.e.Window().StopAnimation(f.tween)
	}
	f.tween = nil
	f.v = target
}
