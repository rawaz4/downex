// Package dialogs is the main window's small dialogs (plan step 13): Remove
// (RemoveDialog.svelte), Restart and Download again (RestartDialog.svelte, with the
// existing-file choice remembered under downex.existingFile), the open confirmation
// (OpenDialog.svelte) and Move to queue (MoveToQueueDialog.svelte), on the widgets dialog shell
// through views.App.NewDialog. Each constructor takes the App and the request its opener
// serves and returns a views.Modal for app.Dialogs.Show; the composition (views/app) calls it
// from the matching App.Open field. What a dialog's button does is App.svelte's function for
// it (report(store.remove(…)), restartDownloads, openFile, moveToQueue); a constructor's
// callback replaces it when the composition gives one.
//
// Wording, sizes and colours are today's: every string is the Svelte component's, byte for
// byte. The widgets and methods belong to the UI thread; the confirmations run their calls
// through App.Go and come back with App.UI.
package dialogs

import (
	"downex/ui/core/text"
	"downex/ui/kit"
	"downex/ui/kit/icons"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// quote is RestartDialog's quote: “name”, the name isolated (U+2068…U+2069) so that a
// right-to-left name keeps its own order in the sentence.
func quote(name string) string { return "“" + text.Isolate(name) + "”" }

// toneKind is a .tone's colour: .tone.danger or .tone.info.
type toneKind uint8

const (
	toneDanger toneKind = iota
	toneInfo
)

// tone is the dialogs' .tone: a 32 px square (--r-md) in --danger-soft or --info-soft with an
// 18 px icon in --danger or --info, and a 1 px CanvasText border under forced colours. It is
// hidden from assistive technology (aria-hidden).
type tone struct {
	kit.Element
	kind toneKind
	icon *widgets.Icon
}

func newTone(kind toneKind, name string) *tone {
	t := &tone{kind: kind}
	t.Semantics.Hidden = true
	t.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Justify: kit.JustifyCenter, Flex: kit.FlexNone,
		Width: kit.Px(32), Height: kit.Px(32),
	}
	n, _ := icons.ByName(name)
	t.icon = widgets.NewIcon(n, 18, func(th *tokens.Theme) kit.Color {
		if t.kind == toneInfo {
			return th.Info
		}
		return th.Danger
	})
	t.Add(t.icon)
	return t
}

// setIcon changes the icon (Remove: alert while the files are deleted too).
func (t *tone) setIcon(name string) {
	if n, ok := icons.ByName(name); ok {
		t.icon.SetName(n)
	}
}

// Paint is kit.Painter's.
func (t *tone) Paint(p *kit.PaintContext) {
	th := views.TokensOf(&t.Element)
	box := views.SnapBox(p, 8)
	ground := th.DangerSoft
	if t.kind == toneInfo {
		ground = th.InfoSoft
	}
	p.Canvas.FillRoundedRect(box, ground)
	if th.IsForced() {
		w := p.Scale.SnapWidth(1)
		p.Canvas.StrokeRoundedRect(kit.RoundRect(box.Rect.Inset(kit.UniformInsets(w/2)), 8-w/2), kit.Stroke{Width: w}, th.Ink)
	}
}

// names is the dialogs' .names list: the first five names (one line each, cut with an
// ellipsis), then "and N more" in --muted; 8 px 11 px inside a 1 px --line border with 9 px
// corners on --surface-2, 2 px between the lines, var(--type-cell) --ink, at most 9rem high.
// Each name is isolated (<bdi>). Remove's names carry their whole name as a tooltip (title).
//
// It gives way as CSS gives it in the dialog's body (a grid whose items' automatic minimum is
// 0 for a scroll container: .names has overflow: auto, its <li> overflow: hidden): when the
// dialog has less room than its content needs, the list alone becomes shorter, its lines
// sharing what is left equally and each cut at its foot, and the body does not scroll. kit's
// dialog body is a block in a scroll pane, so the list measures the pane after each layout
// (kit.LayoutFollower) and takes the height CSS would give it.
type names struct {
	kit.Element
	lines   []*kit.Element // the <li>s, each clipping its text
	natural kit.DIP        // the content height at the last width asked for
	limit   kit.DIP        // the height the room allows (0: none)
}

// maxNames is how many names the lists show (names.slice(0, 5)).
const maxNames = 5

// namesGap is the gap between the lines (gap: 2px).
const namesGap = 2

func newNames(list []string, titled bool) *names {
	n := &names{}
	n.Semantics.Role = kit.RoleList
	n.Style = kit.Style{
		MinWidth: kit.Px(0), MaxHeight: kit.Rem(9), Overflow: kit.OverflowHidden,
		Padding: kit.EdgesOf(kit.Px(8), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)),
	}
	shown := list[:min(len(list), maxNames)]
	more := len(list) - len(shown)
	for i, name := range shown {
		li := views.NewLine(name, views.FontCell, views.InkInk)
		li.Semantics.PosInSet, li.Semantics.SetSize = i+1, len(shown)+boolInt(more > 0)
		if titled {
			li.Tooltip = name
		}
		n.add(li)
	}
	if more > 0 {
		li := views.NewLine("and "+itoa(more)+" more", views.FontCell, views.InkMuted)
		li.Semantics.PosInSet, li.Semantics.SetSize = len(shown)+1, len(shown)+1
		n.add(li)
	}
	return n
}

func (n *names) add(t *views.Text) {
	li := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, MinWidth: kit.Px(0), Overflow: kit.OverflowHidden}}
	li.Semantics.Role = kit.RoleListItem
	li.Semantics.PosInSet, li.Semantics.SetSize = t.Semantics.PosInSet, t.Semantics.SetSize
	li.Tooltip, t.Tooltip = t.Tooltip, ""
	t.Semantics.PosInSet, t.Semantics.SetSize = 0, 0
	li.Add(t)
	n.lines = append(n.lines, li)
	n.Add(li)
}

// ContentWidths is kit.Layouter's: the widest line.
func (n *names) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	for _, li := range n.lines {
		a, b := ctx.IntrinsicWidths(li)
		lo, hi = max(lo, a), max(hi, b)
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's: the lines and the gaps between them.
func (n *names) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	var h kit.DIP
	for i, li := range n.lines {
		if i > 0 {
			h += namesGap
		}
		h += ctx.HeightFor(li, w, w)
	}
	n.natural = h
	return h
}

// LayoutContent is kit.Layouter's: the lines at their height, or sharing a shorter box
// equally.
func (n *names) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	natural := n.ContentHeight(ctx, w)
	share := kit.DIP(-1)
	if k := len(n.lines); k > 0 && box.Height() < natural-0.01 {
		share = max(0, (box.Height()-namesGap*kit.DIP(k-1))/kit.DIP(k))
	}
	y := box.Min.Y
	for _, li := range n.lines {
		h := ctx.HeightFor(li, w, w)
		if share >= 0 {
			h = min(h, share)
		}
		ctx.Place(li, kit.R(box.Min.X, y, box.Max.X, y+h), w)
		y += h + namesGap
	}
}

// LaidOut is kit.LayoutFollower's: the height the dialog's room leaves the list (the room less
// everything else in the body), at most its content's and 9rem.
func (n *names) LaidOut() {
	var pane *widgets.ScrollPane
	for p := n.Parent(); p != nil && pane == nil; p = p.Parent() {
		pane, _ = p.Widget().(*widgets.ScrollPane)
	}
	w := n.Window()
	if pane == nil || w == nil {
		return
	}
	_, content, view := pane.ScrollRange()
	h := n.Bounds().Height()
	rest := content - h
	full := n.natural + n.insetsHeight()
	target := view - rest
	limit := kit.DIP(0)
	if target < full-0.25 {
		limit = max(target, n.insetsHeight())
	}
	if abs(limit-n.limit) < 0.25 {
		return
	}
	n.limit = limit
	n.Style.MaxHeight = kit.Rem(9)
	if limit > 0 {
		n.Style.MaxHeight = kit.Px(min(limit, w.Environment().Scale.Rem(9)))
	}
	n.Relayout()
}

// insetsHeight is the list's padding and border, top and bottom.
func (n *names) insetsHeight() kit.DIP { return 2*8 + 2*1 }

func abs(v kit.DIP) kit.DIP {
	if v < 0 {
		return -v
	}
	return v
}

// Paint is kit.Painter's.
func (n *names) Paint(p *kit.PaintContext) {
	t := views.TokensOf(&n.Element)
	box := views.SnapBox(p, 9)
	p.Canvas.FillRoundedRect(box, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(box.Rect.Inset(kit.UniformInsets(w)), 9-w), t.Surface2)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// footButton is a footer button of the default size (the shell makes it the footer's 32 px).
func footButton(label string, v widgets.Variant, onPress func()) *widgets.Button {
	return widgets.NewButton(label, "", v, widgets.SizeMd, onPress)
}

// fixRows makes RadioRows' rows draw as RestartDialog's and MoveToQueueDialog's do where they
// differ, and hands each row to tune (nil: nothing more). The components keep a row's .tick
// (16 px) whether or not it is chosen, with the check drawn only in the chosen one; RadioRows
// hides it in the rows that are not, so their text runs 26 px further (a note wraps
// elsewhere, Move's state moves to the end). Here the check is a 16 px icon in every row, in
// --accent on the chosen one and drawn in no colour on the others. RadioRows has no option
// for it (see the result's needs); it puts a row's children as icon, label (inline) or text
// box, note (inline), check.
func fixRows(g *widgets.RadioRows, tune func(i int, row *kit.Element, icon *widgets.Icon, note *kit.Label)) {
	for i, row := range g.Children() {
		kids := row.Children()
		if len(kids) < 3 || i >= len(g.Options) {
			continue
		}
		last := kids[len(kids)-1]
		old, ok := last.Widget().(*widgets.Icon)
		if !ok {
			continue
		}
		value := g.Options[i].Value
		tick := widgets.NewIcon(old.Name, 16, func(t *tokens.Theme) kit.Color {
			if g.Value == value {
				return t.Accent
			}
			return kit.Color{}
		})
		tick.Style.Margin = old.Style.Margin
		row.Remove(old)
		row.Add(tick)
		if tune == nil || len(kids) != 4 {
			continue
		}
		icon, ok1 := kids[0].Widget().(*widgets.Icon)
		note, ok2 := kids[2].Widget().(*kit.Label)
		if ok1 && ok2 {
			tune(i, row, icon, note)
		}
	}
}

// bodyPane is a dialog's scrolling body (the shell's ScrollPane around its Body).
func bodyPane(d *views.Dialog) (*widgets.ScrollPane, bool) {
	if p := d.Body.Parent(); p != nil {
		s, ok := p.Widget().(*widgets.ScrollPane)
		return s, ok
	}
	return nil, false
}
