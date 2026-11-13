package downloads

import (
	"reflect"
	"sync"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The fonts of the list and the pane (DownloadRow.svelte, DownloadTable.svelte,
// DetailsPanel.svelte, ConnectionBoxes.svelte, SpeedChart.svelte); body's tabular figures and
// optical sizing throughout.
func uiFont(size kit.Length, weight, lineHeight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: lineHeight, Tabular: true, AutoOptical: true}
}

var (
	// fontName is .name: 500 var(--type-body) / 1.15 (400 in Compact).
	fontName        = uiFont(tokens.TypeBody, 500, 1.15)
	fontNameCompact = uiFont(tokens.TypeBody, 400, 1.15)
	// fontSub is .sub: var(--type-meta) / 1.15.
	fontSub = uiFont(tokens.TypeMeta, 400, 1.15)
	// fontNum is .num: var(--type-body) at body's 1.4; fontStrong its .strong (600).
	fontNum    = uiFont(tokens.TypeBody, 400, 1.4)
	fontStrong = uiFont(tokens.TypeBody, 600, 1.4)
	// fontPct is .pct: 600 var(--type-cell) (var(--type-small) in Compact).
	fontPct        = uiFont(tokens.TypeCell, 600, 1.4)
	fontPctCompact = uiFont(tokens.TypeSmall, 600, 1.4)
	// fontStatus is .c-status: var(--type-cell), 600 for the states that set it.
	fontStatus     = uiFont(tokens.TypeCell, 400, 1.4)
	fontStatusBold = uiFont(tokens.TypeCell, 600, 1.4)
	// fontInline is .inline-act: 600 var(--type-cell) / 1.2.
	fontInline = uiFont(tokens.TypeCell, 600, 1.2)
	// fontReview is .review: 600 var(--type-small) / 1.
	fontReview = uiFont(tokens.TypeSmall, 600, 1)
	// fontHead is .th button: 500 var(--type-meta) / 1 (600 when sorted).
	fontHead       = uiFont(tokens.TypeMeta, 500, 1)
	fontHeadSorted = uiFont(tokens.TypeMeta, 600, 1)
)

// iconSets are the icon sets of the kit's resources (one per backend, so one per process),
// for the file tiles and status marks the widgets do not draw. The set is released with its
// window's resources by widgets.ReleaseIcons' owner, so this one is released by ReleaseIcons.
var iconSets struct {
	sync.Mutex
	m map[kit.Resources]*icons.Set
}

// iconsOf is the icon set that draws for e's window (nil outside a window).
func iconsOf(e *kit.Element) *icons.Set {
	w := e.Window()
	if w == nil || w.Host() == nil {
		return nil
	}
	res := w.Host().Resources()
	if res == nil {
		return nil
	}
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

// ReleaseIcons releases the tile and mark paths this package made with res (call it before res
// closes, beside widgets.ReleaseIcons).
func ReleaseIcons(res kit.Resources) {
	iconSets.Lock()
	defer iconSets.Unlock()
	if s := iconSets.m[res]; s != nil {
		s.Release()
		delete(iconSets.m, res)
	}
}

// tok is the theme e draws with.
func tok(e *kit.Element) *tokens.Theme { return views.TokensOf(e) }

// grayTheme is a theme drawn through CSS filter: grayscale(1) (the stale list's .body): every
// colour's luminance by the filter's matrix, alpha kept.
type grayTheme struct {
	base widgets.Theme
	mu   sync.Mutex
	from *tokens.Theme
	gray *tokens.Theme
}

// Tokens is widgets.Theme's.
func (g *grayTheme) Tokens(env kit.Environment) *tokens.Theme {
	t := g.base.Tokens(env)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.from != t {
		g.from, g.gray = t, grayscale(t)
	}
	return g.gray
}

// grayscale is t with every colour turned grey as filter: grayscale(1) turns it (Filter
// Effects 1 §13.1.1: 0.2126 R + 0.7152 G + 0.0722 B on each channel).
func grayscale(t *tokens.Theme) *tokens.Theme {
	g := *t
	v := reflect.ValueOf(&g).Elem()
	colorType := reflect.TypeOf(kit.Color{})
	shadowsType := reflect.TypeOf([]kit.Shadow(nil))
	for i := range v.NumField() {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Type() {
		case colorType:
			f.Set(reflect.ValueOf(gray(f.Interface().(kit.Color))))
		case shadowsType:
			src := f.Interface().([]kit.Shadow)
			out := make([]kit.Shadow, len(src))
			for j, s := range src {
				s.Color = gray(s.Color)
				out[j] = s
			}
			f.Set(reflect.ValueOf(out))
		}
	}
	return &g
}

func gray(c kit.Color) kit.Color {
	y := 0.2126*c.R + 0.7152*c.G + 0.0722*c.B
	return kit.Color{R: y, G: y, B: y, A: c.A}
}

// tileKind is FileIcon's tile of a kind name ("video"; anything else is "other").
func tileKind(kind string) icons.TileKind {
	for k := icons.TileVideo; k <= icons.TileOther; k++ {
		if k.String() == kind {
			return k
		}
	}
	return icons.TileOther
}

// tile is FileIcon.svelte: a file kind's tile at its size, decorative.
type tile struct {
	kit.Element
	kind icons.TileKind
	size kit.DIP
}

func newTile(size kit.DIP) *tile {
	t := &tile{size: size}
	t.Semantics.Hidden = true
	t.PassThrough = true
	t.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return t
}

func (t *tile) set(kind icons.TileKind, size kit.DIP) {
	if kind == t.kind && size == t.size {
		return
	}
	if size != t.size {
		t.size = size
		t.Style.Width, t.Style.Height = kit.Px(size), kit.Px(size)
		t.Relayout()
	}
	t.kind = kind
	t.Invalidate()
}

// Paint is kit.Painter's: the tile at the box's top-left, on whole device pixels as an <svg>
// box is placed.
func (t *tile) Paint(p *kit.PaintContext) {
	set := iconsOf(&t.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	set.DrawTile(p.Canvas, t.kind, at, t.size, tok(&t.Element))
}

// mark is StatusMark.svelte: a state's mark at its size, decorative; morph draws the check in
// (a download that completed while its row was shown).
type mark struct {
	kit.Element
	kind  icons.MarkKind
	size  kit.DIP
	morph *morphAnim
}

func newMark(size kit.DIP) *mark {
	m := &mark{size: size}
	m.Semantics.Hidden = true
	m.PassThrough = true
	m.Style = kit.Style{Width: kit.Px(size), Height: kit.Px(size), Flex: kit.FlexNone}
	return m
}

func (m *mark) set(kind icons.MarkKind, size kit.DIP) {
	if size != m.size {
		m.size = size
		m.Style.Width, m.Style.Height = kit.Px(size), kit.Px(size)
		m.Relayout()
	}
	if kind != m.kind {
		m.kind = kind
		m.Invalidate()
	}
}

// Paint is kit.Painter's.
func (m *mark) Paint(p *kit.PaintContext) {
	set := iconsOf(&m.Element)
	if set == nil {
		return
	}
	at := kit.Pt(p.Scale.Snap(p.Origin.X), p.Scale.Snap(p.Origin.Y))
	t := tok(&m.Element)
	if a := m.morph; a != nil && m.kind == icons.MarkCompleted {
		a.paint(p, set, at, m.size, t)
		return
	}
	set.DrawMark(p.Canvas, m.kind, at, m.size, t)
}

// stopMorph ends the check's draw-in.
func (m *mark) stopMorph() {
	if m.morph != nil {
		m.morph.dead = true
		m.morph = nil
		m.Invalidate()
	}
}

// startMorph plays the completed mark's morph (StatusMark's .morph): the mark settles from
// 60% over 420 ms and its check draws itself in over 420 ms after 80 ms, both ease-out.
func (m *mark) startMorph() {
	w := m.Window()
	m.stopMorph()
	if w == nil || !w.MotionOK() {
		return
	}
	m.morph = &morphAnim{m: m, start: w.Now()}
	w.Animate(m.morph)
}

// morphAnim is the check's draw-in.
type morphAnim struct {
	m     *mark
	start time.Time
	now   time.Time
	dead  bool
	path  kit.Path
}

const (
	morphSettle = 420 * time.Millisecond
	morphDelay  = 80 * time.Millisecond
	morphDraw   = 420 * time.Millisecond
)

// checkPath is the completed mark's check (StatusMark.svelte), and checkLength its length in
// the mark's 14-unit grid (pathLength="1" makes the dash 1 of it).
const (
	checkPath   = "M2.8 7.4l2.7 2.7L11.2 4.4"
	checkLength = 11.8789
)

func (a *morphAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	a.now = now
	a.m.Invalidate()
	if now.Sub(a.start) >= morphDelay+morphDraw {
		a.Finish()
		return false
	}
	return true
}

func (a *morphAnim) Finish() {
	if a.m.morph == a {
		a.m.morph = nil
		a.m.Invalidate()
	}
	if a.path != nil {
		a.path.Release()
		a.path = nil
	}
	a.dead = true
}

func (a *morphAnim) paint(p *kit.PaintContext, set *icons.Set, at kit.Point, size kit.DIP, t *tokens.Theme) {
	el := a.now.Sub(a.start)
	scale := float32(1)
	if el < morphSettle {
		scale = 0.6 + 0.4*kit.EaseOut(float32(el)/float32(morphSettle))
	}
	drawn := float32(0)
	if el > morphDelay {
		drawn = kit.EaseOut(min(float32(el-morphDelay)/float32(morphDraw), 1))
	}
	// scale about the mark's centre (CSS scale's origin)
	c := kit.Pt(at.X+size/2, at.Y+size/2)
	p.Canvas.PushTransform(kit.Translation(-c.X, -c.Y).Then(kit.Scaling(scale, scale)).Then(kit.Translation(c.X, c.Y)))
	defer p.Canvas.Pop()
	if a.path == nil {
		if w := a.m.Window(); w != nil && w.Host() != nil {
			a.path, _ = w.Host().Resources().NewSVGPath(checkPath, kit.FillNonZero)
		}
	}
	if a.path == nil || drawn <= 0 {
		return
	}
	k := size / 14
	col := t.Faint
	if t.IsForced() {
		col = t.Ink
	}
	p.Canvas.PushTransform(kit.Scaling(k, k).Then(kit.Translation(at.X, at.Y)))
	on := checkLength * drawn
	p.Canvas.StrokePath(a.path, kit.Stroke{Width: 1.7, Cap: kit.CapRound, Join: kit.JoinRound, Dash: kit.Dash{On: max(on, 0.001), Off: checkLength * 2}}, col)
	p.Canvas.Pop()
}
