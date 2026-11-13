package views

import (
	"slices"

	"downex/ui/kit"
)

// Z is a layer's place in the stack: today's CSS z-indexes, so that a layer goes where the
// WebView window put the same thing. Layers of the same Z keep the order they were first
// set in.
type Z int

// The window's layers, bottom first (the z-indexes of App.svelte, Toasts.svelte,
// Dialog.svelte, TitleBar.svelte, PostActionBanner.svelte and CommandPalette.svelte).
const (
	// ZToastsBehind is the plain toasts (and the tools toast) while a modal dialog is open:
	// under its scrim (.toasts.behind, .tools-toast.behind: 30).
	ZToastsBehind Z = 30
	// ZDialog is a modal dialog and its scrim (.backdrop: 40).
	ZDialog Z = 40
	// ZTitleBar is a frameless window's title bar above the scrim (.titlebar.frameless: 45);
	// the native scrim starts below it (App.ModalTop), so the bar needs no layer of its own.
	ZTitleBar Z = 45
	// ZBanner is the post-queue countdown above an open dialog (.post-action: 50).
	ZBanner Z = 50
	// ZToasts is the plain toasts and the tools toast while no dialog is open (.toasts and
	// .tools-toast: 60); ZNotices the notices, which stay usable above a dialog (60, after
	// the plain toasts).
	ZToasts  Z = 60
	ZNotices Z = 60
	// ZPalette is the command palette and its dim backdrop (70): above every toast.
	ZPalette Z = 70
)

// Layers keeps a window's layers in their Z order. Set adds a widget as a layer (or moves one
// already in the window) at a Z and restacks without taking anything out of the window
// (kit.Window.RaiseLayer), so focus, motion and state survive. Layers it does not manage stay
// under the ones it does. Its methods belong to the UI thread.
type Layers struct {
	w     *kit.Window
	items []layerItem
	seq   int
}

type layerItem struct {
	l   kit.Widget
	z   Z
	seq int
}

// NewLayers manages w's layers.
func NewLayers(w *kit.Window) *Layers { return &Layers{w: w} }

func (ls *Layers) index(l kit.Widget) int {
	return slices.IndexFunc(ls.items, func(it layerItem) bool { return it.l == l })
}

// Set puts l at z: it becomes a layer of the window if it is not one yet, and every managed
// layer is put in its place.
func (ls *Layers) Set(l kit.Widget, z Z) {
	if l == nil {
		return
	}
	if i := ls.index(l); i >= 0 {
		ls.items[i].z = z
	} else {
		ls.seq++
		ls.items = append(ls.items, layerItem{l: l, z: z, seq: ls.seq})
	}
	if !slices.Contains(ls.w.Layers(), l) {
		ls.w.PushLayer(l)
	}
	ls.restack()
}

// Remove takes l out of the window and forgets it.
func (ls *Layers) Remove(l kit.Widget) {
	ls.Forget(l)
	if slices.Contains(ls.w.Layers(), l) {
		ls.w.RemoveLayer(l)
	}
}

// Forget stops managing l (it already left the window).
func (ls *Layers) Forget(l kit.Widget) {
	if i := ls.index(l); i >= 0 {
		ls.items = slices.Delete(ls.items, i, i+1)
	}
}

// Z is l's place, ok false when Layers does not manage it.
func (ls *Layers) Z(l kit.Widget) (z Z, ok bool) {
	if i := ls.index(l); i >= 0 {
		return ls.items[i].z, true
	}
	return 0, false
}

// Order is the managed layers bottom first.
func (ls *Layers) Order() []kit.Widget {
	items := slices.Clone(ls.items)
	slices.SortStableFunc(items, func(a, b layerItem) int {
		if a.z != b.z {
			return int(a.z - b.z)
		}
		return a.seq - b.seq
	})
	out := make([]kit.Widget, len(items))
	for i, it := range items {
		out[i] = it.l
	}
	return out
}

// restack raises every managed layer in order, so they end above the unmanaged ones, in Z
// order.
func (ls *Layers) restack() {
	order := ls.Order()
	cur := ls.w.Layers()
	// Nothing to do when the managed layers are already the top of the stack, in order.
	if len(cur) >= len(order) && slices.Equal(cur[len(cur)-len(order):], order) {
		return
	}
	for _, l := range order {
		ls.w.RaiseLayer(l)
	}
}
