//go:build darwin && cgo

package mac

// [popups] Drag and drop onto a window.

/*
#include <stdlib.h>
#include "drop_darwin.h"
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"strings"
	"unsafe"

	"downex/ui/kit"
)

// The window as a dragging destination (NSDraggingDestination on its content view): a link
// dragged from a browser, text, or files from the Finder. What the drag carries is read as
// today's WKWebView page saw it (DropData); a DropHandler decides what a drop does, as
// App.svelte's dragover and drop handlers do; an editable text field under the pointer can
// take the text, as a browser's text field takes a drop the page leaves to it. The names
// are ui/win's, so the glue calls both backends alike.

// DropAction is what a drop at a point does.
type DropAction uint8

const (
	// DropRefuse: nothing happens (NSDragOperationNone).
	DropRefuse DropAction = iota
	// DropCopy: the handler takes the drop (NSDragOperationCopy).
	DropCopy
	// DropToField: the text field under the pointer takes the text (DropEvent.Field).
	DropToField
)

// DropEvent is a drag at a point of the window.
type DropEvent struct {
	// At is the point in the window's content area, in DIPs.
	At kit.Point
	// Data is what the drag carries; the handler must not keep it past the call.
	Data *DropData
	// Field reports an editable hosted text field under the point.
	Field bool
}

// DropHandler decides what drags over a window do. Its methods run on the main thread
// during the drag, outside the window's calls into its root.
type DropHandler interface {
	// DragOver answers what a drop at e would do (it is asked at every move).
	DragOver(e DropEvent) DropAction
	// Drop is the drop at e: it answers what it did.
	Drop(e DropEvent) DropAction
	// DragLeave says that the drag left the window or was cancelled.
	DragLeave()
}

// DropData is what a drag over the window carries, as the WKWebView page of today's Mac
// window saw it (the DataTransfer's types and getData): browsers offer a dragged link as
// public.url beside its text and HTML, and the Finder offers files, whose names and paths
// WebKit never gives the page.
type DropData struct {
	// URIList is getData('text/uri-list'): the URL of a dragged link (public.url), or the
	// URL in a dropped Internet location (.webloc) or Internet shortcut (.url); "" when
	// there is none or it does not parse as an absolute URL.
	URIList string
	// Text is getData('text/plain'): public.utf8-plain-text, else the URL.
	Text string
	// HTML is getData('text/html'): public.html.
	HTML string
	// Files reports the type "Files": files (file URLs) or promised files are dragged.
	Files bool
	// hasText and hasHTML: the formats are offered (their text may be empty).
	hasText, hasHTML bool
}

// The DataTransfer types DropData reports.
const (
	TypeURIList = "text/uri-list"
	TypePlain   = "text/plain"
	TypeHTML    = "text/html"
	TypeFiles   = "Files"
)

// Types is the DataTransfer's types: text/plain, text/uri-list, text/html and Files, those
// that are there.
func (d *DropData) Types() []string {
	var t []string
	for _, typ := range []string{TypePlain, TypeURIList, TypeHTML, TypeFiles} {
		if d.Has(typ) {
			t = append(t, typ)
		}
	}
	return t
}

// Has reports one of Types.
func (d *DropData) Has(typ string) bool {
	switch typ {
	case TypePlain:
		return d.hasText || d.Text != ""
	case TypeURIList:
		return d.URIList != ""
	case TypeHTML:
		return d.hasHTML || d.HTML != ""
	case TypeFiles:
		return d.Files
	}
	return false
}

// dropRaw is what the pasteboard offers, before DropData's rules.
type dropRaw struct {
	url, text, html, file, webloc   string
	files, hasText, hasHTML, hasURL bool
}

// dropDataOf applies WebKit's rules to a pasteboard: with files, only "Files" (WebKit hides
// the rest so as not to expose paths) and the URL of an Internet location among them;
// otherwise the link, its text and its HTML.
func dropDataOf(r dropRaw) DropData {
	var d DropData
	d.Files = r.files
	if r.files {
		switch {
		case r.webloc != "":
			d.URIList = validURL(strings.TrimSpace(r.webloc))
		case strings.EqualFold(fileExt(r.file), ".url"):
			d.URIList = shortcutURL(r.file)
		}
	} else {
		if r.hasURL {
			d.URIList = validURL(firstLine(r.url))
		}
		if r.hasText {
			d.Text, d.hasText = r.text, true
		}
		if r.hasHTML {
			d.HTML, d.hasHTML = r.html, true
		}
	}
	if d.Text == "" && !d.hasText {
		d.Text = d.URIList
	}
	return d
}

func fileExt(p string) string {
	i := strings.LastIndexAny(p, "./")
	if i < 0 || p[i] != '.' {
		return ""
	}
	return p[i:]
}

// dropTarget is one window's drop state.
type dropTarget struct {
	w       *Window
	h       DropHandler
	handle  cgo.Handle
	data    DropData
	entered bool
}

var dropTargets = map[*Window]*dropTarget{}

// EnableDrop makes the window a drop target, with h deciding what drops do (a later call
// replaces h). It is for the main thread; the window stops being one when it closes or
// DisableDrop is called.
func (w *Window) EnableDrop(h DropHandler) error {
	if h == nil {
		return errInvalid("EnableDrop without a handler")
	}
	onMain()
	if w.closed || w.ns == nil {
		return fmt.Errorf("mac: window closed: %w", kit.ErrClosed)
	}
	if t := dropTargets[w]; t != nil {
		t.h = h
		return nil
	}
	t := &dropTarget{w: w, h: h}
	t.handle = cgo.NewHandle(t)
	dropTargets[w] = t
	C.dxpDropEnable(w.ns, C.uintptr_t(t.handle))
	return nil
}

// DisableDrop stops the window being a drop target. It is for the main thread.
func (w *Window) DisableDrop() {
	t := dropTargets[w]
	if t == nil {
		return
	}
	delete(dropTargets, w)
	if w.ns != nil {
		C.dxpDropDisable(w.ns)
	}
	t.entered = false
	t.handle.Delete()
}

// dropCall runs a drop handler's f as a call into the window, but only when no call is in
// progress: a drag that arrives while the main thread runs inside a root call (a modal
// loop) is refused rather than run re-entrantly or queued past the drag that asked.
func (w *Window) dropCall(f func()) {
	if w.closed || w.depth > 0 {
		return
	}
	w.call(f)
}

// fieldText is the text a text field takes from a drop: the plain text, else the URL.
func fieldText(d *DropData) string {
	if d.Text != "" {
		return d.Text
	}
	return d.URIList
}

// goDrop is a dragging method of a window's view: 1 entered, 2 updated, 3 exited, 4
// perform. It returns 1 for a copy, 0 for none.
//
//export goDrop
func goDrop(h C.uintptr_t, method C.int, x, y C.double, raw *C.dxpDropData, field, copyOK bool) (ret C.int) {
	if h == 0 {
		return 0
	}
	t, _ := cgo.Handle(h).Value().(*dropTarget) // DisableDrop clears the view's before it deletes it
	if t == nil || dropTargets[t.w] != t {
		return 0
	}
	w := t.w
	defer func() {
		if v := recover(); v != nil {
			ret = 0
			w.app.safely(func() { panic(v) })
		}
	}()
	if raw != nil {
		t.data = dropDataOf(rawOf(raw))
	}
	switch method {
	case 1:
		t.entered = true
	case 3:
		was := t.entered
		t.entered, t.data = false, DropData{}
		if was {
			w.dropCall(t.h.DragLeave)
		}
		return 0
	}
	if !t.entered && method == 2 {
		return 0
	}
	e := DropEvent{At: kit.Pt(float32(x), float32(y)), Data: &t.data, Field: field}
	act := DropRefuse
	w.dropCall(func() {
		if method == 4 {
			act = t.h.Drop(e)
		} else {
			act = t.h.DragOver(e)
		}
	})
	if act == DropToField && (!field || fieldText(&t.data) == "") {
		act = DropRefuse
	}
	if act != DropRefuse && !copyOK {
		act = DropRefuse // the source does not let its data be copied
	}
	if method == 4 {
		if act == DropToField {
			cs := C.CString(stripNULs(fieldText(&t.data)))
			if !C.dxpDropInsert(w.ns, x, y, cs) {
				act = DropRefuse
			}
			C.free(unsafe.Pointer(cs))
		}
		t.entered, t.data = false, DropData{}
	}
	if act == DropRefuse {
		return 0
	}
	return 1
}

func rawOf(r *C.dxpDropData) dropRaw {
	s := func(p *C.char) string {
		if p == nil {
			return ""
		}
		return C.GoString(p)
	}
	return dropRaw{
		url: s(r.url), text: s(r.text), html: s(r.html), file: s(r.file), webloc: s(r.webloc),
		files: bool(r.files), hasText: bool(r.hasText), hasHTML: bool(r.hasHTML), hasURL: bool(r.hasURL),
	}
}
