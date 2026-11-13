package progress

import (
	"math"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/display"
	"downex/ui/core/progress"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// The complete view's texts.
const (
	completeTitle    = "Download complete"
	dontShowText     = "Don’t show this dialog again"
	dontShowHintText = "Downloads will no longer end with this dialog. Settings can turn it back on."
)

// completeView is section.complete (DESKTOP-SPEC §8): the tile with the Mint check, "Download
// complete" over the name, the size; the facts (the address, Saved to, File size, Time taken,
// the format and quality of a stream) with copy buttons; "Don’t show this dialog again"; an
// action error; and Open, Open with…, Open folder and Close. It scrolls only when the window
// is smaller than it (fitted to it otherwise), never sideways.
type completeView struct {
	*widgets.ScrollPane
	body *kit.Element

	tile    *tile
	badge   *doneBadge
	title   *views.Text
	name    *views.Text
	figure  *views.Text
	facts   *facts
	addrI   int
	addr    *address
	copyA   *copyButton
	path    *views.Text
	copyP   *copyButton
	size    *views.Text
	taken   *views.Text
	formatI int
	format  *views.Text
	qualI   int
	quality *views.Text
	live    *kit.Element

	pref      *kit.Element
	dontShow  *widgets.Checkbox
	prefHint  *views.Text
	prefError *views.Text
	note      *note
	open      *widgets.Button
	openWith  *widgets.Button
	folder    *widgets.Button
	close     *widgets.Button
}

// ringRoom is a focus ring's reach outside its box: its 2 px offset and 2 px width.
var ringRoom = kit.Px(4)

func newCompleteView(s *Screen) *completeView {
	v := &completeView{}
	// The pane clips to its viewport: the body keeps ringRoom inside it for the buttons' focus
	// ring (the actions row is flush with the pane's start and end), and the pane gives the same
	// room back outside, so nothing moves and the window keeps its size.
	v.body = box(kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, RowGap: tokens.Sp7, MinWidth: kit.Px(0),
		Padding: kit.EdgesAll(ringRoom)})
	v.ScrollPane = widgets.NewScrollPane(v.body)
	v.Style.Flex = kit.FlexOf(0, 1, kit.Length{})
	v.Style.Margin = kit.EdgesAll(kit.Px(-4))
	v.Semantics.Role = kit.RoleRegion
	v.Semantics.Name = completeTitle

	// header.head.done-head: the tile with its badge, the titles, the figure
	v.tile = newTile(40)
	v.badge = newDoneBadge()
	tileBox := &badgedTile{tile: v.tile, badge: v.badge}
	tileBox.Style = kit.Style{Width: kit.Px(40), Height: kit.Px(40), Flex: kit.FlexNone}
	tileBox.Add(v.tile, v.badge)
	h1 := views.NewText(completeTitle, fontCell600, views.InkAccent, kit.TextOptions{})
	h1.Semantics.Role, h1.Semantics.Level = kit.RoleHeading, 1
	v.title = h1
	v.name = views.NewText("", fontSection, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere, MaxLines: 2, Truncate: kit.TruncateEnd})
	v.name.Style.MinWidth = kit.Px(0)
	titles := box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(3), Flex: kit.FlexOf(1, 1, kit.Length{}), MinWidth: kit.Px(0)}, h1, v.name)
	v.figure = views.NewText("", fontFigure, views.InkInk, kit.TextOptions{})
	v.figure.Style.Flex = kit.FlexNone
	v.figure.Semantics.Hidden = true
	head := box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: kit.Px(14), MinWidth: kit.Px(0),
		MinHeight: kit.Px(52), Flex: kit.FlexNone}, tileBox, titles, v.figure)

	// dl.facts
	v.facts = newFacts(2, 7)
	v.addr = newAddress()
	v.copyA = newCopyButton("Copy address", func() { s.copy(false) })
	v.addrI = v.facts.add("Address", box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp2, MinWidth: kit.Px(0)},
		v.addr, v.copyA), false)
	v.path = views.NewText("", views.FontMono, views.InkInk2, kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	v.path.Style.MinWidth = kit.Px(0)
	v.copyP = newCopyButton("Copy path", func() { s.copy(true) })
	v.facts.add("Saved to", box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp2, MinWidth: kit.Px(0)},
		v.path, v.copyP), false)
	wrapText := func() *views.Text {
		t := views.NewText("", fontCell, views.InkInk, kit.TextOptions{Wrap: kit.TextWrapAnywhere})
		t.Semantics.Role = kit.RoleDefinition
		return t
	}
	v.size, v.taken, v.format, v.quality = wrapText(), wrapText(), wrapText(), wrapText()
	v.facts.add("File size", v.size, false)
	v.facts.add("Time taken", v.taken, false)
	v.formatI = v.facts.add("Format", v.format, false)
	v.qualI = v.facts.add("Quality", v.quality, false)
	v.live = liveRegion()
	v.facts.addLive(v.live)

	// div.pref
	v.dontShow = widgets.NewCheckbox(dontShowText, false, s.onDontShowAgain)
	v.dontShow.Label.SetFont(fontCell)
	v.prefHint = views.NewText(dontShowHintText, fontSmall, views.InkFaint, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.prefError = views.NewText("", fontSmall, views.InkDanger, kit.TextOptions{Wrap: kit.TextWrapWords})
	v.prefError.Semantics.Role, v.prefError.Semantics.Live = kit.RoleAlert, kit.LiveAssertive
	v.pref = box(kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3}, alignStart(v.dontShow), v.prefHint, v.prefError)

	v.note = newNote(views.NoteFail, " ")
	v.note.Semantics.Role, v.note.Semantics.Live = kit.RoleAlert, kit.LiveAssertive

	v.open = widgets.NewButton("Open", "open", widgets.Primary, widgets.SizeLg, s.open)
	v.open.Semantics.Shortcut = "Enter"
	v.openWith = widgets.NewButton("Open with…", "", widgets.Secondary, widgets.SizeLg, s.chooseApp)
	v.folder = widgets.NewButton("Open folder", "folder", widgets.Secondary, widgets.SizeLg, s.openFolder)
	v.close = widgets.NewButton("Close", "", widgets.Ghost, widgets.SizeLg, s.closeWindow)
	v.close.Semantics.Shortcut = "Escape"
	actions := &completeActions{}
	actions.Style = kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp4, RowGap: tokens.Sp3,
		Wrap: true, Padding: kit.Edges{Top: tokens.Sp6}, Border: kit.Edges{Top: kit.Px(1)}, Flex: kit.FlexNone}
	actions.Add(v.open, v.openWith, v.folder, spacer(), v.close)
	v.openWith.SetHidden(!s.pw.o.Caps.OpenWith)

	v.body.Add(head, v.facts, v.pref, v.note, actions)
	return v
}

// alignStart keeps a widget as wide as its content at the start of a block (an inline label).
func alignStart(w kit.Widget) kit.Widget {
	w.Base().Style.AlignSelf = kit.AlignStart
	return w
}

// completeActions is .complete .actions: the buttons over a 1 px --line rule.
type completeActions struct{ kit.Element }

func (a *completeActions) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&a.Element)
	b := p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size()))
	p.Canvas.FillRect(kit.R(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+p.Scale.SnapWidth(1)), t.Line)
}

// set shows the download.
func (v *completeView) set(s *Screen, d *downexv1.Download) {
	v.tile.set(tileOf(categories.KindOf(d)))
	name := categories.DisplayName(d)
	v.name.SetText(name)
	v.name.Tooltip = name
	size := progress.SizeText(d)
	v.figure.SetText(size)
	v.facts.setTerm(v.addrI, progress.AddressLabel(d))
	v.addr.set(d.GetUrl())
	v.path.SetText(display.JoinPath(d.GetDir(), categories.RawName(d)))
	v.size.SetText(size)
	v.taken.SetText(progress.TimeTakenSummary(d))
	f := progress.MediaFormatText(d)
	v.format.SetText(f)
	v.facts.setShown(v.formatI, f != "")
	q := progress.QualityText(d)
	v.quality.SetText(escapeBidi(q))
	v.quality.Tooltip = escapeBidi(progress.QualityDetail(d))
	v.facts.setShown(v.qualI, q != "")
	v.render(s)
}

// render shows the window's state: copied, the choice, the errors, busy.
func (v *completeView) render(s *Screen) {
	v.copyA.setCopied(s.copied == copiedAddress)
	v.copyP.setCopied(s.copied == copiedPath)
	live := ""
	switch s.copied {
	case copiedAddress:
		live = "Address copied"
	case copiedPath:
		live = "Path copied"
	}
	setLive(v.live, live)
	v.dontShow.SetChecked(s.dontShowAgain)
	v.prefHint.SetHidden(!s.dontShowAgain)
	setError(v.prefError, s.prefError)
	v.note.SetHidden(s.actionError == "")
	if s.actionError != "" {
		v.note.Text.SetText(escapeBidi(s.actionError))
	}
	for _, b := range []*widgets.Button{v.open, v.openWith, v.folder} {
		b.SetDisabled(s.busy, false)
	}
}

// badgedTile is span.done-tile: the tile with the badge over its bottom end corner.
type badgedTile struct {
	kit.Element
	tile  *tile
	badge *doneBadge
}

func (b *badgedTile) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 40, 40 }
func (b *badgedTile) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP   { return 40 }

// LayoutContent places the badge at inset-block-end -3 px, inset-inline-end -4 px: a 16 px
// circle with a 2 px --surface border (20 px in all).
func (b *badgedTile) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	ctx.Place(b.tile, box, box.Width())
	x := box.Max.X + 4 - 20
	if ctx.RTL {
		x = box.Min.X - 4
	}
	y := box.Max.Y + 3 - 20
	ctx.Place(b.badge, kit.R(x, y, x+20, y+20), 20)
}

// doneBadge is .badge: an --accent circle in a 2 px --surface ring with the --accent-on check,
// which pops in (220 ms) and draws itself (420 ms after 120 ms); still under reduced motion.
type doneBadge struct {
	kit.Element
	pop, draw float32 // 0 to 1
	check     kit.Path
	res       kit.Resources
	started   bool
}

func newDoneBadge() *doneBadge {
	b := &doneBadge{pop: 1, draw: 1}
	b.Semantics.Hidden = true
	b.PassThrough = true
	return b
}

// checkLength is the check's path length (M3.5 8.4 l2.9 2.9 6-6.6, pathLength 1).
var checkLength = float32(math.Hypot(2.9, 2.9) + math.Hypot(6, 6.6))

// play runs the badge's entrance (pop, then the check draws in).
func (b *doneBadge) play() {
	w := b.Window()
	if w == nil || !w.MotionOK() {
		b.pop, b.draw = 1, 1
		return
	}
	b.pop, b.draw = 0, 0
	ease := kit.CubicBezier(0.2, 0.8, 0.3, 1)
	w.Animate(&kit.Tween{From: 0, To: 1, Duration: 220 * time.Millisecond, Ease: ease, Set: func(v float32) {
		b.pop = v
		b.Invalidate()
	}})
	w.After(120*time.Millisecond, func() {
		w.Animate(&kit.Tween{From: 0, To: 1, Duration: 420 * time.Millisecond, Ease: kit.CubicBezier(0.3, 0.7, 0.4, 1), Set: func(v float32) {
			b.draw = v
			b.Invalidate()
		}})
	})
}

func (b *doneBadge) Detached() {
	if b.check != nil {
		b.check.Release()
		b.check, b.res = nil, nil
	}
}

func (b *doneBadge) Paint(p *kit.PaintContext) {
	w := b.Window()
	if w == nil || w.Host() == nil || b.pop <= 0 {
		return
	}
	t := views.TokensOf(&b.Element)
	r := kit.RectAt(p.Origin, p.Size())
	c := r.Center()
	scale := 0.6 + 0.4*b.pop
	if b.pop < 1 {
		p.Canvas.PushOpacity(b.pop)
		p.Canvas.PushTransform(kit.Translation(-c.X, -c.Y).Then(kit.Scaling(scale, scale)).Then(kit.Translation(c.X, c.Y)))
	}
	ring, fill := t.Surface, t.Accent
	ink := t.AccentOn
	if t.IsForced() {
		ring, fill, ink = t.Chrome, t.Focus, t.Chrome
	}
	p.Canvas.FillEllipse(r, ring)
	p.Canvas.FillEllipse(r.Inset(kit.UniformInsets(2)), fill)
	if res := w.Host().Resources(); b.check == nil || b.res != res {
		b.Detached()
		if path, err := res.NewSVGPath("M3.5 8.4l2.9 2.9 6-6.6", kit.FillNonZero); err == nil {
			b.check, b.res = path, res
		}
	}
	if b.check != nil && b.draw > 0 {
		// the 16-unit check drawn 10 px wide, in the middle of the circle
		k := kit.DIP(10.0 / 16)
		st := kit.Stroke{Width: 2.4, Cap: kit.CapRound, Join: kit.JoinRound}
		if b.draw < 1 {
			st.Dash = kit.Dash{On: kit.DIP(checkLength * b.draw), Off: kit.DIP(checkLength * 2)}
		}
		p.Canvas.PushTransform(kit.Scaling(k, k).Then(kit.Translation(c.X-5, c.Y-5)))
		p.Canvas.StrokePath(b.check, st, ink)
		p.Canvas.Pop()
	}
	if b.pop < 1 {
		p.Canvas.Pop()
		p.Canvas.Pop()
	}
}
