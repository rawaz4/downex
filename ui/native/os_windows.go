//go:build windows

package main

import (
	"downex/ui/kit"
	"downex/ui/native/glue"
	"downex/ui/win"
)

// The window backend of this system: ui/win (Win32, Direct2D and DirectWrite).
type (
	osApp    = win.App
	osWindow = win.Window
)

// windowSpec is a window to open: its title, client size and minimum in DIPs, and how it
// opens.
type windowSpec struct {
	Title         string
	Size, MinSize kit.Size
	// CustomFrame: the window draws its own title bar (the main window on Windows).
	CustomFrame bool
	// Maximized: it opens maximised, Size being the size it restores to.
	Maximized bool
}

// openWindow opens root's window on a as ws says, with the run's flags (-render, -dpi,
// -noactivate) and b's answer to another program's WM_CLOSE.
func openWindow(a *osApp, root kit.Root, ws windowSpec, o options, b *backend) (*osWindow, error) {
	wo := win.WindowOptions{
		Title: ws.Title, Size: ws.Size, MinSize: ws.MinSize,
		CustomFrame: ws.CustomFrame, Software: o.software, NoActivate: o.noActivate, ForceDPI: o.dpi,
		OnForeignClose: b.foreignClose,
	}
	if ws.Maximized {
		return a.NewMaximizedWindow(root, wo)
	}
	return a.NewWindow(root, wo)
}

// progressFrames are the frames the progress window's first size and its minimum leave out:
// the system frame (today's sizes are the outer window's on Windows).
func progressFrames() (initial, min kit.Size) {
	f := win.SystemFrameSize()
	return f, f
}

// mainFrame is what a frame adds to the main window's content (mainMinContent): nothing,
// the window draws its own title bar and its sizes are the client area's.
func mainFrame() kit.Size { return kit.Size{} }

// osBackend adds this system's parts to a window's backend: none on Windows (Setup
// registers with the browsers, and a silent Setup /RELAUNCH starts the window again after an
// update).
func osBackend(b *backend, gw *glue.Window) {}
