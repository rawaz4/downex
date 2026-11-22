//go:build darwin && cgo

package main

import (
	"context"
	"os"
	"runtime"
	"time"

	"downex/internal/client"
	"downex/internal/platform"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/native/glue"
)

// The window backend of this system: ui/mac (AppKit, Core Graphics and Core Text).
type (
	osApp    = mac.App
	osWindow = mac.Window
)

// windowSpec is a window to open: its title, content size and minimum in points, and how it
// opens.
type windowSpec struct {
	Title         string
	Size, MinSize kit.Size
	// CustomFrame is Windows' own title bar: macOS keeps the system's (host.WindowSpec).
	CustomFrame bool
	// Maximized: it opens zoomed, Size being the size it restores to.
	Maximized bool
	// Dialog: a dialog's window (the standalone Confirm download window).
	Dialog bool
}

// openWindow opens root's window on a as ws says (-noactivate shows it without activating;
// -render and -dpi have no meaning here: Core Graphics draws at the screen's scale).
func openWindow(a *osApp, root kit.Root, ws windowSpec, o options, b *backend) (*osWindow, error) {
	wo := mac.WindowOptions{
		Title: ws.Title, Size: ws.Size, MinSize: ws.MinSize, NoActivate: o.noActivate,
		AcceptsFirstMouse: acceptsFirstMouse(ws, b.view),
	}
	if ws.Maximized {
		return a.NewMaximizedWindow(root, wo)
	}
	return a.NewWindow(root, wo)
}

// acceptsFirstMouse: the standalone Confirm download window (a Dialog) and the progress
// windows open over the browser the user is in, so the click that brings them forward also
// presses what it lands on ("Start download", Pause); the main window's only brings it
// forward, as the Mac's windows do.
func acceptsFirstMouse(ws windowSpec, v host.View) bool { return ws.Dialog || v.IsProgress() }

// progressFrames are the frames the progress window's first size and its minimum leave out.
// Today's Mac window (Wails) made the window with a content rect of 520 × 300 and the
// minimum 480 × 260 on the outer frame; the page then fits the outer height from the frame.
func progressFrames() (initial, min kit.Size) {
	return kit.Size{}, mac.SystemFrameSize()
}

// mainFrame is what the frame adds to the main window's content (mainMinContent): today's
// Mac window kept its minimum and its stored size on the frame.
func mainFrame() kit.Size { return mac.SystemFrameSize() }

// osBackend adds this system's parts to a window's backend: the menu bar's Settings…, the
// start from the new Downex.app after an update and the browsers' registration
// (macupdate.go).
func osBackend(b *backend, gw *glue.Window) {
	b.menu = gw.SetMenu
	b.updated = func(quit func()) {
		l, err := client.FindLayout(os.Executable)
		if err != nil {
			b.log.Warn("cannot locate Downex.app to open it again after the update", "error", err)
			return
		}
		exe, args, ok := reopenCommand(l, os.Getpid())
		if !ok {
			return // a development build: nothing was replaced
		}
		if err := platform.StartDetached(exe, args...); err != nil {
			b.log.Warn("cannot open Downex again after the update", "error", err)
			return
		}
		b.log.Info("the update replaced Downex.app: the window starts again from it")
		quit()
	}
	files := gw.Session.Files()
	b.mainStarted = func() { go registerBrowsers(b, files) }
}

// registerTimeout bounds the browsers' registration at the main window's start.
const registerTimeout = time.Minute

// registerBrowsers registers Downex with the browsers (registersBrowsers says when), off the
// UI thread, keeping what is registered (host.Files.KeepBrowsersRegistered); a failure is
// only logged (Settings → Integration can register again).
func registerBrowsers(b *backend, files *host.Files) {
	l, err := client.FindLayout(os.Executable)
	if err != nil || !registersBrowsers(runtime.GOOS, l) || files == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()
	if err := files.KeepBrowsersRegistered(ctx); err != nil {
		b.log.Warn("cannot register Downex with the browsers", "error", err)
	}
}

// The Mac names its shortcuts with ⌘ (the keys take ⌘ and Ctrl alike).
func init() { kit.MacKeys = true }
