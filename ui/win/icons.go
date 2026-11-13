//go:build windows

package win

import (
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// windowIcons are a window's own big and small icons (title bar, taskbar, Alt+Tab), loaded
// from the executable's icon resource at the sizes of its monitor's DPI, so that the system
// never scales a 96-DPI icon up. The window owns them: replaced ones and, when the window
// is gone, the last ones are destroyed.
type windowIcons struct {
	big, small windows.Handle
	dpi        uint32 // the DPI they were loaded at; 0: none yet
}

// applyIcons gives the window its icons at the DPI of its monitor (the frame's DPI, which
// a forced content DPI does not change) if it has none at that DPI yet.
func (w *Window) applyIcons() {
	if w.app.iconID == 0 || w.hwnd == 0 {
		return
	}
	dpi := w.sysDPI()
	if dpi == w.icons.dpi {
		return
	}
	inst, id := w.app.inst, w.app.iconID
	big := w32.LoadIcon(inst, id, w32.GetSystemMetricsForDpi(w32.SM_CXICON, dpi))
	small := w32.LoadIcon(inst, id, w32.GetSystemMetricsForDpi(w32.SM_CXSMICON, dpi))
	if big == 0 || small == 0 {
		w32.DestroyIcon(big)
		w32.DestroyIcon(small)
		return // the ones it has stay
	}
	w32.SendMessage(w.hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(big))
	w32.SendMessage(w.hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(small))
	w32.DestroyIcon(w.icons.big)
	w32.DestroyIcon(w.icons.small)
	w.icons = windowIcons{big: big, small: small, dpi: dpi}
}

// releaseIcons destroys the window's icons once the window is gone (WM_NCDESTROY).
func (w *Window) releaseIcons() {
	w32.DestroyIcon(w.icons.big)
	w32.DestroyIcon(w.icons.small)
	w.icons = windowIcons{}
}
