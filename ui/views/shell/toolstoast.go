package shell

import (
	"math"

	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// The tools toast's texts (App.svelte).
const (
	toolsTitle = "Setting up video downloads"
	toolsBar   = "Downloading yt-dlp and Deno"
	toolsNote  = "Downex downloads yt-dlp and Deno from GitHub once; then it reads video pages."
)

// ToolsToast is the progress toast of the first download of yt-dlp and Deno (App.svelte's
// .tools-toast, SITE-MEDIA-SPEC §5.4): at the bottom start, clear of the toasts, 16 px above
// the status bar and from the sidebar's edge (from the window's under 721 px), at most 360 px
// wide; it rises in. It is a layer the composition stacks with views.Layers (z 60, under a
// dialog's scrim at 30 while one is open).
type ToolsToast struct {
	kit.Element
	card     *toolsCard
	bar      *widgets.ProgressBar
	detail   *views.Text
	sidebarW kit.Length
}

type toolsCard struct {
	kit.Element
	rise   float32
	tween  *kit.Tween
	pushed bool
}

// NewToolsToast makes the toast's layer.
func NewToolsToast() *ToolsToast {
	tt := &ToolsToast{sidebarW: tokens.SidebarW}
	tt.PassThrough = true
	tt.Style = kit.Style{Display: kit.DisplayBlock}
	c := &toolsCard{rise: 1}
	c.Semantics = kit.Semantics{Role: kit.RoleStatus, Name: toolsTitle, Live: kit.LivePolite}
	c.Style = kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: kit.Px(8),
		Padding: kit.Edges{Top: kit.Px(12), Start: kit.Px(14), End: kit.Px(14), Bottom: kit.Px(14)},
		Border:  kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(kit.Px(11)),
	}
	tone := &toolsTone{}
	tone.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Width: kit.Px(22), Height: kit.Px(22), Flex: kit.FlexNone}
	tone.Semantics.Hidden = true
	tone.Add(widgets.NewIcon(icons.Stream, 15, func(t *tokens.Theme) kit.Color { return t.AccentText }))
	title := views.NewText(toolsTitle, uiFont(tokens.TypeBody, 600), views.InkInk, kit.TextOptions{})
	head := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(10)}}
	head.Add(tone, title)
	tt.bar = widgets.NewProgressBar(toolsBar, false)
	tt.detail = views.NewParagraph("", uiFont(tokens.TypeSmall, 400), views.InkMuted)
	tt.detail.Semantics.Hidden = true
	note := views.NewParagraph(toolsNote, uiFont(tokens.TypeSmall, 400), views.InkMuted)
	c.Add(head, tt.bar, tt.detail, note)
	tt.card = c
	tt.Add(c)
	return tt
}

// Update shows the tools' status: the bar at the downloads' fraction, the line under it
// (state.InstallText without the percentage, which the bar has).
func (tt *ToolsToast) Update(s *state.ToolsStatus, fraction float64) {
	tt.bar.Set(fraction, widgets.BarDownloading)
	setText(tt.detail, state.InstallText(s, math.NaN(), false))
}

// Bar is the toast's progress bar; Detail its first line (tests).
func (tt *ToolsToast) Bar() *widgets.ProgressBar { return tt.bar }
func (tt *ToolsToast) Detail() string            { return tt.detail.Text() }
func (tt *ToolsToast) Card() *kit.Element        { return tt.card.Base() }

// ContentWidths is kit.Layouter's (the layer fills the window).
func (tt *ToolsToast) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (tt *ToolsToast) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the card at the bottom start (inset-block-end:
// calc(var(--statusbar-h) + 16px); inset-inline-start: calc(var(--sidebar-w) + 16px), 16 px
// under 721 px; inline-size: min(360px, 100vw - 32px)).
func (tt *ToolsToast) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	vw := box.Width()
	start := kit.DIP(16)
	if vw > 720 {
		sw := ctx.Scale.Rem(14)
		if vw >= 721 && vw <= 999 {
			sw = 56
		}
		start += sw
	}
	w := min(360, vw-32)
	h := ctx.HeightFor(tt.card, w, w)
	bottom := box.Max.Y - (32 + 16) // --statusbar-h + 16px
	x := box.Min.X + start
	if ctx.RTL {
		x = box.Max.X - start - w
	}
	ctx.Place(tt.card, kit.R(x, bottom-h, x+w, bottom), w)
}

// Attached is kit.Attacher's: the rise (opacity 0 and 8 px lower, --dur-4, --ease-out).
func (c *toolsCard) Attached() {
	w := c.Window()
	if w == nil || !w.MotionOK() {
		c.rise = 1
		return
	}
	c.rise = 0
	c.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur4, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
		c.rise = v
		c.Invalidate()
	}}
	w.Animate(c.tween)
}

// Paint is kit.Painter's: the rise, then --raised with a --line border, 11 px corners and
// --shadow-lg.
func (c *toolsCard) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&c.Element)
	c.pushed = c.rise < 1
	if c.pushed {
		p.Canvas.PushOpacity(c.rise)
		p.Canvas.PushTransform(kit.Translation(0, 8*(1-c.rise)))
	}
	r := views.SnapBox(p, 11)
	for i := len(t.ShadowLg) - 1; i >= 0; i-- {
		p.Canvas.DrawShadow(r, t.ShadowLg[i])
	}
	p.Canvas.FillRoundedRect(r, t.Raised)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, t.Line)
}

// PaintOver is kit.OverPainter's.
func (c *toolsCard) PaintOver(p *kit.PaintContext) {
	if c.pushed {
		p.Canvas.Pop()
		p.Canvas.Pop()
		c.pushed = false
	}
}

// toolsTone is .tools-tone: a 22 px --accent-soft circle.
type toolsTone struct{ kit.Element }

// Paint is kit.Painter's.
func (t *toolsTone) Paint(p *kit.PaintContext) {
	p.Canvas.FillRoundedRect(views.SnapBox(p, 11), views.TokensOf(&t.Element).AccentSoft)
}
