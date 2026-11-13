package kit

// Style is how an element is laid out and how its box is drawn: the CSS properties the
// Svelte window uses, in CSS's terms, so that a component's CSS reads across one to one.
// Sizes are border-box sizes (app.css sets box-sizing: border-box everywhere). The zero
// value is CSS's initial values: a block box, auto sizes, no padding, border or margin,
// flex 0 1 auto, align-items normal (stretch) and justify-content normal (start).
type Style struct {
	// Display is how the element lays out its children.
	Display Display
	// Hidden is display: none: the element and everything in it take no room, draw
	// nothing, take no input or focus and are left out of the semantic tree.
	Hidden bool

	Width, Height       Length
	MinWidth, MinHeight Length // unset is auto: a flex item's automatic minimum (CSS §4.5)
	MaxWidth, MaxHeight Length // unset is none
	Margin              Edges
	Padding             Edges
	Border              Edges // border widths
	// Overflow is what happens to content outside the box.
	Overflow Overflow

	// Flex and AlignSelf are how the element sits in a flex container (and AlignSelf in
	// a block, see DisplayBlock).
	Flex      Flex
	AlignSelf Align

	// Axis, Reverse, Wrap, Justify, AlignItems and AlignContent are how a DisplayFlex
	// element lays out its children (flex-direction, flex-wrap, justify-content,
	// align-items, align-content).
	Axis         Axis
	Reverse      bool
	Wrap         bool
	Justify      Justify
	AlignItems   Align
	AlignContent Align
	// RowGap and ColumnGap are CSS's row-gap and column-gap (gap: row column). A block
	// stacks its children with RowGap between them, as a one-column CSS grid does.
	RowGap, ColumnGap Length

	// Background, BorderColor, Radius and Shadows draw the box (background-color,
	// border-color, border-radius, box-shadow in CSS order: the first is on top).
	Background  Color
	BorderColor Color
	Radius      Corners
	Shadows     []Shadow

	// Container makes the element a size container (CSS container-type: inline-size):
	// the Breakpoints of the elements inside it test its content width, not the window's,
	// and its own intrinsic width ignores its content, as size containment does.
	Container bool
	// Breakpoints restyle the element while their queries match: CSS media queries
	// against the window's width, or container queries against the nearest Container.
	Breakpoints []Breakpoint
}

// Display is how an element lays out its children.
type Display uint8

// The display types.
const (
	// DisplayBlock stacks the children from the top, each as wide as the content box
	// (less its margins) unless it has a width of its own, with RowGap between them: CSS
	// block flow without margin collapsing, or a one-column grid (app.css's .field). A
	// child with AlignSelf start, center or end is as wide as its content instead, at that
	// side (CSS justify-self).
	DisplayBlock Display = iota
	// DisplayFlex is CSS flexbox (Flexbox Level 1 §9).
	DisplayFlex
)

// Axis is a flex container's main axis (flex-direction).
type Axis uint8

// The axes.
const (
	AxisRow    Axis = iota // flex-direction: row (CSS's initial value)
	AxisColumn             // flex-direction: column
)

// Align is where items sit on the cross axis (align-items, align-self, align-content).
type Align uint8

// The alignments.
const (
	// AlignAuto is align-self: auto (the container's AlignItems) and, for align-items and
	// align-content, CSS's normal, which is stretch for flex layout.
	AlignAuto Align = iota
	AlignStart
	AlignEnd
	AlignCenter
	AlignBaseline // first-baseline alignment
	AlignStretch
	// The distributions are for AlignContent only (between flex lines).
	AlignSpaceBetween
	AlignSpaceAround
	AlignSpaceEvenly
)

// Justify is where items sit on a flex container's main axis (justify-content).
type Justify uint8

// The justifications.
const (
	JustifyStart Justify = iota // normal and flex-start
	JustifyEnd
	JustifyCenter
	JustifySpaceBetween
	JustifySpaceAround
	JustifySpaceEvenly
)

// Overflow is CSS's overflow for content that does not fit the box.
type Overflow uint8

// The overflow modes.
const (
	// OverflowVisible lets content run outside the box (CSS's initial value).
	OverflowVisible Overflow = iota
	// OverflowHidden clips the content to the padding box. As in CSS, a flex item that
	// clips has no content-based minimum size, so it can shrink below its content (the
	// names that end in an ellipsis rely on it).
	OverflowHidden
	// OverflowClipInline clips the content to the padding box's left and right sides only
	// and lets it run past the top and bottom (CSS overflow-x: clip with overflow-y:
	// visible). It is for cells whose text ends in an ellipsis: the text is cut only where
	// it must be, at the ends of its line, and the ascenders, descenders and marks that
	// reach past its line box (Kurdish and Arabic marks, y, g, p) are never cut. As in CSS,
	// clip makes no scroll container, so a flex item keeps its content-based minimum size;
	// give it MinWidth Px(0) to let it shrink below its content.
	OverflowClipInline
)

// Flex is CSS's flex shorthand: grow, shrink and basis. The zero value is CSS's initial
// 0 1 auto.
type Flex struct {
	Grow  float32
	less1 float32 // shrink less 1, so that the zero value shrinks by 1
	Basis Length
}

// FlexOf is flex: grow shrink basis.
func FlexOf(grow, shrink float32, basis Length) Flex {
	return Flex{Grow: grow, less1: shrink - 1, Basis: basis}
}

// Shrink is the flex-shrink factor.
func (f Flex) Shrink() float32 { return f.less1 + 1 }

// The flex shorthands app.css uses.
var (
	// FlexNone is flex: none (0 0 auto): the item keeps its size.
	FlexNone = FlexOf(0, 0, Length{})
	// Flex1 is flex: 1 (1 1 0%): the items share the room in proportion.
	Flex1 = FlexOf(1, 1, Pct(0))
	// FlexAuto is flex: 1 1 auto: the item grows and shrinks from its content size.
	FlexAuto = FlexOf(1, 1, Length{})
)

// Query is the width condition of a CSS media or container query: (min-width: MinWidth)
// and (max-width: MaxWidth), each inclusive and left out when not set, as CSS writes them
// (Toolbar.svelte's (max-width: 1179.98px) is Query{MaxWidth: Px(1179.98)}). Rem lengths
// follow the text size, as in CSS.
type Query struct {
	MinWidth, MaxWidth Length
}

// Matches reports whether a width matches q.
func (q Query) Matches(width DIP, s Scale) bool {
	if v, ok := q.MinWidth.Resolve(s, -1); ok && !(width >= v) {
		return false
	}
	if v, ok := q.MaxWidth.Resolve(s, -1); ok && !(width <= v) {
		return false
	}
	return true
}

// Breakpoint changes an element's style while When matches: Apply edits a copy of the
// element's Style, which the layout then uses. Breakpoints apply in order.
type Breakpoint struct {
	When  Query
	Apply func(s *Style)
}

// FocusMode is how an element takes the keyboard focus (HTML's tabindex).
type FocusMode uint8

// The focus modes.
const (
	// FocusNone: the element never takes the focus.
	FocusNone FocusMode = iota
	// FocusTab: Tab reaches the element, and a click or the code can focus it
	// (tabindex="0", buttons and fields).
	FocusTab
	// FocusClick: a click or the code can focus the element, Tab skips it
	// (tabindex="-1": a dialog that focuses itself, the grid's header buttons).
	FocusClick
)

// Exempt marks a region outside every modal that keeps the keyboard while a focus trap is
// on (focus-trap.ts's data-focus-trap-exempt).
type Exempt uint8

// The exemptions.
const (
	ExemptNone Exempt = iota
	// ExemptKeep: the focus may move into the region, Tab joins it to the trap's cycle,
	// and a trap that focuses itself leaves the focus there (the post-queue countdown).
	ExemptKeep
	// ExemptTab: Tab joins the region to the cycle, but a trap that focuses itself takes
	// the focus from it (data-focus-trap-exempt="tab": the window controls).
	ExemptTab
)

// Ring is how an element shows the keyboard focus: CSS :focus-visible's outline. The
// zero value is app.css's outline: 2px solid var(--focus) with outline-offset 2px.
type Ring struct {
	// None draws no ring (an element that shows its focus another way: a field's border).
	None bool
	// Offset and Width override outline-offset and outline-width when set (rows and cells
	// use an inset ring: Offset Px(-2)).
	Offset, Width Length
}

func (r Ring) metrics(s Scale) (offset, width DIP) {
	return r.Offset.or(s, -1, 2), r.Width.or(s, -1, 2)
}
