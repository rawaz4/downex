//go:build darwin && cgo && (desktop || dev || bindings)

// The C interface of dock_darwin.m for dock_darwin.go. Declarations only: dock_darwin.go
// exports a Go function, so its preamble (which includes this file) must not define
// anything. Every function may be called from any thread; AppKit work is done on the
// main thread.

#ifndef DOWNEX_DOCK_DARWIN_H
#define DOWNEX_DOCK_DARWIN_H

// downexBecomeAccessory switches the app to the accessory activation policy (no Dock
// icon, no menu bar) and activates it again. Idempotent; returns at once.
void downexBecomeAccessory(void);

// downexInstallAppHooks replaces two methods of Wails' application delegate once it is
// set (applicationShouldTerminate:, handleSecondInstanceNotification:) and asks for the
// activation of the app (macOS 14+). The first call counts; returns at once.
void downexInstallAppHooks(void);

// downexReplyTerminate lets a termination postponed by NSTerminateLater proceed.
void downexReplyTerminate(void);

// downexYieldActivation lets the app of the bundle at bundlePath (UTF-8) take the focus
// from this one (macOS 14+). It waits at most 250 ms for the main thread; it does
// nothing before AppKit has started.
void downexYieldActivation(const char *bundlePath);

#endif
