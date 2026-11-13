package settings_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/widgets"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// walk visits e and everything in it.
func walk(e *kit.Element, f func(*kit.Element) bool) bool {
	if !f(e) {
		return false
	}
	for _, c := range e.Children() {
		if !walk(c, f) {
			return false
		}
	}
	return true
}

// find is the first element under root that matches.
func find(root *kit.Element, match func(*kit.Element) bool) *kit.Element {
	var out *kit.Element
	walk(root, func(e *kit.Element) bool {
		if match(e) {
			out = e
			return false
		}
		return true
	})
	return out
}

// findAll are the elements under root that match.
func findAll(root *kit.Element, match func(*kit.Element) bool) []*kit.Element {
	var out []*kit.Element
	walk(root, func(e *kit.Element) bool {
		if match(e) && !hiddenUnder(root, e) {
			out = append(out, e)
		}
		return true
	})
	return out
}

// hiddenUnder reports e or an ancestor (up to root) display: none.
func hiddenUnder(root, e *kit.Element) bool {
	for x := e; x != nil; x = x.Parent() {
		if x.Style.Hidden {
			return true
		}
		if x == root {
			break
		}
	}
	return false
}

// tab is a page's tab (role tab, named after its label).
func tab(t testing.TB, d *settings.Dialog, page string) *kit.Element {
	t.Helper()
	e := find(d.Card(), func(e *kit.Element) bool { return e.Semantics.AutomationID == "settings-tab-"+page })
	if e == nil {
		t.Fatalf("no tab %s", page)
	}
	return e
}

// buttonNamed is the visible button whose label (or accessible name) is name.
func buttonNamed(t testing.TB, root *kit.Element, name string) *kit.Element {
	t.Helper()
	bs := findAll(root, func(e *kit.Element) bool {
		switch w := e.Widget().(type) {
		case *widgets.Button:
			return w.Label().Text() == name || e.Semantics.Name == name
		case *widgets.IconButton:
			return e.Semantics.Name == name
		}
		return e.Semantics.Role == kit.RoleButton && e.Semantics.Name == name
	})
	if len(bs) == 0 {
		t.Fatalf("no button %q", name)
	}
	return bs[0]
}

func hasButton(root *kit.Element, name string) bool {
	return len(findAll(root, func(e *kit.Element) bool {
		if b, ok := e.Widget().(*widgets.Button); ok {
			return b.Label().Text() == name || e.Semantics.Name == name
		}
		return (e.Semantics.Role == kit.RoleButton) && e.Semantics.Name == name
	})) > 0
}

// inputNamed is the visible input whose accessible name is name.
func inputNamed(t testing.TB, root *kit.Element, name string) *widgets.Input {
	t.Helper()
	in := maybeInput(root, name)
	if in == nil {
		t.Fatalf("no input %q", name)
	}
	return in
}

// inputsNamed are the visible inputs whose accessible name is name.
func inputsNamed(root *kit.Element, name string) []*kit.Element {
	return findAll(root, func(e *kit.Element) bool {
		_, ok := e.Widget().(*widgets.Input)
		return ok && e.Semantics.Name == name
	})
}

func maybeInput(root *kit.Element, name string) *widgets.Input {
	es := findAll(root, func(e *kit.Element) bool {
		_, ok := e.Widget().(*widgets.Input)
		return ok && e.Semantics.Name == name
	})
	if len(es) == 0 {
		return nil
	}
	return es[0].Widget().(*widgets.Input)
}

// switchNamed is the visible switch named name (a SwitchRow's).
func switchNamed(t testing.TB, root *kit.Element, sub string) *widgets.Switch {
	t.Helper()
	es := findAll(root, func(e *kit.Element) bool {
		_, ok := e.Widget().(*widgets.Switch)
		return ok && contains(e.Semantics.Name, sub)
	})
	if len(es) == 0 {
		t.Fatalf("no switch %q", sub)
	}
	return es[0].Widget().(*widgets.Switch)
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// typeInto replaces an input's text as typing does (its OnChange runs).
func typeInto(h *viewstest.Harness, in *widgets.Input, s string) {
	f, ok := in.Native().(*hostcaptest.Field)
	if !ok {
		h.T.Fatalf("input %q has no field", in.Semantics.Name)
	}
	f.Replace(s)
	h.Settle()
}

// texts are the text of every visible label under root.
func texts(root *kit.Element) []string {
	var out []string
	for _, e := range findAll(root, func(*kit.Element) bool { return true }) {
		switch w := e.Widget().(type) {
		case *kit.Label:
			out = append(out, w.Text())
		case *views.Text:
			out = append(out, w.Text())
		case interface{ TextWidget() *views.Text }:
			out = append(out, w.TextWidget().Text())
		}
		if e.Semantics.Role == kit.RoleText && e.Semantics.Name != "" {
			if _, ok := e.Widget().(*kit.Label); !ok {
				if _, ok := e.Widget().(*views.Text); !ok {
					out = append(out, e.Semantics.Name)
				}
			}
		}
	}
	return out
}

func hasText(root *kit.Element, sub string) bool {
	for _, s := range texts(root) {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

// open shows a Settings dialog for a request in a harness and lets it load.
func open(h *viewstest.Harness, req views.SettingsRequest, o settings.Options) *settings.Dialog {
	d := settings.New(h.App, req, o)
	h.App.Dialogs.Show(d)
	h.Settle()
	return d
}

// press presses a button as a DOM click() does (also one scrolled out of view).
func press(h *viewstest.Harness, e *kit.Element) {
	h.T.Helper()
	p, ok := e.Widget().(interface{ Press() })
	if !ok {
		h.T.Fatalf("%T cannot be pressed", e.Widget())
	}
	p.Press()
	h.Settle()
}
