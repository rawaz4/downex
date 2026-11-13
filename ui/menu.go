package main

import (
	"encoding/json"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// The macOS menu bar and window options (docs/MACOS-SPEC.md §5). They are built on every
// system so that they are tested on every system; only the macOS window uses them
// (menu_darwin.go; other systems ignore the Mac options and get no menu bar).

const (
	// productName names the app in its menu.
	productName = "Downex"
	// menuName is the Wails event that passes a command of the menu bar to the page; its
	// payload is {"command": "settings"} (Settings…, the main window only).
	menuName = "downex:menu"
	// settingsCommand asks the page to show its Settings dialog.
	settingsCommand = "settings"
)

// menuEvent is the payload of menuName.
type menuEvent struct {
	Command string `json:"command"`
}

// menuActions are what the items of the menu bar do (menu_darwin.go: the Wails runtime).
type menuActions struct {
	about    func() // shows the About dialog
	settings func() // asks the page for its Settings dialog (App.showSettings)
	hide     func() // hides the app
	quit     func() // ends this window's process, as closing the window does
}

// macMenu is the menu bar of view v: the app menu (About, Settings… on the main window,
// Hide, Quit), File (Close Window: one window per process), and the standard Edit menu,
// whose commands text fields need for their shortcuts (⌘C, ⌘V …), and Window menu. The
// page handles ⌘, itself while nothing else is open; the menu gets it otherwise and its
// clicks always.
func macMenu(v view, act menuActions) *menu.Menu {
	do := func(fn func()) menu.Callback { return func(*menu.CallbackData) { fn() } }
	m := menu.NewMenu()
	app := m.AddSubmenu(productName) // macOS titles the first menu with the app's name
	app.AddText("About "+productName, nil, do(act.about))
	if v.progress == 0 {
		app.AddSeparator()
		app.AddText("Settings…", keys.CmdOrCtrl(","), do(act.settings))
	}
	app.AddSeparator()
	app.AddText("Hide "+productName, keys.CmdOrCtrl("h"), do(act.hide))
	app.AddSeparator()
	app.AddText("Quit "+productName, keys.CmdOrCtrl("q"), do(act.quit))
	file := m.AddSubmenu("File")
	file.AddText("Close Window", keys.CmdOrCtrl("w"), do(act.quit))
	m.Append(menu.EditMenu())
	m.Append(menu.WindowMenu())
	return m
}

// aboutText is the text of the About dialog: the product, and this build's version.
func aboutText() (title, message string) {
	return productName, "Version " + version + "\n\nA download manager."
}

// macOptions are the window's macOS options: the standard title bar, the app's theme c
// (the stored choice, theme.go: the system's appearance, or Aqua or Dark Aqua when the
// app forces light or dark, as the page does), Tab moving the focus through buttons and
// links as it does on other systems (WebKit's default, without the system's keyboard
// navigation, reaches text fields and lists only, which would strand keyboard users in
// dialogs), and the About text. Wails shows its About only from its own app menu, which
// has no Settings…; macMenu's About shows the same text (menu_darwin.go).
func macOptions(c themeChoice) *mac.Options {
	title, message := aboutText()
	appearance := mac.DefaultAppearance
	switch c {
	case themeLight:
		appearance = mac.NSAppearanceNameAqua
	case themeDark:
		appearance = mac.NSAppearanceNameDarkAqua
	}
	return &mac.Options{
		TitleBar:    mac.TitleBarDefault(),
		Appearance:  appearance,
		Preferences: &mac.Preferences{TabFocusesLinks: mac.Enabled},
		About:       &mac.AboutInfo{Title: title, Message: message},
	}
}

// showSettings asks the main window's page to show its Settings dialog (Settings… in the
// menu bar). A progress window has no settings; a request before the page has loaded is
// dropped (the item can be chosen again).
func (a *App) showSettings() {
	if a.view.progress > 0 {
		return
	}
	a.mu.Lock()
	h, ready := a.host, a.pageReady
	a.mu.Unlock()
	if h == nil || !ready {
		return
	}
	payload, err := json.Marshal(menuEvent{Command: settingsCommand})
	if err != nil {
		return
	}
	h.Emit(menuName, payload)
	a.log.Info("settings requested from the menu")
}
