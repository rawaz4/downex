package kit

import (
	"fmt"
	"strings"
)

// Semantics is what an element tells assistive technology, in the terms of SemanticNode.
// The kit adds what it knows itself: the id, the parent, the bounds, the focus and
// disabled states, offscreen, and the Focus and ScrollIntoView actions; a widget adds what
// only it knows through SemanticFiller.
type Semantics struct {
	// Role is what the element is; RoleNone (the zero value) is not exposed, and its
	// children stand in its place (layout boxes).
	Role Role
	// Name is the accessible name; when empty, roles that take their name from their
	// content (buttons, cells, rows, options, tabs, links, headings, menu items) take the
	// text inside them.
	Name, Description, Value string
	Range                    RangeValue
	// States are added to the ones the kit works out.
	States      NodeState
	Toggle      ToggleState
	Expand      ExpandState
	Actions     Actions // added to the ones the kit works out
	Live        Politeness
	Orientation Orientation
	Sort        SortDirection
	Grid        GridInfo
	Cell        CellInfo
	PosInSet    int
	SetSize     int
	Level       int
	Shortcut    string
	// Controls is the widget this one controls (aria-controls).
	Controls     Widget
	AutomationID string
	// Hidden keeps the element and everything in it out of the tree (aria-hidden="true":
	// decoration, the rows' hover actions).
	Hidden bool
	// Fieldset marks the <fieldset> of today's window (SemanticNode.Fieldset). [ax]
	Fieldset bool
}

// nameFromContent reports whether a role takes its name from the text inside it.
func nameFromContent(r Role) bool {
	switch r {
	case RoleButton, RoleCell, RoleCheckBox, RoleColumnHeader, RoleRowHeader, RoleHeading, RoleLink,
		RoleMenuItem, RoleMenuItemCheckBox, RoleMenuItemRadio, RoleOption, RoleRadioButton, RoleRow,
		RoleSwitch, RoleTab, RoleTooltip, RoleText, RoleListItem, RoleTerm, RoleDefinition:
		return true
	}
	return false
}

// presentationalChildren reports whether a role's children are not exposed of their own
// (ARIA's "children presentational").
func presentationalChildren(r Role) bool {
	switch r {
	case RoleButton, RoleCheckBox, RoleImage, RoleMenuItemCheckBox, RoleMenuItemRadio, RoleOption,
		RoleProgressBar, RoleRadioButton, RoleScrollBar, RoleSeparator, RoleSlider, RoleSwitch, RoleTab:
		return true
	}
	return false
}

// textContent is the text a widget shows, for the names of the roles that take theirs
// from their content.
type textContent interface {
	TextContent() string
}

// virtualNodes are nodes a widget keeps without elements: a virtual list's rows that are
// not laid out.
type virtualNodes interface {
	virtualNode(id NodeID) (SemanticNode, bool)
	virtualChildren(dst []NodeID, id NodeID) []NodeID
	performVirtual(r ActionRequest) error
}

// gridCells is a widget that finds its own cells (a virtual list).
type gridCells interface {
	cellNode(row, col int) NodeID
}

// selectionNodes is a widget that lists its own selection, the items not laid out
// included.
type selectionNodes interface {
	appendSelection(dst []NodeID) []NodeID
}

// activeDescendant is a focused widget that moves an active item while it keeps the focus
// (aria-activedescendant: the grid's cursor row).
type activeDescendant interface {
	ActiveDescendant() NodeID
}

// ScrollShower is a widget that scrolls its content: a list scrolling to a row, a scroll
// pane. Window.ScrollIntoView asks every ScrollShower around an element to show it, and the
// semantic tree offers ScrollIntoView (UI Automation's ScrollItem) on what is inside one.
type ScrollShower interface {
	// ScrollToShow scrolls as little as it takes to bring e (a descendant) into view.
	ScrollToShow(e *Element)
}

// semTree is the window's SemanticTree.
type semTree struct{ w *Window }

var _ SemanticTree = (*semTree)(nil)

// exposed reports whether e is a node of the tree.
func (w *Window) exposed(e *Element) bool {
	if e == w.root {
		return true
	}
	if e.win != w || e.Semantics.Role == RoleNone {
		return false
	}
	return w.reachable(e)
}

// reachable reports whether e is in a part of the tree that is exposed: shown, not
// inert, not hidden from assistive technology, not inside presentational children.
func (w *Window) reachable(e *Element) bool {
	for p := e; p != nil; p = p.parent {
		if p.hidden() || p.flags&isParked != 0 || p.Inert || p.Semantics.Hidden {
			return false
		}
		if p != e && presentationalChildren(p.Semantics.Role) {
			return false
		}
	}
	return true
}

// exposedParent is the id of the nearest exposed ancestor of e.
func (w *Window) exposedParent(e *Element) NodeID {
	for p := e.parent; p != nil; p = p.parent {
		if p == w.root || p.Semantics.Role != RoleNone {
			return p.NodeID()
		}
	}
	return 0
}

// exposedSelf is the nearest exposed element at or above e.
func (w *Window) exposedSelf(e *Element) *Element {
	for p := e; p != nil; p = p.parent {
		if w.exposed(p) {
			return p
		}
	}
	return w.root
}

// textOf is the text inside e, for a name from content.
func (w *Window) textOf(e *Element) string {
	var b strings.Builder
	var walk func(*Element)
	walk = func(p *Element) {
		if p.hidden() || p.flags&isParked != 0 || p.Semantics.Hidden {
			return
		}
		if p != e && p.Semantics.Name != "" {
			add(&b, p.Semantics.Name)
			return
		}
		if t, ok := p.self.(textContent); ok {
			add(&b, t.TextContent())
		}
		for _, c := range p.kids {
			walk(c)
		}
	}
	walk(e)
	return b.String()
}

func add(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(s)
}

// node is e's semantic node now.
func (w *Window) node(e *Element) SemanticNode {
	s := &e.Semantics
	n := SemanticNode{
		ID:           e.NodeID(),
		Parent:       w.exposedParent(e),
		Role:         s.Role,
		Name:         s.Name,
		Description:  s.Description,
		Value:        s.Value,
		Range:        s.Range,
		States:       s.States,
		Toggle:       s.Toggle,
		Expand:       s.Expand,
		Actions:      s.Actions,
		Live:         s.Live,
		Orientation:  s.Orientation,
		Sort:         s.Sort,
		Grid:         s.Grid,
		Cell:         s.Cell,
		PosInSet:     s.PosInSet,
		SetSize:      s.SetSize,
		Level:        s.Level,
		Shortcut:     s.Shortcut,
		AutomationID: s.AutomationID,
		Fieldset:     s.Fieldset, // [ax]
		Bounds:       e.WindowBounds(),
	}
	if e == w.root {
		n.Role, n.Parent = RoleWindow, 0
		if n.Name == "" {
			n.Name = w.opts.Title
		}
		n.Bounds = RectAt(Point{}, w.size)
	}
	if s.Controls != nil {
		n.Controls = s.Controls.Base().NodeID()
	}
	if n.Name == "" && nameFromContent(n.Role) {
		n.Name = w.textOf(e)
	}
	if e.focusable() || (e.Focus != FocusNone && e.Disabled && e.DisabledFocusable) {
		n.States |= NodeFocusable
		n.Actions |= ActionFocus
	}
	if w.focused == e {
		n.States |= NodeFocused
	}
	if !e.enabled() {
		n.States |= NodeDisabled
	}
	if _, ok := e.visibleRect(); !ok && e != w.root {
		n.States |= NodeOffscreen
	}
	for p := e.parent; p != nil; p = p.parent {
		if _, ok := p.self.(ScrollShower); ok {
			n.Actions |= ActionScrollIntoView
			break
		}
	}
	if f, ok := e.self.(SemanticFiller); ok {
		f.FillSemantics(&n)
	}
	if d := w.decorator(e); d != nil {
		d.decorate(e, &n)
	}
	if n.States&NodeProtected != 0 {
		n.Value = ""
	}
	return n
}

// rowDecorator is a widget that knows things about some of its descendants' nodes: a list
// about its rows (their position, their selection and its actions).
type rowDecorator interface {
	decorate(e *Element, n *SemanticNode)
	performOn(e *Element, r ActionRequest) (bool, error)
}

func (w *Window) decorator(e *Element) rowDecorator {
	for p := e.parent; p != nil; p = p.parent {
		if d, ok := p.self.(rowDecorator); ok {
			return d
		}
	}
	return nil
}

// useSemantics notes that assistive technology reads the tree: from now on a list's rows
// carry ids of their own that last while their rows exist. Until then nobody has seen an
// id, so rows keep their widgets' and no memory goes to ids.
func (w *Window) useSemantics() {
	if w.semUsed {
		return
	}
	w.semUsed = true
	for _, l := range w.lists {
		l.bindIDs()
	}
}

func (t *semTree) Root() NodeID {
	t.w.useSemantics()
	return t.w.root.NodeID()
}

func (t *semTree) Node(id NodeID) (SemanticNode, bool) {
	w := t.w
	w.useSemantics()
	if e := w.nodes[id]; e != nil {
		if !w.exposed(e) {
			return SemanticNode{}, false
		}
		return w.node(e), true
	}
	if v := w.virtual[id]; v != nil {
		return v.virtualNode(id)
	}
	return SemanticNode{}, false
}

func (t *semTree) AppendChildren(dst []NodeID, id NodeID) []NodeID {
	w := t.w
	w.useSemantics()
	e := w.nodes[id]
	if e == nil {
		if v := w.virtual[id]; v != nil {
			return v.virtualChildren(dst, id)
		}
		return dst
	}
	if !w.exposed(e) || presentationalChildren(e.Semantics.Role) {
		return dst
	}
	return w.appendExposed(dst, e)
}

// appendExposed appends the exposed children of e, looking through RoleNone boxes.
func (w *Window) appendExposed(dst []NodeID, e *Element) []NodeID {
	for _, c := range e.kids {
		if c.hidden() || c.flags&isParked != 0 || c.Inert || c.Semantics.Hidden {
			continue
		}
		if c.Semantics.Role == RoleNone {
			dst = w.appendExposed(dst, c)
			continue
		}
		dst = append(dst, c.NodeID())
	}
	return dst
}

func (t *semTree) Focused() NodeID {
	t.w.useSemantics()
	return t.w.focusedNode()
}

// focusedNode is the node with the focus (the active item of a focused list).
func (w *Window) focusedNode() NodeID {
	e := w.focused
	if e == nil {
		return 0
	}
	if a, ok := e.self.(activeDescendant); ok {
		if id := a.ActiveDescendant(); id != 0 {
			return id
		}
	}
	return w.exposedSelf(e).NodeID()
}

func (t *semTree) HitTest(p Point) NodeID {
	w := t.w
	w.useSemantics()
	e := w.hitTest(p, false)
	if e == nil {
		return w.root.NodeID()
	}
	return w.exposedSelf(e).NodeID()
}

func (t *semTree) Cell(grid NodeID, row, col int) NodeID {
	w := t.w
	w.useSemantics()
	e := w.nodes[grid]
	if e == nil || !w.exposed(e) {
		return 0
	}
	if g, ok := e.self.(gridCells); ok {
		return g.cellNode(row, col)
	}
	var found NodeID
	var walk func(*Element)
	walk = func(p *Element) {
		for _, c := range p.kids {
			if found != 0 || !w.reachable(c) {
				continue
			}
			switch c.Semantics.Role {
			case RoleCell, RoleColumnHeader, RoleRowHeader:
				if c.Semantics.Cell.Row == row && c.Semantics.Cell.Col == col {
					found = c.NodeID()
					return
				}
			}
			walk(c)
		}
	}
	walk(e)
	return found
}

func (t *semTree) AppendSelection(dst []NodeID, container NodeID) []NodeID {
	w := t.w
	w.useSemantics()
	e := w.nodes[container]
	if e == nil || !w.exposed(e) {
		return dst
	}
	if s, ok := e.self.(selectionNodes); ok {
		return s.appendSelection(dst)
	}
	var walk func(*Element)
	walk = func(p *Element) {
		for _, c := range p.kids {
			if !w.reachable(c) {
				continue
			}
			if c.Semantics.Role != RoleNone && w.node(c).States&NodeSelected != 0 {
				dst = append(dst, c.NodeID())
			}
			walk(c)
		}
	}
	walk(e)
	return dst
}

func (t *semTree) Perform(r ActionRequest) error {
	w := t.w
	w.useSemantics()
	w.enter()
	defer w.leave()
	e := w.nodes[r.Node]
	if e == nil {
		if v := w.virtual[r.Node]; v != nil {
			return v.performVirtual(r)
		}
		return fmt.Errorf("perform on node %d: %w", r.Node, ErrNoNode)
	}
	if !w.exposed(e) {
		return fmt.Errorf("perform on node %d: %w", r.Node, ErrNoNode)
	}
	n := w.node(e)
	if n.Actions&r.Action == 0 || r.Action == 0 {
		return fmt.Errorf("action %#x on node %d (%v): %w", uint32(r.Action), r.Node, n.Role, ErrUnsupported)
	}
	if n.States&NodeDisabled != 0 && r.Action != ActionScrollIntoView {
		return fmt.Errorf("action %#x on node %d: %w", uint32(r.Action), r.Node, ErrNotEnabled)
	}
	if d := w.decorator(e); d != nil {
		if done, err := d.performOn(e, r); done {
			return err
		}
	}
	switch r.Action {
	case ActionFocus:
		w.focusVisible = true
		if !w.Focus(e) {
			return fmt.Errorf("focus node %d: %w", r.Node, ErrNotEnabled)
		}
		return nil
	case ActionScrollIntoView:
		w.ScrollIntoView(e)
		return nil
	}
	if h, ok := e.self.(ActionHandler); ok {
		return h.PerformAction(r)
	}
	return fmt.Errorf("action %#x on node %d: %w", uint32(r.Action), r.Node, ErrUnsupported)
}

// ScrollIntoView scrolls every scrolling ancestor of e so that e shows.
func (w *Window) ScrollIntoView(e *Element) {
	for p := e.parent; p != nil; p = p.parent {
		if s, ok := p.self.(ScrollShower); ok {
			s.ScrollToShow(e)
		}
	}
}

// ---- change notification ----------------------------------------------------------------

type semKey struct {
	kind SemanticEventKind
	id   NodeID
}

type semQueue struct {
	events []SemanticEvent
	seen   map[semKey]int
}

func (q *semQueue) push(ev SemanticEvent, dedupe bool) {
	if dedupe {
		if q.seen == nil {
			q.seen = map[semKey]int{}
		}
		k := semKey{ev.Kind, ev.Node}
		if ev.Kind == SemanticFocusChanged {
			k.id = 0
		}
		if i, ok := q.seen[k]; ok {
			q.events[i] = ev
			return
		}
		q.seen[k] = len(q.events)
	}
	q.events = append(q.events, ev)
}

// semListening reports whether change events are worth sending: assistive technology has
// read the tree (a client that never asked has nothing to update), and the window is open.
func (w *Window) semListening() bool { return w.semUsed && w.host != nil && !w.closed }

// semNode queues NodeChanged for e and for the ancestors whose name comes from content
// that includes e's.
func (w *Window) semNode(e *Element) {
	if !w.semListening() {
		return
	}
	for p := e; p != nil; p = p.parent {
		exposed := w.exposed(p)
		if exposed {
			w.sem.push(SemanticEvent{Kind: SemanticNodeChanged, Node: p.NodeID()}, true)
		}
		// An element that is not a node of its own shows in the nearest node above it;
		// a node's content shows in its parent's name when that comes from content.
		par := p.parent
		if par == nil || (exposed && !(nameFromContent(par.Semantics.Role) && par.Semantics.Name == "")) {
			return
		}
	}
}

// semChildren queues ChildrenChanged for the node e's children show under.
func (w *Window) semChildren(e *Element) {
	if !w.semListening() {
		return
	}
	w.sem.push(SemanticEvent{Kind: SemanticChildrenChanged, Node: w.exposedSelf(e).NodeID()}, true)
}

// semFocus queues FocusChanged for the node with the focus now.
func (w *Window) semFocus() {
	if !w.semListening() {
		return
	}
	w.sem.push(SemanticEvent{Kind: SemanticFocusChanged, Node: w.focusedNode()}, true)
}

// Announce has assistive technology read text, as a live region would (the grid's I key
// reads out a download's progress).
func (w *Window) Announce(text string, p Politeness, about Widget) {
	if !w.semListening() || text == "" {
		return
	}
	var id NodeID
	if about != nil {
		id = about.Base().NodeID()
	}
	w.sem.push(SemanticEvent{Kind: SemanticAnnounce, Node: id, Text: text, Politeness: p}, false)
	if w.depth == 0 {
		w.flushSemantics()
	}
}

func (w *Window) flushSemantics() {
	if len(w.sem.events) == 0 || w.host == nil {
		w.sem = semQueue{}
		return
	}
	evs := w.sem.events
	w.sem = semQueue{}
	for _, ev := range evs {
		w.stats.SemEvents++
		w.host.NotifySemantics(ev)
	}
}

// rebindNode makes e stand for node id (a recycled list row taking another row's id).
func (w *Window) rebindNode(e *Element, id NodeID) {
	if e.NodeID() == id {
		return
	}
	if w.nodes[e.NodeID()] == e {
		delete(w.nodes, e.NodeID())
	}
	e.semID = id
	if id == 0 {
		e.semID = 0
	}
	w.nodes[e.NodeID()] = e
}
