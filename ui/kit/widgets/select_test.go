package widgets_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// The Select.test.ts cases. The listbox is a popup window of its own here, so "hidden while
// closed" is "no popup while closed"; data-value is Value() and the active descendant Active().

var selectOptions = []widgets.SelectOption{
	{Value: "none", Label: "Unlimited"},
	{Value: "20", Label: "20 MiB/s"},
	{Value: "10", Label: "10 MiB/s", Detail: "half"},
	{Value: "5", Label: "5 MiB/s", Disabled: true},
	{Value: "1", Label: "1 MiB/s", Meta: "~1 GiB/h"},
}

type selectRig struct {
	*rig
	s       *widgets.Select
	changes []string
}

func setupSelect(t *testing.T, value string, opts []widgets.SelectOption, o widgets.SelectOptions) *selectRig {
	t.Helper()
	if opts == nil {
		opts = selectOptions
	}
	if o.Label == "" && o.AriaLabel == "" {
		o.Label = "Speed limit"
	}
	sr := &selectRig{}
	sr.s = widgets.NewSelect(opts, value, o, func(v string) { sr.changes = append(sr.changes, v) })
	sr.s.Style.Width = kit.Px(150)
	sr.rig = newRig(t, kit.Size{}, sr.s)
	sr.win.Focus(sr.s)
	sr.settle()
	return sr
}

// options are the open list's options, in order.
func (sr *selectRig) options() []kit.SemanticNode {
	l := sr.s.List()
	if l == nil {
		sr.t.Fatal("the list is not open")
	}
	return byRole(sr.t, l, kit.RoleOption)
}

// clickOption clicks the option at index i of the open list.
func (sr *selectRig) clickOption(i int) {
	os := sr.options()
	sr.host.Top().Click(os[i].Bounds.Center())
	sr.settle()
}

// chooseOption picks the option of value v as a user does (opens the list, clicks it), and
// refuses one that is missing or disabled (testing.ts's chooseOption).
func (sr *selectRig) chooseOption(v string) error {
	i := slices.IndexFunc(sr.s.Options, func(o widgets.SelectOption) bool { return o.Value == v })
	if i < 0 {
		return fmt.Errorf("no option %q", v)
	}
	if sr.s.Options[i].Disabled {
		return fmt.Errorf("option %q is disabled", v)
	}
	if !sr.s.IsOpen() {
		sr.click(sr.s.Base())
	}
	sr.clickOption(i)
	return nil
}

func TestSelectTrigger(t *testing.T) {
	t.Run("is a combobox named by its label, carrying its value", func(t *testing.T) {
		sr := setupSelect(t, "20", nil, widgets.SelectOptions{})
		n := node(t, sr.s.Base())
		if n.Role != kit.RoleComboBox || n.Name != "Speed limit" {
			t.Fatalf("role %v name %q", n.Role, n.Name)
		}
		if sr.s.Value() != "20" || n.Value != "20 MiB/s" {
			t.Errorf("value %q, shown %q", sr.s.Value(), n.Value)
		}
		if n.States&kit.NodeHasPopup == 0 || n.Expand != kit.ExpandClosed {
			t.Errorf("states %b expand %v", n.States, n.Expand)
		}
	})
	t.Run("lists its options in order, the chosen selected and the disabled marked", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		if sr.s.List() != nil || len(sr.host.Open()) != 0 {
			t.Fatal("a list is open while closed")
		}
		sr.click(sr.s.Base())
		os := sr.options()
		var names []string
		for _, o := range os {
			names = append(names, o.Name)
		}
		want := []string{"Unlimited", "20 MiB/s", "10 MiB/s half", "5 MiB/s", "1 MiB/s ~1 GiB/h"}
		if !slices.Equal(names, want) {
			t.Errorf("options %q, want %q", names, want)
		}
		if os[0].States&kit.NodeSelected == 0 || os[1].States&kit.NodeSelected != 0 {
			t.Error("the chosen option is not the selected one")
		}
		if os[3].States&kit.NodeDisabled == 0 {
			t.Error("the disabled option is not marked disabled")
		}
	})
	t.Run("shows the placeholder when the value is none of the options, and the error state", func(t *testing.T) {
		sr := setupSelect(t, "", nil, widgets.SelectOptions{Placeholder: "Choose a limit", Invalid: true})
		texts := sr.loop.Canvas.Texts()
		if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, "Choose a limit") }) {
			t.Errorf("drawn %q", texts)
		}
		if n := node(t, sr.s.Base()); n.States&kit.NodeInvalid == 0 {
			t.Error("not invalid")
		}
	})
}

func TestSelectKeyboard(t *testing.T) {
	t.Run("rings the keyboard's active option, not the pointer's", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.key(kit.KeyArrowDown, 0)
		if !sr.s.ViaKeys() || !sr.s.List().RingShown() {
			t.Fatal("no ring after ArrowDown")
		}
		sr.host.Top().Move(sr.options()[1].Bounds.Center())
		sr.settle()
		if sr.s.ViaKeys() || sr.s.List().RingShown() || sr.s.Active() != "20" {
			t.Fatalf("after a pointer move: keys %v ring %v active %q", sr.s.ViaKeys(), sr.s.List().RingShown(), sr.s.Active())
		}
		sr.key(kit.KeyArrowDown, 0)
		if !sr.s.ViaKeys() {
			t.Fatal("no ring after ArrowDown again")
		}
		sr.key(kit.KeyEscape, 0)
		sr.click(sr.s.Base())
		if sr.s.ViaKeys() {
			t.Error("opened with the mouse, but ringed")
		}
	})
	t.Run("opens on the chosen option, moves past disabled ones, and chooses with Enter", func(t *testing.T) {
		sr := setupSelect(t, "20", nil, widgets.SelectOptions{})
		sr.key(kit.KeyArrowDown, 0)
		if !sr.s.IsOpen() || node(t, sr.s.Base()).Expand != kit.ExpandOpen {
			t.Fatal("not open")
		}
		steps := []struct {
			k    kit.Key
			want string
		}{
			{0, "20"}, {kit.KeyArrowDown, "10"}, {kit.KeyArrowDown, "1"}, {kit.KeyArrowDown, "1"},
			{kit.KeyHome, "none"}, {kit.KeyEnd, "1"}, {kit.KeyArrowUp, "10"},
		}
		for _, st := range steps {
			if st.k != 0 {
				sr.key(st.k, 0)
			}
			if got := sr.s.Active(); got != st.want {
				t.Fatalf("after %v: active %q, want %q", st.k, got, st.want)
			}
		}
		sr.key(kit.KeyEnter, 0)
		if sr.s.IsOpen() || sr.s.Value() != "10" || !slices.Equal(sr.changes, []string{"10"}) {
			t.Errorf("open %v value %q changes %q", sr.s.IsOpen(), sr.s.Value(), sr.changes)
		}
	})
	t.Run("opens with Enter, Space, Up and Alt+Down; Home and End open on the ends", func(t *testing.T) {
		for _, c := range []struct {
			k    kit.Key
			mods kit.Modifiers
			want string
		}{
			{kit.KeyEnter, 0, "none"}, {kit.KeySpace, 0, "none"}, {kit.KeyArrowUp, 0, "none"},
			{kit.KeyArrowDown, kit.ModAlt, "none"}, {kit.KeyEnd, 0, "1"}, {kit.KeyHome, 0, "none"},
		} {
			sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
			sr.key(c.k, c.mods)
			if !sr.s.IsOpen() || sr.s.Active() != c.want {
				t.Errorf("%v+%v: open %v active %q, want %q", c.mods, c.k, sr.s.IsOpen(), sr.s.Active(), c.want)
			}
		}
	})
	t.Run("closes on Escape without a change, and keeps the Escape from the dialog around it", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyEscape, 0)
		if sr.s.IsOpen() || sr.s.Value() != "none" || len(sr.changes) > 0 {
			t.Fatalf("open %v value %q changes %q", sr.s.IsOpen(), sr.s.Value(), sr.changes)
		}
		escapes := func() int {
			n := 0
			for _, e := range sr.keysSeen {
				if e.Key == kit.KeyEscape && e.Action == kit.KeyDown {
					n++
				}
			}
			return n
		}
		if escapes() != 0 {
			t.Fatal("the window saw the Escape that closed the list")
		}
		sr.key(kit.KeyEscape, 0) // closed: Escape is the dialog's again
		if escapes() != 1 {
			t.Errorf("the window saw %d Escapes, want 1", escapes())
		}
	})
	t.Run("chooses the active option with Tab and Alt+Up", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyTab, 0)
		if sr.s.IsOpen() || sr.s.Value() != "20" {
			t.Fatalf("Tab: open %v value %q", sr.s.IsOpen(), sr.s.Value())
		}
		sr.win.Focus(sr.s)
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyArrowDown, 0)
		sr.key(kit.KeyArrowUp, kit.ModAlt)
		if sr.s.IsOpen() || sr.s.Value() != "10" || !slices.Equal(sr.changes, []string{"20", "10"}) {
			t.Errorf("Alt+Up: open %v value %q changes %q", sr.s.IsOpen(), sr.s.Value(), sr.changes)
		}
	})
	t.Run("moves by ten with PageUp and PageDown", func(t *testing.T) {
		var many []widgets.SelectOption
		for i := 1; i <= 30; i++ {
			many = append(many, widgets.SelectOption{Value: fmt.Sprint(i), Label: fmt.Sprintf("%d connections", i)})
		}
		sr := setupSelect(t, "1", many, widgets.SelectOptions{})
		for _, st := range []struct {
			k    kit.Key
			want string
		}{{kit.KeyArrowDown, "1"}, {kit.KeyPageDown, "11"}, {kit.KeyPageDown, "21"}, {kit.KeyPageDown, "30"}, {kit.KeyPageUp, "20"}} {
			sr.key(st.k, 0)
			if got := sr.s.Active(); got != st.want {
				t.Fatalf("after %v: %q, want %q", st.k, got, st.want)
			}
		}
	})
	t.Run("chooses by typing while closed, and moves by typing while open", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.typeText("1")
		if sr.s.IsOpen() || sr.s.Value() != "10" || sr.changes[len(sr.changes)-1] != "10" {
			t.Fatalf("typed 1: open %v value %q", sr.s.IsOpen(), sr.s.Value())
		}
		sr.typeText("1") // the same letter again: the next match
		if sr.s.Value() != "1" {
			t.Fatalf("typed 1 again: %q", sr.s.Value())
		}
		sr.wait(520e6)
		sr.key(kit.KeyArrowDown, 0)
		sr.typeText("u")
		if sr.s.Active() != "none" {
			t.Fatalf("typed u: active %q", sr.s.Active())
		}
		sr.wait(520e6)
		sr.typeText("2")
		if sr.s.Active() != "20" || sr.s.Value() != "1" {
			t.Errorf("typed 2: active %q value %q (nothing chosen until Enter)", sr.s.Active(), sr.s.Value())
		}
	})
}

func TestSelectPointer(t *testing.T) {
	t.Run("chooses with a click, ignores disabled options and closes on an outside press", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.click(sr.s.Base())
		sr.clickOption(3) // 5 MiB/s, disabled
		if !sr.s.IsOpen() || len(sr.changes) > 0 {
			t.Fatal("a disabled option was chosen")
		}
		sr.clickOption(1)
		if sr.s.IsOpen() || !slices.Equal(sr.changes, []string{"20"}) || !sr.s.Base().Focused() {
			t.Fatalf("open %v changes %q focused %v", sr.s.IsOpen(), sr.changes, sr.s.Base().Focused())
		}
		sr.click(sr.s.Base())
		if !sr.s.IsOpen() {
			t.Fatal("not open again")
		}
		sr.press(kit.Pt(1000, 700))
		if sr.s.IsOpen() {
			t.Error("an outside press left it open")
		}
	})
	t.Run("a press on its own trigger is the trigger's (it toggles), not an outside press", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.click(sr.s.Base())
		sr.host.Press(sr.s.Base().WindowBounds().Center())
		if !sr.s.IsOpen() {
			t.Fatal("the press on the trigger closed it as an outside press")
		}
	})
	t.Run("chooseOption picks by value like a user, and refuses missing and disabled options", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		if err := sr.chooseOption("10"); err != nil {
			t.Fatal(err)
		}
		if sr.s.Value() != "10" || sr.s.IsOpen() || sr.changes[0] != "10" {
			t.Fatalf("value %q open %v", sr.s.Value(), sr.s.IsOpen())
		}
		if err := sr.chooseOption("5"); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Errorf("disabled: %v", err)
		}
		if err := sr.chooseOption("99"); err == nil || !strings.Contains(err.Error(), `no option "99"`) {
			t.Errorf("missing: %v", err)
		}
	})
	t.Run("lists groups under their heading", func(t *testing.T) {
		sr := setupSelect(t, "1", []widgets.SelectOption{
			{Value: "1", Label: "Main queue", Group: "Running"},
			{Value: "3", Label: "Night", Detail: "starts 01:00", Group: "Stopped"},
			{Value: "2", Label: "Later", Group: "Stopped"},
		}, widgets.SelectOptions{Label: "Queue"})
		sr.click(sr.s.Base())
		var names []string
		for _, o := range sr.options() {
			names = append(names, o.Name)
		}
		if !slices.Equal(names, []string{"Main queue", "Night starts 01:00", "Later"}) {
			t.Errorf("options %q", names)
		}
		groups := byRole(t, sr.s.List(), kit.RoleGroup)
		if len(groups) != 2 || groups[1].Name != "Stopped" {
			t.Fatalf("groups %v", groups)
		}
		if err := sr.chooseOption("2"); err != nil || sr.s.Value() != "2" {
			t.Errorf("chose %q: %v", sr.s.Value(), err)
		}
	})
	t.Run("cannot be opened while disabled", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{Disabled: true})
		sr.click(sr.s.Base())
		if sr.s.IsOpen() || node(t, sr.s.Base()).States&kit.NodeDisabled == 0 {
			t.Error("a disabled Select opened")
		}
	})
	t.Run("can be named by aria-label alone, and names its listbox the same", func(t *testing.T) {
		sr := setupSelect(t, "a", []widgets.SelectOption{{Value: "a", Label: "A"}}, widgets.SelectOptions{AriaLabel: "Speed limit unit"})
		sr.click(sr.s.Base())
		if n := node(t, sr.s.Base()); n.Name != "Speed limit unit" {
			t.Errorf("combobox %q", n.Name)
		}
		lb := byRole(t, sr.s.List(), kit.RoleListBox)
		if len(lb) != 1 || lb[0].Name != "Speed limit unit" {
			t.Errorf("listbox %v", lb)
		}
	})
	t.Run("names its listbox after its label when it opens", func(t *testing.T) {
		sr := setupSelect(t, "none", nil, widgets.SelectOptions{})
		sr.click(sr.s.Base())
		lb := byRole(t, sr.s.List(), kit.RoleListBox)
		if len(lb) != 1 || lb[0].Name != "Speed limit" {
			t.Errorf("listbox %v", lb)
		}
	})
}

// TestSelectPlacement is "where its list opens (DESIGN-SYSTEM.md §3.5)": the window is
// 1190 × 752. The list is a window of its own, placed in the owner's window coordinates, so
// the dialog backdrop's containing block of today's fixed list plays no part.
func TestSelectPlacement(t *testing.T) {
	view := kit.Size{W: 1190, H: 752}
	t.Run("opens 4 px under its trigger", func(t *testing.T) {
		pl := widgets.PlaceSelectList(kit.R(300, 507, 450, 543), 5, view)
		if pl.Up || pl.Top != 547 || pl.Left != 300 || pl.MaxHeight != 752-543-4-8 {
			t.Errorf("%+v", pl)
		}
	})
	t.Run("opens 4 px above its trigger when there is no room below", func(t *testing.T) {
		pl := widgets.PlaceSelectList(kit.R(300, 690, 450, 726), 5, view)
		if !pl.Up || pl.Bottom != 686 || 752-pl.Bottom != 66 {
			t.Errorf("%+v", pl)
		}
	})
	t.Run("the open list sits there", func(t *testing.T) {
		s := widgets.NewSelect(selectOptions, "none", widgets.SelectOptions{Label: "Speed limit"}, nil)
		s.Style.Width, s.Style.Height = kit.Px(150), kit.Px(36)
		s.Style.Margin = kit.Edges{Top: kit.Px(507), Start: kit.Px(300)}
		r := newRig(t, view, s)
		r.root.Style.Padding = kit.Edges{}
		r.root.Relayout()
		r.settle()
		r.click(s.Base())
		b := s.ListBounds()
		if b.Min.Y != 547 || b.Min.X != 300 || b.Width() < 150 {
			t.Errorf("list at %v", b)
		}
	})
}
