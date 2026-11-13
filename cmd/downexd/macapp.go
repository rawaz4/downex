package main

import "downex/internal/client"

// opensInBackground reports whether downexd opens exe, a program of its installation l,
// without activating it (macOS: startapp_darwin.go). That is the progress app of
// Downex.app: downexd opens progress windows by itself, for a browser hand-off that
// starts at once and as the download complete dialog (docs/DESKTOP-SPEC.md §7–§8), so
// they show without taking the focus from the app the user works in. The window that
// it opens for the menu-bar icon, a click on a notification or a browser hand-off to
// confirm answers the user, and is activated.
func opensInBackground(l client.Layout, exe string) bool {
	return l.Bundle != "" && exe == l.Progress
}
