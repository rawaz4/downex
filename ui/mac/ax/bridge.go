package ax

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"downex/ui/kit"
)

// Ref is one accessibility object of the system side: an element the System made for a
// node, a native field's view, or the window's content view (the container). 0 is none.
type Ref uintptr

// Host is the window a Bridge serves. Its methods are called on the UI thread.
type Host interface {
	// Semantics is the window's semantic tree now, or nil while its root keeps none or the
	// window is closed.
	Semantics() kit.SemanticTree
	// Perform does r as a call into the window's root (the kit's never-nest rule): now, or,
	// while another call into the root is in progress (a modal loop the root entered),
	// after it has returned, reporting nil.
	Perform(r kit.ActionRequest) error
	// Post does r as a call into the root once the current call has returned: a press must
	// not keep its caller waiting (a button may open a dialog).
	Post(r kit.ActionRequest) error
	// HostedField is the native text field (its view) shown in node n's box, or 0: the
	// field stands in for the node (it appears once, at the node, with the node's label).
	HostedField(n *kit.SemanticNode) Ref
}

// Notification is an NSAccessibility notification the bridge posts.
type Notification uint8

// The notifications.
const (
	FocusChanged            Notification = iota + 1 // NSAccessibilityFocusedUIElementChangedNotification
	ValueChanged                                    // NSAccessibilityValueChangedNotification
	TitleChanged                                    // NSAccessibilityTitleChangedNotification
	SelectedChildrenChanged                         // NSAccessibilitySelectedChildrenChangedNotification
	SelectedRowsChanged                             // NSAccessibilitySelectedRowsChangedNotification
	RowCountChanged                                 // NSAccessibilityRowCountChangedNotification
	LayoutChanged                                   // NSAccessibilityLayoutChangedNotification
	ExpandedChanged                                 // WebKit's AXExpandedChanged
)

// System is the Objective-C side as the bridge sees it. Its methods run on the UI thread.
type System interface {
	// Container is the window's content view: the root node's stand-in, not an element of
	// its own (its children show under the system's AXWindow).
	Container() Ref
	// NewElement makes the element of node id (retained by the bridge until Destroy).
	NewElement(id kit.NodeID) Ref
	// Destroy posts NSAccessibilityUIElementDestroyedNotification for r, marks it dead
	// (every later request of it answers nothing) and drops the bridge's reference.
	Destroy(r Ref)
	// Post posts n about r.
	Post(r Ref, n Notification)
	// Announce has VoiceOver read text (NSAccessibilityAnnouncementRequestedNotification).
	Announce(text string, p Priority)
	// DecorateField gives the native field the node's accessibility: its parent element, its
	// label (the node's name), help (description), identifier and required state.
	DecorateField(field, parent Ref, n *kit.SemanticNode)
	// Later runs f on the UI thread once the current work there is done. It reports false
	// when f will never run (the application is gone).
	Later(f func()) bool
}

// MaxElements is when a bridge first looks for elements of nodes that are gone: past it
// (and each time the elements have doubled since) it drops theirs. An element of a node
// that lives is never dropped, so that a client never meets a new object standing in for
// a node it already holds (VoiceOver would lose its place).
const MaxElements = 4096

// maxOnDemand bounds the elements of nodes that are not on screen (rows and cells a client
// reaches through the table API, the selection): past it such a node gets no element (the
// client hears nothing there) until nodes are gone. Nodes on screen always get one; there
// are only as many of them as the window lays out. A variable for tests.
var maxOnDemand = 16 * MaxElements

// maxDepth bounds the walks up and down the tree.
const maxDepth = 64

// Bridge is the accessibility of one window (or popup): the elements of its nodes, made
// when a client asks for them, and the notifications of its changes. It belongs to the UI
// thread.
type Bridge struct {
	host   Host
	sys    System
	closed bool

	elems map[kit.NodeID]*elem

	// Live regions whose content changed, announced together once the flush is over, and
	// the text each one last announced (a region reads out a change once).
	liveDue    []kit.NodeID
	liveWanted bool
	liveSaid   map[kit.NodeID]string

	sweepWanted bool
	nextSweep   int // the element count at which element() sweeps

	// layoutWanted: a LayoutChanged is due once the current work is done.
	layoutWanted bool

	kids []kit.NodeID

	// Stats count what the bridge did, for tests and diagnostics.
	Stats Stats
}

// Stats count what a bridge did.
type Stats struct {
	// Refused counts nodes off screen left without an element past maxOnDemand.
	Created, Destroyed, Posted, Announced, Refused int
}

type elem struct {
	ref  Ref
	last kit.SemanticNode
	seen bool
}

// New makes the bridge of a window. Nothing is read from the tree until a client asks.
func New(h Host, s System) *Bridge {
	return &Bridge{host: h, sys: s, elems: map[kit.NodeID]*elem{}, liveSaid: map[kit.NodeID]string{},
		nextSweep: MaxElements}
}

// Closed reports a closed bridge.
func (b *Bridge) Closed() bool { return b.closed }

// Len is the number of elements alive.
func (b *Bridge) Len() int { return len(b.elems) }

func (b *Bridge) tree() kit.SemanticTree {
	if b.closed {
		return nil
	}
	return b.host.Semantics()
}

// Close destroys every element; the bridge makes none again.
func (b *Bridge) Close() {
	if b.closed {
		return
	}
	b.closed = true
	for id, e := range b.elems {
		delete(b.elems, id)
		b.sys.Destroy(e.ref)
		b.Stats.Destroyed++
	}
	b.liveDue, b.liveSaid = nil, nil
	b.liveWanted, b.sweepWanted, b.layoutWanted = false, false, false
}

// later runs f through the System once the current work is done; it reports false when f
// will never run.
func (b *Bridge) later(f func()) bool { return b.sys.Later(f) }

// ---- elements -----------------------------------------------------------------------

// Object is the accessibility object of node id: the container for the root, the native
// field of a hosted text field, else the node's element (made now when none is). It is 0
// for a node that is gone.
func (b *Bridge) Object(id kit.NodeID) Ref {
	st := b.tree()
	if st == nil || id == 0 {
		return 0
	}
	if id == st.Root() {
		return b.sys.Container()
	}
	n, ok := st.Node(id)
	if !ok {
		b.evict(id)
		return 0
	}
	if f := b.field(st, &n); f != 0 {
		return f
	}
	return b.element(id, &n)
}

// field is the native field standing in for n (decorated), or 0.
func (b *Bridge) field(st kit.SemanticTree, n *kit.SemanticNode) Ref {
	if n.Role != kit.RoleTextField {
		return 0
	}
	f := b.host.HostedField(n)
	if f == 0 {
		return 0
	}
	parent := b.sys.Container()
	if n.Parent != 0 && n.Parent != st.Root() {
		if p, ok := st.Node(n.Parent); ok {
			parent = b.element(n.Parent, &p)
		}
	}
	b.sys.DecorateField(f, parent, n)
	return f
}

// element is the element of node id (n as read now), made when none is. A living node
// keeps its element for as long as it lives (see MaxElements); past maxOnDemand a node
// off screen that is not focused gets none (0).
func (b *Bridge) element(id kit.NodeID, n *kit.SemanticNode) Ref {
	if e := b.elems[id]; e != nil {
		return e.ref
	}
	if len(b.elems) >= b.nextSweep {
		b.Sweep()
		b.nextSweep = max(MaxElements, 2*len(b.elems))
	}
	if len(b.elems) >= maxOnDemand && n != nil && n.States&kit.NodeOffscreen != 0 && n.States&kit.NodeFocused == 0 {
		if st := b.tree(); st == nil || st.Focused() != id {
			b.Stats.Refused++
			return 0
		}
	}
	r := b.sys.NewElement(id)
	if r == 0 {
		return 0
	}
	b.Stats.Created++
	e := &elem{ref: r}
	if n != nil {
		e.last, e.seen = *n, true
	}
	b.elems[id] = e
	return r
}

// Known reports whether node id has an element now.
func (b *Bridge) Known(id kit.NodeID) bool { return b.elems[id] != nil }

func (b *Bridge) evict(id kit.NodeID) {
	if e := b.elems[id]; e != nil {
		delete(b.elems, id)
		delete(b.liveSaid, id)
		b.sys.Destroy(e.ref)
		b.Stats.Destroyed++
	}
}

// Sweep destroys the elements of nodes that are gone.
func (b *Bridge) Sweep() {
	b.sweepWanted = false
	st := b.tree()
	if st == nil {
		return
	}
	for id := range b.elems {
		if _, ok := st.Node(id); !ok {
			b.evict(id)
		}
	}
}

// ---- reading --------------------------------------------------------------------------

// Value kinds of Info.Value.
const (
	ValueNone   = iota
	ValueText   // Info.Text
	ValueNumber // Info.Number
)

// Actions an element offers (NSAccessibility's).
const (
	ActPress = 1 << iota
	ActIncrement
	ActDecrement
	ActShowMenu
	ActScrollToVisible
)

// Settable attributes of an element.
const (
	SetValue = 1 << iota
	SetFocused
	SetSelected
	SetExpanded
	SetSelectedRows // a table's or list's selection
)

// Info is what NSAccessibility shows of a node at one moment.
type Info struct {
	Role, Subrole, RoleDescription string
	// Title is AXTitle and Label AXDescription. WebKit gives a named control or group its
	// name as both (aria-label) or as the title alone (named by its content); the kit does
	// not say which, so a named node has both (VoiceOver reads a description equal to the
	// title once). A listbox option has the title alone, text its value alone, and a live
	// region without a name neither (its text is in its children).
	Title                   string
	Label, Help, Identifier string
	// Value is the kind of the node's value: Text or Number (a toggle's 0/1/2, a range's
	// number, a heading's level).
	Value            int
	Text             string
	Number           float64
	ValueDescription string
	// HasRange: Min and Max are given (a progress bar, slider, spin button).
	HasRange bool
	Min, Max float64

	Enabled, Focused, Selected, Expandable, Expanded, Required, Protected, Invalid bool
	Busy, Modal, Multiselect                                                       bool
	// Control: WebKit gives the node AXInvalid ("true" or "false"); other nodes have it
	// only while Invalid.
	Control bool
	// Orientation: 0 none, 1 horizontal, 2 vertical. Sort: 0 none, 1 ascending, 2 descending.
	Orientation, Sort int

	// Table is a grid or table: Rows and Cols count every data row (header row included).
	Table      bool
	Rows, Cols int
	// Row is a row: Index is its row. Cell is a cell or header: its row and column ranges.
	Row, Cell      bool
	Index          int
	RowLoc, RowLen int
	ColLoc, ColLen int
	// PosInSet and SetSize are WebKit's AXARIAPosInSet/AXARIASetSize (0: none); Shortcut
	// its AXKeyShortcutsValue ("": none).
	PosInSet, SetSize, Level int
	Shortcut                 string
	// Checkable is a menu item with a checked state (menuitemcheckbox, menuitemradio):
	// WebKit gives it AXMenuItemMarkChar, Mark ("✓" when checked, else none), and AXValue
	// 1 or 0.
	Checkable bool
	Mark      string

	Bounds   kit.Rect
	Actions  int
	Settable int
}

// Info is node id's information now, false when it is gone (its element is destroyed).
func (b *Bridge) Info(id kit.NodeID) (Info, bool) {
	st := b.tree()
	if st == nil {
		return Info{}, false
	}
	n, ok := st.Node(id)
	if !ok {
		b.evict(id)
		return Info{}, false
	}
	if e := b.elems[id]; e != nil {
		e.last, e.seen = n, true
	}
	return b.info(st, id, &n), true
}

func (b *Bridge) info(st kit.SemanticTree, id kit.NodeID, n *kit.SemanticNode) Info {
	var in Info
	in.Role, in.Subrole = RoleOf(n)
	if in.Role == RoleList && in.Subrole == SubContentList && descriptionList(st, id) {
		in.Subrole = SubDescList
	}
	in.RoleDescription = RoleDescription(in.Role, in.Subrole)
	// A live region without a name has neither: WebKit leaves it unnamed, its text in its
	// children (the announcement carries the text, FlushLive).
	in.Title, in.Label = n.Name, n.Name
	in.Help = n.Description
	in.Identifier = n.AutomationID
	in.Enabled = n.States&kit.NodeDisabled == 0
	in.Focused = n.States&kit.NodeFocused != 0 || (id != 0 && st.Focused() == id)
	in.Selected = selected(n)
	in.Expandable = n.Expand != kit.ExpandNone
	in.Expanded = n.Expand == kit.ExpandOpen
	in.Required = n.States&kit.NodeRequired != 0
	in.Protected = n.States&kit.NodeProtected != 0
	in.Invalid = n.States&kit.NodeInvalid != 0
	in.Busy = n.States&kit.NodeBusy != 0
	in.Modal = n.States&kit.NodeModal != 0
	in.Multiselect = n.States&kit.NodeMultiSelectable != 0
	in.Control = isControl(n.Role)
	in.Orientation = int(n.Orientation)
	in.Sort = int(n.Sort)
	in.PosInSet, in.SetSize, in.Level, in.Shortcut = n.PosInSet, n.SetSize, n.Level, n.Shortcut
	in.Bounds = n.Bounds
	if n.Bounds.Empty() {
		in.Bounds = kit.Rect{}
	}

	// The value, as WebKit gives it for the same role.
	switch {
	case n.Role == kit.RoleOption:
		// WebKit's option: its name is its title, its value empty.
		in.Value, in.Text, in.Label = ValueText, "", ""
	case in.Role == RoleStaticText:
		// Text's text is its value; WebKit leaves its title and label empty.
		in.Value, in.Text, in.Title, in.Label = ValueText, n.Name, "", ""
	case n.Role == kit.RoleHeading:
		if n.Level > 0 {
			in.Value, in.Number = ValueNumber, float64(n.Level)
		}
	case n.Range.Valid:
		in.Value, in.Number = ValueNumber, n.Range.Value
		in.HasRange, in.Min, in.Max = true, n.Range.Min, n.Range.Max
		in.ValueDescription = n.Value
	case n.Role == kit.RoleTab:
		in.Value = ValueNumber
		if in.Selected {
			in.Number = 1
		}
	case n.Role == kit.RoleRadioButton:
		in.Value = ValueNumber
		if in.Selected {
			in.Number = 1
		}
	case isMenuItem(n.Role):
		// WebKit: a checkable item's AXValue is 1 or 0 and its AXMenuItemMarkChar "✓"
		// while checked; a plain item has neither.
		if n.Toggle != kit.ToggleNone || n.Role != kit.RoleMenuItem {
			in.Checkable, in.Value = true, ValueNumber
			if n.Toggle == kit.ToggleOn {
				in.Number, in.Mark = 1, "✓"
			}
		}
	case n.Toggle != kit.ToggleNone:
		in.Value = ValueNumber
		switch n.Toggle {
		case kit.ToggleOn:
			in.Number = 1
		case kit.ToggleMixed:
			in.Number = 2
		}
	case n.Role == kit.RoleTextField || n.Role == kit.RoleComboBox:
		in.Value, in.Text = ValueText, n.Value
		if in.Protected {
			in.Text = ""
		}
	case n.Value != "":
		in.Value, in.Text = ValueText, n.Value
	}

	switch {
	case isGrid(n.Role):
		in.Table, in.Rows, in.Cols = true, n.Grid.Rows, n.Grid.Cols
	case n.Role == kit.RoleRow:
		in.Row, in.Index = true, n.Cell.Row
	case isCell(n.Role):
		in.Cell = true
		in.RowLoc, in.RowLen = n.Cell.Row, max(n.Cell.RowSpan, 1)
		in.ColLoc, in.ColLen = n.Cell.Col, max(n.Cell.ColSpan, 1)
	}

	in.Actions = actionsOf(n)
	in.Settable = settableOf(n)
	return in
}

// actionsOf is what an element offers.
func actionsOf(n *kit.SemanticNode) int {
	a := 0
	if pressAction(n) != 0 {
		a |= ActPress
	}
	if !rangeReadOnly(n) && n.Range.Step > 0 {
		a |= ActIncrement | ActDecrement
	}
	if n.States&kit.NodeHasPopup != 0 && n.Actions&kit.ActionExpand != 0 {
		a |= ActShowMenu
	}
	if n.Actions&kit.ActionScrollIntoView != 0 {
		a |= ActScrollToVisible
	}
	return a
}

func settableOf(n *kit.SemanticNode) int {
	s := 0
	if (n.Role == kit.RoleTextField || n.Role == kit.RoleComboBox || n.Actions&kit.ActionSetValue != 0) && !valueReadOnly(n) {
		s |= SetValue
	}
	if !rangeReadOnly(n) {
		s |= SetValue
	}
	if n.Actions&kit.ActionFocus != 0 {
		s |= SetFocused
	}
	if n.Actions&(kit.ActionSelect|kit.ActionAddToSelection|kit.ActionRemoveFromSelection) != 0 {
		s |= SetSelected
	}
	if n.Expand != kit.ExpandNone && n.Actions&(kit.ActionExpand|kit.ActionCollapse) != 0 {
		s |= SetExpanded
	}
	if isSelectionContainer(n.Role) && !n.Fieldset { // a fieldset's radios are no selection of it
		s |= SetSelectedRows
	}
	return s
}

// pressAction is the kit action AXPress does on n, 0 when none: a check box, switch or
// toggle button is toggled (a toggle button the kit only presses is invoked), a button,
// link or menu item invoked, a disclosure opened or closed, an option or tab selected.
func pressAction(n *kit.SemanticNode) kit.Actions {
	switch {
	case n.Toggle != kit.ToggleNone && n.Actions&kit.ActionToggle != 0 && !isMenuItem(n.Role):
		return kit.ActionToggle
	case n.Actions&kit.ActionInvoke != 0:
		return kit.ActionInvoke
	case n.Actions&kit.ActionToggle != 0:
		return kit.ActionToggle
	case n.Expand == kit.ExpandClosed && n.Actions&kit.ActionExpand != 0:
		return kit.ActionExpand
	case n.Expand == kit.ExpandOpen && n.Actions&kit.ActionCollapse != 0:
		return kit.ActionCollapse
	case n.Actions&kit.ActionSelect != 0:
		return kit.ActionSelect
	}
	return 0
}

// Parent is the parent object of node id: the container for the root's children, 0 for a
// node that is gone.
func (b *Bridge) Parent(id kit.NodeID) Ref {
	st := b.tree()
	if st == nil {
		return 0
	}
	n, ok := st.Node(id)
	if !ok {
		b.evict(id)
		return 0
	}
	if n.Parent == 0 || n.Parent == st.Root() {
		return b.sys.Container()
	}
	return b.Object(n.Parent)
}

// Children appends the objects of node id's children, in reading order (a hosted field in
// place of its node).
func (b *Bridge) Children(dst []Ref, id kit.NodeID) []Ref {
	st := b.tree()
	if st == nil {
		return dst
	}
	b.kids = st.AppendChildren(b.kids[:0], id)
	kids := append([]kit.NodeID(nil), b.kids...)
	for _, c := range kids {
		if r := b.Object(c); r != 0 {
			dst = append(dst, r)
		}
	}
	return dst
}

// RootChildren is Children of the root.
func (b *Bridge) RootChildren(dst []Ref) []Ref {
	st := b.tree()
	if st == nil {
		return dst
	}
	return b.Children(dst, st.Root())
}

// HitTest is the deepest object at p (window DIPs): the container when no node is there.
func (b *Bridge) HitTest(p kit.Point) Ref {
	st := b.tree()
	if st == nil {
		return 0
	}
	if bad(float64(p.X)) || bad(float64(p.Y)) {
		return 0
	}
	return b.Object(st.HitTest(p))
}

func bad(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

// Focused is the object with the keyboard focus (the active item of a focused list), or 0
// when the focus is on the window itself or nowhere.
func (b *Bridge) Focused() Ref {
	st := b.tree()
	if st == nil {
		return 0
	}
	id := st.Focused()
	if id == 0 || id == st.Root() {
		return 0
	}
	return b.Object(id)
}

// Rows are a table's rows that are laid out (WebKit too lists only the DOM's), header
// row included; visible leaves out the ones scrolled out of view.
func (b *Bridge) Rows(dst []Ref, id kit.NodeID, visible bool) []Ref {
	st := b.tree()
	if st == nil {
		return dst
	}
	for _, r := range b.findRole(st, nil, id, kit.RoleRow, 3) {
		if visible {
			if n, ok := st.Node(r); !ok || n.States&kit.NodeOffscreen != 0 {
				continue
			}
		}
		if o := b.Object(r); o != 0 {
			dst = append(dst, o)
		}
	}
	return dst
}

// Selection appends the objects of a container's selected items, those not laid out
// included (each gets its element).
func (b *Bridge) Selection(dst []Ref, id kit.NodeID) []Ref {
	st := b.tree()
	if st == nil {
		return dst
	}
	for _, s := range st.AppendSelection(nil, id) {
		if o := b.Object(s); o != 0 {
			dst = append(dst, o)
		}
	}
	return dst
}

// ColumnHeaders are a table's column headers (within 3 levels).
func (b *Bridge) ColumnHeaders(dst []Ref, id kit.NodeID) []Ref {
	st := b.tree()
	if st == nil {
		return dst
	}
	for _, h := range b.findRole(st, nil, id, kit.RoleColumnHeader, 3) {
		if o := b.Object(h); o != 0 {
			dst = append(dst, o)
		}
	}
	return dst
}

// Columns is the number of columns of table id, as WebKit lists AXColumn elements: its
// column count, else the number of its column headers; 0 for a node that is not a table.
func (b *Bridge) Columns(id kit.NodeID) int {
	st := b.tree()
	if st == nil {
		return 0
	}
	n, ok := st.Node(id)
	if !ok || !isGrid(n.Role) {
		return 0
	}
	if n.Grid.Cols > 0 {
		return n.Grid.Cols
	}
	return len(b.findRole(st, nil, id, kit.RoleColumnHeader, 3))
}

// ColumnCells appends the objects of column col of table id: the cell (or header) at col
// of each of the table's rows that are laid out, header row first, as WebKit's AXColumn
// lists its children.
func (b *Bridge) ColumnCells(dst []Ref, id kit.NodeID, col int) []Ref {
	st := b.tree()
	if st == nil || col < 0 || col >= b.Columns(id) {
		return dst
	}
	for _, r := range b.findRole(st, nil, id, kit.RoleRow, 3) {
		if c := b.cellAt(st, r, col); c != 0 {
			if o := b.Object(c); o != 0 {
				dst = append(dst, o)
			}
		}
	}
	return dst
}

// ColumnHeader is the object of column col's header of table id (AXColumn's AXHeader),
// 0 when it has none.
func (b *Bridge) ColumnHeader(id kit.NodeID, col int) Ref {
	st := b.tree()
	if st == nil || col < 0 {
		return 0
	}
	if h := columnOf(st, b.findRole(st, nil, id, kit.RoleColumnHeader, 3), col); h != 0 {
		return b.Object(h)
	}
	return 0
}

// cellAt is row's cell (or header) in column col, 0 for none. A row whose cells give no
// columns (every Col 0) places them in order.
func (b *Bridge) cellAt(st kit.SemanticTree, row kit.NodeID, col int) kit.NodeID {
	var cells []kit.NodeID
	for _, k := range st.AppendChildren(nil, row) {
		cells = b.appendCells(st, cells, k, 2)
	}
	return columnOf(st, cells, col)
}

// appendCells appends id when it is a cell or header, else the cells under it (depth
// levels).
func (b *Bridge) appendCells(st kit.SemanticTree, dst []kit.NodeID, id kit.NodeID, depth int) []kit.NodeID {
	n, ok := st.Node(id)
	if !ok {
		return dst
	}
	if isCell(n.Role) {
		return append(dst, id)
	}
	if depth > 1 {
		for _, k := range st.AppendChildren(nil, id) {
			dst = b.appendCells(st, dst, k, depth-1)
		}
	}
	return dst
}

// columnOf is the one of cells (a row's, in order) that covers column col (its Col and
// ColSpan; when none of them gives a column, its index), 0 for none.
func columnOf(st kit.SemanticTree, cells []kit.NodeID, col int) kit.NodeID {
	nodes := make([]kit.SemanticNode, 0, len(cells))
	placed := false
	for _, c := range cells {
		n, _ := st.Node(c)
		nodes = append(nodes, n)
		placed = placed || n.Cell.Col != 0
	}
	for i, n := range nodes {
		c := n.Cell.Col
		if !placed {
			c = i
		}
		if col >= c && col < c+max(n.Cell.ColSpan, 1) {
			return cells[i]
		}
	}
	return 0
}

// Cell is the object of the cell at col and row of table id (made for a row not laid
// out), 0 when there is none.
func (b *Bridge) Cell(id kit.NodeID, col, row int) Ref {
	st := b.tree()
	if st == nil {
		return 0
	}
	n, ok := st.Node(id)
	if !ok || !isGrid(n.Role) || row < 0 || col < 0 || row >= n.Grid.Rows || (n.Grid.Cols > 0 && col >= n.Grid.Cols) {
		return 0
	}
	return b.Object(st.Cell(id, row, col))
}

// findRole appends the nodes of role within depth levels under from (not looking inside a
// match).
func (b *Bridge) findRole(st kit.SemanticTree, dst []kit.NodeID, from kit.NodeID, role kit.Role, depth int) []kit.NodeID {
	if depth <= 0 {
		return dst
	}
	for _, id := range st.AppendChildren(nil, from) {
		n, ok := st.Node(id)
		if !ok {
			continue
		}
		if n.Role == role {
			dst = append(dst, id)
			continue
		}
		dst = b.findRole(st, dst, id, role, depth-1)
	}
	return dst
}

// ancestor is the nearest ancestor of n whose role is, or 0.
func ancestor(st kit.SemanticTree, n *kit.SemanticNode, is func(kit.Role) bool) kit.NodeID {
	for id, i := n.Parent, 0; id != 0 && i < maxDepth; i++ {
		p, ok := st.Node(id)
		if !ok {
			return 0
		}
		if is(p.Role) {
			return id
		}
		id = p.Parent
	}
	return 0
}

// liveName is what a live region without a name reads out: the names inside it, at most 4
// levels deep.
func liveName(st kit.SemanticTree, id kit.NodeID) string {
	var sb strings.Builder
	var walk func(id kit.NodeID, depth int)
	walk = func(id kit.NodeID, depth int) {
		if depth > 4 {
			return
		}
		for _, c := range st.AppendChildren(nil, id) {
			n, ok := st.Node(c)
			if !ok {
				continue
			}
			if n.Name != "" {
				if sb.Len() > 0 {
					sb.WriteByte(' ')
				}
				sb.WriteString(n.Name)
				continue
			}
			walk(c, depth+1)
		}
	}
	walk(id, 0)
	return sb.String()
}

// descriptionList reports a list of terms and their values (dl: WebKit's
// AXDescriptionList): its first child is a term or a definition.
func descriptionList(st kit.SemanticTree, id kit.NodeID) bool {
	kids := st.AppendChildren(nil, id)
	if len(kids) == 0 {
		return false
	}
	n, ok := st.Node(kids[0])
	return ok && (n.Role == kit.RoleTerm || n.Role == kit.RoleDefinition)
}

// liveRegion is the live region node n (of id) is in, itself included, or 0.
func liveRegion(st kit.SemanticTree, n *kit.SemanticNode, id kit.NodeID) kit.NodeID {
	for i := 0; i < maxDepth; i++ {
		if liveSetting(n) != kit.LiveOff {
			return id
		}
		if id = n.Parent; id == 0 {
			return 0
		}
		p, ok := st.Node(id)
		if !ok {
			return 0
		}
		n = &p
	}
	return 0
}

// ---- actions ------------------------------------------------------------------------

// ErrRefused is an action the node does not take now (disabled, read-only, out of range).
var ErrRefused = errors.New("ax: action refused")

// node is id's node now, or an error.
func (b *Bridge) node(id kit.NodeID) (kit.SemanticTree, kit.SemanticNode, error) {
	st := b.tree()
	if st == nil {
		return nil, kit.SemanticNode{}, kit.ErrClosed
	}
	n, ok := st.Node(id)
	if !ok {
		b.evict(id)
		return nil, kit.SemanticNode{}, fmt.Errorf("ax: node %d: %w", id, kit.ErrNoNode)
	}
	return st, n, nil
}

// Press is AXPress: checked now, done once the caller's request has returned (never
// synchronously: a button may open a modal dialog).
func (b *Bridge) Press(id kit.NodeID) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	if n.States&kit.NodeDisabled != 0 {
		return fmt.Errorf("ax: press node %d: %w", id, kit.ErrNotEnabled)
	}
	a := pressAction(&n)
	if a == 0 {
		return fmt.Errorf("ax: press node %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Post(kit.ActionRequest{Node: id, Action: a})
}

// Step is AXIncrement (up) or AXDecrement: the range moves by its step, clamped.
func (b *Bridge) Step(id kit.NodeID, up bool) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	switch {
	case n.States&kit.NodeDisabled != 0:
		return fmt.Errorf("ax: step node %d: %w", id, kit.ErrNotEnabled)
	case rangeReadOnly(&n) || n.Range.Step <= 0:
		return fmt.Errorf("ax: step node %d: %w", id, kit.ErrUnsupported)
	}
	v := n.Range.Value + n.Range.Step
	if !up {
		v = n.Range.Value - n.Range.Step
	}
	v = math.Min(math.Max(v, n.Range.Min), n.Range.Max)
	if v == n.Range.Value {
		return nil
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionSetRange, Number: v})
}

// ShowMenu is AXShowMenu: a popup trigger opens its popup (posted like a press).
func (b *Bridge) ShowMenu(id kit.NodeID) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	switch {
	case n.States&kit.NodeDisabled != 0:
		return fmt.Errorf("ax: show menu %d: %w", id, kit.ErrNotEnabled)
	case n.States&kit.NodeHasPopup == 0 || n.Actions&kit.ActionExpand == 0:
		return fmt.Errorf("ax: show menu %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Post(kit.ActionRequest{Node: id, Action: kit.ActionExpand})
}

// ScrollToVisible is AXScrollToVisible, allowed on a disabled node too.
func (b *Bridge) ScrollToVisible(id kit.NodeID) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	if n.Actions&kit.ActionScrollIntoView == 0 {
		return fmt.Errorf("ax: scroll node %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionScrollIntoView})
}

// SetText is setAccessibilityValue: with a string.
func (b *Bridge) SetText(id kit.NodeID, s string) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	switch {
	case n.States&kit.NodeDisabled != 0:
		return fmt.Errorf("ax: set value %d: %w", id, kit.ErrNotEnabled)
	case valueReadOnly(&n):
		return fmt.Errorf("ax: set value %d: %w", id, ErrRefused)
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionSetValue, Text: s})
}

// SetNumber is setAccessibilityValue: with a number: a range's value, refused outside
// [Min, Max] or NaN.
func (b *Bridge) SetNumber(id kit.NodeID, v float64) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	switch {
	case !n.Range.Valid:
		return fmt.Errorf("ax: set number %d: %w", id, kit.ErrUnsupported)
	case n.States&kit.NodeDisabled != 0:
		return fmt.Errorf("ax: set number %d: %w", id, kit.ErrNotEnabled)
	case rangeReadOnly(&n):
		return fmt.Errorf("ax: set number %d: %w", id, ErrRefused)
	case math.IsNaN(v) || v < n.Range.Min || v > n.Range.Max:
		return fmt.Errorf("ax: set number %d to %v: %w", id, v, ErrRefused)
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionSetRange, Number: v})
}

// SetFocused is setAccessibilityFocused:YES (ActionFocus, which turns the focus ring on).
func (b *Bridge) SetFocused(id kit.NodeID) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	if n.Actions&kit.ActionFocus == 0 {
		return fmt.Errorf("ax: focus %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionFocus})
}

// SetSelected is setAccessibilitySelected:.
func (b *Bridge) SetSelected(id kit.NodeID, on bool) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	if selected(&n) == on {
		return nil
	}
	a := kit.ActionRemoveFromSelection
	if on {
		a = kit.ActionSelect
		if n.Actions&kit.ActionSelect == 0 {
			a = kit.ActionAddToSelection
		}
	}
	if n.Actions&a == 0 {
		return fmt.Errorf("ax: select %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Perform(kit.ActionRequest{Node: id, Action: a})
}

// SetExpanded is setAccessibilityExpanded:.
func (b *Bridge) SetExpanded(id kit.NodeID, open bool) error {
	_, n, err := b.node(id)
	if err != nil {
		return err
	}
	if (n.Expand == kit.ExpandOpen) == open {
		return nil
	}
	a := kit.ActionCollapse
	if open {
		a = kit.ActionExpand
	}
	if n.Expand == kit.ExpandNone || n.Actions&a == 0 {
		return fmt.Errorf("ax: expand %d: %w", id, kit.ErrUnsupported)
	}
	return b.host.Post(kit.ActionRequest{Node: id, Action: a})
}

// SetSelection is setAccessibilitySelectedRows: or setAccessibilitySelectedChildren: of a
// container: the items of ids become its selection.
func (b *Bridge) SetSelection(container kit.NodeID, ids []kit.NodeID) error {
	st, _, err := b.node(container)
	if err != nil {
		return err
	}
	want := map[kit.NodeID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for i, id := range ids {
		n, ok := st.Node(id)
		if !ok {
			continue
		}
		a := kit.ActionAddToSelection
		if i == 0 && n.Actions&kit.ActionSelect != 0 {
			a = kit.ActionSelect
		}
		if err := b.host.Perform(kit.ActionRequest{Node: id, Action: a}); err != nil {
			return err
		}
	}
	for _, id := range st.AppendSelection(nil, container) {
		if !want[id] {
			if err := b.host.Perform(kit.ActionRequest{Node: id, Action: kit.ActionRemoveFromSelection}); err != nil &&
				!errors.Is(err, kit.ErrUnsupported) {
				return err
			}
		}
	}
	return nil
}

// ---- events -------------------------------------------------------------------------

// Notify posts the notifications of a change the kit reports (kit.Host's NotifySemantics).
func (b *Bridge) Notify(ev kit.SemanticEvent) {
	st := b.tree()
	if st == nil {
		return
	}
	switch ev.Kind {
	case kit.SemanticFocusChanged:
		b.focusChanged(st, ev.Node)
	case kit.SemanticNodeChanged:
		b.nodeChanged(st, ev.Node)
	case kit.SemanticChildrenChanged:
		b.childrenChanged(st, ev.Node)
	case kit.SemanticAnnounce:
		if ev.Text == "" {
			return
		}
		p := PriorityOf(ev.Politeness)
		if p == 0 {
			p = PriorityMedium
		}
		b.announce(ev.Text, p)
	}
}

func (b *Bridge) post(r Ref, n Notification) {
	if r == 0 {
		return
	}
	b.Stats.Posted++
	b.sys.Post(r, n)
}

func (b *Bridge) announce(text string, p Priority) {
	b.Stats.Announced++
	b.sys.Announce(text, p)
}

func (b *Bridge) focusChanged(st kit.SemanticTree, id kit.NodeID) {
	if id == 0 || id == st.Root() {
		return
	}
	n, ok := st.Node(id)
	if !ok {
		return
	}
	if b.host.HostedField(&n) != 0 && n.Role == kit.RoleTextField {
		return // the field's own focus: AppKit posts it
	}
	b.post(b.element(id, &n), FocusChanged)
	// The grid's cursor row (an active descendant) is its selected row for VoiceOver.
	if n.Role == kit.RoleRow {
		if g := ancestor(st, &n, isGrid); g != 0 && b.elems[g] != nil {
			b.post(b.elems[g].ref, SelectedRowsChanged)
		}
	}
}

func (b *Bridge) nodeChanged(st kit.SemanticTree, id kit.NodeID) {
	n, ok := st.Node(id)
	if !ok {
		b.evict(id)
		return
	}
	changed := true
	if e := b.elems[id]; e != nil && e.seen {
		old := e.last
		e.last = n
		changed = old.Name != n.Name || old.Value != n.Value || old.Description != n.Description
		b.raiseChanges(st, e.ref, &old, &n)
	} else if e != nil {
		e.last, e.seen = n, true
	}
	if !changed {
		return
	}
	if live := liveRegion(st, &n, id); live != 0 {
		b.liveChanged(live)
	}
}

// raiseChanges posts the notifications between what the client saw (old) and n.
func (b *Bridge) raiseChanges(st kit.SemanticTree, r Ref, old, n *kit.SemanticNode) {
	role, _ := RoleOf(n)
	if old.Name != n.Name {
		if role == RoleStaticText {
			b.post(r, ValueChanged)
		} else {
			b.post(r, TitleChanged)
		}
	}
	valueChanged := old.Value != n.Value || old.Toggle != n.Toggle || old.Level != n.Level ||
		old.Range.Valid != n.Range.Valid || (n.Range.Valid && old.Range.Value != n.Range.Value)
	if valueChanged && !(n.States&kit.NodeProtected != 0 && old.Toggle == n.Toggle) {
		b.post(r, ValueChanged)
	}
	if old.Expand != n.Expand {
		b.post(r, ExpandedChanged)
	}
	if isGrid(n.Role) && old.Grid.Rows != n.Grid.Rows {
		b.post(r, RowCountChanged)
	}
	if selected(old) != selected(n) {
		if c := ancestor(st, n, isSelectionContainer); c != 0 {
			if e := b.elems[c]; e != nil {
				if n.Role == kit.RoleRow {
					b.post(e.ref, SelectedRowsChanged)
				} else {
					b.post(e.ref, SelectedChildrenChanged)
				}
			}
		}
	}
}

func (b *Bridge) childrenChanged(st kit.SemanticTree, id kit.NodeID) {
	n, ok := st.Node(id)
	// A grid's or list box's rows come and go as it scrolls (the kit lays out only those in
	// view): no layout change for VoiceOver, as WebKit posts none for the DOM's rows.
	if !ok || !virtualized(st, &n) {
		b.Relayout()
	}
	if !b.sweepWanted && len(b.elems) > 0 {
		b.sweepWanted = b.later(func() {
			if !b.closed {
				b.Sweep()
			}
		})
	}
	// Content added to a live region is read out.
	if ok && id != st.Root() {
		if live := liveRegion(st, &n, id); live != 0 {
			b.liveChanged(live)
		}
	}
}

// Relayout posts one NSAccessibilityLayoutChangedNotification about the window once the
// current work is done, however many changes ask for it before then: the window's content
// was added, removed or moved (a dialog opened, a native field placed).
func (b *Bridge) Relayout() {
	if b.closed || b.layoutWanted {
		return
	}
	b.layoutWanted = b.later(func() {
		b.layoutWanted = false
		if !b.closed {
			b.post(b.sys.Container(), LayoutChanged)
		}
	})
}

// virtualized reports a node inside (or being) a grid or list box, whose rows the kit
// realises as they scroll into view.
func virtualized(st kit.SemanticTree, n *kit.SemanticNode) bool {
	is := func(r kit.Role) bool { return isGrid(r) || r == kit.RoleListBox }
	return is(n.Role) || ancestor(st, n, is) != 0
}

// liveChanged queues a live region's announcement for after the flush, once per region.
func (b *Bridge) liveChanged(region kit.NodeID) {
	for _, r := range b.liveDue {
		if r == region {
			return
		}
	}
	if !b.liveWanted {
		if b.liveWanted = b.later(b.FlushLive); !b.liveWanted {
			return // nothing will flush: keep nothing queued
		}
	}
	b.liveDue = append(b.liveDue, region)
}

// FlushLive announces the live regions whose content changed: their content (the names
// inside them, else their own name), once for each new text.
func (b *Bridge) FlushLive() {
	b.liveWanted = false
	due := b.liveDue
	b.liveDue = nil
	st := b.tree()
	if st == nil {
		return
	}
	for _, id := range due {
		n, ok := st.Node(id)
		if !ok {
			delete(b.liveSaid, id)
			continue
		}
		p := PriorityOf(liveSetting(&n))
		if p == 0 {
			continue
		}
		text := liveName(st, id)
		if text == "" {
			text = n.Name
		}
		if text == "" {
			text = n.Value
		}
		if text == "" || b.liveSaid[id] == text {
			b.liveSaid[id] = text
			continue
		}
		b.liveSaid[id] = text
		b.announce(text, p)
	}
}
