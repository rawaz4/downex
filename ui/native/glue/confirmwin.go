//go:build windows || (darwin && cgo)

package glue

import (
	"strconv"

	"downex/ui/host"
)

// The standalone Confirm download window and the main window (docs/DESKTOP-SPEC.md §1): a
// download the user just started in the browser is confirmed in a window of its own, and the
// main window, when it is open (in the tray, minimised or shown), does not show the same
// confirmation too: it does not open confirmations by itself while that window is open, and
// takes away the dialog of a download that window shows.

// confirmHooks are how the main window hears of the standalone Confirm download window.
type confirmHooks struct {
	shown   func(id int64) // that window shows the confirmation of id
	changed func()         // that window opened or closed
}

// SetConfirmHooks is for the main window: shown is told each download whose confirmation the
// standalone Confirm download window shows, changed when that window opens or closes (read
// ConfirmWindowOpen then). Both run on the UI thread.
func (w *Window) SetConfirmHooks(shown func(id int64), changed func()) {
	if w == nil {
		return
	}
	w.hooks = confirmHooks{shown: shown, changed: changed}
}

// ConfirmWindowOpen reports the standalone Confirm download window open (UI thread).
func (w *Window) ConfirmWindowOpen() bool { return w != nil && w.h != nil && w.h.confirm != nil }

// ConfirmShown is for the standalone Confirm download window: it shows the confirmation of
// id, which the main window then does not show (UI thread).
func (w *Window) ConfirmShown(id int64) {
	if w == nil || w.h == nil {
		return
	}
	if m := w.h.main; m != nil && m.hooks.shown != nil {
		m.hooks.shown(id)
	}
}

// MarkClosing is for the standalone Confirm download window: it is about to close (it asked
// for its close, which is on its way). A later start's "--confirm <id> --raise" then opens a
// new Confirm download window instead of going to this one, which would drop it (UI thread).
// The host keeps this window as its Confirm download window until the new one opens or it
// has closed, so that the main window never takes the browser's confirmations meanwhile.
func (w *Window) MarkClosing() {
	if w != nil {
		w.closing = true
	}
}

// ReopenConfirm is for the standalone Confirm download window, closing: ids are a request it
// took just before it began to close, and a new Confirm download window shows them (UI
// thread). The request runs at once rather than posted: this window's close is posted
// already, and when it is the process's last window the UI thread's loop ends with it, before
// a later post would run.
func (w *Window) ReopenConfirm(ids []int64) {
	if w == nil || w.h == nil || len(ids) == 0 {
		return
	}
	w.closing = true // never handed back to this window
	args := make([]string, 0, 2*len(ids)+1)
	for _, id := range ids {
		args = append(args, "--"+host.ConfirmFlag, strconv.FormatInt(id, 10))
	}
	w.h.Request(append(args, "--"+host.RaiseFlag))
}

// confirmChanged tells the main window that the standalone Confirm download window opened or
// closed.
func (h *Host) confirmChanged() {
	if m := h.main; m != nil && m.hooks.changed != nil {
		m.hooks.changed()
	}
}
