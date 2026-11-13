//go:build windows

package win

import (
	"fmt"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// Window placement memory (today's ui/window_state.go): a main window opens at the size the
// user left it, maximised if it was. The window reads its restored size with
// GetWindowPlacement, so the size kept while it is maximised or minimised is the last one
// the user gave it, and NewMaximizedWindow opens it maximised from its first frame.

// wpfRestoreToMaximize is WinUser.h's WPF_RESTORETOMAXIMIZED (10.0.26100.0).
const wpfRestoreToMaximize = 0x0002

// Placement is a top-level window's size as the user left it.
type Placement struct {
	// Normal is the client size in DIPs of the window when it is neither maximised nor
	// minimised: its size now, or the size it restores to.
	Normal kit.Size
	// Maximized: the window is maximised, or minimised from maximised (it restores to
	// maximised).
	Maximized bool
	// Minimized: the window is minimised.
	Minimized bool
}

// Placement reads the window's placement (GetWindowPlacement): its restored client size, in
// the DIPs of the window's scale, and whether it is maximised or minimised.
func (w *Window) Placement() (Placement, error) {
	if w.closed || w.hwnd == 0 {
		return Placement{}, fmt.Errorf("win: Placement: %w", kit.ErrClosed)
	}
	if err := w32.GetWindowPlacement(w.hwnd, &w.placement); err != nil {
		return Placement{}, fmt.Errorf("win: %w", err)
	}
	// rcNormalPosition is the window's restored rectangle, frame included (in workspace
	// coordinates, which only move it); the frame is what outerSize adds to a client area.
	n := w.placement.NormalPosition
	fw, fh := w.outerSize(0, 0, w.sysDPI())
	ppd := w.ppd()
	p := Placement{
		Normal: kit.Size{
			W: float32(max(n.Right-n.Left-fw, 0)) / ppd,
			H: float32(max(n.Bottom-n.Top-fh, 0)) / ppd,
		},
		Minimized: w32.IsIconic(w.hwnd),
	}
	if p.Minimized {
		p.Maximized = w.placement.Flags&wpfRestoreToMaximize != 0
	} else {
		p.Maximized = w32.IsZoomed(w.hwnd)
	}
	return p, nil
}

// NewMaximizedWindow is NewWindow for a window that opens maximised and restores to
// opts.Size, placed and fitted into the work area as NewWindow places it. The window is
// maximised while it is still cloaked, before the root lays it out (Attach), so its first
// frame is drawn and shown maximised. With NoActivate the system's show restores it
// (SW_SHOWNOACTIVATE), so it is maximised again at once, after it was first shown. A
// maximise that fails leaves the window open at its restored size; HostErr says why.
func (a *App) NewMaximizedWindow(root kit.Root, opts WindowOptions) (*Window, error) {
	if root == nil {
		return a.NewWindow(nil, opts)
	}
	w, err := a.NewWindow(maximizeOnAttach{root}, opts)
	if err != nil {
		return nil, err
	}
	if !w.closed && !w32.IsZoomed(w.hwnd) {
		w.maximize()
	}
	return w, nil
}

// maximizeOnAttach is a root that maximises its window when it is attached, before the
// window is shown; the rest is the root's.
type maximizeOnAttach struct{ kit.Root }

// Attach implements kit.Root.
func (m maximizeOnAttach) Attach(h kit.Host) {
	if w, ok := h.(*Window); ok && !w.closed {
		w.setDWM(w32.DWMWA_CLOAK, 1) // NewWindow uncloaks it after its first frame
		w.maximize()
	}
	m.Root.Attach(h)
}

// maximize maximises the window, keeping its restored rectangle (SetWindowPlacement with
// SW_SHOWMAXIMIZED); a failure is kept in hostErr.
func (w *Window) maximize() {
	err := w32.GetWindowPlacement(w.hwnd, &w.placement)
	if err == nil {
		w.placement.ShowCmd = w32.SW_MAXIMIZE // SW_SHOWMAXIMIZED
		err = w32.SetWindowPlacement(w.hwnd, &w.placement)
	}
	if err != nil {
		w.hostErr = fmt.Errorf("win: maximise as the window opens: %w", err)
	}
}

// HostErr is the last failure of a window call that reports none (Resize, the maximise of
// NewMaximizedWindow), or nil.
func (w *Window) HostErr() error { return w.hostErr }
