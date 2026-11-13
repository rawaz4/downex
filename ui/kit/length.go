package kit

// A Length is a CSS length in a Style: DIPs (CSS px), rem (which follow the Windows text
// size, see Scale), or a percentage of the containing block. Its zero value is "not set",
// which every property reads as its CSS initial value: auto for sizes and flex bases,
// none for maximums, 0 for padding, borders, margins and gaps. Auto is CSS's auto where a
// property takes it (margins, sizes).
type Length struct {
	v    float32
	unit lengthUnit
}

type lengthUnit uint8

const (
	unitUnset lengthUnit = iota
	unitPx
	unitRem
	unitPct
	unitAuto
)

// Px is v DIPs, CSS's v px.
func Px(v float32) Length { return Length{v, unitPx} }

// Rem is v rem: v × 16 DIPs at the default text size, more with a larger Windows text size.
func Rem(v float32) Length { return Length{v, unitRem} }

// Pct is v percent of the containing block (its width for widths, margins and padding,
// its height for heights).
func Pct(v float32) Length { return Length{v, unitPct} }

// Auto is CSS's auto.
var Auto = Length{unit: unitAuto}

// IsSet reports whether l was given (Auto counts as given).
func (l Length) IsSet() bool { return l.unit != unitUnset }

// IsAuto reports whether l is auto, given or not.
func (l Length) IsAuto() bool { return l.unit == unitUnset || l.unit == unitAuto }

// isFixed reports whether l resolves without a containing block: px or rem.
func (l Length) isFixed() bool { return l.unit == unitPx || l.unit == unitRem }

// Resolve is l in DIPs with base the containing block's size (negative when it is not
// definite), or false when l is auto, not set, or a percentage of an indefinite size.
func (l Length) Resolve(s Scale, base DIP) (DIP, bool) {
	switch l.unit {
	case unitPx:
		return l.v, true
	case unitRem:
		return s.Rem(l.v), true
	case unitPct:
		if base < 0 {
			return 0, false
		}
		return base * l.v / 100, true
	}
	return 0, false
}

// or is l in DIPs, or d when l does not resolve.
func (l Length) or(s Scale, base, d DIP) DIP {
	if v, ok := l.Resolve(s, base); ok {
		return v
	}
	return d
}

// Edges are lengths on the four sides of a box, with CSS's logical sides: Start and End
// are inline-start and inline-end (left and right in a left-to-right layout, mirrored in a
// right-to-left one), Top and Bottom the block sides.
type Edges struct {
	Top, End, Bottom, Start Length
}

// EdgesAll is l on every side (CSS padding: l).
func EdgesAll(l Length) Edges { return Edges{l, l, l, l} }

// EdgesOf is block on the top and bottom and inline on the start and end (CSS padding:
// block inline).
func EdgesOf(block, inline Length) Edges { return Edges{block, inline, block, inline} }

// EdgesInline is l on the start and end only (CSS padding-inline: l).
func EdgesInline(l Length) Edges { return Edges{End: l, Start: l} }

// EdgesBlock is l on the top and bottom only (CSS padding-block: l).
func EdgesBlock(l Length) Edges { return Edges{Top: l, Bottom: l} }

// resolve is e in DIPs on the physical sides, percentages of base (the containing block's
// width, as CSS resolves padding and margins on every side). Auto and unset are 0.
func (e Edges) resolve(s Scale, base DIP, rtl bool) Insets {
	in := Insets{
		Top:    e.Top.or(s, base, 0),
		Bottom: e.Bottom.or(s, base, 0),
		Left:   e.Start.or(s, base, 0),
		Right:  e.End.or(s, base, 0),
	}
	if rtl {
		in.Left, in.Right = in.Right, in.Left
	}
	return in
}

// autoSides reports which physical sides of e are auto (a margin that takes free space).
func (e Edges) autoSides(rtl bool) (top, right, bottom, left bool) {
	top, bottom = e.Top.unit == unitAuto, e.Bottom.unit == unitAuto
	left, right = e.Start.unit == unitAuto, e.End.unit == unitAuto
	if rtl {
		left, right = right, left
	}
	return
}

// Corners are the radii of a box's corners with CSS's logical corners: TopStart is
// border-start-start-radius (the top left in a left-to-right layout), TopEnd
// border-start-end-radius, BottomEnd border-end-end-radius and BottomStart
// border-end-start-radius.
type Corners struct {
	TopStart, TopEnd, BottomEnd, BottomStart Length
}

// CornersAll is l at every corner (CSS border-radius: l).
func CornersAll(l Length) Corners { return Corners{l, l, l, l} }

// resolve is c in DIPs on the physical corners; percentages are of base (CSS resolves them
// per axis; the kit takes the box's width, which the radii used here never depend on).
func (c Corners) resolve(s Scale, base DIP, rtl bool) Radii {
	r := Radii{
		TopLeft:     c.TopStart.or(s, base, 0),
		TopRight:    c.TopEnd.or(s, base, 0),
		BottomRight: c.BottomEnd.or(s, base, 0),
		BottomLeft:  c.BottomStart.or(s, base, 0),
	}
	if rtl {
		r = Radii{TopLeft: r.TopRight, TopRight: r.TopLeft, BottomRight: r.BottomLeft, BottomLeft: r.BottomRight}
	}
	return r
}
