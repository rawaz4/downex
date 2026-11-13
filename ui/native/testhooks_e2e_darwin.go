//go:build darwin && cgo && e2etest

package main

import (
	"os"
	"path/filepath"

	"downex/ui/native/glue"
)

// The hooks of an e2etest build of the macOS window (never shipped; the product compiles
// testhooks_release_darwin.go):
//
//	DOWNEX_E2E_UI_DIR=<folder>  the window's per-user files (ui-theme.json, ui-prefs.json,
//	                            ui-window.json) in <folder>, its log in <folder>/logs and its
//	                            single-instance locks and hand-off sockets in <folder>/locks,
//	                            in place of the user's own folders and NSTemporaryDirectory()
//
// It lets a copy of the bundle run beside the user's own Downex on a private daemon
// (DOWNEX_ENDPOINT) without meeting its locks or files. The variable is a DOWNEX_* one, so
// platform.StartApp forwards it to the progress app the main window starts.
const envE2EUIDir = "DOWNEX_E2E_UI_DIR"

// e2eConfig applies the hooks to c.
func e2eConfig(c *glue.Config) {
	d := os.Getenv(envE2EUIDir)
	if d == "" {
		return
	}
	// The hand-off socket's path must fit sun_path (103 bytes): give a short folder.
	locks, logs := filepath.Join(d, "locks"), filepath.Join(d, "logs")
	_ = os.MkdirAll(locks, 0o700)
	_ = os.MkdirAll(logs, 0o700) // as platform.LogDir makes the product's
	c.Dir, c.LogDir, c.LockDir = d, logs, locks
}
