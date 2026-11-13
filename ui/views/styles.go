package views

import (
	"math"
	"strings"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
)

// The screen-level styles of app.css that several screens repeat (.field, .label-row, .hint,
// .error-text, .caps, .note, .mono, .vsep, .tag, .codec, .spinner), and their fonts. The
// widgets have the controls (.btn, .input, .chip, .pill, kbd, .switch, .check); a screen's own
// rules (a Settings page's .s-title, the details pane's .title) stay in its lane, built from
// the fonts here.

// The fonts.
var (
	// FontBody is body's 400 var(--type-body) / 1.4 var(--font-ui), tabular figures.
	FontBody = widgets.FontBody
	// FontMono is .mono: var(--mono-weight) 0.75rem/1.4 var(--font-mono), -0.01em.
	FontMono = widgets.FontMono
	// FontLabel is .field > label and .label-row > label: 600 var(--type-cell).
	FontLabel = uiFont(tokens.TypeCell, 600)
	// FontHint is .hint and .error-text: var(--type-small).
	FontHint = uiFont(tokens.TypeSmall, 400)
	// FontCell is var(--type-cell) at 400 (.note, details values).
	FontCell = uiFont(tokens.TypeCell, 400)
	// FontCaps is .caps: 600 var(--type-caps), +0.07em (upper case).
	FontCaps = kit.Font{Family: tokens.FontUI, Size: tokens.TypeCaps, Weight: 600, LetterSpacing: 0.07, LineHeight: 1.4, Tabular: true, AutoOptical: true}
	// FontTag is .tag and .codec: 650 var(--type-tag), +0.04em, line-height 1 (upper case).
	FontTag = kit.Font{Family: tokens.FontUI, Size: tokens.TypeTag, Weight: 650, LetterSpacing: 0.04, LineHeight: 1, Tabular: true, AutoOptical: true}
	// FontPage is the page title: 650 var(--type-page) / 1.2 var(--font-display), -0.02em
	// (Toolbar's .title, main.ts's .fatal h1).
	FontPage = kit.Font{Family: tokens.FontDisplay, Size: tokens.TypePage, Weight: 650, LineHeight: 1.2, LetterSpacing: -0.02, AutoOptical: true}
)

// SectionFont is a section title's font: 650 var(--type-section) / lineHeight
// var(--font-display), -0.01em (Settings' .s-title at 1.3, the details pane's .title at 1.2).
func SectionFont(lineHeight float32) kit.Font {
	return kit.Font{Family: tokens.FontDisplay, Size: tokens.TypeSection, Weight: 650, LineHeight: lineHeight, LetterSpacing: -0.01, AutoOptical: true}
}

// uiFont is the UI font at a size and weight with body's line height and figures.
func uiFont(size kit.Length, weight float32) kit.Font {
	return kit.Font{Family: tokens.FontUI, Size: size, Weight: weight, LineHeight: 1.4, Tabular: true, AutoOptical: true}
}

// Text is a kit.Label whose colour is an Ink read from the theme when it paints.
type Text struct {
	*kit.Label
	Ink Ink
}

// NewText makes a text of a font and ink (nil: --ink).
func NewText(s string, f kit.Font, ink Ink, o kit.TextOptions) *Text {
	if ink == nil {
		ink = InkInk
	}
	return &Text{Label: kit.NewLabel(s, f, o), Ink: ink}
}

// NewLine is a one-line text that ends in an ellipsis when it does not fit (its tooltip then
// shows it whole).
func NewLine(s string, f kit.Font, ink Ink) *Text {
	t := NewText(s, f, ink, kit.TextOptions{Truncate: kit.TruncateEnd})
	t.Style.MinWidth = kit.Px(0)
	return t
}

// NewParagraph is a text that wraps at word boundaries.
func NewParagraph(s string, f kit.Font, ink Ink) *Text {
	return NewText(s, f, ink, kit.TextOptions{Wrap: kit.TextWrapWords})
}

// Paint is kit.Painter's: the ink of the theme in effect, then the label.
func (t *Text) Paint(p *kit.PaintContext) {
	t.SetColor(t.Ink(TokensOf(t.Base())))
	t.Label.Paint(p)
}

// Field is app.css's .field: a label over its control, with hints and errors under it, 6 px
// apart (display: grid; gap: var(--sp-3)).
type Field struct {
	kit.Element
	Label *Text
}

// NewField makes a field: label (600 var(--type-cell), --ink) above control, then extras (a
// Hint, an ErrorText). An empty label leaves it out.
func NewField(label string, control kit.Widget, extras ...kit.Widget) *Field {
	f := &Field{}
	f.Style = kit.Style{Display: kit.DisplayBlock, RowGap: tokens.Sp3, MinWidth: kit.Px(0)}
	if label != "" {
		f.Label = NewParagraph(label, FontLabel, InkInk)
		f.Add(f.Label)
	}
	if control != nil {
		f.Add(control)
	}
	f.Add(extras...)
	return f
}

// NewLabelRow is app.css's .label-row: the label at the start and a hint (or free space) at
// the end on one baseline; a hint without room beside the label goes under it (flex-wrap:
// wrap; justify-content: space-between; gap: 0 var(--sp-6)).
func NewLabelRow(label string, end kit.Widget) *kit.Element {
	e := &kit.Element{Style: kit.Style{
		Display: kit.DisplayFlex, Wrap: true, AlignItems: kit.AlignBaseline, Justify: kit.JustifySpaceBetween,
		ColumnGap: tokens.Sp6, MinWidth: kit.Px(0),
	}}
	l := NewText(label, FontLabel, InkInk, kit.TextOptions{})
	e.Add(l)
	if end != nil {
		e.Add(end)
	}
	return e
}

// NewHint is app.css's .hint: var(--type-small) --faint, wrapping.
func NewHint(s string) *Text { return NewParagraph(s, FontHint, InkFaint) }

// NewErrorText is app.css's .error-text: var(--type-small) --danger, wrapping.
func NewErrorText(s string) *Text { return NewParagraph(s, FontHint, InkDanger) }

// NewCaps is app.css's .caps: a small-caps heading (600 var(--type-caps), +0.07em, upper
// case, --faint). It shows s in upper case and keeps s as its accessible name.
func NewCaps(s string) *Text {
	t := NewLine(strings.ToUpper(s), FontCaps, InkFaint)
	t.Semantics.Name = s
	return t
}

// NewMono is app.css's .mono text (addresses and paths), one line with an ellipsis.
func NewMono(s string, ink Ink) *Text { return NewLine(s, FontMono, ink) }

// NoteTone is a .note's tone.
type NoteTone uint8

// The note tones (.note, .note.info, .note.warn, .note.fail).
const (
	NotePlain NoteTone = iota
	NoteInfo
	NoteWarn
	NoteFail
)

// Note is app.css's .note: a hint or an error in a box (8 11 px padding, a 1 px border, 8 px
// corners, --surface-2, --ink-2 var(--type-cell) text; info, warn and fail grounds).
type Note struct {
	kit.Element
	Tone NoteTone
	Text *Text
}

// NewNote makes a note of s (nil content: s as a paragraph); more children follow the text.
func NewNote(tone NoteTone, s string, more ...kit.Widget) *Note {
	n := &Note{Tone: tone}
	n.Style = kit.Style{
		Display: kit.DisplayBlock, Padding: kit.EdgesOf(kit.Px(8), kit.Px(11)), Border: kit.EdgesAll(kit.Px(1)),
		Radius: kit.CornersAll(tokens.RMd), MinWidth: kit.Px(0),
	}
	if s != "" {
		n.Text = NewParagraph(s, FontCell, InkInk2)
		n.Add(n.Text)
	}
	n.Add(more...)
	return n
}

// colors are the note's ground and border.
func (n *Note) colors(t *tokens.Theme) (ground, border kit.Color) {
	switch n.Tone {
	case NoteInfo:
		return t.InfoSoft, t.InfoLine
	case NoteWarn:
		return t.WarnSoft, t.WarnLine
	case NoteFail:
		return t.DangerSoft, t.DangerLine
	}
	return t.Surface2, t.Line
}

// Paint is kit.Painter's.
func (n *Note) Paint(p *kit.PaintContext) {
	t := TokensOf(&n.Element)
	ground, border := n.colors(t)
	if t.IsForced() {
		border = t.Ink
	}
	// As CSS paints a background (border-box) and a border: the ground under the whole box,
	// the see-through border colour over it as a 1 px ring. The border colour filled under
	// the whole box tinted the ground too, so dark notes drew too bright.
	r := SnapBox(p, 8)
	p.Canvas.FillRoundedRect(r, ground)
	w := p.Scale.SnapWidth(1)
	p.Canvas.StrokeRoundedRect(r.Inset(w/2), kit.Stroke{Width: w}, border)
}

// NewVSep is app.css's .vsep: a 1 × 20 px --line rule with 6 px either side.
func NewVSep() kit.Widget {
	s := &vsep{}
	s.Semantics.Role = kit.RoleNone
	s.Style = kit.Style{Flex: kit.FlexNone, Width: kit.Px(1), Height: kit.Px(20), Margin: kit.EdgesInline(tokens.Sp3)}
	return s
}

type vsep struct{ kit.Element }

func (s *vsep) Paint(p *kit.PaintContext) {
	p.Canvas.FillRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), TokensOf(&s.Element).Line)
}

// Tag is app.css's .tag: HLS, DASH and a media download's site, its hue at 11% with the hue
// as text (lighter in the dark theme: color-mix(hue 55%, --ink)), 17 px high.
type Tag struct {
	kit.Element
	// Hue is --tag-hue (nil: --c-video).
	Hue   Ink
	label *kit.Label
}

// NewTag makes a tag showing s in upper case.
func NewTag(s string, hue Ink) *Tag {
	t := &Tag{Hue: hue}
	t.Semantics.Role = kit.RoleText
	t.Semantics.Name = s
	t.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone, ColumnGap: kit.Px(3),
		Height: kit.Px(17), Padding: kit.EdgesInline(kit.Px(5)),
	}
	t.label = kit.NewLabel(strings.ToUpper(s), FontTag, kit.TextOptions{})
	t.label.Semantics.Role = kit.RoleNone
	t.Add(t.label)
	return t
}

// Paint is kit.Painter's.
func (t *Tag) Paint(p *kit.PaintContext) {
	th := TokensOf(&t.Element)
	hue := th.CVideo
	if t.Hue != nil {
		hue = t.Hue(th)
	}
	ink := hue
	if th.Dark && !th.IsForced() {
		ink = kit.Mix(hue, 0.55, th.Ink)
	}
	p.Canvas.FillRoundedRect(SnapBox(p, 5), kit.Mix(hue, 0.11, kit.Color{}))
	t.label.SetColor(ink)
}

// Codec is app.css's .codec: a codec name (AV1, H.264) on --chrome with a 1 px --line border,
// 4 px corners, --ink-2 text, 17 px high.
type Codec struct {
	kit.Element
	label *kit.Label
}

// NewCodec makes a codec chip showing s in upper case.
func NewCodec(s string) *Codec {
	c := &Codec{}
	c.Semantics.Role = kit.RoleText
	c.Semantics.Name = s
	c.Style = kit.Style{
		Display: kit.DisplayFlex, AlignItems: kit.AlignCenter, Flex: kit.FlexNone,
		Height: kit.Px(17), Padding: kit.EdgesInline(kit.Px(5)), Border: kit.EdgesAll(kit.Px(1)),
	}
	c.label = kit.NewLabel(strings.ToUpper(s), FontTag, kit.TextOptions{})
	c.label.Semantics.Role = kit.RoleNone
	c.Add(c.label)
	return c
}

// Paint is kit.Painter's.
func (c *Codec) Paint(p *kit.PaintContext) {
	t := TokensOf(&c.Element)
	r := SnapBox(p, tokensRXs)
	p.Canvas.FillRoundedRect(r, t.Line)
	w := p.Scale.SnapWidth(1)
	p.Canvas.FillRoundedRect(kit.RoundRect(r.Rect.Inset(kit.UniformInsets(w)), tokensRXs-w), t.Chrome)
	c.label.SetColor(t.Ink2)
}

const tokensRXs = 4 // --r-xs

// Spinner is app.css's .spinner: the indeterminate "Getting video info" ring, 14 px, a 2 px
// --seg-track ring with an --accent top, one turn in 900 ms; still under reduced motion.
type Spinner struct {
	kit.Element
	deg  float32
	anim *spinAnim
	arc  kit.Path
	res  kit.Resources
}

// NewSpinner makes a spinner.
func NewSpinner() *Spinner {
	s := &Spinner{}
	s.Semantics.Hidden = true
	s.Style = kit.Style{Flex: kit.FlexNone, Width: kit.Px(14), Height: kit.Px(14)}
	return s
}

// SpinPeriod is the spinner's turn.
const SpinPeriod = 900 * time.Millisecond

type spinAnim struct {
	s     *Spinner
	start time.Time
	dead  bool
}

func (a *spinAnim) Step(now time.Time) bool {
	if a.dead {
		return false
	}
	if a.start.IsZero() {
		a.start = now
	}
	f := math.Mod(float64(now.Sub(a.start))/float64(SpinPeriod), 1)
	a.s.deg = float32(f * 360)
	a.s.Invalidate()
	return true
}

func (a *spinAnim) Finish() { a.dead = true }

// Attached is kit.Attacher's: the turn starts unless motion is reduced.
func (s *Spinner) Attached() {
	w := s.Window()
	if w == nil || !w.MotionOK() {
		return
	}
	s.anim = &spinAnim{s: s}
	w.Animate(s.anim)
}

// Detached is kit.Detacher's.
func (s *Spinner) Detached() {
	if s.anim != nil {
		s.anim.dead = true
		s.anim = nil
	}
	if s.arc != nil {
		s.arc.Release()
		s.arc, s.res = nil, nil
	}
}

// spinRadius is the ring's centre line: 7 px less half the 2 px border.
const spinRadius = 6

// Paint is kit.Painter's: the track ring, then the accent top border (the quarter between the
// diagonals) turned by the animation.
func (s *Spinner) Paint(p *kit.PaintContext) {
	t := TokensOf(&s.Element)
	c := kit.RectAt(p.Origin, p.Size()).Center()
	stroke := kit.Stroke{Width: 2}
	p.Canvas.StrokeEllipse(kit.R(c.X-spinRadius, c.Y-spinRadius, c.X+spinRadius, c.Y+spinRadius), stroke, t.SegTrack)
	w := s.Window()
	if w == nil || w.Host() == nil {
		return
	}
	if res := w.Host().Resources(); s.res != res || s.arc == nil {
		if s.arc != nil {
			s.arc.Release()
		}
		var d kit.PathData
		k := kit.DIP(spinRadius * math.Sqrt2 / 2)
		d.MoveTo(kit.Pt(-k, -k))
		d.ArcTo(kit.Size{W: spinRadius, H: spinRadius}, 0, false, true, kit.Pt(k, -k))
		arc, err := res.NewPath(d)
		if err != nil {
			return
		}
		s.arc, s.res = arc, res
	}
	p.Canvas.PushTransform(kit.Rotation(s.deg).Then(kit.Translation(c.X, c.Y)))
	p.Canvas.StrokePath(s.arc, stroke, t.Accent)
	p.Canvas.Pop()
}

// SnapBox is the element's border box in the paint's device pixels, rounded by rad (a widget's
// box on whole pixels, as Chromium draws it).
func SnapBox(p *kit.PaintContext, rad kit.DIP) kit.RRect {
	return kit.RoundRect(p.Scale.SnapRect(kit.RectAt(p.Origin, p.Size())), rad)
}
