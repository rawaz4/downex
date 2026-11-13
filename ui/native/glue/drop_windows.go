//go:build windows

package glue

import (
	"downex/ui/views/app"
	"downex/ui/win"
)

// DropTarget decides what drags over the main window do: views/app.Main (App.svelte's
// onDragOver and onDrop). Its methods run on the UI thread.
type DropTarget interface {
	DragOver(app.Drop) app.DropEffect
	HandleDrop(app.Drop) app.DropEffect
}

// EnableDrop makes the window an OLE drop target whose drags t decides: a link dragged from
// Chrome, Edge or Firefox (UniformResourceLocatorW, text/x-moz-url, CF_UNICODETEXT and the
// HTML formats, read as today's WebView2 page saw them: win.DropData) opens the Add dialog;
// files dropped from Explorer are refused, as today; text dropped into a text field goes into
// the field. It is for the UI thread, after Attach.
func (g *Glue) EnableDrop(t DropTarget) error {
	if t == nil {
		return errorf("EnableDrop without a target")
	}
	return g.w.EnableDrop(dropBridge{t: t})
}

// dropBridge is the win.DropHandler of a DropTarget.
type dropBridge struct{ t DropTarget }

func (b dropBridge) DragOver(e win.DropEvent) win.DropAction {
	return actionOf(b.t.DragOver(dropOf(e)))
}

// Drop runs the target's drop at once: it opens a dialog at most, which enters no modal loop.
func (b dropBridge) Drop(e win.DropEvent) win.DropAction { return actionOf(b.t.HandleDrop(dropOf(e))) }

func (dropBridge) DragLeave() {} // App.svelte has no dragleave handler

// dropOf is the views' Drop of a drag: the DataTransfer's text/uri-list and text/plain, its
// "Files" type, the offered formats, and whether the point is over an editable field.
func dropOf(e win.DropEvent) app.Drop {
	if e.Data == nil {
		return app.Drop{OnField: e.Field}
	}
	d := e.Data
	return app.Drop{
		URIList: d.URIList,
		Text:    d.Text,
		Files:   d.Files,
		URIs:    d.Has(win.TypeURIList),
		Plain:   d.Has(win.TypePlain),
		OnField: e.Field,
	}
}

func actionOf(e app.DropEffect) win.DropAction {
	switch e {
	case app.DropCopy:
		return win.DropCopy
	case app.DropField:
		return win.DropToField
	}
	return win.DropRefuse
}
