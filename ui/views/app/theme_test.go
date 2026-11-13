package app_test

import (
	"testing"

	"downex/ui/host"
	"downex/ui/kit"
)

// labelOf is the first label with text inside e.
func labelOf(e *kit.Element) *kit.Label {
	for _, k := range e.Children() {
		if l, ok := k.Widget().(*kit.Label); ok && l.Text() != "" {
			return l
		}
		if l := labelOf(k); l != nil {
			return l
		}
	}
	return nil
}

// TestThemeSwitchRecoloursTheSidebar: in the composed window, a theme change shows the
// sidebar's labels in the new theme's ink at the next frame, light→dark, dark→light, and when
// only the environment changes (a contrast theme, which never reaches Theme.Changes). Before,
// the labels kept the previous theme's ink until a relaunch (the routed finding on
// navitem.go and app.go's Theme watch).
func TestThemeSwitchRecoloursTheSidebar(t *testing.T) {
	f := quiet(t, "light")
	full := kit.RectAt(kit.Point{}, f.h.Window.Size())
	check := func(when string) {
		t.Helper()
		f.h.Settle()
		f.h.Window.Paint(nopCanvas{}, full)
		th := f.h.App.Tokens()
		cats, _, sts := f.m.Shell.Sidebar().Items()
		if len(cats) == 0 || len(sts) == 0 {
			t.Fatalf("%s: no sidebar items", when)
		}
		for i, group := range [][]*kit.Element{cats, sts} {
			for j, e := range group {
				l := labelOf(e)
				if l == nil {
					t.Fatalf("%s: item %d/%d has no label", when, i, j)
				}
				want := th.Ink2
				if i == 0 && j == 0 { // All downloads, the active filter
					want = th.Ink
				}
				if got := l.Color(); got != want {
					t.Errorf("%s: %q drawn in %v, want %v", when, l.Text(), got, want)
				}
			}
		}
	}
	check("light")
	f.h.App.Theme.Set(host.ThemeDark)
	check("light→dark")
	f.h.App.Theme.Set(host.ThemeLight)
	check("dark→light")
	f.h.App.Theme.Set(host.ThemeDark)
	check("light→dark again")
	env := f.h.Window.Environment()
	env.HighContrast = true // the High Contrast Black colours
	env.SystemColors = kit.SystemColors{
		Canvas: kit.RGB(0x000000), CanvasText: kit.RGB(0xffffff), LinkText: kit.RGB(0xffff00),
		GrayText: kit.RGB(0x3ff23f), Highlight: kit.RGB(0x1aebff), HighlightText: kit.RGB(0x000000),
		ButtonFace: kit.RGB(0x000000), ButtonText: kit.RGB(0xffffff),
	}
	f.h.Loop.Send(kit.EnvironmentEvent{Env: env})
	check("a contrast theme")
	env.HighContrast = false
	f.h.Loop.Send(kit.EnvironmentEvent{Env: env})
	check("the contrast theme off")
}
