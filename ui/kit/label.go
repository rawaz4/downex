package kit

import "math"

// Font is a TextStyle whose size is a CSS length, as app.css's type tokens are (rem, so
// that text follows the Windows text size); the layout resolves it for the window's scale.
type Font struct {
	// Family is a CSS font-family list (TextStyle.Family).
	Family string
	// Size is the font size: Rem(0.8125) is --type-body.
	Size Length
	// Weight, OpticalSize, LetterSpacing (em), LineHeight (a multiple; 0 = normal), Italic
	// and Tabular are TextStyle's.
	Weight, OpticalSize, LetterSpacing, LineHeight float32
	Italic, Tabular                                bool
	// AutoOptical is CSS's font-optical-sizing: auto, Chromium's default for every text: the
	// 'opsz' axis follows the resolved size (TextStyle.OpticalSize = Size). It wins over
	// OpticalSize.
	AutoOptical bool
}

// Style is f for a window's scale.
func (f Font) Style(s Scale) TextStyle {
	t := TextStyle{
		Family:        f.Family,
		Size:          f.Size.or(s, -1, 0),
		Weight:        f.Weight,
		Italic:        f.Italic,
		OpticalSize:   f.OpticalSize,
		LetterSpacing: f.LetterSpacing,
		LineHeight:    f.LineHeight,
		Tabular:       f.Tabular,
	}
	if f.AutoOptical {
		t.OpticalSize = t.Size
	}
	return t
}

// fitSlop is how much wider than its box a line may measure and still count as fitting:
// Chromium compares layout widths in 1/64 px units.
const fitSlop = 1.0 / 64

// Label is a run of text in one font: a single line that ends in an ellipsis when it does
// not fit, or a paragraph that wraps (and may be clamped to a number of lines). It knows
// when its text is cut (Truncated) and then gives its whole text as its tooltip, which is
// the fit rule's requirement. Its node is RoleText named by its text, unless its
// Semantics say otherwise.
//
// A label holds at most two text layouts: the text unbounded (its natural size) and,
// when that does not fit, the text laid out in its box; it releases them when its font or
// options change and when it leaves its window. When its text changes, a layout that can
// hold other text (TextRelayouter) lays the new text out in place.
type Label struct {
	Element
	text  string
	font  Font
	opts  TextOptions
	color Color
	// A file name's label (NewFileName) lays out shown, the name with its embeddings.
	fileName bool
	shown    string

	style     TextStyle // font for the scale of the last layout
	natural   TextLayout
	measurer  TextMeasurer // what made natural
	stale     bool         // the text changed since natural was laid out
	fitted    TextLayout
	fitW      DIP
	drawn     TextLayout
	offset    Point // where drawn sits in the content box
	truncated bool
	err       error
}

// NewLabel makes a label. Its Style is left at the zero value; set it as for any element.
func NewLabel(text string, font Font, opts TextOptions) *Label {
	l := &Label{text: text, font: font, opts: opts}
	l.self = l
	l.Semantics.Role = RoleText
	return l
}

// Text is the label's text.
func (l *Label) Text() string { return l.text }

// TextContent is the text the label shows, for the names of its ancestors.
func (l *Label) TextContent() string { return l.text }

// SetText changes the label's text.
func (l *Label) SetText(s string) {
	if s == l.text {
		return
	}
	l.text = s
	if l.fileName {
		l.shown = l.fileNameText()
	}
	l.textChanged()
	l.SemanticsChanged()
}

// Font is the label's font.
func (l *Label) Font() Font { return l.font }

// SetFont changes the label's font.
func (l *Label) SetFont(f Font) {
	if f == l.font {
		return
	}
	l.font = f
	l.changed()
}

// Options are the label's text options (MaxWidth is the kit's: the label's box).
func (l *Label) Options() TextOptions { return l.opts }

// SetOptions changes how the label lays out its text.
func (l *Label) SetOptions(o TextOptions) {
	if o == l.opts {
		return
	}
	l.opts = o
	if l.fileName {
		l.shown = l.fileNameText()
	}
	l.changed()
}

// Color is the text's colour.
func (l *Label) Color() Color { return l.color }

// SetColor changes the text's colour.
func (l *Label) SetColor(c Color) {
	if c == l.color {
		return
	}
	l.color = c
	// a parent's Paint sets its labels' colours (a button's, a row's): a label inside the
	// area being painted paints next with it
	if l.win != nil && l.win.paintedNow(&l.Element) {
		return
	}
	l.Invalidate()
}

// Truncated reports whether the last layout cut the text: an ellipsis, or a line clamp.
func (l *Label) Truncated() bool { return l.truncated }

// TooltipText is the whole text while it is cut (TooltipSource).
func (l *Label) TooltipText() string {
	if l.truncated {
		return l.text
	}
	return ""
}

// TextLayout is the layout the label draws, or nil (before its first layout, or when the
// text could not be laid out: Err says why). It belongs to the label.
func (l *Label) TextLayout() TextLayout { return l.drawn }

// TextOrigin is where the drawn layout's box sits, in the label's coordinates.
func (l *Label) TextOrigin() Point { return l.ContentRect().Min.Add(l.offset) }

// Err is the error of the last layout, if it failed.
func (l *Label) Err() error { return l.err }

// layoutText is the text the label lays out: its text, or a file name's with its
// embeddings.
func (l *Label) layoutText() string {
	if l.fileName {
		return l.shown
	}
	return l.text
}

func (l *Label) changed() {
	l.Invalidate()
	l.release()
	l.Relayout()
}

// textChanged is changed for a new text: the natural layout is kept for the next layout
// pass to lay the text out in, when it can.
func (l *Label) textChanged() {
	if _, ok := l.natural.(TextRelayouter); !ok {
		l.changed()
		return
	}
	l.Invalidate()
	if l.fitted != nil {
		l.fitted.Release()
		l.fitted = nil
	}
	l.drawn, l.stale = nil, true
	l.Relayout()
}

func (l *Label) release() {
	if l.natural != nil {
		l.natural.Release()
	}
	if l.fitted != nil {
		l.fitted.Release()
	}
	l.natural, l.fitted, l.drawn, l.measurer, l.stale = nil, nil, nil, nil, false
}

// Detached releases the label's layouts.
func (l *Label) Detached() { l.release() }

// natural lays the text out unbounded once for the scale in effect.
func (l *Label) naturalLayout(ctx *LayoutContext) TextLayout {
	st := l.font.Style(ctx.Scale)
	if l.natural != nil && st == l.style && !l.stale {
		return l.natural
	}
	o := l.opts
	o.MaxWidth, o.MaxLines, o.Truncate = 0, 0, TruncateNone
	if r, ok := l.natural.(TextRelayouter); ok && l.stale && st == l.style && ctx.Text == l.measurer {
		// The new text in the layout the old one had (a released one fails: made anew).
		l.stale = false
		err := r.Relayout(l.layoutText(), st, o)
		l.err = err
		if err == nil {
			return l.natural
		}
		l.natural, l.measurer = nil, nil
	}
	l.release()
	l.style = st
	if ctx.Text == nil {
		return nil
	}
	t, err := ctx.Text.LayoutText(l.layoutText(), st, o)
	l.err = err
	if err != nil {
		return nil
	}
	l.natural, l.measurer = t, ctx.Text
	return t
}

// fit is the layout of the text in a box w wide: the natural one when it fits.
func (l *Label) fit(ctx *LayoutContext, w DIP) TextLayout {
	n := l.naturalLayout(ctx)
	if n == nil {
		return nil
	}
	m := n.Metrics()
	tooWide := m.Width > w+fitSlop && (l.opts.Wrap != TextWrapNone || l.opts.Truncate != TruncateNone)
	tooLong := l.opts.MaxLines > 0 && m.Lines > l.opts.MaxLines
	if !tooWide && !tooLong {
		return n // it fits, or it may run out of its box (text-overflow: clip)
	}
	if l.fitted != nil && l.fitW == w {
		return l.fitted
	}
	if l.fitted != nil {
		l.fitted.Release()
		l.fitted = nil
	}
	o := l.opts
	o.MaxWidth = max(w, fitSlop)
	t, err := ctx.Text.LayoutText(l.layoutText(), l.style, o)
	l.err = err
	if err != nil {
		l.fitted = nil
		return n
	}
	l.fitted, l.fitW = t, w
	return t
}

// ContentWidths is Layouter's: the text's max-content width, and its min-content width
// (the whole line when it cannot wrap).
func (l *Label) ContentWidths(ctx *LayoutContext) (min, max DIP) {
	n := l.naturalLayout(ctx)
	if n == nil {
		return 0, 0
	}
	m := n.Metrics()
	if l.opts.Wrap == TextWrapNone {
		return m.Width, m.Width
	}
	return m.MinWidth, m.Width
}

// TrailingSpace is TrailingSpacer's: the width of the spaces the text ends with, which its
// max-content width (TextMetrics.Width) leaves out and its advance counts. A label that
// cannot lay its text out has none.
func (l *Label) TrailingSpace(ctx *LayoutContext) DIP {
	n := l.naturalLayout(ctx)
	if n == nil {
		return 0
	}
	m := n.Metrics()
	return max(m.Advance-m.Width, 0)
}

// TrailingSpacer is a widget whose content can end in white space that its max-content
// width leaves out, as CSS hangs a line's trailing spaces: a Label whose text ends with a
// space.
type TrailingSpacer interface {
	TrailingSpace(ctx *LayoutContext) DIP
}

// InlineAdvance is how far c moves the pen of a flow that sets widgets one after the other
// on a line (a paragraph of one label per word, each with its trailing space): its
// max-content border-box width and the trailing spaces that width leaves out. A word flow
// fits a word on its line by IntrinsicWidths (its trailing space may hang past the line's
// end) and starts the next word at InlineAdvance; starting it at the word's IntrinsicWidths
// runs the words together ("Thelistshows…"), since the platform's text engine never counts
// trailing spaces in a text's width.
func (ctx *LayoutContext) InlineAdvance(c Widget) DIP {
	_, w := ctx.IntrinsicWidths(c)
	if c.Base().Style.Hidden {
		return w
	}
	t, ok := c.(TrailingSpacer)
	if !ok {
		t, ok = c.Base().self.(TrailingSpacer)
	}
	if ok {
		w += t.TrailingSpace(ctx)
	}
	return w
}

// ContentHeight is Layouter's.
func (l *Label) ContentHeight(ctx *LayoutContext, w DIP) DIP {
	t := l.fit(ctx, w)
	if t == nil {
		return 0
	}
	return t.Metrics().Height
}

// LayoutContent is Layouter's.
func (l *Label) LayoutContent(ctx *LayoutContext, box Rect) {
	w := box.Width()
	t := l.fit(ctx, w)
	l.drawn, l.offset = t, Point{}
	was := l.truncated
	l.truncated = false
	if t == nil {
		return
	}
	m := t.Metrics()
	l.truncated = m.Truncated
	if t == l.natural && m.Width < w {
		// The natural layout is as wide as its text; place it in the box as the options
		// align it (a fitted layout aligns itself within MaxWidth).
		align := l.opts.Align
		rtl := m.Direction == DirRTL
		switch {
		case align == TextAlignCenter:
			l.offset.X = (w - m.Width) / 2
		case (align == TextAlignEnd) != rtl:
			l.offset.X = w - m.Width
		}
	}
	if was != l.truncated {
		l.SemanticsChanged()
	}
}

// inkReach and inkSide are how far past its line boxes and past the ends of its lines a
// label's glyphs may draw, in em. Glyphs are not bound by the line box (CSS line-height 1.15
// is less than Segoe UI's ascent plus descent): Kurdish and Arabic marks, the descenders
// of y, g and p, and the fallback fonts of other scripts reach past it by up to about a
// third of an em; at the ends of a line only overhangs (f, j, italics) reach out.
const (
	inkReach = 0.5
	inkSide  = 0.25
)

// inkOverflow is how far the label's glyphs may draw outside its box, rounded up to whole
// DIPs: a change repaints them whole, and a paint that reaches only them draws them.
func (l *Label) inkOverflow() Insets {
	if l.drawn == nil {
		return Insets{}
	}
	v := DIP(math.Ceil(float64(l.style.Size * inkReach)))
	h := DIP(math.Ceil(float64(l.style.Size * inkSide)))
	return Insets{Top: v, Right: h, Bottom: v, Left: h}
}

// ContentBaseline is Baseliner's.
func (l *Label) ContentBaseline() (DIP, bool) {
	if l.drawn == nil {
		return 0, false
	}
	return l.drawn.Metrics().Baseline, true
}

// baselineAt is the first line's baseline from the border box's top, which does not
// depend on the width for a label.
func (l *Label) baselineAt(ctx *LayoutContext, w DIP) (DIP, bool) {
	n := l.naturalLayout(ctx)
	if n == nil {
		return 0, false
	}
	st := ctx.style(&l.Element)
	pb, _ := ctx.boxInsets(st, -1)
	return pb.Top + n.Metrics().Baseline, true
}

// Paint is Painter's: the text in its colour, or the palette's Text when it has none.
func (l *Label) Paint(p *PaintContext) {
	if l.drawn == nil {
		return
	}
	c := l.color
	if c == (Color{}) {
		c = p.Palette.Text
	}
	p.DrawText(l.drawn, l.TextOrigin(), c)
}

// FillSemantics is SemanticFiller's: a label is named by its text.
func (l *Label) FillSemantics(n *SemanticNode) {
	if n.Name == "" && n.Role == RoleText {
		n.Name = l.text
	}
}
