//go:build windows

package win

import (
	"fmt"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
	"downex/ui/win/uia"
)

// UI Automation of the popup windows (a menu, a Select's list): a popup is a top-level
// window of its own, which UI Automation reaches by its HWND, so it answers WM_GETOBJECT as
// a Window does (uia_window.go), with a provider (uia.Tree) over its own root's semantic
// tree: the Menu and its MenuItems (with their Toggle state for the radio and check box
// items), or the List and its ListItems (SelectionItem; the active one has the keyboard
// focus in the popup's tree). The provider is made at the first WM_GETOBJECT that asks for
// UI Automation's root object, its events follow the popup root's semantic events (focus
// changed as the active item moves), and it is disconnected when the popup closes, before
// its root's last call. The trigger stays in the owner's tree, where its ExpandCollapse
// state follows the popup (the widgets report it).

// uiaGetObject answers WM_GETOBJECT for UI Automation's root object (ok false for anything
// else, which DefWindowProc answers).
func (p *popupWindow) uiaGetObject(wp, lp uintptr) (uintptr, bool) {
	if int32(uint32(lp)) != uia.UiaRootObjectId || p.closed || p.uiaDone || !p.attached {
		return 0, false
	}
	if p.uia == nil {
		if p.root.Semantics() == nil {
			return 0, false
		}
		t, err := uia.NewTree(popupUIAHost{p})
		if err != nil {
			p.owner.hostErr = fmt.Errorf("win: UI Automation of a popup: %w", err)
			return 0, false
		}
		p.uia = t
	}
	return p.uia.GetObject(wp, lp)
}

// uiaNotify raises the UI Automation events of a semantic change in the popup: nothing
// while no client has read it.
func (p *popupWindow) uiaNotify(e kit.SemanticEvent) {
	if p.uia != nil {
		p.uia.Notify(e)
	}
}

// uiaDestroyed disconnects the popup's providers (clients' elements of it report that they
// are gone, UI Automation drops what it holds) while the popup and its root are still
// whole; no provider is made for it again.
func (p *popupWindow) uiaDestroyed() {
	p.uiaDone = true
	if t := p.uia; t != nil {
		p.uia = nil
		t.Close()
	}
}

// popupUIAHost is a popup as its provider sees it.
type popupUIAHost struct{ p *popupWindow }

var _ uia.Host = popupUIAHost{}

func (h popupUIAHost) HWND() uintptr { return uintptr(h.p.hwnd) }

// Semantics is the popup root's tree while the popup is open.
func (h popupUIAHost) Semantics() kit.SemanticTree {
	if h.p.closed || !h.p.attached {
		return nil
	}
	return h.p.root.Semantics()
}

// origin is the popup's client origin on the screen, in pixels: where it was last placed
// (a WS_POPUP window without a frame: its window and client origins are one).
func (h popupUIAHost) origin() w32.Point {
	if h.p.hwnd != 0 {
		return w32.ClientToScreen(h.p.hwnd, w32.Point{})
	}
	return h.p.at
}

// ToScreen maps popup DIPs to physical screen pixels.
func (h popupUIAHost) ToScreen(r kit.Rect) uia.Rect {
	o := h.origin()
	ppd := float64(h.p.ppd)
	return uia.Rect{
		Left: float64(o.X) + float64(r.Min.X)*ppd, Top: float64(o.Y) + float64(r.Min.Y)*ppd,
		Width: float64(r.Width()) * ppd, Height: float64(r.Height()) * ppd,
	}
}

// FromScreen maps a physical screen point to popup DIPs.
func (h popupUIAHost) FromScreen(x, y float64) kit.Point {
	o := h.origin()
	ppd := float64(h.p.ppd)
	return kit.Pt(float32((x-float64(o.X))/ppd), float32((y-float64(o.Y))/ppd))
}

// Perform does r as a call into the popup's root, after the call in progress when one is.
func (h popupUIAHost) Perform(r kit.ActionRequest) error {
	st := h.Semantics()
	if st == nil {
		return fmt.Errorf("win: UI Automation action on a closed popup: %w", kit.ErrClosed)
	}
	ran := false
	var err error
	h.p.call(func() {
		ran = true
		err = st.Perform(r)
	})
	if !ran {
		return nil // queued behind the call in progress
	}
	return err
}

// Post does r as a call into the popup's root once the client's call has returned.
func (h popupUIAHost) Post(r kit.ActionRequest) error {
	p := h.p
	return p.Post(func() {
		if st := (popupUIAHost{p}).Semantics(); st != nil {
			_ = st.Perform(r) // the client was told it was invoked; a node gone since is no error to report
		}
	})
}

// A popup hosts no native fields and draws no title bar.
func (h popupUIAHost) HostedWindow(*kit.SemanticNode) uintptr { return 0 }
func (h popupUIAHost) HostedBounds(uintptr) (kit.Rect, bool)  { return kit.Rect{}, false }
func (h popupUIAHost) CustomFrame() bool                      { return false }
