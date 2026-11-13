package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// item describes a menu item for comparison: its label (or "-" for a separator) and its
// shortcut, such as "⌘,".
func item(i *menu.MenuItem) string {
	if i.Type == menu.SeparatorType {
		return "-"
	}
	s := i.Label
	if a := i.Accelerator; a != nil {
		if !slices.Equal(a.Modifiers, []keys.Modifier{keys.CmdOrCtrlKey}) {
			s += " (other modifiers)"
		}
		s += " ⌘" + a.Key
	}
	return s
}

func items(m *menu.Menu) []string {
	var out []string
	for _, i := range m.Items {
		out = append(out, item(i))
	}
	return out
}

func TestMacMenu(t *testing.T) {
	var calls []string
	record := func(name string) func() { return func() { calls = append(calls, name) } }
	act := menuActions{about: record("about"), settings: record("settings"), hide: record("hide"), quit: record("quit")}

	m := macMenu(view{}, act)
	if len(m.Items) != 4 || m.Items[0].SubMenu == nil || m.Items[1].SubMenu == nil ||
		m.Items[2].Role != menu.EditMenuRole || m.Items[3].Role != menu.WindowMenuRole {
		t.Fatalf("menu bar = %+v; want the app menu, File, Edit and Window", m.Items)
	}
	app, file := m.Items[0].SubMenu, m.Items[1].SubMenu
	if m.Items[0].Label != "Downex" || m.Items[1].Label != "File" {
		t.Errorf("menus %q, %q", m.Items[0].Label, m.Items[1].Label)
	}
	want := []string{"About Downex", "-", "Settings… ⌘,", "-", "Hide Downex ⌘h", "-", "Quit Downex ⌘q"}
	if got := items(app); !reflect.DeepEqual(got, want) {
		t.Errorf("app menu = %q, want %q", got, want)
	}
	if got := items(file); !reflect.DeepEqual(got, []string{"Close Window ⌘w"}) {
		t.Errorf("File menu = %q", got)
	}
	for _, i := range append(slices.Clone(app.Items), file.Items...) {
		if i.Type != menu.SeparatorType {
			i.Click(&menu.CallbackData{MenuItem: i})
		}
	}
	if want := []string{"about", "settings", "hide", "quit", "quit"}; !slices.Equal(calls, want) {
		t.Errorf("the items did %q, want %q", calls, want)
	}

	// A progress window has no settings.
	p := macMenu(view{progress: 3}, act)
	if got, want := items(p.Items[0].SubMenu), []string{"About Downex", "-", "Hide Downex ⌘h", "-", "Quit Downex ⌘q"}; !reflect.DeepEqual(got, want) {
		t.Errorf("progress window's app menu = %q, want %q", got, want)
	}
}

func TestMacOptions(t *testing.T) {
	o := macOptions(themeSystem)
	if !reflect.DeepEqual(o.TitleBar, mac.TitleBarDefault()) || o.Appearance != mac.DefaultAppearance {
		t.Errorf("title bar %+v, appearance %q: want the standard title bar and the system's appearance", o.TitleBar, o.Appearance)
	}
	if o.Preferences == nil || !o.Preferences.TabFocusesLinks.IsSet() || !o.Preferences.TabFocusesLinks.Get() {
		t.Error("Tab does not move the focus through buttons and links")
	}
	if o.About == nil || o.About.Title != "Downex" || !strings.Contains(o.About.Message, "Version "+version) {
		t.Errorf("About = %+v", o.About)
	}
	if title, message := aboutText(); title != o.About.Title || message != o.About.Message {
		t.Errorf("the menu's About (%q, %q) differs from the options' (%+v)", title, message, o.About)
	}
}

func TestShowSettings(t *testing.T) {
	host := &fakeHost{}
	a, _ := newTestApp(t, newFakeDaemon(), nil)
	a.showSettings() // no window yet
	a.startup(host)
	a.showSettings() // the page has not loaded
	host.mu.Lock()
	early := len(host.menus)
	host.mu.Unlock()
	if early != 0 {
		t.Errorf("%d requests before the page was ready", early)
	}
	a.domReady()
	a.showSettings()
	host.mu.Lock()
	got := slices.Clone(host.menus)
	host.mu.Unlock()
	if len(got) != 1 || string(got[0]) != `{"command":"settings"}` {
		t.Errorf("menu events = %q", got)
	}

	// A progress window has no settings to show.
	ph := &fakeHost{}
	p, _ := newTestApp(t, newFakeDaemon(), nil)
	p.view = view{progress: 4}
	p.startup(ph)
	p.domReady()
	p.showSettings()
	ph.mu.Lock()
	defer ph.mu.Unlock()
	if len(ph.menus) != 0 {
		t.Errorf("a progress window asked its page for settings: %q", ph.menus)
	}
}
