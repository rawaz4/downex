package ax

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"downex/ui/kit"
)

// fakeTree is a SemanticTree over a map, with a grid whose rows past `laid` are made on
// demand by Cell.
type fakeTree struct {
	nodes    map[kit.NodeID]*kit.SemanticNode
	kids     map[kit.NodeID][]kit.NodeID
	focus    kit.NodeID
	hit      kit.NodeID
	sel      map[kit.NodeID][]kit.NodeID
	performs []kit.ActionRequest
	next     kit.NodeID
	cells    map[[3]int]kit.NodeID
}

func newFake() *fakeTree {
	t := &fakeTree{nodes: map[kit.NodeID]*kit.SemanticNode{}, kids: map[kit.NodeID][]kit.NodeID{},
		sel: map[kit.NodeID][]kit.NodeID{}, next: 100, cells: map[[3]int]kit.NodeID{}}
	t.add(0, kit.SemanticNode{ID: 1, Role: kit.RoleWindow, Name: "Downex", Bounds: kit.R(0, 0, 900, 600)})
	return t
}

func (t *fakeTree) add(parent kit.NodeID, n kit.SemanticNode) kit.NodeID {
	if n.ID == 0 {
		t.next++
		n.ID = t.next
	}
	n.Parent = parent
	t.nodes[n.ID] = &n
	if parent != 0 {
		t.kids[parent] = append(t.kids[parent], n.ID)
	}
	return n.ID
}

func (t *fakeTree) remove(id kit.NodeID) {
	n := t.nodes[id]
	delete(t.nodes, id)
	t.kids[n.Parent] = slices.DeleteFunc(t.kids[n.Parent], func(c kit.NodeID) bool { return c == id })
}

func (t *fakeTree) Root() kit.NodeID { return 1 }
func (t *fakeTree) Node(id kit.NodeID) (kit.SemanticNode, bool) {
	n := t.nodes[id]
	if n == nil {
		return kit.SemanticNode{}, false
	}
	m := *n
	if id == t.focus {
		m.States |= kit.NodeFocused
	}
	return m, true
}
func (t *fakeTree) AppendChildren(dst []kit.NodeID, id kit.NodeID) []kit.NodeID {
	return append(dst, t.kids[id]...)
}
func (t *fakeTree) Focused() kit.NodeID            { return t.focus }
func (t *fakeTree) HitTest(p kit.Point) kit.NodeID { return t.hit }
func (t *fakeTree) Cell(grid kit.NodeID, row, col int) kit.NodeID {
	k := [3]int{int(grid), row, col}
	if id, ok := t.cells[k]; ok {
		if _, live := t.nodes[id]; live {
			return id
		}
	}
	id := t.add(grid, kit.SemanticNode{Role: kit.RoleCell, Name: fmt.Sprintf("r%dc%d", row, col), Cell: kit.CellInfo{Row: row, Col: col}})
	t.kids[grid] = t.kids[grid][:len(t.kids[grid])-1] // not laid out: not a child
	t.cells[k] = id
	return id
}
func (t *fakeTree) AppendSelection(dst []kit.NodeID, c kit.NodeID) []kit.NodeID {
	return append(dst, t.sel[c]...)
}
func (t *fakeTree) Perform(r kit.ActionRequest) error {
	n, ok := t.nodes[r.Node]
	if !ok {
		return kit.ErrNoNode
	}
	if n.Actions&r.Action == 0 {
		return kit.ErrUnsupported
	}
	t.performs = append(t.performs, r)
	return nil
}

type fakeHost struct {
	t      *fakeTree
	posted []kit.ActionRequest
	fields map[kit.NodeID]Ref
	gone   bool
}

func (h *fakeHost) Semantics() kit.SemanticTree {
	if h.gone {
		return nil
	}
	return h.t
}
func (h *fakeHost) Perform(r kit.ActionRequest) error { return h.t.Perform(r) }
func (h *fakeHost) Post(r kit.ActionRequest) error    { h.posted = append(h.posted, r); return nil }
func (h *fakeHost) HostedField(n *kit.SemanticNode) Ref {
	return h.fields[n.ID]
}

type post struct {
	r Ref
	n Notification
}

type fakeSys struct {
	next      Ref
	made      map[Ref]kit.NodeID
	dead      map[Ref]bool
	posts     []post
	announced []string
	later     []func()
	decorated map[Ref]Ref
	gone      bool // the application is gone: Later runs nothing
}

const container Ref = 7

func newSys() *fakeSys {
	return &fakeSys{next: 1000, made: map[Ref]kit.NodeID{}, dead: map[Ref]bool{}, decorated: map[Ref]Ref{}}
}

func (s *fakeSys) Container() Ref { return container }
func (s *fakeSys) NewElement(id kit.NodeID) Ref {
	s.next++
	s.made[s.next] = id
	return s.next
}
func (s *fakeSys) Destroy(r Ref)              { s.dead[r] = true }
func (s *fakeSys) Post(r Ref, n Notification) { s.posts = append(s.posts, post{r, n}) }
func (s *fakeSys) Announce(t string, p Priority) {
	s.announced = append(s.announced, fmt.Sprintf("%d:%s", p, t))
}
func (s *fakeSys) DecorateField(f, parent Ref, n *kit.SemanticNode) { s.decorated[f] = parent }
func (s *fakeSys) Later(f func()) bool {
	if s.gone {
		return false
	}
	s.later = append(s.later, f)
	return true
}
func (s *fakeSys) runLater() {
	for len(s.later) > 0 {
		f := s.later[0]
		s.later = s.later[1:]
		f()
	}
}

// page is a window like the main one: a title bar with a button and a search field, a
// grid with a header row and two laid-out rows, a status region and a slider.
type page struct {
	t                                        *fakeTree
	h                                        *fakeHost
	s                                        *fakeSys
	b                                        *Bridge
	tb, settings, search, grid, head, r1, r2 kit.NodeID
	status, statusText, slider, check, menu  kit.NodeID
}

func newPage() *page {
	t := newFake()
	p := &page{t: t}
	p.tb = t.add(1, kit.SemanticNode{Role: kit.RoleTitleBar, Name: "Title bar", AutomationID: "titlebar"})
	p.settings = t.add(p.tb, kit.SemanticNode{Role: kit.RoleButton, Name: "Settings", Actions: kit.ActionInvoke | kit.ActionFocus,
		Bounds: kit.R(800, 4, 830, 30)})
	p.search = t.add(p.tb, kit.SemanticNode{Role: kit.RoleTextField, Name: "Search downloads", Actions: kit.ActionSetValue,
		Bounds: kit.R(300, 6, 600, 30), AutomationID: "titlebar.search"})
	p.menu = t.add(p.tb, kit.SemanticNode{Role: kit.RoleButton, Name: "More", States: kit.NodeHasPopup, Expand: kit.ExpandClosed,
		Actions: kit.ActionInvoke | kit.ActionExpand})
	p.grid = t.add(1, kit.SemanticNode{Role: kit.RoleGrid, Name: "Downloads", Grid: kit.GridInfo{Rows: 10001, Cols: 4},
		States: kit.NodeMultiSelectable})
	p.head = t.add(p.grid, kit.SemanticNode{Role: kit.RoleRow, Cell: kit.CellInfo{Row: 0}})
	for c, name := range []string{"Name", "Size", "Progress", "Status"} {
		t.add(p.head, kit.SemanticNode{Role: kit.RoleColumnHeader, Name: name, Cell: kit.CellInfo{Col: c}})
	}
	p.r1 = t.add(p.grid, kit.SemanticNode{Role: kit.RoleRow, Name: "a.pdf, Completed", Cell: kit.CellInfo{Row: 1},
		Actions: kit.ActionSelect | kit.ActionAddToSelection | kit.ActionRemoveFromSelection | kit.ActionScrollIntoView})
	p.r2 = t.add(p.grid, kit.SemanticNode{Role: kit.RoleRow, Name: "b.pdf, Paused", Cell: kit.CellInfo{Row: 2},
		States: kit.NodeOffscreen, Actions: kit.ActionSelect | kit.ActionScrollIntoView})
	p.status = t.add(1, kit.SemanticNode{Role: kit.RoleStatus})
	p.statusText = t.add(p.status, kit.SemanticNode{Role: kit.RoleText, Name: "2 downloads"})
	p.slider = t.add(1, kit.SemanticNode{Role: kit.RoleSlider, Name: "Limit", Value: "40 KB/s",
		Range: kit.RangeValue{Valid: true, Min: 0, Max: 100, Value: 40, Step: 1}, Actions: kit.ActionSetRange})
	p.check = t.add(1, kit.SemanticNode{Role: kit.RoleSwitch, Name: "Start at login", Toggle: kit.ToggleOff, Actions: kit.ActionToggle})
	p.h = &fakeHost{t: t, fields: map[kit.NodeID]Ref{}}
	p.s = newSys()
	p.b = New(p.h, p.s)
	return p
}

func (p *page) id(r Ref) kit.NodeID { return p.s.made[r] }

func TestRoles(t *testing.T) {
	cases := []struct {
		n             kit.SemanticNode
		role, subrole string
	}{
		{kit.SemanticNode{Role: kit.RoleButton}, "AXButton", ""},
		{kit.SemanticNode{Role: kit.RoleButton, Toggle: kit.ToggleOn}, "AXCheckBox", "AXToggle"},
		{kit.SemanticNode{Role: kit.RoleButton, States: kit.NodeHasPopup}, "AXPopUpButton", ""},
		{kit.SemanticNode{Role: kit.RoleSwitch}, "AXCheckBox", "AXSwitch"},
		{kit.SemanticNode{Role: kit.RoleTextField, States: kit.NodeProtected}, "AXTextField", "AXSecureTextField"},
		{kit.SemanticNode{Role: kit.RoleMain}, "AXGroup", "AXLandmarkMain"},
		{kit.SemanticNode{Role: kit.RoleNavigation}, "AXGroup", "AXLandmarkNavigation"},
		{kit.SemanticNode{Role: kit.RoleTitleBar}, "AXGroup", "AXLandmarkBanner"},
		{kit.SemanticNode{Role: kit.RoleDialog}, "AXGroup", "AXApplicationDialog"},
		{kit.SemanticNode{Role: kit.RoleGrid}, "AXTable", ""},
		{kit.SemanticNode{Role: kit.RoleRow}, "AXRow", "AXTableRow"},
		{kit.SemanticNode{Role: kit.RoleColumnHeader}, "AXCell", ""},
		{kit.SemanticNode{Role: kit.RoleTab}, "AXRadioButton", "AXTabButton"},
		{kit.SemanticNode{Role: kit.RoleTabList}, "AXTabGroup", ""},
		{kit.SemanticNode{Role: kit.RoleComboBox}, "AXComboBox", ""},
		{kit.SemanticNode{Role: kit.RoleSpinButton}, "AXIncrementor", ""},
		{kit.SemanticNode{Role: kit.RoleMenuItemRadio}, "AXMenuItem", ""},
		{kit.SemanticNode{Role: kit.RoleProgressBar}, "AXProgressIndicator", ""},
		{kit.SemanticNode{Role: kit.RoleList}, "AXList", "AXContentList"},
		{kit.SemanticNode{Role: kit.RoleListBox}, "AXList", ""},
		// A <fieldset> (WebKit1 probe: AXGroup/AXFieldset, "group"), its radios in it too.
		{kit.SemanticNode{Role: kit.RoleGroup}, "AXGroup", "AXApplicationGroup"},
		{kit.SemanticNode{Role: kit.RoleGroup, Fieldset: true}, "AXGroup", "AXFieldset"},
		{kit.SemanticNode{Role: kit.RoleRadioGroup}, "AXRadioGroup", ""},
		{kit.SemanticNode{Role: kit.RoleRadioGroup, Fieldset: true}, "AXGroup", "AXFieldset"},
	}
	for _, c := range cases {
		r, s := RoleOf(&c.n)
		if r != c.role || s != c.subrole {
			t.Errorf("%v: %s/%s, want %s/%s", c.n.Role, r, s, c.role, c.subrole)
		}
	}
	// Every role but None maps to something AppKit knows.
	for r := kit.RoleWindow; r <= kit.RoleDocument; r++ {
		if role, _ := RoleOf(&kit.SemanticNode{Role: r}); role == RoleUnknown {
			t.Errorf("role %v has no AX role", r)
		}
	}
	if RoleDescription("AXGroup", "AXLandmarkMain") != "main" || RoleDescription("AXButton", "") != "" {
		t.Error("role descriptions")
	}
	if PriorityOf(kit.LiveAssertive) != PriorityHigh || PriorityOf(kit.LivePolite) != PriorityMedium || PriorityOf(kit.LiveOff) != 0 {
		t.Error("priorities")
	}
}

func TestIdentityAndNavigation(t *testing.T) {
	p := newPage()
	if p.b.Len() != 0 || p.s.next != 1000 {
		t.Fatal("elements made before a client asked")
	}
	kids := p.b.RootChildren(nil)
	if len(kids) != 5 {
		t.Fatalf("root children %d", len(kids))
	}
	again := p.b.RootChildren(nil)
	if !slices.Equal(kids, again) {
		t.Fatal("identity not stable")
	}
	if p.b.Parent(p.id(kids[0])) != container {
		t.Error("the root's children's parent is the container")
	}
	tbKids := p.b.Children(nil, p.tb)
	if p.b.Parent(p.id(tbKids[0])) != kids[0] {
		t.Error("parent of a title-bar button")
	}
	if p.b.Object(1) != container {
		t.Error("root object")
	}
	// A node gone: its element is destroyed, and reads fail softly.
	r := p.b.Object(p.check)
	p.t.remove(p.check)
	if _, ok := p.b.Info(p.check); ok {
		t.Fatal("info of a gone node")
	}
	if !p.s.dead[r] || p.b.Known(p.check) {
		t.Fatal("gone node not destroyed")
	}
	if p.b.Press(p.check) == nil || p.b.Parent(p.check) != 0 {
		t.Fatal("actions on a gone node")
	}
	// Closing destroys all, and nothing is made again.
	n := p.b.Len()
	p.b.Close()
	dead := 0
	for range p.s.dead {
		dead++
	}
	if dead != n+1 || p.b.Len() != 0 {
		t.Fatalf("close destroyed %d of %d", dead-1, n)
	}
	if p.b.Object(p.settings) != 0 || len(p.b.RootChildren(nil)) != 0 {
		t.Fatal("elements after close")
	}
}

func TestInfo(t *testing.T) {
	p := newPage()
	in, _ := p.b.Info(p.settings)
	if in.Role != "AXButton" || in.Label != "Settings" || in.Title != "Settings" || !in.Control || !in.Enabled || in.Actions&ActPress == 0 || in.Settable&SetFocused == 0 {
		t.Errorf("button %+v", in)
	}
	if in.Bounds != kit.R(800, 4, 830, 30) {
		t.Error("bounds")
	}
	in, _ = p.b.Info(p.statusText)
	if in.Role != "AXStaticText" || in.Value != ValueText || in.Text != "2 downloads" || in.Label != "" {
		t.Errorf("text %+v", in)
	}
	// A live region without a name stays unnamed, as WebKit leaves it: its text is its
	// child's value only (VoiceOver would read it twice).
	in, _ = p.b.Info(p.status)
	if in.Subrole != "AXApplicationStatus" || in.Label != "" || in.Title != "" {
		t.Errorf("status %+v", in)
	}
	if in, _ := p.b.Info(p.statusText); in.Title != "" || in.Label != "" {
		t.Errorf("text has a title or label %+v", in)
	}
	in, _ = p.b.Info(p.slider)
	if in.Value != ValueNumber || in.Number != 40 || !in.HasRange || in.Max != 100 || in.ValueDescription != "40 KB/s" ||
		in.Actions&(ActIncrement|ActDecrement) == 0 || in.Settable&SetValue == 0 {
		t.Errorf("slider %+v", in)
	}
	in, _ = p.b.Info(p.check)
	if in.Subrole != "AXSwitch" || in.Value != ValueNumber || in.Number != 0 || in.Actions&ActPress == 0 {
		t.Errorf("switch %+v", in)
	}
	in, _ = p.b.Info(p.grid)
	if !in.Table || in.Rows != 10001 || in.Cols != 4 || !in.Multiselect || in.Settable&SetSelectedRows == 0 {
		t.Errorf("grid %+v", in)
	}
	in, _ = p.b.Info(p.r1)
	if !in.Row || in.Index != 1 || in.Actions&ActScrollToVisible == 0 {
		t.Errorf("row %+v", in)
	}
	in, _ = p.b.Info(p.menu)
	if in.Role != "AXPopUpButton" || !in.Expandable || in.Expanded || in.Actions&ActShowMenu == 0 {
		t.Errorf("menu button %+v", in)
	}
	// Protected values never show.
	pw := p.t.add(1, kit.SemanticNode{Role: kit.RoleTextField, Value: "secret", States: kit.NodeProtected, Actions: kit.ActionSetValue})
	in, _ = p.b.Info(pw)
	if in.Text != "" || !in.Protected || in.Subrole != SubSecureField {
		t.Errorf("password %+v", in)
	}
	// Focus: the active descendant is focused.
	p.t.focus = p.r1
	if in, _ := p.b.Info(p.r1); !in.Focused {
		t.Error("focused row")
	}
}

func TestGrid(t *testing.T) {
	p := newPage()
	rows := p.b.Rows(nil, p.grid, false)
	if len(rows) != 3 {
		t.Fatalf("rows %d", len(rows))
	}
	if vis := p.b.Rows(nil, p.grid, true); len(vis) != 2 {
		t.Fatalf("visible rows %d", len(vis))
	}
	if h := p.b.ColumnHeaders(nil, p.grid); len(h) != 4 {
		t.Fatalf("headers %d", len(h))
	}
	// A cell of a row not laid out is made on demand, and keeps its element.
	c := p.b.Cell(p.grid, 2, 5000)
	if c == 0 {
		t.Fatal("no cell")
	}
	if p.b.Cell(p.grid, 2, 5000) != c {
		t.Fatal("cell identity")
	}
	in, _ := p.b.Info(p.id(c))
	if !in.Cell || in.RowLoc != 5000 || in.ColLoc != 2 || in.RowLen != 1 {
		t.Errorf("cell %+v", in)
	}
	if p.b.Cell(p.grid, 4, 0) != 0 || p.b.Cell(p.grid, 0, 10001) != 0 || p.b.Cell(p.grid, -1, 0) != 0 {
		t.Fatal("out of range cell")
	}
	// Selection includes rows not laid out.
	far := p.t.add(p.grid, kit.SemanticNode{Role: kit.RoleRow, Cell: kit.CellInfo{Row: 9000}})
	p.t.kids[p.grid] = p.t.kids[p.grid][:len(p.t.kids[p.grid])-1]
	p.t.sel[p.grid] = []kit.NodeID{p.r1, far}
	if s := p.b.Selection(nil, p.grid); len(s) != 2 || p.id(s[1]) != far {
		t.Fatal("selection")
	}
	// Setting the selected rows.
	if err := p.b.SetSelection(p.grid, []kit.NodeID{p.r2}); err != nil {
		t.Fatal(err)
	}
	want := []kit.ActionRequest{{Node: p.r2, Action: kit.ActionSelect}, {Node: p.r1, Action: kit.ActionRemoveFromSelection}}
	if !slices.Equal(p.t.performs, want) {
		t.Fatalf("selection performs %v", p.t.performs)
	}
}

func TestActions(t *testing.T) {
	p := newPage()
	// Press is posted, never done at once.
	if err := p.b.Press(p.settings); err != nil || len(p.h.posted) != 1 || p.h.posted[0].Action != kit.ActionInvoke ||
		len(p.t.performs) != 0 {
		t.Fatalf("press: %v %v", err, p.h.posted)
	}
	// A switch is toggled.
	p.h.posted = nil
	if err := p.b.Press(p.check); err != nil || p.h.posted[0].Action != kit.ActionToggle {
		t.Fatal("toggle")
	}
	// Disabled refuses.
	p.t.nodes[p.settings].States |= kit.NodeDisabled
	if err := p.b.Press(p.settings); !errors.Is(err, kit.ErrNotEnabled) {
		t.Fatalf("disabled press: %v", err)
	}
	// A plain group cannot be pressed.
	if err := p.b.Press(p.status); !errors.Is(err, kit.ErrUnsupported) {
		t.Fatalf("group press: %v", err)
	}
	// Increment/decrement step and clamp.
	if err := p.b.Step(p.slider, true); err != nil || p.t.performs[0].Number != 41 {
		t.Fatalf("increment %v %v", err, p.t.performs)
	}
	p.t.nodes[p.slider].Range.Value = 100
	p.t.performs = nil
	if err := p.b.Step(p.slider, true); err != nil || len(p.t.performs) != 0 {
		t.Fatal("increment at max")
	}
	if err := p.b.Step(p.slider, false); err != nil || p.t.performs[0].Number != 99 {
		t.Fatal("decrement")
	}
	// SetNumber refuses outside the range and NaN.
	if err := p.b.SetNumber(p.slider, 101); !errors.Is(err, ErrRefused) {
		t.Fatal("out of range")
	}
	if err := p.b.SetNumber(p.slider, nan()); !errors.Is(err, ErrRefused) {
		t.Fatal("NaN")
	}
	// SetText refuses read-only.
	if err := p.b.SetText(p.settings, "x"); err == nil {
		t.Fatal("set value on a button")
	}
	p.t.performs = nil
	if err := p.b.SetText(p.search, "pdf"); err != nil || p.t.performs[0].Text != "pdf" {
		t.Fatal("set text")
	}
	// Scroll to visible, even disabled.
	p.t.nodes[p.r2].States |= kit.NodeDisabled
	if err := p.b.ScrollToVisible(p.r2); err != nil {
		t.Fatal(err)
	}
	// Show menu expands, posted.
	p.h.posted = nil
	if err := p.b.ShowMenu(p.menu); err != nil || p.h.posted[0].Action != kit.ActionExpand {
		t.Fatal("show menu")
	}
	if err := p.b.ShowMenu(p.check); !errors.Is(err, kit.ErrUnsupported) {
		t.Fatal("show menu on a switch")
	}
	// Selected and expanded setters.
	p.t.performs = nil
	if err := p.b.SetSelected(p.r1, true); err != nil || p.t.performs[0].Action != kit.ActionSelect {
		t.Fatal("select")
	}
	p.h.posted = nil
	if err := p.b.SetExpanded(p.menu, true); err != nil || p.h.posted[0].Action != kit.ActionExpand {
		t.Fatal("expand")
	}
	if err := p.b.SetFocused(p.slider); !errors.Is(err, kit.ErrUnsupported) {
		t.Fatal("focus on an unfocusable node")
	}
}

func nan() float64 { var z float64; return z / z }

func TestEvents(t *testing.T) {
	p := newPage()
	r1 := p.b.Object(p.r1)
	grid := p.b.Object(p.grid)
	p.b.Info(p.r1)
	// Focus on a row: focus changed on it, selected rows changed on the grid.
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged, Node: p.r1})
	if !slices.Equal(p.s.posts, []post{{r1, FocusChanged}, {grid, SelectedRowsChanged}}) {
		t.Fatalf("focus posts %v", p.s.posts)
	}
	// Focus on the root or nothing posts nothing.
	p.s.posts = nil
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged, Node: 1})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged})
	if len(p.s.posts) != 0 {
		t.Fatal("focus on the root")
	}
	// Name change: title changed; selection change: selected rows on the grid.
	p.t.nodes[p.r1].Name = "a.pdf, Failed"
	p.t.nodes[p.r1].States |= kit.NodeSelected
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.r1})
	if !slices.Equal(p.s.posts, []post{{r1, TitleChanged}, {grid, SelectedRowsChanged}}) {
		t.Fatalf("change posts %v", p.s.posts)
	}
	// Unchanged: nothing.
	p.s.posts = nil
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.r1})
	if len(p.s.posts) != 0 {
		t.Fatal("posts without a change")
	}
	// Range value change.
	sl := p.b.Object(p.slider)
	p.b.Info(p.slider)
	p.t.nodes[p.slider].Range.Value = 50
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.slider})
	if !slices.Contains(p.s.posts, post{sl, ValueChanged}) {
		t.Fatal("range value change")
	}
	// Row count.
	p.b.Info(p.grid)
	p.t.nodes[p.grid].Grid.Rows = 10002
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.grid})
	if !slices.Contains(p.s.posts, post{grid, RowCountChanged}) {
		t.Fatal("row count change")
	}
	// Announce: polite medium, assertive high, empty dropped.
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticAnnounce, Text: "45%", Politeness: kit.LivePolite})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticAnnounce, Text: "Lost", Politeness: kit.LiveAssertive})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticAnnounce, Text: ""})
	if !slices.Equal(p.s.announced, []string{"1:45%", "2:Lost"}) {
		t.Fatalf("announced %v", p.s.announced)
	}
}

func TestLiveRegions(t *testing.T) {
	p := newPage()
	// Changes inside the status region are read out once per flush, once per new text,
	// also when no element was ever made for the node.
	p.t.nodes[p.statusText].Name = "3 downloads"
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.statusText})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.status})
	p.s.runLater()
	if !slices.Equal(p.s.announced, []string{"1:3 downloads"}) {
		t.Fatalf("live %v", p.s.announced)
	}
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.statusText})
	p.s.runLater()
	if len(p.s.announced) != 1 {
		t.Fatalf("same text read again: %v", p.s.announced)
	}
	// An alert is assertive.
	al := p.t.add(1, kit.SemanticNode{Role: kit.RoleAlert, Name: ""})
	msg := p.t.add(al, kit.SemanticNode{Role: kit.RoleText, Name: "Cannot reach Downex"})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: al})
	p.s.runLater()
	if p.s.announced[len(p.s.announced)-1] != "2:Cannot reach Downex" {
		t.Fatalf("alert %v", p.s.announced)
	}
	// The same alert again does not repeat ("alerts once").
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: msg})
	p.s.runLater()
	if len(p.s.announced) != 2 {
		t.Fatalf("alert repeated %v", p.s.announced)
	}
	// Rows outside a live region never announce.
	p.t.nodes[p.r1].Name = "a.pdf, 50%"
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.r1})
	p.s.runLater()
	if len(p.s.announced) != 2 {
		t.Fatal("a row announced")
	}
}

func TestChildrenChangedSweeps(t *testing.T) {
	p := newPage()
	kids := p.b.RootChildren(nil)
	p.b.Children(nil, p.grid)
	r2 := p.b.Object(p.r2)
	p.t.remove(p.r2)
	// The grid's rows come and go as it scrolls: no layout change (WebKit posts none).
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: p.grid})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: p.r1})
	p.s.runLater()
	for _, ps := range p.s.posts {
		if ps.n == LayoutChanged {
			t.Fatal("layout changed for the grid's rows")
		}
	}
	if !p.s.dead[r2] || p.b.Known(p.r2) {
		t.Fatal("removed row not destroyed")
	}
	if p.s.dead[kids[0]] {
		t.Fatal("living element destroyed")
	}
	// Elsewhere (a dialog opened): one layout change about the window per flush, however
	// many changes.
	p.s.posts = nil
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: 1})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: p.tb})
	p.b.Relayout()
	if len(p.s.posts) != 0 {
		t.Fatal("layout change posted before the flush")
	}
	p.s.runLater()
	if !slices.Equal(p.s.posts, []post{{container, LayoutChanged}}) {
		t.Fatalf("posts %v", p.s.posts)
	}
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: p.tb})
	p.s.runLater()
	if len(p.s.posts) != 2 {
		t.Fatalf("a later change: posts %v", p.s.posts)
	}
}

// TestApplicationGone: with nothing to run its later work, a bridge queues nothing, and
// works again once Later does.
func TestApplicationGone(t *testing.T) {
	p := newPage()
	p.b.RootChildren(nil)
	p.s.gone = true
	p.t.nodes[p.statusText].Name = "3 downloads"
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.statusText})
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticChildrenChanged, Node: 1})
	if len(p.b.liveDue) != 0 || p.b.liveWanted || p.b.sweepWanted || p.b.layoutWanted {
		t.Fatalf("work queued with nothing to run it: due %v live %v sweep %v layout %v",
			p.b.liveDue, p.b.liveWanted, p.b.sweepWanted, p.b.layoutWanted)
	}
	p.s.gone = false
	p.t.nodes[p.statusText].Name = "4 downloads"
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticNodeChanged, Node: p.statusText})
	p.s.runLater()
	if !slices.Equal(p.s.announced, []string{"1:4 downloads"}) {
		t.Fatalf("announced %v", p.s.announced)
	}
}

// TestLastingIdentity: a living node keeps its element however many a client makes; only
// nodes gone lose theirs.
func TestLastingIdentity(t *testing.T) {
	p := newPage()
	p.t.nodes[p.grid].Grid.Rows = 100000
	p.t.focus = p.r1
	first := p.b.Object(p.r1)
	settings := p.b.Object(p.settings)
	// A client walking a huge virtual grid cell by cell (VoiceOver's table navigation).
	const n = 3 * MaxElements
	cells := make([]Ref, n)
	for row := range n {
		if cells[row] = p.b.Cell(p.grid, 0, row); cells[row] == 0 {
			t.Fatal("cell")
		}
	}
	for row := range n {
		if p.b.Cell(p.grid, 0, row) != cells[row] {
			t.Fatalf("row %d's cell got a new object while it lives", row)
		}
	}
	if p.b.Object(p.r1) != first || p.b.Object(p.settings) != settings {
		t.Fatal("a laid-out node got a new object")
	}
	if len(p.s.dead) != 0 {
		t.Fatalf("%d living elements destroyed", len(p.s.dead))
	}
	// Nodes gone are dropped once the count doubles (every element dropped is destroyed).
	for row := range n / 2 {
		p.t.remove(p.t.cells[[3]int{int(p.grid), row, 0}])
	}
	for row := n; len(p.s.dead) < n/2; row++ {
		p.b.Cell(p.grid, 0, row)
		if row > 4*n {
			t.Fatalf("nodes gone never dropped: %d elements", p.b.Len())
		}
	}
	for row := n / 2; row < n; row++ {
		if p.s.dead[cells[row]] {
			t.Fatal("a living cell destroyed")
		}
	}
	for row := range n / 2 {
		if !p.s.dead[cells[row]] {
			t.Fatal("a cell gone not destroyed")
		}
	}
}

// TestBoundedOnDemand: past maxOnDemand, nodes off screen get no element (and none is
// replaced); nodes on screen and the focused node still do.
func TestBoundedOnDemand(t *testing.T) {
	old := maxOnDemand
	maxOnDemand = 64
	defer func() { maxOnDemand = old }()
	p := newPage()
	for row := 0; p.b.Len() < 64; row++ {
		p.t.Cell(p.grid, row, 0)
		id := p.t.cells[[3]int{int(p.grid), row, 0}]
		p.t.nodes[id].States |= kit.NodeOffscreen
		if p.b.Cell(p.grid, 0, row) == 0 {
			t.Fatal("cell under the bound")
		}
	}
	far := p.t.Cell(p.grid, 9000, 0)
	p.t.nodes[far].States |= kit.NodeOffscreen
	if p.b.Cell(p.grid, 0, 9000) != 0 || p.b.Stats.Refused != 1 {
		t.Fatal("an element off screen past the bound")
	}
	p.t.focus = far
	if p.b.Cell(p.grid, 0, 9000) == 0 {
		t.Fatal("the focused node has no element")
	}
	if p.b.Object(p.settings) == 0 {
		t.Fatal("a node on screen has no element")
	}
	if len(p.s.dead) != 0 {
		t.Fatal("a living element destroyed")
	}
}

func TestParity(t *testing.T) {
	p := newPage()
	menu := p.t.add(1, kit.SemanticNode{Role: kit.RoleMenu, Name: "Sort"})
	on := p.t.add(menu, kit.SemanticNode{Role: kit.RoleMenuItemCheckBox, Name: "Name", Toggle: kit.ToggleOn})
	off := p.t.add(menu, kit.SemanticNode{Role: kit.RoleMenuItemRadio, Name: "Dark", Toggle: kit.ToggleOff})
	plain := p.t.add(menu, kit.SemanticNode{Role: kit.RoleMenuItem, Name: "Open", Shortcut: "Meta+K"})
	// WebKit: checkable items have AXValue 1/0 and the mark while checked.
	if in, _ := p.b.Info(on); !in.Checkable || in.Value != ValueNumber || in.Number != 1 || in.Mark != "✓" || in.Title != "Name" {
		t.Errorf("checked item %+v", in)
	}
	if in, _ := p.b.Info(off); !in.Checkable || in.Value != ValueNumber || in.Number != 0 || in.Mark != "" {
		t.Errorf("unchecked item %+v", in)
	}
	if in, _ := p.b.Info(plain); in.Checkable || in.Value != ValueNone || in.Shortcut != "Meta+K" {
		t.Errorf("plain item %+v", in)
	}
	// Options: the name is the title alone, the value empty.
	lb := p.t.add(1, kit.SemanticNode{Role: kit.RoleListBox, Name: "Quality"})
	opt := p.t.add(lb, kit.SemanticNode{Role: kit.RoleOption, Name: "1080p", PosInSet: 1, SetSize: 2, States: kit.NodeSelected})
	if in, _ := p.b.Info(opt); in.Role != RoleStaticText || in.Title != "1080p" || in.Label != "" ||
		in.Value != ValueText || in.Text != "" || in.PosInSet != 1 || in.SetSize != 2 || !in.Selected {
		t.Errorf("option %+v", in)
	}
	// dl, dt, dd; role=group.
	dl := p.t.add(1, kit.SemanticNode{Role: kit.RoleList})
	p.t.add(dl, kit.SemanticNode{Role: kit.RoleTerm})
	dd := p.t.add(dl, kit.SemanticNode{Role: kit.RoleDefinition})
	if in, _ := p.b.Info(dl); in.Subrole != "AXDescriptionList" || in.RoleDescription != "description list" {
		t.Errorf("dl %+v", in)
	}
	if in, _ := p.b.Info(dd); in.Subrole != "AXDescription" || in.RoleDescription != "description" {
		t.Errorf("dd %+v", in)
	}
	ul := p.t.add(1, kit.SemanticNode{Role: kit.RoleList})
	p.t.add(ul, kit.SemanticNode{Role: kit.RoleListItem})
	if in, _ := p.b.Info(ul); in.Subrole != "AXContentList" {
		t.Errorf("ul %+v", in)
	}
	g := p.t.add(1, kit.SemanticNode{Role: kit.RoleGroup, Name: "Limits"})
	if in, _ := p.b.Info(g); in.Subrole != "AXApplicationGroup" || in.Title != "Limits" || in.Control {
		t.Errorf("group %+v", in)
	}
	bad := p.t.add(1, kit.SemanticNode{Role: kit.RoleTextField, Name: "Host", States: kit.NodeInvalid | kit.NodeBusy})
	if in, _ := p.b.Info(bad); !in.Invalid || !in.Control || !in.Busy {
		t.Errorf("invalid field %+v", in)
	}
}

func TestHostedFields(t *testing.T) {
	p := newPage()
	const field Ref = 55
	p.h.fields[p.search] = field
	kids := p.b.Children(nil, p.tb)
	n := 0
	for _, k := range kids {
		if k == field {
			n++
		}
		if p.id(k) == p.search {
			t.Fatal("the field's node shows besides the field")
		}
	}
	if n != 1 {
		t.Fatalf("field shows %d times", n)
	}
	if p.s.decorated[field] != p.b.Object(p.tb) {
		t.Fatal("field parent")
	}
	p.t.hit = p.search
	if p.b.HitTest(kit.Pt(400, 10)) != field {
		t.Fatal("hit test on the field")
	}
	if p.b.HitTest(kit.Pt(float32(nan()), 1)) != 0 {
		t.Fatal("NaN hit test")
	}
	// A hidden field (no field placed) leaves its node standing alone.
	delete(p.h.fields, p.search)
	kids = p.b.Children(nil, p.tb)
	if p.id(kids[1]) != p.search {
		t.Fatal("hidden field's node")
	}
	// Focus on a hosted field posts nothing of ours.
	p.h.fields[p.search] = field
	p.s.posts = nil
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticFocusChanged, Node: p.search})
	if len(p.s.posts) != 0 {
		t.Fatal("focus posted for a hosted field")
	}
	p.t.focus = p.search
	if p.b.Focused() != field {
		t.Fatal("focused field")
	}
}

func TestClosedTree(t *testing.T) {
	p := newPage()
	p.h.gone = true
	if p.b.Object(p.settings) != 0 || p.b.Focused() != 0 || p.b.HitTest(kit.Pt(1, 1)) != 0 {
		t.Fatal("objects without a tree")
	}
	if _, ok := p.b.Info(p.settings); ok {
		t.Fatal("info without a tree")
	}
	p.b.Notify(kit.SemanticEvent{Kind: kit.SemanticAnnounce, Text: "x"})
	if len(p.s.announced) != 0 {
		t.Fatal("announce without a tree")
	}
}

// TestColumns: a table's columns as WebKit lists AXColumn elements: one per column, each
// with the cell at its index of every row laid out (header row first) and its header.
func TestColumns(t *testing.T) {
	p := newPage()
	for c := 0; c < 4; c++ {
		p.t.add(p.r1, kit.SemanticNode{Role: kit.RoleCell, Name: fmt.Sprintf("r1c%d", c), Cell: kit.CellInfo{Row: 1, Col: c}})
	}
	// A row whose cells give no columns: they count in order.
	for c := 0; c < 4; c++ {
		wrap := p.t.add(p.r2, kit.SemanticNode{Role: kit.RoleNone})
		p.t.add(wrap, kit.SemanticNode{Role: kit.RoleCell, Name: fmt.Sprintf("r2c%d", c)})
	}
	if n := p.b.Columns(p.grid); n != 4 {
		t.Fatalf("columns %d", n)
	}
	if p.b.Columns(p.slider) != 0 {
		t.Fatal("a slider has columns")
	}
	for c, head := range []string{"Name", "Size", "Progress", "Status"} {
		cells := p.b.ColumnCells(nil, p.grid, c)
		var names []string
		for _, r := range cells {
			n, _ := p.t.Node(p.id(r))
			names = append(names, n.Name)
		}
		want := []string{head, fmt.Sprintf("r1c%d", c), fmt.Sprintf("r2c%d", c)}
		if !slices.Equal(names, want) {
			t.Errorf("column %d cells %q, want %q", c, names, want)
		}
		h := p.b.ColumnHeader(p.grid, c)
		if n, _ := p.t.Node(p.id(h)); n.Name != head {
			t.Errorf("column %d header %q", c, n.Name)
		}
	}
	if p.b.ColumnCells(nil, p.grid, 4) != nil || p.b.ColumnCells(nil, p.grid, -1) != nil || p.b.ColumnHeader(p.grid, 4) != 0 {
		t.Error("a column out of range has cells or a header")
	}
	// A table without a column count counts its headers.
	p.t.nodes[p.grid].Grid.Cols = 0
	if c := p.b.Columns(p.grid); c != 4 {
		t.Errorf("columns from the headers %d", c)
	}
}
