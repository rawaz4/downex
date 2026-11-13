//go:build !darwin && (desktop || dev || bindings)

package main

import "github.com/wailsapp/wails/v2/pkg/menu"

// appMenu: only macOS has a menu bar (menu_darwin.go); the window has none elsewhere.
func appMenu(*App) *menu.Menu { return nil }
