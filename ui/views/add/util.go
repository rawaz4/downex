package add

import (
	"strings"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/views"
)

// disabler is a control that HTML's disabled turns off.
type disabler interface {
	kit.Widget
	SetDisabled(disabled, focusable bool)
}

// setDisabled is the disabled attribute on a button: a focused button that becomes disabled
// loses the focus to the document (HTML's focusing steps' fix-up: document.activeElement is
// the body, nothing shows a focus ring), and the focus does not come back when it is enabled.
func setDisabled(b disabler, off bool) {
	b.SetDisabled(off, false)
	if !off {
		return
	}
	if w := b.Base().Window(); w != nil && w.Focused() == b.Base() {
		w.Blur()
	}
}

// overlay is the dialogs' .address and .folder boxes (position: relative): its first child
// fills the box (the field, whose padding leaves room), the others sit over it, centred on the
// block axis, at an inline inset from the start or the end (position: absolute).
type overlay struct {
	kit.Element
	anchors []anchor
}

type anchor struct {
	w     kit.Widget
	end   bool
	inset kit.DIP
}

func newOverlay(field kit.Widget) *overlay {
	o := &overlay{}
	o.Style = kit.Style{MinWidth: kit.Px(0)}
	o.Add(field)
	return o
}

// at puts w over the field, inset from the start (end false) or the end.
func (o *overlay) at(w kit.Widget, end bool, inset kit.DIP) {
	o.anchors = append(o.anchors, anchor{w: w, end: end, inset: inset})
	o.Add(w)
}

func (o *overlay) field() *kit.Element { return o.Children()[0] }

// ContentWidths is kit.Layouter's: the field's.
func (o *overlay) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	return ctx.IntrinsicWidths(o.field().Widget())
}

// ContentHeight is kit.Layouter's: the field's.
func (o *overlay) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	return ctx.HeightFor(o.field().Widget(), w, w)
}

// LayoutContent is kit.Layouter's.
func (o *overlay) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	w := box.Width()
	ctx.Place(o.field().Widget(), box, w)
	for _, a := range o.anchors {
		e := a.w.Base()
		if e.Style.Hidden {
			continue
		}
		_, aw := ctx.IntrinsicWidths(a.w)
		ah := ctx.HeightFor(a.w, aw, w)
		y := box.Min.Y + (box.Height()-ah)/2
		x := box.Min.X + a.inset
		if a.end != ctx.RTL {
			x = box.Max.X - a.inset - aw
		}
		ctx.Place(a.w, kit.R(x, y, x+aw, y+ah), w)
	}
}

// run is a piece of inline text: its words flow with the other runs' (an inline link in a
// note, an address and its error in a failure's line).
type run struct {
	text string
	font kit.Font
	ink  views.Ink
	// press makes the run a link (a .btn.link inside the text).
	press func()
	// name is the link's accessible name.
	name string
	// anywhere breaks the run between any characters (word-break: break-all).
	anywhere bool
}

// flow lays runs out as a paragraph: words wrap at spaces (anywhere in a break-all run), one
// line box of the tallest font, a space of the run's font between words.
type flow struct {
	kit.Element
	words []*flowWord
	place []kit.Rect
	// Text is the paragraph's text, as textContent reads it.
	Text string
}

type flowWord struct {
	w     kit.Widget
	label *views.Text
	space bool // a space before it
	font  kit.Font
}

// linkWord is a word of a link run: a button that its run's words share.
type linkWord struct {
	kit.Pressable
	label *views.Text
}

// Paint is kit.Painter's: --accent-text, underlined while hovered (.btn.link).
func (l *linkWord) Paint(p *kit.PaintContext) {
	if l.Hovered() {
		t := views.TokensOf(&l.Element)
		r := kit.RectAt(p.Origin, p.Size())
		y := p.Scale.Snap(r.Max.Y - 3)
		p.Canvas.FillRect(kit.R(r.Min.X, y, r.Max.X, y+p.Scale.SnapWidth(1)), t.AccentText)
	}
}

// HoverChanged is kit.HoverHandler's.
func (l *linkWord) HoverChanged(h bool) {
	l.Pressable.HoverChanged(h)
	for _, k := range l.Parent().Children() {
		if w, ok := k.Widget().(*linkWord); ok && w != l && w.OnPress != nil {
			w.Invalidate()
		}
	}
}

func accentText(t *tokens.Theme) kit.Color { return t.AccentText }

var _ = accentText

func newFlow(runs ...run) *flow {
	f := &flow{}
	f.Style = kit.Style{MinWidth: kit.Px(0)}
	var all strings.Builder
	pending := false // a space comes before the next word
	for _, r := range runs {
		all.WriteString(r.text)
		var toks []string
		if r.anywhere {
			for _, ch := range r.text {
				toks = append(toks, string(ch))
			}
		} else {
			toks = strings.Split(r.text, " ")
		}
		var link *linkWord
		for i, tok := range toks {
			if !r.anywhere && i > 0 {
				pending = true
			}
			if tok == "" || tok == " " {
				pending = pending || tok == " "
				continue
			}
			lab := views.NewText(tok, r.font, r.ink, kit.TextOptions{Direction: kit.DirLTR})
			lab.Semantics.Role = kit.RoleNone
			word := &flowWord{label: lab, space: pending && len(f.words) > 0, font: r.font}
			pending = false
			if r.press != nil {
				lw := &linkWord{label: lab}
				lw.OnPress = r.press
				lw.Cursor = kit.CursorPointer
				lw.Semantics.Role = kit.RoleNone
				if link == nil {
					lw.Focus = kit.FocusTab
					lw.Semantics.Role = kit.RoleButton
					lw.Semantics.Name = r.name
					link = lw
				}
				lw.Add(lab)
				word.w = lw
			} else {
				word.w = lab
			}
			f.words = append(f.words, word)
			f.Add(word.w)
		}
	}
	f.Text = all.String()
	f.Semantics.Role = kit.RoleText
	f.Semantics.Name = f.Text
	return f
}

// Link is the first word of the link run (the button), or nil.
func (f *flow) Link() *linkWord {
	for _, w := range f.words {
		if l, ok := w.w.(*linkWord); ok && l.Focus == kit.FocusTab {
			return l
		}
	}
	return nil
}

func (f *flow) spaceWidth(ctx *kit.LayoutContext, fnt kit.Font) kit.DIP {
	if ctx.Text == nil {
		v, _ := fnt.Size.Resolve(ctx.Scale, -1)
		return v * 0.25
	}
	l, err := ctx.Text.LayoutText(" ", fnt.Style(ctx.Scale), kit.TextOptions{})
	if err != nil {
		return 0
	}
	defer l.Release()
	return l.Metrics().Advance
}

// lay places the words at width w (unbounded when w < 0) and reports the height.
func (f *flow) lay(ctx *kit.LayoutContext, w kit.DIP) kit.DIP {
	f.place = f.place[:0]
	var x, y, lineH kit.DIP
	spaces := map[kit.Font]kit.DIP{}
	for i, word := range f.words {
		_, ww := ctx.IntrinsicWidths(word.w)
		wh := ctx.HeightFor(word.w, ww, w)
		sp := kit.DIP(0)
		if word.space && i > 0 {
			s, ok := spaces[word.font]
			if !ok {
				s = f.spaceWidth(ctx, word.font)
				spaces[word.font] = s
			}
			sp = s
		}
		if i > 0 && w >= 0 && x+sp+ww > w+0.01 {
			y += lineH
			x, lineH, sp = 0, 0, 0
		}
		x += sp
		f.place = append(f.place, kit.R(x, y, x+ww, y+wh))
		x += ww
		lineH = max(lineH, wh)
	}
	return y + lineH
}

// ContentWidths is kit.Layouter's: the widest word, and the whole line.
func (f *flow) ContentWidths(ctx *kit.LayoutContext) (lo, hi kit.DIP) {
	for _, word := range f.words {
		_, ww := ctx.IntrinsicWidths(word.w)
		lo = max(lo, ww)
	}
	f.lay(ctx, -1)
	for _, r := range f.place {
		hi = max(hi, r.Max.X)
	}
	return lo, hi
}

// ContentHeight is kit.Layouter's.
func (f *flow) ContentHeight(ctx *kit.LayoutContext, w kit.DIP) kit.DIP { return f.lay(ctx, w) }

// LayoutContent is kit.Layouter's.
func (f *flow) LayoutContent(ctx *kit.LayoutContext, box kit.Rect) {
	f.lay(ctx, box.Width())
	for i, word := range f.words {
		r := f.place[i].Add(box.Min)
		if ctx.RTL {
			r = kit.R(box.Max.X-(r.Max.X-box.Min.X), r.Min.Y, box.Max.X-(r.Min.X-box.Min.X), r.Max.Y)
		}
		ctx.Place(word.w, r, box.Width())
	}
}
