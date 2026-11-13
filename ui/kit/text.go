package kit

// TextStyle is how text looks: the CSS font properties app.css uses. It is comparable, so
// it can key a cache, and it stays comparable.
type TextStyle struct {
	// Family is a CSS font-family list, as app.css writes them: names separated by commas,
	// each bare or in single or double quotes. The first family in the list that is
	// installed sets the text; characters it lacks come from the system's font fallback
	// (Chinese, Japanese, Korean, Arabic, colour emoji). The generic names system-ui and
	// sans-serif stand for the platform's UI font (Segoe UI on Windows), monospace and
	// ui-monospace for its monospaced one (Consolas); names the platform does not have are
	// skipped. Empty means system-ui.
	Family string
	// Size is the font size in DIPs (CSS px). It must be above 0.
	Size DIP
	// Weight is the CSS font-weight from 1 to 1000 (400 regular, 600 semibold, 650 the
	// page title, 350 Cascadia Mono's semi-light): a variable font's 'wght' axis takes
	// exactly this value, and a family without one gives its nearest weight. Zero means
	// 400.
	Weight float32
	// Italic picks the family's italic (a family without one is slanted).
	Italic bool
	// OpticalSize is the value of a variable font's 'opsz' axis; fonts without the axis
	// ignore it. Zero leaves the face's own, which for a named instance such as Segoe UI
	// Variable Text is the size it was designed for. CSS's font-optical-sizing: auto, which
	// Chromium applies by default, is OpticalSize = Size (Font.AutoOptical). A backend sets
	// the axis whatever family name reached the font: on Windows that takes the font's
	// typographic family, since DirectWrite ignores 'opsz' set through a named instance's
	// WWS family (ui/win does it).
	OpticalSize float32
	// LetterSpacing is the space added after every character, in em (CSS letter-spacing:
	// 0.07em for the caps headings, -0.02em for the page title).
	LetterSpacing float32
	// LineHeight is the height of each line box as a multiple of Size (CSS line-height:
	// 1.4 for the body); 0 is CSS's normal, the font's ascent + descent + line gap. The
	// font's ascent and descent sit centred in the line box (CSS half-leading), so the
	// first baseline is (box - (ascent + descent)) / 2 + ascent below the top.
	LineHeight float32
	// Tabular asks for figures of one width, the OpenType 'tnum' feature (CSS
	// font-variant-numeric: tabular-nums, which app.css sets on the body).
	Tabular bool
}

// TextOptions is how text is laid out in its box. It is comparable, so it can key a cache,
// and it stays comparable.
type TextOptions struct {
	// MaxWidth is the box's width in DIPs. Zero means unbounded: the box is as wide as the
	// widest line, and nothing wraps or is cut.
	MaxWidth DIP
	// Wrap is where lines may break.
	Wrap TextWrap
	// MaxLines above 0 keeps at most that many lines; when text is left over, the last
	// line kept ends with an ellipsis (CSS -webkit-line-clamp).
	MaxLines int
	// Truncate is what happens to a line that is still wider than MaxWidth after wrapping,
	// which with TextWrapNone is any line that does not fit.
	Truncate Truncate
	// Align places each line in the box.
	Align TextAlign
	// Direction is the paragraph's base direction.
	Direction Direction
	// Antialias is how this text's glyphs are antialiased.
	Antialias Antialias
}

// TextWrap is where a layout may break its lines. Hard line breaks (\n, \r\n, \r, U+2028
// and U+2029) always end a line, and spaces are kept as given: the kit collapses white
// space where CSS would.
type TextWrap uint8

// The wrap modes.
const (
	// TextWrapNone keeps each paragraph on one line (CSS white-space: nowrap).
	TextWrapNone TextWrap = iota
	// TextWrapWords breaks lines where words allow (Unicode line breaking, UAX #14); a
	// word longer than the line overflows it (CSS white-space: normal).
	TextWrapWords
	// TextWrapAnywhere breaks as TextWrapWords, and also inside a word that does not fit
	// on a line of its own (CSS overflow-wrap: anywhere, which addresses and names use).
	TextWrapAnywhere
	// TextWrapCharacters breaks between any two characters (CSS word-break: break-all).
	TextWrapCharacters
)

// Truncate is what a layout does with a line that does not fit its box. The ellipsis is
// U+2026 in the line's font, and cuts fall between grapheme clusters.
type Truncate uint8

// The truncation modes.
const (
	// TruncateNone lets the line run out of the box (CSS text-overflow: clip; whoever
	// draws it clips it).
	TruncateNone Truncate = iota
	// TruncateEnd cuts the line's end off with an ellipsis (CSS text-overflow: ellipsis).
	TruncateEnd
	// TruncateMiddle keeps the start and the end of a line and puts an ellipsis in place
	// of its middle (addresses and paths). It is for single lines: with wrapping it acts
	// as TruncateEnd.
	TruncateMiddle
)

// TextAlign is where a layout puts each line in its box. A line that is wider than the
// box starts at its start edge whatever the alignment, as in CSS.
type TextAlign uint8

// The alignments.
const (
	// TextAlignStart puts lines at the start edge of the paragraph's direction: the left
	// for left-to-right text, the right for right-to-left text (CSS text-align: start).
	TextAlignStart TextAlign = iota
	TextAlignCenter
	// TextAlignEnd puts lines at the end edge (CSS text-align: end: sizes and speeds).
	TextAlignEnd
)

// Direction is a base direction: a paragraph's (TextOptions), and the kit's layout
// direction for the mirrored right-to-left layout. Within a paragraph the text is ordered
// by the Unicode bidirectional algorithm (UAX #9), isolates (U+2066 to U+2069) included,
// which the kit uses to keep a name inserted into a sentence apart from it.
type Direction uint8

// The directions.
const (
	// DirAuto, for a paragraph, takes the direction of its first strong character (UAX #9
	// rules P2 and P3), left to right when it has none: HTML's dir=auto, for names and
	// titles.
	DirAuto Direction = iota
	DirLTR
	DirRTL
)

// Antialias is how a layout's glyphs are antialiased when drawn.
type Antialias uint8

// The antialiasing modes.
const (
	// AntialiasSubpixel is ClearType where the system smooths text with it and the surface
	// allows it (an opaque target, outside opacity layers), and grayscale elsewhere.
	AntialiasSubpixel Antialias = iota
	// AntialiasGrayscale is always grayscale: Cascadia Mono addresses and paths, which the
	// WebView window draws in grayscale.
	AntialiasGrayscale
)

// TextMetrics are the measurements of a TextLayout, in DIPs.
type TextMetrics struct {
	// Width is the width of the widest line without its trailing spaces: with an
	// unbounded MaxWidth, the text's max-content width.
	Width DIP
	// Advance is the width of the widest line with its trailing spaces: where a run drawn
	// after a one-line layout on the same line starts.
	Advance DIP
	// Height is the height of all the lines kept.
	Height DIP
	// Baseline is the first line's baseline, from the top of the box.
	Baseline DIP
	// MinWidth is the text's min-content width: the widest piece its Wrap cannot break
	// (the longest word for TextWrapWords, the widest grapheme for TextWrapAnywhere and
	// TextWrapCharacters, the widest line for TextWrapNone).
	MinWidth DIP
	// Lines is the number of lines kept; empty text has one empty line.
	Lines int
	// Truncated reports that text was cut off: by an ellipsis, or by MaxLines. The kit
	// then gives the whole text to a tooltip.
	Truncated bool
	// Direction is the paragraph's resolved direction, DirLTR or DirRTL.
	Direction Direction
}

// TextHit is what lies under a point of a TextLayout.
type TextHit struct {
	// Pos is the caret position nearest the point: a byte offset into the text, at a
	// cluster boundary (see TextLayout).
	Pos int
	// Inside reports whether the point is on the text itself, not beside or below it.
	Inside bool
}

// FontMetrics are the measurements of a style's font, in DIPs: what an empty line, a text
// field's box or an underline drawn by the kit needs.
type FontMetrics struct {
	// Ascent, Descent and LineGap are the font's, at the style's size.
	Ascent, Descent, LineGap DIP
	// LineHeight is the height of a line box in the style (see TextStyle.LineHeight).
	LineHeight DIP
	// Baseline is the baseline's distance from the top of a line box.
	Baseline DIP
	// CapHeight and XHeight are the heights of capital and lowercase letters.
	CapHeight, XHeight DIP
	// UnderlineOffset is how far below the baseline the font puts the top of its
	// underline, and UnderlineThickness how thick it is.
	UnderlineOffset, UnderlineThickness DIP
}

// TextLayout is a laid-out paragraph: text in a style and options, broken into lines
// once, then measured and hit-tested without drawing and drawn by any canvas of its
// backend. Its box is MaxWidth wide (its Width when MaxWidth is 0) and Height tall; box
// coordinates have (0, 0) at the box's top-left. Text positions are byte offsets into the
// text it was made from, and the ones it returns fall on the text engine's cluster
// boundaries, which are at least grapheme-cluster boundaries (a ligature can make one
// cluster of several graphemes); invalid UTF-8 shows as U+FFFD. Its owner releases it.
type TextLayout interface {
	// Metrics are the layout's measurements.
	Metrics() TextMetrics
	// HitTest finds the text at p, in box coordinates. A point in the ellipsis of a
	// truncated layout hits the start of the text it stands for.
	HitTest(p Point) TextHit
	// Caret is where the caret at pos (moved back to a cluster boundary) goes, in box
	// coordinates: the top of the caret, at the top of its line, and the line's height; the
	// kit draws it as wide as it likes. In text an ellipsis stands for, it is the caret
	// before the ellipsis.
	Caret(pos int) (top Point, height DIP)
	// AppendRangeRects appends to dst the rectangles, in box coordinates, that cover the
	// text from start to end: one per line and direction run, as a selection's highlight.
	AppendRangeRects(dst []Rect, start, end int) []Rect
	// Release frees the layout. It is idempotent; a released layout measures zero and
	// draws nothing.
	Release()
}

// TextRelayouter is a TextLayout that can hold other text in place: a label whose text
// changes every tick (a speed, a percentage) lays the new text out in the layout it has
// rather than making a new one. A backend's layouts may implement it.
type TextRelayouter interface {
	// Relayout lays out text in style with opts, as the TextMeasurer that made the layout
	// would, in place of what it held. On an error the layout is released.
	Relayout(text string, style TextStyle, opts TextOptions) error
}

// TextMeasurer lays out text without a canvas: the layout pass measures with it, and the
// widgets draw what it makes. It is part of Resources and follows its rules.
type TextMeasurer interface {
	// LayoutText lays out text in style with opts. A style whose Size is not a finite
	// number above 0 fails with an error that wraps ErrInvalid.
	LayoutText(text string, style TextStyle, opts TextOptions) (TextLayout, error)
	// FontMetrics are the metrics of style's font, the first installed family of
	// style.Family, at style.Size. It fails as LayoutText does.
	FontMetrics(style TextStyle) (FontMetrics, error)
}
