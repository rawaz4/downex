//go:build windows || (darwin && cgo)

package main

import (
	"runtime"
	"testing"

	"downex/ui/host"
	"downex/ui/kit"
)

// The main window's minimum and stored size are the frame's on macOS, as today's Wails
// window had them (setMinSize: 900 × 600 on the frame, WindowGetSize = the frame in
// ui-window.json), and the client area's on Windows (no frame: nothing changes there).
func TestMainSizesFollowTheFrame(t *testing.T) {
	spec := host.WindowSpec{Width: host.MainWidth, Height: host.MainHeight, MinWidth: host.MainMinWidth, MinHeight: host.MainMinHeight}
	mac := kit.Size{H: 32}
	if got := mainMinContent(spec, mac); got != (kit.Size{W: 900, H: 568}) {
		t.Errorf("macOS content minimum %v, want 900 × 568", got)
	}
	// A file today's window wrote after a window left at 1000 × 700 of content: 1000 × 732.
	if got := mainStartContent(host.WindowState{Width: 1000, Height: 732}, spec, mac); got != (kit.Size{W: 1000, H: 700}) {
		t.Errorf("macOS opens at %v, want the content it was left at, 1000 × 700", got)
	}
	// Closing at that content stores the same frame size again: no growth by a title bar.
	if got := mainPlacedState(kit.Size{W: 1000, H: 700}, false, spec, mac); got != (host.WindowState{Width: 1000, Height: 732}) {
		t.Errorf("macOS stores %+v, want 1000 × 732", got)
	}
	// The smallest window stores the frame minimum.
	if got := mainPlacedState(kit.Size{W: 900, H: 568}, false, spec, mac); got != (host.WindowState{Width: 900, Height: 600}) {
		t.Errorf("macOS smallest stores %+v", got)
	}
	// Windows: no frame, the sizes as before.
	var none kit.Size
	if got := mainMinContent(spec, none); got != (kit.Size{W: 900, H: 600}) {
		t.Errorf("Windows minimum %v", got)
	}
	if got := mainStartContent(host.WindowState{Width: 1000, Height: 732}, spec, none); got != (kit.Size{W: 1000, H: 732}) {
		t.Errorf("Windows opens at %v", got)
	}
	if got := mainPlacedState(kit.Size{W: 1000, H: 700}, true, spec, none); got != (host.WindowState{Width: 1000, Height: 700, Maximised: true}) {
		t.Errorf("Windows stores %+v", got)
	}
	if f := mainFrame(); runtime.GOOS == "windows" && f != none {
		t.Errorf("Windows frame %v", f)
	} else if runtime.GOOS == "darwin" && f.H <= 0 {
		t.Errorf("macOS frame %v: the title bar is missing", f)
	}
}
