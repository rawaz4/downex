package widgets_test

import (
	"slices"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The Tabs.test.ts cases.

var views = []widgets.TabOption{
	{Value: "status", Label: "Status"},
	{Value: "limit", Label: "Speed limit", Dot: true},
	{Value: "gone", Label: "Gone", Disabled: true},
	{Value: "log", Label: "Log", Count: 3, HasCount: true},
}

type tabsRig struct {
	*rig
	tabs    *widgets.Tabs
	panel   *kit.Element
	changes []string
}

func setupTabs(t *testing.T, o widgets.TabsOptions) *tabsRig {
	t.Helper()
	tr := &tabsRig{panel: &kit.Element{}}
	tr.panel.Semantics.Role = kit.RoleTabPanel
	tr.panel.Style.Height = kit.Px(40)
	if o.AriaLabel == "" {
		o.AriaLabel = "Progress view"
	}
	o.Panel = tr.panel
	if o.IDPrefix == "" {
		o.IDPrefix = "pw"
	}
	tr.tabs = widgets.NewTabs(views, "status", o, func(v string) { tr.changes = append(tr.changes, v) })
	tr.rig = newRig(t, kit.Size{}, tr.tabs, tr.panel)
	return tr
}

func (tr *tabsRig) tab(value string) *kit.Element { return tr.tabs.TabWidget(value).Base() }

func TestTabs(t *testing.T) {
	t.Run("is a named tablist with a roving tabindex, aria-controls and stable ids", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		list := node(t, tr.tabs.Base())
		if list.Role != kit.RoleTabList || list.Name != "Progress view" {
			t.Fatalf("tablist %v %q", list.Role, list.Name)
		}
		if n := node(t, tr.tab("status")); n.States&kit.NodeSelected == 0 {
			t.Error("Status is not selected")
		}
		if n := node(t, tr.tab("limit")); n.States&kit.NodeSelected != 0 {
			t.Error("Speed limit is selected")
		}
		order := tr.win.TabOrder()
		if !slices.Contains(order, tr.tab("status")) || slices.Contains(order, tr.tab("limit")) {
			t.Error("the roving tabindex is wrong: only the chosen tab is in the tab order")
		}
		if n := node(t, tr.tab("limit")); n.Controls != tr.panel.NodeID() {
			t.Errorf("controls %v, want the panel %v", n.Controls, tr.panel.NodeID())
		}
		if n := node(t, tr.tab("status")); n.AutomationID != widgets.TabID("pw", "status") {
			t.Errorf("id %q", n.AutomationID)
		}
		if got := widgets.TabID("pw", "a b/c"); got != "pw-tab-a_b_c" {
			t.Errorf("TabID %q", got)
		}
		if r := byRole(t, tr.win, kit.RoleRadioButton); len(r) != 0 {
			t.Error("tabs are not radios")
		}
		if ts := byRole(t, tr.win, kit.RoleTab); len(ts) != 4 {
			t.Errorf("%d tabs", len(ts))
		}
	})
	t.Run("keeps each tab named by its label alone (the dot is hidden; the count is part of it)", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		if n := node(t, tr.tab("limit")); n.Name != "Speed limit" {
			t.Errorf("name %q", n.Name)
		}
		if n := node(t, tr.tab("log")); n.Name != "Log 3" {
			t.Errorf("name %q", n.Name)
		}
		tree := tr.win.Semantics()
		if kids := tree.AppendChildren(nil, tr.tab("limit").NodeID()); len(kids) != 0 {
			t.Errorf("a tab has %d children nodes (its parts are presentational)", len(kids))
		}
	})
	t.Run("moves and activates with the arrows, Home and End, skipping disabled tabs", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		tr.win.Focus(tr.tab("status"))
		steps := []struct {
			k     kit.Key
			focus string
		}{
			{kit.KeyArrowRight, "limit"}, {kit.KeyArrowRight, "log"}, {kit.KeyArrowRight, "status"},
			{kit.KeyArrowLeft, "log"}, {kit.KeyHome, "status"}, {kit.KeyEnd, "log"},
		}
		for _, st := range steps {
			tr.key(st.k, 0)
			if !tr.tab(st.focus).Focused() || tr.tabs.Value != st.focus {
				t.Fatalf("after %v: value %q, want %q focused", st.k, tr.tabs.Value, st.focus)
			}
		}
		if slices.Contains(tr.changes, "gone") {
			t.Error("the disabled tab was chosen")
		}
		if !slices.Contains(tr.win.TabOrder(), tr.tab("log")) {
			t.Error("the chosen tab is not the one Tab reaches")
		}
		tr.click(tr.tab("limit"))
		if tr.changes[len(tr.changes)-1] != "limit" {
			t.Errorf("click: %q", tr.changes)
		}
	})
	t.Run("marks the chosen tab with one indicator under its label", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		bar := tr.tabs.Indicator()
		label := labelOf(tr.tab("status")).WindowBounds()
		tb := tr.tabs.Base().WindowBounds()
		if bar.Empty() || bar.Min.X+tb.Min.X != label.Min.X || bar.Width() != label.Width() || bar.Height() != 2 || bar.Max.Y != tb.Height() {
			t.Errorf("indicator %v under a label at %v", bar, label)
		}
	})
	t.Run("slides its one indicator under the chosen label", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		tr.click(tr.tab("limit"))
		tr.wait(200e6)
		bar := tr.tabs.Indicator()
		label := labelOf(tr.tab("limit")).WindowBounds()
		tb := tr.tabs.Base().WindowBounds()
		if bar.Min.X+tb.Min.X != label.Min.X || bar.Width() != label.Width() {
			t.Errorf("indicator %v under a label at %v", bar, label)
		}
	})
	t.Run("keeps every tab as wide as its label in bold, chosen or not", func(t *testing.T) {
		tr := setupTabs(t, widgets.TabsOptions{})
		before := tr.tab("limit").Size().W
		tr.click(tr.tab("limit"))
		if after := tr.tab("limit").Size().W; after != before {
			t.Errorf("the tab went from %v to %v wide", before, after)
		}
	})
}

func TestTabsFlush(t *testing.T) {
	tr := setupTabs(t, widgets.TabsOptions{Small: true, Flush: true})
	if m := tr.tabs.Base().Style.Margin.Start; m != kit.Px(-10) {
		t.Errorf("the row reaches %v into the padding, want -10px", m)
	}
	var pads []kit.Edges
	for _, v := range []string{"status", "limit", "gone", "log"} {
		pads = append(pads, tr.tab(v).Style.Padding)
	}
	for _, p := range pads {
		if p != pads[0] {
			t.Fatalf("the tabs differ in padding: %v", pads)
		}
	}
}

// labelOf is a tab's label (the deepest element with text).
func labelOf(e *kit.Element) *kit.Element {
	return find(e, func(x *kit.Element) bool {
		l, ok := x.Widget().(*kit.Label)
		return ok && l.Text() != ""
	})
}
