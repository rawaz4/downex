//go:build darwin && cgo

package glue

import (
	"downex/ui/mac"
	"downex/ui/views/app"
)

// DropTarget decides what drags over the main window do: views/app.Main (App.svelte's
// onDragOver and onDrop). Its methods run on the UI thread.
type DropTarget interface {
	DragOver(app.Drop) app.DropEffect
	HandleDrop(app.Drop) app.DropEffect
}

// EnableDrop makes the window a dragging destination whose drags t decides: a link dragged
// from a browser (public.url beside its text, read as today's WKWebView page saw them:
// mac.DropData) opens the Add dialog; files dropped from the Finder are refused, as today;
// text dropped into a text field goes into the field. It is for the main thread, after Attach.
func (g *Glue) EnableDrop(t DropTarget) error {
	if t == nil {
		return errorf("EnableDrop without a target")
	}
	return g.w.EnableDrop(dropBridge{t: t})
}

// dropBridge is the mac.DropHandler of a DropTarget.
type dropBridge struct{ t DropTarget }

func (b dropBridge) DragOver(e mac.DropEvent) mac.DropAction {
	return actionOf(b.t.DragOver(dropOf(e)))
}

// Drop runs the target's drop at once: it opens a dialog at most, which enters no modal loop.
func (b dropBridge) Drop(e mac.DropEvent) mac.DropAction { return actionOf(b.t.HandleDrop(dropOf(e))) }

func (dropBridge) DragLeave() {} // App.svelte has no dragleave handler

// dropOf is the views' Drop of a drag: the DataTransfer's text/uri-list and text/plain, its
// "Files" type, the offered formats, and whether the point is over an editable field.
func dropOf(e mac.DropEvent) app.Drop {
	if e.Data == nil {
		return app.Drop{OnField: e.Field}
	}
	d := e.Data
	return app.Drop{
		URIList: d.URIList,
		Text:    d.Text,
		Files:   d.Files,
		URIs:    d.Has(mac.TypeURIList),
		Plain:   d.Has(mac.TypePlain),
		OnField: e.Field,
	}
}

func actionOf(e app.DropEffect) mac.DropAction {
	switch e {
	case app.DropCopy:
		return mac.DropCopy
	case app.DropField:
		return mac.DropToField
	}
	return mac.DropRefuse
}
