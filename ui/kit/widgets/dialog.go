package widgets

import (
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// DialogSize is a dialog's width class (Dialog.svelte's size).
type DialogSize uint8

// The size classes.
const (
	DialogDefault DialogSize = iota // 32rem
	DialogSm                        // 26rem
	DialogMd                        // 38rem
	DialogLg                        // 48rem
	DialogXl                        // 60rem
)

func (s DialogSize) width() kit.Length {
	switch s {
	case DialogSm:
		return kit.Rem(26)
	case DialogMd:
		return kit.Rem(38)
	case DialogLg:
		return kit.Rem(48)
	case DialogXl:
		return kit.Rem(60)
	}
	return kit.Rem(32)
}

// DialogOptions describe a dialog.
type DialogOptions struct {
	Title string
	// AriaLabel names the dialog when it differs from the visible title (Add shows "Add").
	AriaLabel   string
	Description string
	// Alert makes it an alertdialog (Remove).
	Alert bool
	Size  DialogSize
	// Width wins over Size ("30rem").
	Width kit.Length
	// FixedHeight is a least height that switching pages never lowers (Rem(46) is today's
	// fixedHeight: true), at most the room the window has; a longer page makes the dialog
	// taller, up to that room, with its top where it was.
	FixedHeight kit.Length
	// Flush gives the body no padding or gap (Settings' side navigation).
	Flush bool
	// FocusSelf focuses the dialog itself at first, not its first field.
	FocusSelf bool
	// Fallback is where the focus goes on close when the opener can no longer take it.
	Fallback func() kit.Widget
	// ModalTop is where the backdrop starts: a frameless window's title bar stays usable
	// above it (TitleBar's --modal-top, 44 px in the main window).
	ModalTop kit.DIP
	// OnClose runs when Escape or the close button closed the dialog, after its closing
	// animation.
	OnClose func()
	// OnSubmit runs on Enter in a single-line field of the dialog (a form's implicit
	// submission) and when Submit is called.
	OnSubmit func()
	// Fill makes the dialog a window of its own (docs/DESKTOP-SPEC.md §1, the standalone
	// Confirm download window): it fills the window, with no scrim, shadow, border, corners
	// or motion, and without its close button (the window's title bar closes it). Escape
	// still closes it. NaturalHeight is the height its content asks for.
	Fill bool
}

// Dialog is Dialog.svelte (DESIGN-SYSTEM.md §3.9): a modal over the scrim (--scrim, from
// ModalTop down), --raised with a --line border, 14 px corners and --shadow-lg, centred with
// 16 px around it (8 px in a window 640 px high or less), never wider than the window less 32
// px. The header holds an optional icon, the title (650 16 px display), an optional aside and
// the close button; the body scrolls; the footer (--surface-2 under a --line) holds an optional
// summary at the start and the buttons at the end. It opens (240 ms, --ease-out: the scrim
// fades in, the dialog fades in from 98% at 50% 40%) and closes (140 ms, ease-in) with motion,
// and traps the focus.
type Dialog struct {
	kit.Element
	o DialogOptions

	card     *dialogCard
	head     *kit.Element
	titles   *kit.Element
	titleRow *kit.Element
	title    *kit.Label
	desc     *kit.Label
	icon     *kit.Element
	aside    *kit.Element
	close    *IconButton
	pane     *ScrollPane
	// Body is the dialog's content (a block box: 16 px between its children, 16 20 px padding).
	Body *kit.Element
	foot *dialogFoot

	trap    *kit.FocusTrap
	win     *kit.Window
	natural kit.DIP // Fill: the card's height for the window's width (NaturalHeight)
	busy    bool
	leaving bool
	shown   float32 // 0 hidden, 1 shown: the open and close motion
	tween   *kit.Tween

	// naturalW is the width natural was measured at; measureW a width the next layout
	// measures the card at too (MeasureAt), and measured what it measured there.
	naturalW, measureW kit.DIP
	measured           kit.Size
}

// NewDialog makes a dialog; Show puts it in a window.
func NewDialog(o DialogOptions) *Dialog {
	d := &Dialog{o: o, shown: 1}
	d.card = &dialogCard{d: d}
	c := d.card
	c.Focus = kit.FocusClick
	c.Ring = kit.Ring{None: true}
	c.Semantics.Role = kit.RoleDialog
	if o.Alert {
		c.Semantics.Role = kit.RoleAlertDialog
	}
	c.Semantics.Name = o.AriaLabel
	if c.Semantics.Name == "" {
		c.Semantics.Name = o.Title
	}
	c.Semantics.Description = o.Description
	c.Semantics.States |= kit.NodeModal
	c.Style = kit.Style{
		Display: kit.DisplayFlex, Axis: kit.AxisColumn, Overflow: kit.OverflowHidden,
		Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.R2xl), Container: true,
	}

	d.head = box(kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6, Flex: kit.FlexNone, MinHeight: kit.Px(46),
		Padding: kit.Edges{Top: tokens.Sp7, Start: tokens.Sp8, End: tokens.Sp7},
	})
	d.title = kit.NewLabel(o.Title, kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeDialog, Weight: 650, LineHeight: 1.25, LetterSpacing: -0.01, AutoOptical: true},
		kit.TextOptions{Wrap: kit.TextWrapAnywhere})
	d.title.Semantics.Role = kit.RoleHeading
	d.title.Semantics.Level = 2
	d.title.Style.MinWidth = kit.Px(0)
	d.close = NewIconButton("close", 16, "Close", false, d.Close)
	d.close.Tooltip = "Close (Esc)"
	d.close.Style.Margin = kit.Edges{Start: kit.Auto}
	if o.Fill {
		// No close button at the end of the header: it ends where the body's fields do.
		d.close.Style.Hidden = true
		d.head.Style.Padding.End = tokens.Sp8
		c.Style.Border, c.Style.Radius = kit.Edges{}, kit.Corners{}
	}
	d.titleRow = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, ColumnGap: tokens.Sp6, MinWidth: kit.Px(0)})
	d.layoutHead()

	if o.Description != "" {
		d.desc = kit.NewLabel(o.Description, FontBody, kit.TextOptions{Wrap: kit.TextWrapWords})
		d.desc.Semantics.Role = kit.RoleNone
	}
	d.Body = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp7, Padding: kit.EdgesOf(tokens.Sp7, tokens.Sp8)}}
	if o.Flush {
		d.Body.Style = kit.Style{Display: kit.DisplayBlock}
	}
	d.pane = NewScrollPane(d.Body)
	d.pane.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
	d.pane.Style.MinHeight = kit.Px(0)
	d.rebuild()
	d.Add(d.card)
	return d
}

// stacked reports whether the description sits under the title beside the icon.
func (d *Dialog) stacked() bool { return d.icon != nil && d.o.Description != "" }

func (d *Dialog) layoutHead() {
	d.head.RemoveAll()
	d.titleRow.RemoveAll()
	d.head.Style.AlignItems = kit.AlignCenter
	d.close.Style.AlignSelf = kit.AlignAuto
	d.close.Style.Margin = kit.Edges{Start: kit.Auto}
	if d.icon != nil {
		d.head.Add(d.icon)
	}
	if d.stacked() {
		d.head.Style.AlignItems = kit.AlignStart
		d.close.Style.AlignSelf = kit.AlignStart
		d.close.Style.Margin = kit.Edges{Start: kit.Auto, Top: kit.Px(-2)}
		d.titleRow.Add(d.title)
		if d.aside != nil {
			d.titleRow.Add(d.aside)
		}
		if d.titles == nil {
			d.titles = box(kit.Style{Display: kit.DisplayBlock, RowGap: kit.Px(2), MinWidth: kit.Px(0), Flex: kit.FlexOf(1, 1, kit.Length{})})
		}
		d.titles.RemoveAll()
		d.titles.Add(d.titleRow)
		if d.desc != nil {
			d.titles.Add(d.desc)
			d.desc.Style.Padding = kit.Edges{}
		}
		d.head.Add(d.titles)
	} else {
		d.head.Add(d.title)
		if d.aside != nil {
			d.head.Add(d.aside)
		}
	}
	d.head.Add(d.close)
}

func (d *Dialog) rebuild() {
	c := d.card
	c.RemoveAll()
	c.Add(d.head)
	if d.desc != nil && !d.stacked() {
		d.desc.Style.Padding = kit.Edges{Top: tokens.Sp3, Start: tokens.Sp8, End: tokens.Sp8}
		d.desc.Style.Flex = kit.FlexNone
		c.Add(d.desc)
	}
	c.Add(d.pane)
	if d.foot != nil {
		c.Add(d.foot)
	}
}

// Card is the dialog itself (role dialog or alertdialog), the node its focus trap holds.
func (d *Dialog) Card() *kit.Element { return &d.card.Element }

// CloseButton is the header's close button.
func (d *Dialog) CloseButton() *IconButton { return d.close }

// SetHeaderHidden hides the header (its title and close button) for a dialog that shows its
// title and a close button of its own (Settings: its title above its navigation). The dialog
// keeps its name, and Escape still closes it.
func (d *Dialog) SetHeaderHidden(hidden bool) {
	if d.head.Style.Hidden == hidden {
		return
	}
	d.head.Style.Hidden = hidden
	d.head.Relayout()
	d.card.Relayout()
	d.card.Invalidate()
}

// HeaderHidden reports whether SetHeaderHidden hid the header.
func (d *Dialog) HeaderHidden() bool { return d.head.Style.Hidden }

// SetIcon puts a file tile or a glyph before the title.
func (d *Dialog) SetIcon(w kit.Widget) {
	d.icon = box(kit.Style{Display: kit.DisplayFlex, Flex: kit.FlexNone}, w)
	d.layoutHead()
	d.rebuild()
}

// SetAside puts a control that belongs to the header after the title (the Add dialog's mode).
func (d *Dialog) SetAside(w kit.Widget) {
	d.aside = box(kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexOf(0, 1, kit.Length{}), MinWidth: kit.Px(0)}, w)
	d.layoutHead()
	d.rebuild()
}

// SetFooter gives the dialog its footer: start (a summary, 12.5 px --muted; nil for none) and
// the buttons, the primary last. Buttons of the default size there are 32 px high.
func (d *Dialog) SetFooter(start kit.Widget, buttons ...kit.Widget) {
	d.foot = &dialogFoot{}
	d.foot.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyEnd, ColumnGap: tokens.Sp4, Flex: kit.FlexNone,
		Padding: kit.Edges{Top: tokens.Sp6, Bottom: tokens.Sp6, Start: tokens.Sp8, End: tokens.Sp7}, Border: kit.Edges{Top: kit.Px(1)},
	}
	if d.o.Fill {
		d.foot.Style.Padding.End = tokens.Sp8 // the last button ends where the body's fields do
	}
	if start != nil {
		s := box(kit.Style{MinWidth: kit.Px(0), Margin: kit.Edges{End: kit.Auto}, Flex: kit.FlexOf(0, 1, kit.Length{})}, start)
		d.foot.Add(s)
	}
	for _, b := range buttons {
		if btn, ok := b.(*Button); ok && btn.Size == SizeMd && btn.Variant != Link {
			btn.Size = SizeFoot
			btn.applyStyle()
		}
		d.foot.Add(b)
	}
	d.rebuild()
}

// SetBusy blocks Escape and the close button while an operation runs (aria-busy).
func (d *Dialog) SetBusy(b bool) {
	if b == d.busy {
		return
	}
	d.busy = b
	d.close.SetDisabled(b, false)
	if b {
		d.card.Semantics.States |= kit.NodeBusy
	} else {
		d.card.Semantics.States &^= kit.NodeBusy
	}
	d.card.SemanticsChanged()
}

// Busy reports whether the dialog is busy.
func (d *Dialog) Busy() bool { return d.busy }

// Show puts the dialog over w (a layer above the others) and traps the focus in it.
func (d *Dialog) Show(w *kit.Window) {
	if d.win != nil {
		return
	}
	d.win = w
	d.leaving = false
	w.PushLayer(d)
	d.trap = w.Trap(d.card, kit.TrapOptions{OnEscape: d.Close, Fallback: d.o.Fallback, FocusSelf: d.o.FocusSelf})
	d.animate(0, 1, tokens.Dur4, tokens.EaseOut.Easing())
}

// Shown reports whether the dialog is in a window.
func (d *Dialog) Shown() bool { return d.win != nil }

// Leaving reports whether the dialog plays its closing animation.
func (d *Dialog) Leaving() bool { return d.leaving }

func (d *Dialog) animate(from, to float32, dur time.Duration, ease kit.Easing) {
	w := d.win
	if d.tween != nil {
		w.StopAnimation(d.tween)
		d.tween = nil
	}
	d.shown = from
	if !w.MotionOK() || d.o.Fill {
		d.shown = to
		d.Invalidate()
		return
	}
	t := &kit.Tween{From: from, To: to, Duration: dur, Ease: ease, Set: func(v float32) {
		d.shown = v
		d.Invalidate()
	}}
	d.tween = t
	w.Animate(t)
}

// closeEase is the closing curve, cubic-bezier(0.4, 0, 1, 1).
var closeEase = kit.CubicBezier(0.4, 0, 1, 1)

// Close plays the closing animation (140 ms), then takes the dialog away and calls OnClose
// (Escape, the close button). It does nothing while busy.
func (d *Dialog) Close() {
	if d.busy || d.leaving || d.win == nil {
		return
	}
	w := d.win
	if !w.MotionOK() || d.o.Fill {
		d.Remove()
		d.closed()
		return
	}
	d.leaving = true
	d.PassThrough = true // .backdrop.leaving { pointer-events: none }
	d.animate(1, 0, tokens.DurExit, closeEase)
	_ = w.After(tokens.DurExit, func() {
		if d.leaving {
			d.Remove()
			d.closed()
		}
	})
}

func (d *Dialog) closed() {
	if d.o.OnClose != nil {
		d.o.OnClose()
	}
}

// Remove takes the dialog away at once (the owner closes it after a save), releasing its
// focus trap: the focus goes back where it was.
func (d *Dialog) Remove() {
	if d.win == nil {
		return
	}
	w := d.win
	if d.tween != nil {
		w.StopAnimation(d.tween)
		d.tween = nil
	}
	d.leaving = false
	d.PassThrough = false
	if d.trap != nil {
		d.trap.Release()
		d.trap = nil
	}
	w.RemoveLayer(d)
	d.win = nil
}

// Submit runs OnSubmit unless the dialog is busy.
func (d *Dialog) Submit() {
	if !d.busy && d.o.OnSubmit != nil {
		d.o.OnSubmit()
	}
}

// implicitSubmitter is a single-line field: Enter in it submits the form.
type implicitSubmitter interface{ implicitSubmit() bool }

// HandleKey is kit.KeyHandler's: Enter in a single-line field submits.
func (d *Dialog) HandleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown || e.Key != kit.KeyEnter || e.Mods != 0 || d.win == nil {
		return false
	}
	f := d.win.Focused()
	if f == nil || !d.card.Contains(f) {
		return false
	}
	if s, ok := f.Widget().(implicitSubmitter); ok && s.implicitSubmit() {
		d.Submit()
		return true
	}
	return false
}

// metrics are the room around a dialog in a window of size s: the backdrop's top and the gap
// (16 px, 8 px in a window 640 px high or less).
func (d *Dialog) metrics(s kit.Size) (top, gap, room kit.DIP) {
	top = d.o.ModalTop
	gap = 16
	if s.H <= 640 {
		gap = 8
	}
	return top, gap, max(s.H-top-2*gap, 0)
}

// ContentWidths is kit.Layouter's: a layer takes the window.
func (d *Dialog) ContentWidths(*kit.LayoutContext) (min, max kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (d *Dialog) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// LayoutContent is kit.Layouter's: the dialog in the backdrop, centred, or (with a fixed
// height) with its top where a dialog of its least height has it.
func (d *Dialog) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	s := box.Size()
	if d.o.Fill {
		d.measured = kit.Size{} // what an earlier layout measured may not hold any more
		if mw := d.measureW; mw > 0 {
			d.measureW = 0
			d.measured = kit.Size{W: mw, H: ctx.HeightFor(d.card, mw, mw)}
		}
		d.natural, d.naturalW = ctx.HeightFor(d.card, s.W, s.W), s.W
		ctx.Place(d.card, box, s.W)
		return
	}
	top, gap, room := d.metrics(s)
	wl := d.o.Width
	if !wl.IsSet() {
		wl = d.o.Size.width()
	}
	cw, _ := wl.Resolve(ctx.Scale, s.W)
	cw = min(cw, s.W-32)
	ch := ctx.HeightFor(d.card, cw, s.W)
	least := kit.DIP(0)
	if l, ok := d.o.FixedHeight.Resolve(ctx.Scale, -1); ok {
		least = min(l, room)
	}
	ch = min(max(ch, least), room)
	x := box.Min.X + (s.W-cw)/2
	y := box.Min.Y + top + gap + (room-ch)/2
	if d.o.FixedHeight.IsSet() {
		basis := (room - least) / 2
		y = box.Min.Y + top + gap + min(basis, max(0, room-ch))
	}
	ctx.Place(d.card, kit.R(x, y, x+cw, y+ch), s.W)
}

// Filled reports a dialog that is a window of its own (DialogOptions.Fill).
func (d *Dialog) Filled() bool { return d.o.Fill }

// NaturalHeight is the height a Fill dialog's content asks for at the window's width, as the
// last layout measured it (0 before one): the window fits it.
func (d *Dialog) NaturalHeight() kit.DIP { return d.natural }

// MeasureAt asks the next layout of a Fill dialog to measure the height its content asks for
// at width w too (NaturalHeightAt reads it): a window about to take another width fits its
// height in one resize. It measures once: the layout measures one width per element.
func (d *Dialog) MeasureAt(w kit.DIP) {
	if !d.o.Fill || !(w > 0) || w == d.naturalW || (d.measured.W == w && d.measured.H > 0) {
		return
	}
	d.measureW = w
	d.Relayout()
}

// NaturalHeightAt is the height a Fill dialog's content asks for at width w, as the last
// layout measured it (the window's width, or MeasureAt's); false when none measured w.
func (d *Dialog) NaturalHeightAt(w kit.DIP) (kit.DIP, bool) {
	switch {
	case d.natural > 0 && d.naturalW == w:
		return d.natural, true
	case d.measured.H > 0 && d.measured.W == w:
		return d.measured.H, true
	}
	return 0, false
}

// PreferredWidth is the width the dialog has in a window wider than it (DialogOptions.Width,
// else its Size's), at scale s: a Fill dialog's window opens that wide.
func (d *Dialog) PreferredWidth(s kit.Scale) kit.DIP {
	wl := d.o.Width
	if !wl.IsSet() {
		wl = d.o.Size.width()
	}
	w, _ := wl.Resolve(s, -1)
	return w
}

// backdropBlur is Dialog.svelte's .backdrop { backdrop-filter: blur(1.5px) }.
const backdropBlur kit.DIP = 1.5

// Paint is kit.Painter's: the scrim, from ModalTop down, over the content blurred as
// .backdrop's backdrop-filter blurs it (not with reduced transparency or forced colours;
// the whole backdrop fades in, so the blur is blended at the same opacity).
func (d *Dialog) Paint(p *kit.PaintContext) {
	if d.o.Fill {
		return // the window is the dialog: no scrim over anything
	}
	t := tokensOf(&d.Element)
	sz := p.Size()
	r := kit.R(0, d.o.ModalTop, sz.W, sz.H)
	if f, ok := p.Canvas.(kit.BackdropFilter); ok && !p.Env.ReducedTransparency && !t.IsForced() {
		f.BlurBackdrop(p.Window(r), backdropBlur, d.shown)
	}
	c := t.Scrim
	c.A *= d.shown
	p.FillRect(r, c)
}

// HandlePointer is kit.PointerHandler's: the scrim takes the presses that miss the dialog.
func (d *Dialog) HandlePointer(in *kit.PointerInput) bool {
	if d.o.Fill {
		return false
	}
	return in.Target == &d.Element && in.Local.Y >= d.o.ModalTop
}

// dialogCard is the dialog's box.
type dialogCard struct {
	kit.Element
	d      *Dialog
	pushed bool
}

// Paint is kit.Painter's: the open and close motion (opacity and scale about 50% 40%), the
// shadow, ground and border.
func (c *dialogCard) Paint(p *kit.PaintContext) {
	t := tokensOf(&c.Element)
	s := c.d.shown
	c.pushed = s < 1
	if c.pushed {
		sz := p.Size()
		ox, oy := p.Origin.X+sz.W*0.5, p.Origin.Y+sz.H*0.4
		k := 0.98 + 0.02*s
		p.Canvas.PushOpacity(s)
		p.Canvas.PushTransform(kit.Translation(-ox, -oy).Then(kit.Scaling(k, k)).Then(kit.Translation(ox, oy)))
	}
	if c.d.o.Fill {
		sz := p.Size()
		p.FillRect(kit.R(0, 0, sz.W, sz.H), t.Raised)
		if c.d.desc != nil {
			c.d.desc.SetColor(t.Muted)
		}
		c.d.title.SetColor(t.Ink)
		return
	}
	r := snapBox(p, 14)
	drawShadows(p.Canvas, r, t.ShadowLg)
	p.Canvas.FillRoundedRect(r, t.Raised)
	border := t.Line
	if t.IsForced() {
		border = t.Ink
	}
	strokeInside(p.Canvas, r, p.Scale.SnapWidth(1), border)
	if c.d.desc != nil {
		c.d.desc.SetColor(t.Muted)
	}
	c.d.title.SetColor(t.Ink)
}

// dialogFoot is the footer: --surface-2 under a 1 px --line.
type dialogFoot struct{ kit.Element }

// Paint is kit.Painter's.
func (f *dialogFoot) Paint(p *kit.PaintContext) {
	t := tokensOf(&f.Element)
	sz := p.Size()
	p.FillRect(kit.R(0, 0, sz.W, sz.H), t.Surface2)
	w := p.Scale.SnapWidth(1)
	line := t.Line
	if t.IsForced() {
		line = t.Ink
	}
	p.FillRect(kit.R(0, 0, sz.W, w), line)
}

// PaintOver is kit.OverPainter's.
func (c *dialogCard) PaintOver(p *kit.PaintContext) {
	if c.pushed {
		p.Canvas.Pop()
		p.Canvas.Pop()
		c.pushed = false
	}
}

// HandleKey is kit.KeyHandler's: Enter in a single-line field (the dialog's form).
func (c *dialogCard) HandleKey(e kit.KeyEvent) bool { return c.d.HandleKey(e) }
