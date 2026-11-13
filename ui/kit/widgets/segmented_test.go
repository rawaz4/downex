package widgets_test

import (
	"slices"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The SegmentedControl.test.ts cases (its Switch ones are TestSwitch), and the check box and
// radio rows of the dialogs.

var modes = []widgets.SegmentOption{
	{Value: "files", Label: "Files", Icon: "link"},
	{Value: "stream", Label: "Video or stream", Icon: "play"},
}

type segRig struct {
	*rig
	s       *widgets.SegmentedControl
	changes []string
}

func setupSeg(t *testing.T, opts []widgets.SegmentOption, value string, o widgets.SegmentedOptions, size kit.Size) *segRig {
	t.Helper()
	sr := &segRig{}
	sr.s = widgets.NewSegmentedControl(opts, value, o, func(v string) { sr.changes = append(sr.changes, v) })
	sr.rig = newRig(t, size, sr.s)
	return sr
}

func (sr *segRig) radios() []kit.SemanticNode { return byRole(sr.t, sr.win, kit.RoleRadioButton) }

func (sr *segRig) radio(name string) kit.SemanticNode {
	sr.t.Helper()
	for _, n := range sr.radios() {
		if n.Name == name || strings.HasPrefix(n.Name, name+" ") {
			return n
		}
	}
	sr.t.Fatalf("no radio %q in %v", name, sr.radios())
	return kit.SemanticNode{}
}

func TestSegmentedControlRadios(t *testing.T) {
	t.Run("is a named group of radios, without radio dots", func(t *testing.T) {
		sr := setupSeg(t, modes, "files", widgets.SegmentedOptions{Legend: "Add mode"}, kit.Size{})
		g := byRole(t, sr.win, kit.RoleRadioGroup)
		if len(g) != 1 || g[0].Name != "Add mode" {
			t.Fatalf("groups %v", g)
		}
		if sr.radio("Files").States&kit.NodeSelected == 0 || sr.radio("Video or stream").States&kit.NodeSelected != 0 {
			t.Error("the chosen segment is not the selected radio")
		}
	})
	t.Run("changes with the radios and tells its consumer", func(t *testing.T) {
		sr := setupSeg(t, modes, "files", widgets.SegmentedOptions{Legend: "Add mode"}, kit.Size{})
		sr.clickAt(sr.radio("Video or stream").Bounds.Center())
		if !slices.Equal(sr.changes, []string{"stream"}) || sr.radio("Video or stream").States&kit.NodeSelected == 0 || sr.radio("Files").States&kit.NodeSelected != 0 {
			t.Errorf("changes %q", sr.changes)
		}
	})
	t.Run("can be named by aria-label, keeps disabled options out, and shows counts", func(t *testing.T) {
		sr := setupSeg(t, []widgets.SegmentOption{
			{Value: "system", Label: "System"},
			{Value: "light", Label: "Light", Count: 2, HasCount: true},
			{Value: "dark", Label: "Dark", Disabled: true},
		}, "light", widgets.SegmentedOptions{AriaLabel: "Theme"}, kit.Size{})
		if g := byRole(t, sr.win, kit.RoleRadioGroup); len(g) != 1 || g[0].Name != "Theme" {
			t.Fatalf("groups %v", g)
		}
		if sr.radio("Dark").States&kit.NodeDisabled == 0 {
			t.Error("Dark is not disabled")
		}
		if n := sr.radio("Light"); !strings.Contains(n.Name, "2") {
			t.Errorf("Light %q has no count", n.Name)
		}
		sr.clickAt(sr.radio("Dark").Bounds.Center())
		if len(sr.changes) != 0 {
			t.Error("a disabled segment was chosen")
		}
	})
	t.Run("moves the choice with the arrows, wrapping, past disabled ones", func(t *testing.T) {
		sr := setupSeg(t, []widgets.SegmentOption{
			{Value: "a", Label: "A"}, {Value: "b", Label: "B", Disabled: true}, {Value: "c", Label: "C"},
		}, "a", widgets.SegmentedOptions{AriaLabel: "Letters"}, kit.Size{})
		sr.win.Focus(sr.win.TabOrder()[0])
		sr.key(kit.KeyArrowRight, 0)
		sr.key(kit.KeyArrowRight, 0)
		sr.key(kit.KeyArrowLeft, 0)
		if !slices.Equal(sr.changes, []string{"c", "a", "c"}) {
			t.Errorf("changes %q", sr.changes)
		}
	})
}

func TestSegmentedControlPicking(t *testing.T) {
	t.Run("is never a tablist: switching views is Tabs", func(t *testing.T) {
		sr := setupSeg(t, modes, "files", widgets.SegmentedOptions{Legend: "Add mode"}, kit.Size{})
		if len(byRole(t, sr.win, kit.RoleTabList)) != 0 || len(byRole(t, sr.win, kit.RoleTab)) != 0 || len(sr.radios()) != 2 {
			t.Error("not a radio group")
		}
	})
	t.Run("gives every segment the same width, so the thumb never changes size", func(t *testing.T) {
		sr := setupSeg(t, []widgets.SegmentOption{{Value: "comfortable", Label: "Comfortable"}, {Value: "compact", Label: "Compact"}},
			"comfortable", widgets.SegmentedOptions{Legend: "List density"}, kit.Size{})
		rs := sr.radios()
		if rs[0].Bounds.Width() != rs[1].Bounds.Width() || rs[0].Bounds.Width() <= 0 {
			t.Errorf("widths %v %v", rs[0].Bounds.Width(), rs[1].Bounds.Width())
		}
	})
	opts := []widgets.SegmentOption{{Value: "system", Label: "System"}, {Value: "none", Label: "No proxy"}, {Value: "manual", Label: "Manual"}, {Value: "pac", Label: "PAC script"}}
	t.Run("offers a Select named like the group for narrow places, which picks the same value", func(t *testing.T) {
		// 24rem is 384 DIPs at the default text size: a 300 DIP wide control is narrow
		sr := setupSeg(t, opts, "system", widgets.SegmentedOptions{Legend: "How downloads connect", Fill: true, SelectWhenNarrow: true}, kit.Size{W: 340, H: 400})
		combos := byRole(t, sr.win, kit.RoleComboBox)
		if len(combos) != 1 || combos[0].Name != "How downloads connect" || combos[0].Value != "System" {
			t.Fatalf("combos %v", combos)
		}
		sel := sr.s.Narrow()
		sr.click(sel.Base())
		os := byRole(t, sel.List(), kit.RoleOption)
		sr.host.Top().Click(os[2].Bounds.Center())
		sr.settle()
		if sr.changes[len(sr.changes)-1] != "manual" || sr.s.Value != "manual" {
			t.Fatalf("changes %q value %q", sr.changes, sr.s.Value)
		}
		if len(sr.radios()) != 0 {
			t.Error("the hidden segments are still in the tree")
		}
	})
	t.Run("shows the segments where there is room, and they keep the group's radios", func(t *testing.T) {
		sr := setupSeg(t, opts, "manual", widgets.SegmentedOptions{Legend: "How downloads connect", Fill: true, SelectWhenNarrow: true}, kit.Size{})
		if len(byRole(t, sr.win, kit.RoleComboBox)) != 0 {
			t.Error("the narrow Select shows in a wide place")
		}
		if len(sr.radios()) != 4 || sr.radio("Manual").States&kit.NodeSelected == 0 {
			t.Errorf("radios %v", sr.radios())
		}
	})
	t.Run("has no Select unless asked for one", func(t *testing.T) {
		sr := setupSeg(t, modes, "files", widgets.SegmentedOptions{Legend: "Add mode"}, kit.Size{W: 300, H: 300})
		if len(byRole(t, sr.win, kit.RoleComboBox)) != 0 || sr.s.Narrow() != nil {
			t.Error("a Select appeared")
		}
	})
}

func TestSwitch(t *testing.T) {
	t.Run("is a labelled switch row: the label names it, the hint describes it, the row toggles it", func(t *testing.T) {
		var got []bool
		row := widgets.NewSwitchRow("Launch at login", "Starts the tray icon when you sign in", false, func(v bool) { got = append(got, v) })
		r := newRig(t, kit.Size{}, row)
		n := node(t, row.Switch.Base())
		if n.Role != kit.RoleSwitch || n.Name != "Launch at login" || n.Toggle != kit.ToggleOff || n.Description != "Starts the tray icon when you sign in" {
			t.Fatalf("switch %v %q %v %q", n.Role, n.Name, n.Toggle, n.Description)
		}
		hint := find(row.Base(), func(e *kit.Element) bool {
			l, ok := e.Widget().(*kit.Label)
			return ok && l.Text() == "Starts the tray icon when you sign in"
		})
		r.click(hint)
		if !row.Switch.On || node(t, row.Switch.Base()).Toggle != kit.ToggleOn || !slices.Equal(got, []bool{true}) {
			t.Fatalf("after a click on the hint: on %v, changes %v", row.Switch.On, got)
		}
		r.click(row.Switch.Base())
		if !slices.Equal(got, []bool{true, false}) {
			t.Errorf("after a click on the switch: %v", got)
		}
		r.win.Focus(row.Switch)
		r.key(kit.KeySpace, 0)
		if !slices.Equal(got, []bool{true, false, true}) {
			t.Errorf("after Space: %v", got)
		}
	})
	t.Run("is a disabled control while unavailable", func(t *testing.T) {
		var got []bool
		row := widgets.NewSwitchRow("Show the tray icon", "", true, func(v bool) { got = append(got, v) })
		r := newRig(t, kit.Size{}, row)
		row.SetDisabled(true)
		r.settle()
		n := node(t, row.Switch.Base())
		if n.States&kit.NodeDisabled == 0 || n.Toggle != kit.ToggleOn {
			t.Fatalf("switch %b %v", n.States, n.Toggle)
		}
		r.click(row.Base())
		if len(got) != 0 {
			t.Error("a disabled switch toggled")
		}
	})
}

func TestCheckbox(t *testing.T) {
	var got []bool
	c := widgets.NewCheckbox("Also delete the file from disk (including partial data)", false, func(v bool) { got = append(got, v) })
	r := newRig(t, kit.Size{}, c)
	n := node(t, c.Box.Base())
	if n.Role != kit.RoleCheckBox || n.Name != "Also delete the file from disk (including partial data)" || n.Toggle != kit.ToggleOff {
		t.Fatalf("checkbox %v %q %v", n.Role, n.Name, n.Toggle)
	}
	r.click(c.Label.Base()) // the label toggles it
	if !c.Checked() || !c.Box.Base().Focused() {
		t.Fatalf("after a click on the label: checked %v focused %v", c.Checked(), c.Box.Base().Focused())
	}
	r.key(kit.KeySpace, 0)
	if c.Checked() || !slices.Equal(got, []bool{true, false}) {
		t.Errorf("Space: %v", got)
	}
}

func TestRadioRows(t *testing.T) {
	var got []string
	g := widgets.NewRadioRows("When the file is already there", []widgets.RadioOption{
		{Value: "keep", Label: "Keep both", Note: "Saves the new one as name (1).ext", Icon: "copy"},
		{Value: "replace", Label: "Replace", Note: "Deletes the file first", Icon: "restart"},
	}, "keep", false, func(v string) { got = append(got, v) })
	r := newRig(t, kit.Size{}, g)
	if n := node(t, g.Base()); n.Role != kit.RoleRadioGroup || n.Name != "When the file is already there" {
		t.Fatalf("group %v %q", n.Role, n.Name)
	}
	rows := byRole(t, r.win, kit.RoleRadioButton)
	if len(rows) != 2 || rows[0].Name != "Keep both" || rows[0].Description != "Saves the new one as name (1).ext" || rows[0].States&kit.NodeSelected == 0 {
		t.Fatalf("rows %v", rows)
	}
	order := r.win.TabOrder()
	if len(order) != 1 {
		t.Fatalf("Tab reaches %d rows, want the chosen one", len(order))
	}
	r.win.Focus(order[0])
	r.key(kit.KeyArrowDown, 0)
	if !slices.Equal(got, []string{"replace"}) || g.Value != "replace" {
		t.Fatalf("ArrowDown: %q", got)
	}
	r.key(kit.KeyArrowDown, 0) // wraps
	r.clickAt(byRole(t, r.win, kit.RoleRadioButton)[1].Bounds.Center())
	if !slices.Equal(got, []string{"replace", "keep", "replace"}) {
		t.Errorf("changes %q", got)
	}
}
