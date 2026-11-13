//go:build darwin && (desktop || dev || bindings)

package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/menu"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// appMenu is the menu bar of a's window (macMenu). Wails runs each click in a goroutine
// of its own, and its runtime calls go to the main thread themselves. Before the window
// has started (no runtime yet) the items do nothing.
//
// As the menu's Quit ends the process through Wails, the system's requests to quit it
// (logout, restart, shut down, the Dock's Quit) end it once a has shut down: they are
// let through instead of cancelled (installAppHooks, dock_darwin.m), which also has the
// window activate itself as macOS 14+ expects.
func appMenu(a *App) *menu.Menu {
	installAppHooks(func() {
		a.log.Info("quit requested by the system")
		a.shutdown()
	})
	withRuntime := func(fn func(ctx context.Context)) func() {
		return func() {
			if h, ok := a.runtimeHost().(wailsHost); ok {
				fn(h.ctx)
			}
		}
	}
	return macMenu(a.view, menuActions{
		about:    withRuntime(showAbout),
		settings: a.showSettings,
		hide:     withRuntime(wruntime.Hide),
		quit:     withRuntime(wruntime.Quit),
	})
}

// showAbout shows the About dialog (aboutText) with the app's icon.
func showAbout(ctx context.Context) {
	title, message := aboutText()
	_, _ = wruntime.MessageDialog(ctx, wruntime.MessageDialogOptions{
		Type:          wruntime.InfoDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"OK"},
		DefaultButton: "OK",
	})
}
