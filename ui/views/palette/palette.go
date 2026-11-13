// Package palette is the command palette (Ctrl+K, DESIGN-SYSTEM.md §3.16):
// CommandPalette.svelte on ui/kit. Every action of the window and the downloads, found by
// typing; commands say what they do in plain words, running downloads show their progress.
// Enter runs a command or opens a download as its row's Enter does; Ctrl+Enter shows a
// download in its folder; Escape closes. The scoring, the rows for a query, the highlighting,
// the counts and the texts are ui/core/palette's (ported with their tables); the commands are
// views.Commands' and the downloads palette.Downloads of the store.
//
// The palette is a views.Modal of kind ModalPalette: the composition shows it with
// app.Dialogs.Show(palette.New(app, palette.Options{})) from App.Open.Palette. Its methods
// belong to the UI thread. Today's palette keeps no recent commands, so neither does this one.
package palette

import (
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	core "downex/ui/core/palette"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// Options are where the palette gets its rows and what its downloads' keys do; every field
// has App.svelte's default.
type Options struct {
	// Commands lists the commands (nil: app.Commands.List).
	Commands func() []core.Command
	// Run runs a command by id after the palette has gone (nil: app.Commands.Run).
	Run func(id string)
	// Downloads lists the downloads the palette can find (nil: palette.Downloads over the
	// store, App.svelte's paletteDownloads).
	Downloads func() []core.Download
	// OpenDownload is Enter on a download (nil: app.Act.OpenDownload, openFromPalette).
	OpenDownload func(id int64)
	// ShowFolder is Ctrl+Enter on a download (nil: app.Act.OpenFolder, folderFromPalette);
	// without one the hint is not shown.
	ShowFolder func(id int64)
}

// Palette is the command palette's layer: the dim backdrop over the whole window and the
// palette 7 px from its top.
type Palette struct {
	kit.Element

	app  *views.App
	opts Options

	card    *card
	field   *fieldRow
	query   *queryField
	list    *widgets.ScrollPane
	content *kit.Element
	none    *views.Text
	foot    *foot

	commands  []core.Command
	downloads []core.Download
	entries   []core.Entry
	rows      []*row
	active    int
	text      string // the query

	win     *kit.Window
	done    func()
	trap    *kit.FocusTrap
	unwatch []func()
	tiles   *icons.Set
	dim     float32 // the backdrop's fade, 0 to 1
	rise    float32 // the palette's opening, 0 to 1
	tweens  [2]*kit.Tween
	gone    bool
}

var _ views.Modal = (*Palette)(nil)

// New makes the palette; app.Dialogs.Show shows it.
func New(app *views.App, o Options) *Palette {
	p := &Palette{app: app, opts: o, dim: 1, rise: 1}
	if p.opts.Commands == nil && app.Commands != nil {
		p.opts.Commands = app.Commands.List
	}
	if p.opts.Run == nil && app.Commands != nil {
		p.opts.Run = func(id string) { app.Commands.Run(id) }
	}
	if p.opts.Downloads == nil {
		p.opts.Downloads = p.storeDownloads
	}
	if p.opts.OpenDownload == nil {
		p.opts.OpenDownload = func(id int64) {
			if app.Act.OpenDownload != nil {
				app.Act.OpenDownload(id)
			}
		}
	}
	if p.opts.ShowFolder == nil && app.Act.OpenFolder != nil {
		p.opts.ShowFolder = func(id int64) { app.Act.OpenFolder(id) }
	}
	p.build()
	p.refresh(true)
	return p
}

// storeDownloads is App.svelte's paletteDownloads.
func (p *Palette) storeDownloads() []core.Download {
	a := p.app
	if a.Main == nil {
		return nil
	}
	s := a.Main.Downloads
	return core.Downloads(s.All(), func(d *downexv1.Download) string { return s.NameOf(d.GetId()) }, display.MediaBadge)
}

// build makes the palette's widgets.
func (p *Palette) build() {
	p.Semantics.Role = kit.RoleNone
	p.card = &card{p: p}
	c := p.card
	c.Semantics.Role = kit.RoleDialog
	c.Semantics.Name = core.DialogLabel
	c.Semantics.States |= kit.NodeModal
	c.Style = kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Overflow: kit.OverflowHidden, Border: kit.EdgesAll(kit.Px(1)), Radius: kit.CornersAll(tokens.RXl)}

	p.query = newQueryField(p)
	p.field = newFieldRow(p, p.query)

	p.content = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Padding: kit.Edges{Top: kit.Px(2), End: kit.Px(6), Bottom: kit.Px(6), Start: kit.Px(6)}}}
	p.content.Semantics.Role = kit.RoleListBox
	p.content.Semantics.Name = core.ListLabel
	p.list = widgets.NewScrollPane(p.content)
	p.list.Style.Flex = kit.FlexOf(1, 1, kit.Length{})
	p.list.Style.MinHeight = kit.Px(0)
	p.query.Semantics.Controls = p.content

	p.none = views.NewText("", views.FontBody, views.InkMuted, kit.TextOptions{Wrap: kit.TextWrapWords, Align: kit.TextAlignCenter})
	p.none.Semantics.Hidden = true // aria-hidden: the status says it
	p.none.Style = kit.Style{Flex: kit.FlexNone, Padding: kit.Edges{Top: kit.Px(18), End: kit.Px(16), Bottom: kit.Px(20), Start: kit.Px(16)}, Hidden: true}

	p.foot = newFoot(p)
	c.Add(p.field, p.list, p.none, p.foot)
	p.Add(c)
}

// ModalKind is views.Modal's.
func (p *Palette) ModalKind() views.ModalKind { return views.ModalPalette }

// ShowModal is views.Modal's: the palette over w, its field focused, the focus trapped.
func (p *Palette) ShowModal(w *kit.Window, done func()) kit.Widget {
	if p.win != nil {
		return p
	}
	p.win, p.done, p.gone = w, done, false
	w.PushLayer(p)
	fb := p.app.Fallback
	p.trap = w.Trap(p.card, kit.TrapOptions{OnEscape: p.Close, Fallback: fb})
	p.watch()
	p.animate()
	return p
}

// DismissModal is views.Modal's: the palette goes at once.
func (p *Palette) DismissModal() { p.Close() }

// Shown reports whether the palette is in a window.
func (p *Palette) Shown() bool { return p.win != nil }

// Close takes the palette away (Escape, a press on the backdrop, a row run): it has no
// closing motion. The focus goes back where it was.
func (p *Palette) Close() {
	w := p.win
	if w == nil {
		return
	}
	for _, t := range p.tweens {
		if t != nil {
			w.StopAnimation(t)
		}
	}
	p.tweens = [2]*kit.Tween{}
	for _, f := range p.unwatch {
		f()
	}
	p.unwatch = nil
	if p.trap != nil {
		p.trap.Release()
		p.trap = nil
	}
	w.RemoveLayer(p)
	p.win = nil
	if !p.gone {
		p.gone = true
		if p.done != nil {
			p.done()
		}
	}
}

// Detached is kit.Detacher's: the file tiles' paths go.
func (p *Palette) Detached() {
	if p.tiles != nil {
		p.tiles.Release()
		p.tiles = nil
	}
}

// tileSet is the set that draws the file tiles in this window.
func (p *Palette) tileSet() *icons.Set {
	if p.tiles == nil {
		if w := p.Window(); w != nil && w.Host() != nil {
			p.tiles = icons.NewSet(w.Host().Resources())
		}
	}
	return p.tiles
}

// watch follows the stores while the palette is open: the commands' states and the
// downloads' progress (Svelte's derived props).
func (p *Palette) watch() {
	a := p.app
	if a.Main == nil {
		return
	}
	p.unwatch = append(p.unwatch,
		a.Watch(&a.Main.Downloads.Changes, func() { p.refresh(false) }),
		a.Watch(&a.Main.Queues.Changes, func() { p.refresh(false) }))
}

// The opening motion: the backdrop fades in (--dur-2, ease-out) and the palette rises 4 px
// as it fades in (--dur-3, --ease-out).
var cssEaseOut = kit.CubicBezier(0, 0, 0.58, 1)

func (p *Palette) animate() {
	w := p.win
	if !w.MotionOK() {
		p.dim, p.rise = 1, 1
		return
	}
	p.dim, p.rise = 0, 0
	p.tweens[0] = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur2, Ease: cssEaseOut, Set: func(v float32) { p.dim = v; p.Invalidate() }}
	p.tweens[1] = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) { p.rise = v; p.Invalidate() }}
	w.Animate(p.tweens[0])
	w.Animate(p.tweens[1])
}

// Query is the text typed.
func (p *Palette) Query() string { return p.text }

// SetQuery types q (as the field's input event does): the rows for it, the first current.
func (p *Palette) SetQuery(q string) {
	p.query.SetText(q)
	p.queryChanged(q)
}

func (p *Palette) queryChanged(q string) {
	if q == p.text {
		return
	}
	p.text = q
	p.active = 0 // $effect(() => { void query; active = 0; })
	p.refresh(true)
	p.list.ScrollTo(0)
}

// Entries are the rows shown.
func (p *Palette) Entries() []core.Entry { return p.entries }

// Current is the current row's entry (nil when there are none).
func (p *Palette) Current() *core.Entry {
	if len(p.entries) == 0 {
		return nil
	}
	return &p.entries[min(p.active, len(p.entries)-1)]
}

// CurrentIndex is the current row.
func (p *Palette) CurrentIndex() int {
	if len(p.entries) == 0 {
		return -1
	}
	return min(p.active, len(p.entries)-1)
}

// Rows are the option rows, in the entries' order.
func (p *Palette) Rows() []*row { return p.rows }

// Summary is the live count ("2 commands").
func (p *Palette) Summary() string { return p.foot.summary.Text() }

// Field is the search field.
func (p *Palette) Field() *kit.TextField { return &p.query.TextField }

// Card is the palette's box (role dialog).
func (p *Palette) Card() *kit.Element { return &p.card.Element }

// refresh reads the commands and downloads again and shows their rows for the query;
// rebuild makes the rows anew even when the same ones are listed.
func (p *Palette) refresh(rebuild bool) {
	if p.opts.Commands != nil {
		p.commands = p.opts.Commands()
	}
	if p.opts.Downloads != nil {
		p.downloads = p.opts.Downloads()
	}
	next := core.Entries(p.commands, p.downloads, p.text)
	same := !rebuild && len(next) == len(p.entries)
	for i := 0; same && i < len(next); i++ {
		same = next[i].Key == p.entries[i].Key && next[i].Group == p.entries[i].Group
	}
	p.entries = next
	if same {
		for i, r := range p.rows {
			r.update(&p.entries[i])
		}
	} else {
		p.rebuild()
	}
	p.syncCurrent()
	p.foot.update()
	empty := len(p.entries) == 0
	p.none.SetText(core.NothingMatches(p.text))
	if p.none.Style.Hidden != !empty {
		p.none.SetHidden(!empty)
	}
}

// rebuild makes the rows and their group heads: the entries in runs of one group.
func (p *Palette) rebuild() {
	p.content.RemoveAll()
	p.rows = p.rows[:0]
	var group *kit.Element
	last := ""
	for i := range p.entries {
		e := &p.entries[i]
		if group == nil || e.Group != last {
			last = e.Group
			group = &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
			group.Semantics.Role = kit.RoleGroup
			group.Semantics.Name = e.Group
			head := views.NewCaps(e.Group)
			head.Semantics.Hidden = true // the group is named by it (aria-labelledby)
			head.Style.Padding = kit.Edges{Top: kit.Px(10), Bottom: kit.Px(5), Start: kit.Px(9), End: kit.Px(9)}
			group.Add(head)
			p.content.Add(group)
		}
		r := newRow(p, i)
		r.update(e)
		p.rows = append(p.rows, r)
		group.Add(r)
	}
}

// syncCurrent shows which row is current.
func (p *Palette) syncCurrent() {
	cur := p.CurrentIndex()
	for i, r := range p.rows {
		r.setCurrent(i == cur)
	}
	if cur >= 0 {
		p.query.Semantics.Value = ""
	}
}

// setActive makes row i current (a pointer over it).
func (p *Palette) setActive(i int) {
	if i == p.active || i < 0 || i >= len(p.entries) {
		return
	}
	p.active = i
	p.syncCurrent()
}

// Step moves the current row by delta, wrapping, and shows it.
func (p *Palette) Step(delta int) {
	n := len(p.entries)
	if n == 0 {
		return
	}
	p.active = ((min(p.active, n-1)+delta)%n + n) % n
	p.syncCurrent()
	p.reveal()
}

// reveal scrolls the current row into view (block: nearest) after the layout.
func (p *Palette) reveal() {
	w := p.win
	if w == nil {
		return
	}
	w.AfterLayout(func() {
		if i := p.CurrentIndex(); i >= 0 && i < len(p.rows) {
			p.list.Show(&p.rows[i].Element)
		}
	})
}

// RunCurrent runs the current row (folder: Ctrl+Enter, the download's folder).
func (p *Palette) RunCurrent(folder bool) { p.run(p.Current(), folder) }

// run runs a command or opens a download (folder: shows it in its folder instead). The
// palette goes first; the action runs after it, when the focus is back where it was.
func (p *Palette) run(e *core.Entry, folder bool) {
	if e == nil {
		return
	}
	if e.Type == core.EntryCommand {
		c := e.Command
		if c.Disabled || p.opts.Run == nil {
			return
		}
		id, run := c.ID, p.opts.Run
		p.closeThen(func() { run(id) })
		return
	}
	act := p.opts.OpenDownload
	if folder {
		act = p.opts.ShowFolder
	}
	if act == nil {
		return
	}
	id := e.Item.ID
	p.closeThen(func() { act(id) })
}

// closeThen closes the palette, then runs f once the focus has gone back (tick()).
func (p *Palette) closeThen(f func()) {
	w := p.win
	p.Close()
	if w != nil {
		_ = w.Post(f)
	}
}

// hasDownloads reports download rows among the results (the hints say "open").
func (p *Palette) hasDownloads() bool {
	for i := range p.entries {
		if p.entries[i].Type == core.EntryDownload {
			return true
		}
	}
	return false
}

// HandleKey is the field's keys (onkeydown): the arrows, Page Up and Down, Enter and
// Ctrl+Enter.
func (p *Palette) handleKey(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown {
		return false
	}
	switch e.Key {
	case kit.KeyArrowDown:
		p.Step(1)
	case kit.KeyArrowUp:
		p.Step(-1)
	case kit.KeyPageDown:
		p.Step(5)
	case kit.KeyPageUp:
		p.Step(-5)
	case kit.KeyEnter:
		p.RunCurrent(e.Mods&(kit.ModCtrl|kit.ModMeta) != 0)
	default:
		return false
	}
	return true
}

// LayoutContent is kit.Layouter's: the palette 37.5rem wide (at most the window less the
// backdrop's 12 px sides), at most min(32rem, the window less 24 px) high, 7 px from the top.
func (p *Palette) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	s := box.Size()
	cw := min(ctx.Scale.Rem(37.5), max(0, s.W-2*12))
	ch := ctx.HeightFor(p.card, cw, s.W)
	ch = min(ch, min(ctx.Scale.Rem(32), max(0, s.H-24)))
	x := box.Min.X + (s.W-cw)/2
	y := box.Min.Y + 7
	ctx.Place(p.card, kit.R(x, y, x+cw, y+ch), s.W)
}

// ContentWidths is kit.Layouter's: a layer takes the window.
func (p *Palette) ContentWidths(*kit.LayoutContext) (lo, hi kit.DIP) { return 0, 0 }

// ContentHeight is kit.Layouter's.
func (p *Palette) ContentHeight(*kit.LayoutContext, kit.DIP) kit.DIP { return 0 }

// Paint is kit.Painter's: the backdrop's --dim.
func (p *Palette) Paint(pc *kit.PaintContext) {
	t := views.TokensOf(&p.Element)
	c := t.Dim
	c.A *= p.dim
	sz := pc.Size()
	pc.FillRect(kit.R(0, 0, sz.W, sz.H), c)
}

// HandlePointer is kit.PointerHandler's: a press on the backdrop itself closes the palette.
func (p *Palette) HandlePointer(in *kit.PointerInput) bool {
	if in.Target != &p.Element || in.Action != kit.PointerDown {
		return false
	}
	p.Close()
	return true
}

// card is the palette's box: --raised, a 1 px --line-strong border, 12 px corners, --shadow-lg,
// fading and rising as it opens.
type card struct {
	kit.Element
	p      *Palette
	pushed bool
}

// Paint is kit.Painter's.
func (c *card) Paint(pc *kit.PaintContext) {
	p := c.p
	t := views.TokensOf(&c.Element)
	c.pushed = p.dim < 1 || p.rise < 1
	if c.pushed {
		pc.Canvas.PushOpacity(p.dim * p.rise)
		pc.Canvas.PushTransform(kit.Translation(0, -4*(1-p.rise)))
	}
	r := views.SnapBox(pc, 12)
	for i := len(t.ShadowLg) - 1; i >= 0; i-- { // the first on top
		if s := t.ShadowLg[i]; !s.Inset && s.Color.A > 0 {
			pc.Canvas.DrawShadow(r, s)
		}
	}
	pc.Canvas.FillRoundedRect(r, t.Raised)
	border := t.LineStrong
	if t.IsForced() {
		border = t.Ink
	}
	w := pc.Scale.SnapWidth(1)
	pc.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, border)
}

// PaintOverflow is kit.PaintOverflower's [mac e2e]: the --shadow-lg its Paint draws, and the
// 4 px it rises from while it opens.
func (c *card) PaintOverflow() kit.Insets {
	d := kit.ShadowsOverflow(views.TokensOf(&c.Element).ShadowLg)
	return kit.Insets{Top: d + 4, Right: d, Bottom: d, Left: d}
}

// PaintOver is kit.OverPainter's.
func (c *card) PaintOver(pc *kit.PaintContext) {
	if c.pushed {
		pc.Canvas.Pop()
		pc.Canvas.Pop()
		c.pushed = false
	}
}

// HandleKey is kit.KeyHandler's: the field's keys bubble here.
func (c *card) HandleKey(e kit.KeyEvent) bool { return c.p.handleKey(e) }

// now is the wall clock in epoch milliseconds (App.Clock: the dates of the rows).
func (p *Palette) now() float64 {
	if p.app.Clock != nil {
		return float64(p.app.Clock.Now().UnixMilli())
	}
	return float64(time.Now().UnixMilli())
}

// List is the rows' scroll pane.
func (p *Palette) List() *widgets.ScrollPane { return p.list }
