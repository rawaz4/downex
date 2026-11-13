//go:build darwin

package main

import "downex/internal/platform"

// On macOS downexd opens the progress windows that nobody asked for at that moment in
// the background (opensInBackground, platform.StartAppInBackground) and every other
// window activated (platform.StartApp).
func init() { startApp = macStartApp }

func macStartApp(exe string, args ...string) error {
	if l, err := installLayout(); err == nil && opensInBackground(l, exe) {
		return platform.StartAppInBackground(exe, args...)
	}
	return platform.StartApp(exe, args...)
}
