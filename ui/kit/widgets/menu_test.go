package widgets_test

import (
	"slices"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The ContextMenu.test.ts and MenuButton.test.ts cases. The menu is a popup window; the
// owner's window keeps the keyboard and hands the menu its keys, so "document.activeElement"
// is the menu's focused item (Menu.Active) and the keys are sent to the owner's window.

var contextItems = []widgets.MenuItem{
	{ID: "open", Label: "Open", Disabled: true},
	{ID: "folder", Label: "Open folder"},
	{ID: "pause", Label: "Pause", Separated: true},
	{ID: "remove", Label: "Remove…", Danger: true},
}

type menuRig struct {
	*rig
	anchor   *widgets.Button
	m        *widgets.Menu
	selected []string
	closes   int
}

func openContextMenu(t *testing.T, o widgets.MenuOptions) *menuRig {
	t.Helper()
	mr := &menuRig{}
	mr.anchor = widgets.NewButton("Unlimited", "", widgets.Secondary, widgets.SizeMd, nil)
	mr.rig = newRig(t, kit.Size{}, mr.anchor)
	o.OnSelect = func(id string) { mr.selected = append(mr.selected, id) }
	o.OnClose = func() { mr.closes++ }
	m, err := widgets.OpenMenu(mr.anchor, o)
	if err != nil {
		t.Fatal(err)
	}
	mr.m = m
	mr.settle()
	return mr
}

// item is the menu item named name (its name from content, which starts with its label).
func (mr *menuRig) item(name string) kit.SemanticNode {
	mr.t.Helper()
	for _, role := range []kit.Role{kit.RoleMenuItem, kit.RoleMenuItemRadio, kit.RoleMenuItemCheckBox} {
		for _, n := range byRole(mr.t, mr.m.Root(), role) {
			if n.Name == name || strings.HasPrefix(n.Name, name+" ") {
				return n
			}
		}
	}
	mr.t.Fatalf("no menu item %q", name)
	return kit.SemanticNode{}
}

func (mr *menuRig) clickItem(name string) {
	mr.host.Top().Click(mr.item(name).Bounds.Center())
	mr.settle()
}

func TestContextMenu(t *testing.T) {
	t.Run("focuses the first enabled item and moves with the arrow keys", func(t *testing.T) {
		mr := openContextMenu(t, widgets.MenuOptions{Items: contextItems, Label: "Download actions", At: kit.Pt(10, 10)})
		menus := byRole(t, mr.m.Root(), kit.RoleMenu)
		if len(menus) != 1 || menus[0].Name != "Download actions" {
			t.Fatalf("menus %v", menus)
		}
		steps := []struct {
			do   func()
			want string
		}{
			{func() {}, "Open folder"},
			{func() { mr.key(kit.KeyArrowDown, 0) }, "Pause"},
			{func() { mr.key(kit.KeyEnd, 0) }, "Remove…"},
			{func() { mr.key(kit.KeyArrowDown, 0) }, "Open folder"},
			{func() { mr.typeText("p") }, "Pause"},
		}
		for i, st := range steps {
			st.do()
			if got := mr.m.Active(); got != st.want {
				t.Fatalf("step %d: focus on %q, want %q", i, got, st.want)
			}
		}
		if f := byRole(t, mr.m.Root(), kit.RoleMenuItem); !slices.ContainsFunc(f, func(n kit.SemanticNode) bool { return n.States&kit.NodeFocused != 0 && n.Name == "Pause" }) {
			t.Error("the focused item's node is not focused")
		}
	})
	// The popup shows the menu to UI Automation itself; the owner's key proxy stays out of
	// the owner's tree (before, it stood for the focused item: each item appeared twice, the
	// second time as a 0 x 0 item of the owner).
	t.Run("shows the focused item only in the menu, never again in the owner", func(t *testing.T) {
		mr := openContextMenu(t, widgets.MenuOptions{Items: contextItems, Label: "Download actions", At: kit.Pt(10, 10)})
		mr.key(kit.KeyArrowDown, 0)
		for _, role := range []kit.Role{kit.RoleMenu, kit.RoleMenuItem, kit.RoleMenuItemRadio, kit.RoleMenuItemCheckBox} {
			if n := byRole(t, mr.win, role); len(n) != 0 {
				t.Errorf("the owner's tree has %d nodes of role %v while the menu is open: %v", len(n), role, n)
			}
		}
		if got := mr.m.Active(); got != "Pause" {
			t.Fatalf("focus on %q, want Pause", got)
		}
	})
	t.Run("activates enabled items only and closes on Escape or an outside press", func(t *testing.T) {
		mr := openContextMenu(t, widgets.MenuOptions{Items: contextItems, Label: "Download actions", At: kit.Pt(10, 10)})
		mr.clickItem("Open")
		if len(mr.selected) != 0 {
			t.Fatalf("the disabled item was chosen: %q", mr.selected)
		}
		mr.clickItem("Pause")
		if !slices.Equal(mr.selected, []string{"pause"}) {
			t.Fatalf("selected %q", mr.selected)
		}
		mr.key(kit.KeyEscape, 0)
		if mr.closes != 1 {
			t.Fatalf("Escape: %d closes", mr.closes)
		}
		mr.press(kit.Pt(900, 600))
		if mr.closes != 2 {
			t.Errorf("an outside press: %d closes", mr.closes)
		}
	})
	t.Run("draws radio and checkbox items, notes, headings and a footnote", func(t *testing.T) {
		mr := openContextMenu(t, widgets.MenuOptions{Label: "More actions", Footnote: "Saved on this computer right away.", Items: []widgets.MenuItem{
			{ID: "comfortable", Label: "Comfortable", Kind: widgets.ItemRadio, Checked: true, Heading: "Density"},
			{ID: "compact", Label: "Compact", Kind: widgets.ItemRadio, Note: "18 rows"},
			{ID: "pane", Label: "Details pane", Kind: widgets.ItemCheckbox, Checked: true, Separated: true},
			{ID: "queues", Label: "Manage queues…", Icon: "queue"},
		}})
		if n := mr.item("Comfortable"); n.Role != kit.RoleMenuItemRadio || n.Toggle != kit.ToggleOn {
			t.Errorf("Comfortable %v %v", n.Role, n.Toggle)
		}
		if n := mr.item("Compact"); n.Toggle != kit.ToggleOff || !strings.Contains(n.Name, "18 rows") {
			t.Errorf("Compact %q %v", n.Name, n.Toggle)
		}
		if n := mr.item("Details pane"); n.Role != kit.RoleMenuItemCheckBox || n.Toggle != kit.ToggleOn {
			t.Errorf("Details pane %v %v", n.Role, n.Toggle)
		}
		groups := byRole(t, mr.m.Root(), kit.RoleGroup)
		if len(groups) != 1 || groups[0].Name != "Density" {
			t.Fatalf("groups %v", groups)
		}
		radiosInGroup := 0
		tree := mr.m.Root().Semantics()
		for _, c := range tree.AppendChildren(nil, groups[0].ID) {
			if n, _ := tree.Node(c); n.Role == kit.RoleMenuItemRadio {
				radiosInGroup++
			}
		}
		if radiosInGroup != 2 {
			t.Errorf("%d radios in the group", radiosInGroup)
		}
		if seps := byRole(t, mr.m.Root(), kit.RoleSeparator); len(seps) != 2 {
			t.Errorf("%d separators", len(seps))
		}
		if menu := byRole(t, mr.m.Root(), kit.RoleMenu)[0]; menu.Description != "Saved on this computer right away." {
			t.Errorf("described by %q", menu.Description)
		}
		if mr.m.Active() != "Comfortable" {
			t.Fatalf("first focus %q", mr.m.Active())
		}
		mr.key(kit.KeyEnd, 0)
		if mr.m.Active() != "Manage queues…" {
			t.Fatalf("End: %q", mr.m.Active())
		}
		mr.key(kit.KeyArrowUp, 0)
		if mr.m.Active() != "Details pane" {
			t.Errorf("ArrowUp: %q", mr.m.Active())
		}
	})
	t.Run("opens next to its anchor, above it when there is no room below, and ignores presses on it", func(t *testing.T) {
		// a trigger in the status bar, at the bottom end of a 1024 × 768 window; a 220 × 120 menu
		at := widgets.PlaceMenuAnchored(kit.R(900, 760, 1000, 784), widgets.AlignToEnd, widgets.OpenDown, false, kit.Size{W: 220, H: 120}, kit.Size{W: 1024, H: 768})
		if at.Y != 760-4-120 || at.X != min(1000-220, 1024-220-8) {
			t.Errorf("placed at %v", at)
		}
		mr := openContextMenu(t, widgets.MenuOptions{Label: "Global speed limit", Initial: widgets.FocusLast, Items: []widgets.MenuItem{
			{ID: "a", Label: "Unlimited"}, {ID: "b", Label: "10 MiB/s"},
		}})
		mr.m.Close()
		m, err := widgets.OpenMenu(mr.anchor, widgets.MenuOptions{Label: "Global speed limit", Initial: widgets.FocusLast,
			Anchor:  &widgets.MenuAnchor{El: mr.anchor, Align: widgets.AlignToEnd},
			Items:   []widgets.MenuItem{{ID: "a", Label: "Unlimited"}, {ID: "b", Label: "10 MiB/s"}},
			OnClose: func() { mr.closes++ },
		})
		if err != nil {
			t.Fatal(err)
		}
		mr.m = m
		mr.settle()
		if m.Active() != "10 MiB/s" {
			t.Fatalf("initial focus %q", m.Active())
		}
		mr.host.Press(mr.anchor.Base().WindowBounds().Center())
		if mr.closes != 0 {
			t.Fatal("a press on the anchor closed it")
		}
		mr.press(kit.Pt(900, 600))
		if mr.closes != 1 {
			t.Errorf("an outside press: %d closes", mr.closes)
		}
	})
	t.Run("a point menu stays 4 px inside the window", func(t *testing.T) {
		at := widgets.PlaceMenuAt(kit.Pt(1000, 700), kit.Size{W: 220, H: 120}, kit.Size{W: 1024, H: 768})
		if at.X != 1024-220-4 || at.Y != 768-120-4 {
			t.Errorf("placed at %v", at)
		}
	})
}

var limitItems = []widgets.MenuItem{
	{ID: "0", Label: "Unlimited", Kind: widgets.ItemRadio, Checked: true, Heading: "Global speed limit"},
	{ID: "20", Label: "20 MiB/s", Kind: widgets.ItemRadio},
	{ID: "10", Label: "10 MiB/s", Kind: widgets.ItemRadio},
	{ID: "custom", Label: "Custom…", Separated: true},
}

type menuButtonRig struct {
	*rig
	trigger  *widgets.Button
	mb       *widgets.MenuButton
	selected []string
	toggles  []bool
}

func setupMenuButton(t *testing.T, disabled bool) *menuButtonRig {
	t.Helper()
	r := &menuButtonRig{}
	r.trigger = widgets.NewButton("Unlimited", "", widgets.Ghost, widgets.SizeMd, nil)
	r.mb = widgets.NewMenuButton(r.trigger, "Global speed limit", limitItems, func(id string) { r.selected = append(r.selected, id) })
	r.mb.Footnote = "Per-download and queue limits still apply."
	r.mb.Direction = widgets.OpenUp
	r.mb.Align = widgets.AlignToEnd
	r.mb.OnToggle = func(open bool) { r.toggles = append(r.toggles, open) }
	r.rig = newRig(t, kit.Size{}, r.mb)
	if disabled {
		r.mb.SetDisabled(true)
	}
	r.settle()
	return r
}

func (r *menuButtonRig) menu() *widgets.Menu { return r.mb.Menu() }

func TestMenuButton(t *testing.T) {
	t.Run("opens its menu on click with the focus on the first item, and closes back to the trigger", func(t *testing.T) {
		r := setupMenuButton(t, false)
		n := node(t, r.trigger.Base())
		if n.States&kit.NodeHasPopup == 0 || n.Expand != kit.ExpandClosed {
			t.Fatalf("trigger %b %v", n.States, n.Expand)
		}
		r.click(r.trigger.Base())
		if r.menu() == nil || node(t, r.trigger.Base()).Expand != kit.ExpandOpen || !slices.Equal(r.toggles, []bool{true}) {
			t.Fatalf("not open: %v", r.toggles)
		}
		if got := r.menu().Active(); got != "Unlimited" {
			t.Fatalf("focus %q", got)
		}
		r.key(kit.KeyEscape, 0)
		if r.menu() != nil || !r.trigger.Base().Focused() || r.toggles[len(r.toggles)-1] {
			t.Errorf("Escape: open %v trigger focused %v", r.menu() != nil, r.trigger.Base().Focused())
		}
	})
	t.Run("opens with Enter, Space and ArrowDown on the first item, and ArrowUp on the last", func(t *testing.T) {
		for _, c := range []struct {
			k    kit.Key
			want string
		}{{kit.KeyEnter, "Unlimited"}, {kit.KeySpace, "Unlimited"}, {kit.KeyArrowDown, "Unlimited"}, {kit.KeyArrowUp, "Custom…"}} {
			r := setupMenuButton(t, false)
			r.win.Focus(r.trigger)
			r.key(c.k, 0)
			if r.menu() == nil || r.menu().Active() != c.want {
				t.Errorf("%v: %v", c.k, r.menu() != nil)
			}
		}
	})
	t.Run("has radio items under a heading, and a footnote that describes it", func(t *testing.T) {
		r := setupMenuButton(t, false)
		r.click(r.trigger.Base())
		root := r.menu().Root()
		var checked []kit.ToggleState
		for _, n := range byRole(t, root, kit.RoleMenuItemRadio) {
			checked = append(checked, n.Toggle)
		}
		if !slices.Equal(checked, []kit.ToggleState{kit.ToggleOn, kit.ToggleOff, kit.ToggleOff}) {
			t.Errorf("radios %v", checked)
		}
		if g := byRole(t, root, kit.RoleGroup); len(g) != 1 || g[0].Name != "Global speed limit" {
			t.Errorf("group %v", g)
		}
		if items := byRole(t, root, kit.RoleMenuItem); len(items) != 1 || items[0].Name != "Custom…" {
			t.Errorf("items %v", items)
		}
		if m := byRole(t, root, kit.RoleMenu)[0]; m.Description != "Per-download and queue limits still apply." {
			t.Errorf("footnote %q", m.Description)
		}
	})
	t.Run("chooses an item: the menu closes, the focus goes back, then the choice is told", func(t *testing.T) {
		r := setupMenuButton(t, false)
		r.click(r.trigger.Base())
		mr := &menuRig{rig: r.rig, m: r.menu()}
		mr.clickItem("10 MiB/s")
		if !slices.Equal(r.selected, []string{"10"}) || r.menu() != nil || !r.trigger.Base().Focused() {
			t.Errorf("selected %q open %v focused %v", r.selected, r.menu() != nil, r.trigger.Base().Focused())
		}
	})
	t.Run("toggles from its trigger, and closes on an outside press", func(t *testing.T) {
		r := setupMenuButton(t, false)
		r.click(r.trigger.Base())
		r.host.Press(r.trigger.Base().WindowBounds().Center()) // the trigger's own press
		if r.menu() == nil {
			t.Fatal("the press on the trigger closed it")
		}
		r.click(r.trigger.Base())
		if r.menu() != nil {
			t.Fatal("a click on the trigger left it open")
		}
		r.click(r.trigger.Base())
		r.press(kit.Pt(900, 600))
		if r.menu() != nil {
			t.Error("an outside press left it open")
		}
	})
	t.Run("stays closed while unavailable but keeps the focus", func(t *testing.T) {
		r := setupMenuButton(t, true)
		if n := node(t, r.trigger.Base()); n.States&kit.NodeDisabled == 0 {
			t.Error("not disabled")
		}
		if !r.win.Focus(r.trigger) {
			t.Fatal("an unavailable trigger cannot take the focus")
		}
		r.click(r.trigger.Base())
		r.key(kit.KeyArrowDown, 0)
		if r.menu() != nil {
			t.Error("it opened")
		}
	})
	t.Run("can be opened by its owner on the checked item", func(t *testing.T) {
		r := setupMenuButton(t, false)
		r.mb.OpenMenu(widgets.FocusChecked)
		r.settle()
		if r.menu() == nil || r.menu().Active() != "Unlimited" {
			t.Error("not on the checked item")
		}
	})
}

func TestSplitButton(t *testing.T) {
	t.Run("runs its action from the main half and offers the other ways from the chevron", func(t *testing.T) {
		clicks := 0
		var selected []string
		s := widgets.NewSplitButton("Add", "add", func() { clicks++ }, []widgets.MenuItem{
			{ID: "files", Label: "Add downloads…", Shortcut: "Ctrl+N"},
			{ID: "stream", Label: "Add video or stream…"},
		}, func(id string) { selected = append(selected, id) }, "More ways to add", "Add")
		s.Main.Tooltip = "Add downloads (Ctrl+N)"
		s.Main.Semantics.Shortcut = "Control+N"
		r := newRig(t, kit.Size{}, s)
		if n := node(t, s.Main.Base()); n.Name != "Add" || n.Shortcut != "Control+N" {
			t.Errorf("main %q %q", n.Name, n.Shortcut)
		}
		r.click(s.Main.Base())
		if clicks != 1 {
			t.Fatalf("%d clicks", clicks)
		}
		more := s.More.Trigger.Base()
		if n := node(t, more); n.Name != "More ways to add" || n.States&kit.NodeHasPopup == 0 {
			t.Errorf("more %q %b", n.Name, n.States)
		}
		r.click(more)
		mr := &menuRig{rig: r, m: s.More.Menu()}
		if mr.m == nil {
			t.Fatal("no menu")
		}
		if n := mr.item("Add downloads…"); n.Shortcut != "Ctrl+N" {
			t.Errorf("shortcut %q", n.Shortcut)
		}
		mr.clickItem("Add video or stream…")
		if !slices.Equal(selected, []string{"stream"}) || !more.Focused() {
			t.Errorf("selected %q, focus on more %v", selected, more.Focused())
		}
	})
	t.Run("does nothing while unavailable", func(t *testing.T) {
		clicks := 0
		s := widgets.NewSplitButton("Add", "", func() { clicks++ }, []widgets.MenuItem{{ID: "x", Label: "X"}}, func(string) {}, "More ways to add", "Add")
		r := newRig(t, kit.Size{}, s)
		s.SetDisabled(true)
		r.settle()
		r.click(s.Main.Base())
		r.click(s.More.Trigger.Base())
		if clicks != 0 || s.More.Menu() != nil {
			t.Errorf("%d clicks, menu %v", clicks, s.More.Menu() != nil)
		}
	})
}
