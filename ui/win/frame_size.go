//go:build windows

package win

import (
	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// FrameSize is the window's frame in DIPs: its outer size less its client size, at the DPI of
// the monitor it is on (a custom frame keeps only the side and bottom borders). A window that
// sizes itself to its content asks for its outer size through it (the progress window's fit).
// A closed window's frame is 0 × 0.
func (w *Window) FrameSize() kit.Size {
	if w.closed || w.hwnd == 0 {
		return kit.Size{}
	}
	fw, fh := w.outerSize(0, 0, w.sysDPI())
	ppd := w.ppd()
	return kit.Size{W: float32(fw) / ppd, H: float32(fh) / ppd}
}

// SystemFrameSize is the frame in DIPs of a window with the system's frame (the default
// style, no custom frame) that NewWindow would open on the primary monitor: what a window
// sized from its outer size gives NewWindow before it exists (progress.InitialSize). The
// process is DPI aware once an App exists; call it after NewApp.
func SystemFrameSize() kit.Size {
	dpi := w32.GetDpiForMonitor(w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY))
	if dpi == 0 {
		dpi = 96
	}
	var rc w32.Rect
	w32.AdjustWindowRectExForDpi(&rc, windowStyle, 0, dpi)
	ppd := float32(dpi) / 96
	return kit.Size{W: float32(rc.Right-rc.Left) / ppd, H: float32(rc.Bottom-rc.Top) / ppd}
}
