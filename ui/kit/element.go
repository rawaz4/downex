package kit

import "slices"

// Widget is anything in a window's tree: an Element, or a type that embeds one and adds
// behaviour through the optional interfaces below (Layouter, Painter, PointerHandler and
// the others), which the window finds with type assertions.
type Widget interface {
	// Base is the widget's element.
	Base() *Element
}

// Element is one box of a window's tree: its style, its place among its parent's
// children, its focus and pointer behaviour and its semantic node. Every widget embeds
// one. The zero value is an empty block box that is not exposed to assistive technology
// (RoleNone); add it to a window's tree with its parent's Add.
//
// The exported fields may be set before the element is added; afterwards, a change to one
// of them is followed by Relayout (sizes, display, text) or Invalidate (colours), and
// SemanticsChanged for Semantics, so that the window lays out, paints and tells assistive
// technology only what changed.
type Element struct {
	Style     Style
	Semantics Semantics

	// Focus is how the element takes the keyboard focus.
	Focus FocusMode
	// Disabled is HTML's disabled: not usable, not focusable, and NodeDisabled.
	Disabled bool
	// DisabledFocusable keeps a disabled element focusable (aria-disabled="true", which
	// the design uses so that the focus is not lost when a button becomes unavailable).
	DisabledFocusable bool
	// Inert is HTML's inert: the element and everything in it take no pointer input and no
	// focus and are left out of the semantic tree, but are still drawn.
	Inert bool
	// Autofocus marks the element a focus trap focuses first (data-autofocus).
	Autofocus bool
	// Exempt marks a region that keeps the keyboard while a focus trap is on.
	Exempt Exempt
	// Ring is how the element shows the keyboard focus.
	Ring Ring
	// Frame declares the element part of a custom window frame (see Root.FrameHit): the
	// title bar's ground is FrameCaption, its window buttons FrameMinimize, FrameMaximize
	// and FrameClose (they are buttons all the same: the host sends them the pointer).
	// FrameClient, the zero value, takes the nearest ancestor's part, except that an
	// interactive element (focusable, or one that handles the pointer) that declares no
	// part of its own is client.
	Frame FramePart
	// Tooltip is the element's tooltip text (HTML's title).
	Tooltip string
	// Cursor is the pointer's shape over the element; CursorDefault takes the nearest
	// ancestor's.
	Cursor Cursor
	// PassThrough lets the pointer through the element itself to what is under it, while
	// its children still take it (CSS pointer-events: none on a layer).
	PassThrough bool

	self   Widget
	parent *Element
	kids   []*Element
	win    *Window
	id     NodeID
	semID  NodeID // the node id the element stands for, when it is not its own (a list's rows)
	flags  elementFlags

	bounds    Rect   // the border box, relative to the parent's border box
	insets    Insets // the padding and border used by the last layout
	border    Insets // the border alone
	used      *Style // the style in effect: Style, or styled with its breakpoints applied
	styled    Style
	stylePass uint32 // the layout pass used was worked out in
	queried   DIP    // the query width the breakpoints were applied for
	cache     measureCache
	base      DIP // the first baseline from the border box's top
	hasBase   bool
}

type elementFlags uint16

const (
	needsLayout      elementFlags = 1 << iota // the element's own layout must run again
	childNeedsLayout                          // some descendant's layout must run again
	isBoundary                                // its size never depends on its content
	isParked                                  // a recycled list row out of use
	isHovered                                 // under the pointer (CSS :hover)
	everLaidOut
	inProgress // its layout runs now
)

// measureCache holds the intrinsic sizes of the last layout pass; Relayout clears it.
type measureCache struct {
	minW, maxW DIP
	widths     bool
	forW, h    DIP // the last height asked for, at border-box width forW
	height     bool
	cminW      DIP // the min-content width as if the width were auto
	cmin       bool
	cforW, ch  DIP // the content height suggestion at border-box width cforW
	cheight    bool
}

// Base is e itself.
func (e *Element) Base() *Element { return e }

// Widget is the widget e belongs to: the value that was added to the tree (e when it was
// an Element).
func (e *Element) Widget() Widget {
	if e.self != nil {
		return e.self
	}
	return e
}

// Parent is e's parent, or nil.
func (e *Element) Parent() *Element { return e.parent }

// Window is the window e is in, or nil while it is not in one.
func (e *Element) Window() *Window { return e.win }

// Children are e's children in order. The slice belongs to e: do not change it.
func (e *Element) Children() []*Element { return e.kids }

// Bounds is e's border box from its last layout, relative to its parent's border box.
func (e *Element) Bounds() Rect { return e.bounds }

// Size is e's border-box size from its last layout.
func (e *Element) Size() Size { return e.bounds.Size() }

// ContentRect is e's content box from its last layout, in e's own coordinates.
func (e *Element) ContentRect() Rect {
	return RectAt(Point{}, e.bounds.Size()).Inset(e.insets)
}

// Add appends children to e. A widget already in a tree is moved.
func (e *Element) Add(ws ...Widget) {
	for _, w := range ws {
		e.Insert(len(e.kids), w)
	}
}

// Insert puts w among e's children at index i (clamped to the children).
func (e *Element) Insert(i int, w Widget) {
	c := w.Base()
	if c == nil || c == e || c.contains(e) {
		return
	}
	if c.parent != nil {
		c.parent.remove(c)
	}
	c.self = w
	c.parent = e
	i = min(max(i, 0), len(e.kids))
	e.kids = slices.Insert(e.kids, i, c)
	if e.win != nil {
		e.win.attach(c)
	}
	e.childrenChanged()
}

// Remove takes w out of e's children and reports whether it was there.
func (e *Element) Remove(w Widget) bool {
	c := w.Base()
	if c == nil || c.parent != e {
		return false
	}
	e.remove(c)
	return true
}

// RemoveAll takes every child out of e.
func (e *Element) RemoveAll() {
	for len(e.kids) > 0 {
		e.remove(e.kids[len(e.kids)-1])
	}
}

// SetChildren replaces e's children with ws.
func (e *Element) SetChildren(ws ...Widget) {
	keep := make(map[*Element]bool, len(ws))
	for _, w := range ws {
		keep[w.Base()] = true
	}
	for i := len(e.kids) - 1; i >= 0; i-- {
		if c := e.kids[i]; !keep[c] {
			e.remove(c)
		}
	}
	for i, w := range ws {
		c := w.Base()
		if i < len(e.kids) && e.kids[i] == c {
			continue
		}
		e.Insert(i, w)
	}
}

func (e *Element) remove(c *Element) {
	i := slices.Index(e.kids, c)
	if i < 0 {
		return
	}
	if w := e.win; w != nil {
		w.invalidateElement(c)
		w.detach(c)
	}
	e.kids = slices.Delete(e.kids, i, i+1)
	c.parent = nil
	e.childrenChanged()
}

func (e *Element) contains(d *Element) bool {
	for p := d; p != nil; p = p.parent {
		if p == e {
			return true
		}
	}
	return false
}

// Contains reports whether d is e or inside e.
func (e *Element) Contains(d *Element) bool { return d != nil && e.contains(d) }

func (e *Element) childrenChanged() {
	e.Relayout()
	if e.win != nil {
		e.win.semChildren(e)
	}
}

// SetStyle replaces e's style and lays it out again.
func (e *Element) SetStyle(s Style) {
	e.Style = s
	e.Relayout()
	e.Invalidate()
}

// SetHidden shows or hides e (CSS display: none).
func (e *Element) SetHidden(hidden bool) {
	if e.Style.Hidden == hidden {
		return
	}
	if hidden {
		e.Invalidate()
	}
	e.Style.Hidden = hidden
	e.Relayout()
	if hidden {
		if w := e.win; w != nil && w.focused != nil && e.contains(w.focused) {
			w.focusLost()
		}
	} else {
		e.Invalidate()
	}
	if e.parent != nil && e.win != nil {
		e.win.semChildren(e.parent)
	}
}

// Relayout asks for e to be laid out again in the next frame, with whatever around it
// depends on its size. Call it after changing what e's size depends on.
func (e *Element) Relayout() { e.relayout(false) }

// RelayoutContent asks for e's content to be laid out again in the next frame while its
// own size stays: for a widget whose size never depends on its content (a list that
// scrolls, a list's row bound to new data), so that nothing around it is laid out again.
func (e *Element) RelayoutContent() { e.relayout(true) }

func (e *Element) relayout(contentOnly bool) {
	b := e
	b.flags |= needsLayout
	b.cache = measureCache{}
	// An element whose layout is running now lays out what is marked inside it before
	// it finishes (a Layouter changes its children before it places them), so marks stop
	// there; otherwise they would outlive the pass.
	if !(contentOnly && b.flags&isBoundary != 0) {
		// e's size may change: its parent lays out again, and so on up to the nearest
		// element whose size never depends on its content.
		for b.parent != nil && b.parent.flags&inProgress == 0 {
			b = b.parent
			b.flags |= needsLayout
			b.cache = measureCache{}
			if b.flags&isBoundary != 0 {
				break
			}
		}
	}
	for p := b.parent; p != nil && p.flags&inProgress == 0; p = p.parent {
		p.flags |= childNeedsLayout
	}
	if e.win != nil {
		e.win.layoutWanted()
	}
}

// Invalidate asks for e to be painted again in the next frame.
func (e *Element) Invalidate() {
	if e.win != nil {
		e.win.invalidateElement(e)
	}
}

// SemanticsChanged tells assistive technology that e's semantic node changed (its name,
// value, states or range).
func (e *Element) SemanticsChanged() {
	if e.win != nil {
		e.win.semNode(e)
	}
}

// NodeID is e's semantic node id while it is in a window (0 before).
func (e *Element) NodeID() NodeID {
	if e.semID != 0 {
		return e.semID
	}
	return e.id
}

// Focused reports whether e has the keyboard focus.
func (e *Element) Focused() bool { return e.win != nil && e.win.focused == e }

// Hovered reports whether the pointer is over e or one of its children (CSS :hover).
func (e *Element) Hovered() bool { return e.flags&isHovered != 0 }

// WindowBounds is e's border box in window coordinates.
func (e *Element) WindowBounds() Rect {
	r := e.bounds
	for p := e.parent; p != nil; p = p.parent {
		r = r.Add(p.bounds.Min)
	}
	return r
}

// WindowToLocal maps a window point into e's coordinates.
func (e *Element) WindowToLocal(p Point) Point {
	return p.Sub(e.WindowBounds().Min)
}

// visible reports whether e is laid out and shown: neither it nor an ancestor is hidden
// or a parked list row, and it is in a window.
func (e *Element) visible() bool {
	if e.win == nil {
		return false
	}
	for p := e; p != nil; p = p.parent {
		if p.Style.Hidden || p.flags&isParked != 0 || (p.used != nil && p.used.Hidden) {
			return false
		}
	}
	return true
}

// inert reports whether e or an ancestor is inert.
func (e *Element) inert() bool {
	for p := e; p != nil; p = p.parent {
		if p.Inert {
			return true
		}
	}
	return false
}

// hidden is the style in effect's Hidden (breakpoints included).
func (e *Element) hidden() bool {
	if e.used != nil {
		return e.used.Hidden
	}
	return e.Style.Hidden
}

// visibleRect is e's border box in window coordinates, cut by every ancestor that clips
// (its overflow is hidden, or it is a list's viewport), and whether any of it is left.
func (e *Element) visibleRect() (Rect, bool) {
	r := e.WindowBounds()
	for p := e.parent; p != nil; p = p.parent {
		if p.clips() {
			r = r.Intersect(p.clipRect())
			if r.Empty() {
				return Rect{}, false
			}
		}
	}
	return r, !r.Empty()
}

// clips reports whether e clips its children.
func (e *Element) clips() bool {
	if e.used != nil && e.used.Overflow != OverflowVisible {
		return true
	}
	_, ok := e.self.(clipper)
	return ok
}

// clipUnbounded is how far an inline-only clip (OverflowClipInline) reaches above and below
// its box: past any window.
const clipUnbounded DIP = 1 << 20

// clipRect is the area e clips its children to, in window coordinates.
func (e *Element) clipRect() Rect {
	if c, ok := e.self.(clipper); ok {
		return c.childClip().Add(e.WindowBounds().Min)
	}
	// overflow clips to the padding box: inside the border
	r := e.WindowBounds().Inset(e.border)
	if e.used != nil && e.used.Overflow == OverflowClipInline {
		r.Min.Y, r.Max.Y = r.Min.Y-clipUnbounded, r.Max.Y+clipUnbounded
	}
	return r
}

// clipper is a widget that clips its children to an area of its own (a list's body).
type clipper interface {
	childClip() Rect
}

// focusable reports whether e can take the focus now (by any means).
func (e *Element) focusable() bool {
	if e.Focus == FocusNone || (e.Disabled && !e.DisabledFocusable) {
		return false
	}
	return e.visible() && !e.inert()
}

// tabbable reports whether Tab reaches e.
func (e *Element) tabbable() bool { return e.Focus == FocusTab && e.focusable() }

// enabled reports whether e and its ancestors are not disabled.
func (e *Element) enabled() bool {
	for p := e; p != nil; p = p.parent {
		if p.Disabled {
			return false
		}
	}
	return true
}

// interactive reports whether e takes input itself (for FrameHit).
func (e *Element) interactive() bool {
	if e.Focus != FocusNone {
		return true
	}
	_, ok := e.self.(PointerHandler)
	return ok
}

// The optional behaviours a widget adds to its element, found by type assertion.

// Painter draws a widget's content, after its box's background and border and before its
// children.
type Painter interface {
	Paint(p *PaintContext)
}

// OverPainter draws over a widget's children (a scroll bar, a composite widget's marks).
type OverPainter interface {
	PaintOver(p *PaintContext)
}

// PointerHandler takes pointer events. The window sends each event to the deepest element
// under the pointer (or the one that captured it) and then to its ancestors until a
// handler reports that it used the event.
type PointerHandler interface {
	HandlePointer(in *PointerInput) bool
}

// PointerInput is a PointerEvent on its way through the tree.
type PointerInput struct {
	PointerEvent
	// Local is the pointer's position in the handling element's coordinates.
	Local Point
	// Target is the deepest element under the pointer, or the one with the capture.
	Target *Element
}

// WheelHandler takes wheel events, sent as pointer events are.
type WheelHandler interface {
	HandleWheel(in *WheelInput) bool
}

// WheelInput is a WheelEvent on its way through the tree.
type WheelInput struct {
	WheelEvent
	Local  Point
	Target *Element
}

// KeyHandler takes key events: the focused element's first, then its ancestors' until
// one reports that it used the event.
type KeyHandler interface {
	HandleKey(e KeyEvent) bool
}

// TextHandler takes typed text, sent as key events are.
type TextHandler interface {
	HandleText(e TextEvent) bool
}

// FocusHandler hears that its element got or lost the keyboard focus.
type FocusHandler interface {
	FocusChanged(focused bool)
}

// HoverHandler hears that the pointer came over its element or left it (CSS :hover,
// which holds while the pointer is over a child too).
type HoverHandler interface {
	HoverChanged(hovered bool)
}

// Attacher hears that its element was put into a window.
type Attacher interface {
	Attached()
}

// Detacher hears that its element was taken out of its window: it releases the text
// layouts, paths and images it holds.
type Detacher interface {
	Detached()
}

// ActionHandler does what assistive technology asks of a node beyond moving the focus and
// scrolling it into view (see SemanticTree.Perform): Invoke, Toggle, Expand, SetValue...
type ActionHandler interface {
	PerformAction(r ActionRequest) error
}

// SemanticFiller adds what only the widget knows to its node: a value, a toggle state, a
// selection.
type SemanticFiller interface {
	FillSemantics(n *SemanticNode)
}

// TooltipSource gives an element's tooltip when it is not a fixed Tooltip: a label's whole
// text while an ellipsis cuts it.
type TooltipSource interface {
	TooltipText() string
}
