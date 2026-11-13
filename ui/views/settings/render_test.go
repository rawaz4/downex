package settings_test

import (
	"testing"

	"downex/ui/host"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// The Settings states of the 0.9.2 references (refshots.mjs; ui/state/scene's fixtures).
var settingsStates = []string{
	"settings-general", "settings-downloads", "settings-network", "settings-network-full", "settings-network-editor",
	"settings-integration", "settings-video", "settings-video-ready", "settings-appearance", "settings-updates",
	"settings-about", "settings-about-notices",
}

// stateTabs are the tabs each state's steps click (Ctrl+, opens on General).
var stateTabs = map[string]string{
	"settings-downloads": "downloads", "settings-network": "network", "settings-network-full": "network",
	"settings-network-editor": "network", "settings-integration": "integration", "settings-video": "video",
	"settings-video-ready": "video", "settings-appearance": "appearance", "settings-updates": "updates",
	"settings-about": "about", "settings-about-notices": "about",
}

// openState opens the Settings dialog in a harness as the state's steps do: Ctrl+, (the
// keyboard: General's tab gets a visible focus ring), then a click on the state's tab and
// its button.
func openState(t testing.TB, h *viewstest.Harness, name string) *settings.Dialog {
	t.Helper()
	// the references keep the theme choice System and emulate the system's light or dark
	// mode (Appearance shows System chosen, with its hint)
	dark := themeOf(h) == "dark"
	h.App.WidgetTheme.SystemDark = &dark
	h.App.Theme.SetSystemDark(dark)
	h.App.Theme.Set(host.ThemeSystem)
	h.Settle()
	h.Window.SetFocusVisible(true)
	d := settings.New(h.App, views.SettingsRequest{}, settings.Options{})
	h.App.Dialogs.Show(d)
	h.Settle()
	if pg, ok := stateTabs[name]; ok {
		h.Click(tab(t, d, pg))
		h.Window.SetFocusVisible(false)
	}
	switch name {
	case "settings-network-editor":
		press(h, buttonNamed(t, d.Card(), "Add site login"))
		h.Window.SetFocusVisible(false)
	case "settings-about-notices":
		press(h, buttonNamed(t, d.Card(), "View open-source notices"))
		h.Window.SetFocusVisible(false)
	}
	h.Settle()
	return d
}

// themeOf is the harness's theme ("light" or "dark"), from the theme choice it set.
func themeOf(h *viewstest.Harness) string {
	if h.App.Theme.Choice() == host.ThemeDark {
		return "dark"
	}
	return "light"
}

// isState reports a Settings state.
func isState(name string) bool {
	for _, s := range settingsStates {
		if s == name {
			return true
		}
	}
	return false
}
