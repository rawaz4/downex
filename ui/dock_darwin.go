//go:build darwin && cgo && (desktop || dev || bindings)

package main

// What every macOS window process (the main window and each progress window, each a
// Wails v2.17.0 app of its own) adds to Wails' NSApplication setup (docs/MACOS-SPEC.md
// §5), in dock_darwin.m (ARC). Go and Objective-C call each other only through the C
// functions of dock_darwin.h and goDownexWindowShouldTerminate below; this file's
// preamble therefore only declares (it exports a Go function). The !cgo fallback is
// dock_nocgo_darwin.go (and dock_other.go for hideFromDock).

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -mmacosx-version-min=13.0
#include <stdlib.h>
#include "dock_darwin.h"
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// hideFromDock makes a progress window's process an accessory app (MACOS-SPEC §5: no Dock
// icon per progress window). It returns at once; AppKit applies it on the main thread.
// Calling it again does nothing.
func hideFromDock() { C.downexBecomeAccessory() }

// yieldActivation lets the apps of bundles (app bundle paths) take the focus from this
// one (macOS 14+: NSApplication yieldActivationToApplicationWithBundleIdentifier:, on the
// main thread). It does nothing before AppKit has started, or on older systems.
func yieldActivation(bundles ...string) {
	for _, b := range bundles {
		cs := C.CString(b)
		C.downexYieldActivation(cs)
		C.free(unsafe.Pointer(cs))
	}
}

var (
	// systemQuit is the window's shutdown before the system's request to quit proceeds.
	systemQuit atomic.Pointer[func()]
	quitOnce   sync.Once
)

// installAppHooks makes the system's requests to quit this process (logout, restart,
// shut down, the Dock's Quit) run quit and then let the termination proceed, and has
// the window activate itself the way macOS 14+ expects, or at least show in front of
// other apps' windows (dock_darwin.m). Called once before wails.Run; AppKit applies it
// on the main thread once Wails has set up its delegate.
func installAppHooks(quit func()) {
	systemQuit.Store(&quit)
	C.downexInstallAppHooks()
}

// goDownexWindowShouldTerminate is called on the main thread when the system asks the
// app to quit, which dock_darwin.m has postponed (NSTerminateLater): the window shuts
// down (once; App.shutdown waits at most shutdownWait) on another goroutine, so that the
// main thread keeps running, and then the termination proceeds.
//
//export goDownexWindowShouldTerminate
func goDownexWindowShouldTerminate() {
	go func() {
		quitOnce.Do(func() {
			if f := systemQuit.Load(); f != nil {
				(*f)()
			}
		})
		C.downexReplyTerminate()
	}()
}
