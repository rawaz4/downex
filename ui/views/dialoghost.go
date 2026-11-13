package views

import (
	"slices"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// ModalKind names what a modal is: App.svelte's DialogState kinds and the palette.
type ModalKind uint8

// The modal kinds.
const (
	ModalNone ModalKind = iota
	ModalAdd
	ModalSettings
	ModalQueues
	ModalMove
	ModalRemove
	ModalRestart
	ModalOpen
	ModalConfirm
	// ModalPalette is the command palette: it has its own place beside the dialog's
	// (App.svelte's palette state), above every toast.
	ModalPalette
	// ModalUpdate is the pop-up that offers an update downexd found (docs/M6-SPEC.md §4).
	ModalUpdate
)

var modalNames = [...]string{"none", "add", "settings", "queues", "move", "remove", "restart", "open", "confirm", "palette", "update"}

func (k ModalKind) String() string {
	if int(k) < len(modalNames) {
		return modalNames[k]
	}
	return "modal"
}

// Modal is what the DialogHost shows: a dialog of a screen lane (a *Dialog, made with
// App.NewDialog) or the palette.
type Modal interface {
	// ModalKind is what it is.
	ModalKind() ModalKind
	// ShowModal puts the modal into w and returns its layer (the host stacks it: ZDialog, or
	// ZPalette for the palette). The modal calls done exactly once when it has left the
	// window: after its closing motion when the user closed it, at once when its owner
	// removed it (after a save) or DismissModal took it away.
	ShowModal(w *kit.Window, done func()) kit.Widget
	// DismissModal takes it away at once, without its OnClose: a newer modal replaces it
	// (App.svelte assigning `dialog` again) or the window closes.
	DismissModal()
}

// DialogHost shows the window's modals as App.svelte does: one dialog at a time (a new one
// replaces the one shown), and the palette beside it. While either is open the main content
// is inert (SetMain: `.app inert={dialog !== null || palette}`); while a dialog is open the
// plain toasts and the tools toast sit under its scrim and the notices above it
// (`<Toasts behind={dialog !== null}>`), the palette above them all. Closing gives the focus
// back to where it was, or App.FallbackFocus. Changes tells of every change, on the UI thread
// (the title bar's modal look, the confirm queue waiting for the dialog to close). Its methods
// belong to the UI thread.
type DialogHost struct {
	Changes state.Signal

	a       *App
	dialog  *modalEntry
	palette *modalEntry
	main    []*kit.Element
	behind  []kit.Widget // the layers that go behind a dialog: the plain toasts, the tools toast
	menu    bool
	// returnTo is what had the focus when the first modal opened: it gets it back when the
	// last one closes, if the dialog's trap could not give it back (the main content was
	// still inert when the trap let go).
	returnTo *kit.Element
}

type modalEntry struct {
	m     Modal
	layer kit.Widget
	gone  bool
}

func newDialogHost(a *App) *DialogHost {
	h := &DialogHost{a: a}
	h.behind = []kit.Widget{a.ToastLayer.Plain()}
	return h
}

// SetMain sets the elements made inert while a modal is open (the sidebar, the work card and
// the status bar; the title bar's search and buttons).
func (h *DialogHost) SetMain(els ...*kit.Element) {
	for _, e := range h.main {
		e.Inert = false
	}
	h.main = slices.Clone(els)
	h.apply()
}

// AddBehind adds a layer that goes under a dialog's scrim as the plain toasts do (the tools
// toast: `.tools-toast.behind`). It must be managed by App.Layers.
func (h *DialogHost) AddBehind(l kit.Widget) {
	if !slices.Contains(h.behind, l) {
		h.behind = append(h.behind, l)
	}
	h.apply()
}

// ToastsZ is where the plain toasts go now: ZToastsBehind while a dialog is open, else
// ZToasts.
func (h *DialogHost) ToastsZ() Z {
	if h.dialog != nil {
		return ZToastsBehind
	}
	return ZToasts
}

// Show shows m: a dialog replaces the dialog shown (which is taken away without its OnClose,
// as App.svelte's `dialog = …` replaces the component), the palette replaces the palette.
func (h *DialogHost) Show(m Modal) {
	if m == nil {
		return
	}
	slot := &h.dialog
	z := ZDialog
	if m.ModalKind() == ModalPalette {
		slot, z = &h.palette, ZPalette
	}
	if !h.Modal() {
		h.returnTo = h.a.Window.Focused()
	}
	if old := *slot; old != nil {
		*slot = nil
		h.drop(old)
		old.m.DismissModal()
	}
	e := &modalEntry{m: m}
	*slot = e
	e.layer = m.ShowModal(h.a.Window, func() { h.done(e) })
	if e.gone { // it left at once
		return
	}
	if e.layer != nil {
		h.a.Layers.Set(e.layer, z)
	}
	h.apply()
	h.Changes.Notify()
}

// ShowIfFree shows a dialog only while no dialog is open (App.svelte's requestRestart: "Another
// dialog came first: this one is not shown"); it reports whether it did. The palette does not
// count.
func (h *DialogHost) ShowIfFree(m Modal) bool {
	if m == nil || (m.ModalKind() != ModalPalette && h.dialog != nil) || (m.ModalKind() == ModalPalette && h.palette != nil) {
		return false
	}
	h.Show(m)
	return true
}

func (h *DialogHost) done(e *modalEntry) {
	if e.gone {
		return
	}
	e.gone = true
	switch {
	case h.dialog == e:
		h.dialog = nil
	case h.palette == e:
		h.palette = nil
	default:
		return // replaced already
	}
	h.drop(e)
	h.apply()
	h.restoreFocus()
	h.Changes.Notify()
}

// restoreFocus gives the focus back to what had it before the modals once the last one has
// gone and nothing has the focus (focus-trap.ts's restore, App.svelte's fallbackFocus).
func (h *DialogHost) restoreFocus() {
	if h.Modal() {
		return
	}
	w := h.a.Window
	prev := h.returnTo
	h.returnTo = nil
	w.Defer(func() {
		if h.Modal() || w.Focused() != nil {
			return
		}
		if prev != nil && w.CanRestoreFocus(prev) {
			w.Focus(prev.Widget())
			return
		}
		if f := h.a.Fallback(); f != nil {
			w.Focus(f)
		}
	})
}

// drop stops managing a modal's layer (the modal takes it out of the window).
func (h *DialogHost) drop(e *modalEntry) {
	e.gone = true
	if e.layer != nil {
		h.a.Layers.Forget(e.layer)
	}
}

// apply makes the main content inert while a modal is open and stacks the toasts.
func (h *DialogHost) apply() {
	modal := h.Modal()
	for _, e := range h.main {
		if e.Inert != modal {
			e.Inert = modal
			e.Invalidate()
			e.SemanticsChanged()
		}
	}
	z := h.ToastsZ()
	for _, l := range h.behind {
		if cur, ok := h.a.Layers.Z(l); ok && cur != z {
			h.a.Layers.Set(l, z)
		}
	}
}

// Dialog is the dialog shown (nil for none); Kind its kind (ModalNone).
func (h *DialogHost) Dialog() Modal {
	if h.dialog == nil {
		return nil
	}
	return h.dialog.m
}

// Kind is the kind of the dialog shown, ModalNone when none is.
func (h *DialogHost) Kind() ModalKind {
	if h.dialog == nil {
		return ModalNone
	}
	return h.dialog.m.ModalKind()
}

// DialogOpen reports that a dialog is shown (App.svelte's dialog !== null).
func (h *DialogHost) DialogOpen() bool { return h.dialog != nil }

// PaletteOpen reports that the palette is shown.
func (h *DialogHost) PaletteOpen() bool { return h.palette != nil }

// Palette is the palette shown (nil for none).
func (h *DialogHost) Palette() Modal {
	if h.palette == nil {
		return nil
	}
	return h.palette.m
}

// Modal reports that a dialog or the palette is shown: the main content is inert and the
// title bar looks modal (TitleBar's modal={dialog !== null || palette}).
func (h *DialogHost) Modal() bool { return h.dialog != nil || h.palette != nil }

// SetMenuOpen tells the host that the context menu opened or closed (App.svelte's `menu`):
// the confirm queue waits for it as for a dialog.
func (h *DialogHost) SetMenuOpen(open bool) {
	if open != h.menu {
		h.menu = open
		h.Changes.Notify()
	}
}

// MenuOpen reports that the context menu is open.
func (h *DialogHost) MenuOpen() bool { return h.menu }

// Busy reports that a dialog or the context menu is open: a confirmation that opens by itself
// waits (App.svelte: `dialog !== null || menu !== null`).
func (h *DialogHost) Busy() bool { return h.dialog != nil || h.menu }

// DismissAll takes every modal away at once (the window is closing).
func (h *DialogHost) DismissAll() {
	for _, slot := range []**modalEntry{&h.dialog, &h.palette} {
		if e := *slot; e != nil {
			*slot = nil
			h.drop(e)
			e.m.DismissModal()
		}
	}
	h.apply()
	h.Changes.Notify()
}

// Dialog is a screen lane's dialog: today's Dialog.svelte shell (widgets.Dialog) that the
// DialogHost shows. Close (Escape, the close button) plays the closing motion and then runs
// the options' OnClose; Remove takes it away at once (after a save). Use App.NewDialog.
type Dialog struct {
	*widgets.Dialog
	kind ModalKind
	done func()
	gone bool
}

var _ Modal = (*Dialog)(nil)

// NewDialog makes a dialog of a kind with the window's defaults: its focus falls back to
// App.FallbackFocus when its opener is gone, and its scrim starts below a frameless title
// bar (ModalTop), unless o sets them.
func (a *App) NewDialog(kind ModalKind, o widgets.DialogOptions) *Dialog {
	d := &Dialog{kind: kind}
	if o.Fallback == nil {
		o.Fallback = a.Fallback
	}
	if o.ModalTop == 0 {
		o.ModalTop = a.ModalTop()
	}
	onClose := o.OnClose
	o.OnClose = func() {
		d.finish()
		if onClose != nil {
			onClose()
		}
	}
	d.Dialog = widgets.NewDialog(o)
	return d
}

// ModalKind is Modal's.
func (d *Dialog) ModalKind() ModalKind { return d.kind }

// ShowModal is Modal's: widgets.Dialog.Show.
func (d *Dialog) ShowModal(w *kit.Window, done func()) kit.Widget {
	d.done, d.gone = done, false
	d.Dialog.Show(w)
	return d.Dialog
}

// DismissModal is Modal's: the dialog goes at once, without OnClose.
func (d *Dialog) DismissModal() {
	d.Dialog.Remove()
	d.finish()
}

// Remove takes the dialog away at once (its owner closes it after a save); its OnClose does
// not run.
func (d *Dialog) Remove() {
	d.Dialog.Remove()
	d.finish()
}

func (d *Dialog) finish() {
	if d.gone {
		return
	}
	d.gone = true
	if d.done != nil {
		d.done()
	}
}
