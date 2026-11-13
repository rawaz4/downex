//go:build darwin && cgo

package main

import (
	"os"
	"runtime"
)

// A cgo build for macOS is the executable of Downex Agent.app (docs/MACOS-SPEC.md
// §1), whose menu-bar icon needs AppKit, and AppKit runs on the process's main
// thread only. Locking the main goroutine here, in an init function, makes main run
// on the main thread, which main then hands over with tray.RunMain: the daemon runs
// on other goroutines while the main thread runs the AppKit event loop once the
// tray starts. Every other build (cgo off, other systems) leaves main as it is.
func init() {
	runtime.LockOSThread()
	// An app bundle launched by LaunchServices may get a process serial number argument.
	if len(os.Args) > 1 {
		os.Args = append(os.Args[:1:1], withoutProcessSerial(os.Args[1:])...)
	}
}
