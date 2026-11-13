package app

import (
	"regexp"
	"strings"

	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views"
)

// keyRoot is the composition's root element: a key nobody inside used reaches it before the
// window's OnKey, so a pasted link works wherever the focus is in the window.
type keyRoot struct {
	kit.Element
	m *Main
}

// HandleKey is kit.KeyHandler's: Ctrl+V and Shift+Insert paste a link (the shortcuts are the
// window's OnKey's: HandleKey); while a menu of the page header or the status bar is open, its
// keys are its own and the window's shortcuts do not run.
func (r *keyRoot) HandleKey(e kit.KeyEvent) bool {
	if r.m.Shell.MenuOpen() && e.Action == kit.KeyDown && e.Key != kit.KeyTab {
		return e.Key != kit.KeyEscape
	}
	return r.m.pasteKey(e)
}

// WidgetTheme is widgets.ThemeSource's: the window's layers (dialogs, toasts) take the App's
// theme from the window's content.
func (r *keyRoot) WidgetTheme() widgets.Theme { return r.m.App.WidgetTheme }

// HandleKey is what the window's kit.WindowOptions.OnKey calls with the keys no element used
// (App.svelte's onWindowKeydown and onWindowPaste): a pasted link opens Add, then the
// shortcuts of the command registry.
func (m *Main) HandleKey(e kit.KeyEvent) bool {
	if m.pasteKey(e) {
		return true
	}
	k := views.KeyContextOf(m.App.Window)
	if m.Shell.MenuOpen() {
		k.InMenu = true // a menu of the page header or the status bar has the keyboard
	}
	return m.App.Commands.HandleKey(e, k)
}

// isPaste reports the paste keys (Ctrl+V, Shift+Insert; ⌘V).
func isPaste(e kit.KeyEvent) bool {
	if e.Action != kit.KeyDown {
		return false
	}
	mod := e.Mods&(kit.ModCtrl|kit.ModMeta) != 0
	return (mod && e.Mods&(kit.ModAlt|kit.ModShift) == 0 && e.Key == kit.KeyV) ||
		(e.Mods == kit.ModShift && e.Key == kit.KeyInsert)
}

func (m *Main) pasteKey(e kit.KeyEvent) bool {
	if !isPaste(e) {
		return false
	}
	w := m.App.Window
	if views.KeyContextOf(w).Editable {
		return false // a field takes its own paste
	}
	h := w.Host()
	if h == nil {
		return false
	}
	s, err := h.ClipboardText()
	if err != nil {
		return false
	}
	return m.Paste(s)
}

// Paste is a paste outside the fields (App.svelte's onWindowPaste): with no dialog, menu or
// palette open, the clipboard's http(s) links open the Add dialog with them, one per line. It
// reports whether it used the text.
func (m *Main) Paste(s string) bool {
	d := m.App.Dialogs
	if d.DialogOpen() || d.MenuOpen() || d.PaletteOpen() {
		return false
	}
	urls := webLinks(s)
	if len(urls) == 0 {
		return false
	}
	m.App.Open.Add(views.AddRequest{URLs: strings.Join(urls, "\n")})
	return true
}

// webLinks are the http(s) addresses among s's tokens (splitUrls(text).filter(/^https?:\/\//i)).
func webLinks(s string) []string {
	var out []string
	for _, u := range validate.SplitURLs(s) {
		if webLink.MatchString(u) {
			out = append(out, u)
		}
	}
	return out
}

var (
	webLink     = regexp.MustCompile(`(?i)^https?://`)
	commentLine = regexp.MustCompile(`(?m)^#.*$`)
)

// ---- drag and drop ----------------------------------------------------------------------------

// Drop is what is dragged over or dropped on the window, as the drop target reads it (the
// glue lane's OLE IDropTarget in ui/win: CF_UNICODETEXT and the "UniformResourceLocatorW"
// or "text/uri-list" formats, CF_HDROP for files).
type Drop struct {
	// URIList is the text/uri-list data ("" without it); Text the plain text.
	URIList, Text string
	// Files: the data carries files (CF_HDROP, Explorer's drag).
	Files bool
	// URIs and Plain report that the formats are offered (a drag over the window knows the
	// formats before their data).
	URIs, Plain bool
	// OnField: the point is over an editable field (text dropped there is the field's).
	OnField bool
}

// DropEffect is the drop target's answer.
type DropEffect uint8

// The effects.
const (
	// DropNone refuses the drop (the pointer shows no drop; nothing happens).
	DropNone DropEffect = iota
	// DropCopy takes it (a link: the Add dialog opens with it).
	DropCopy
	// DropField leaves it to the field under the pointer (text into a text field).
	DropField
)

// leaveToField is App.svelte's leaveToField: text dropped into an editable field, not files.
func (d Drop) leaveToField() bool { return d.OnField && !d.Files }

// DragOver is App.svelte's onDragOver: links and text are taken while no dialog is open;
// anything else is refused unless a field takes it (a dropped file never replaces the app).
func (m *Main) DragOver(d Drop) DropEffect {
	if !m.App.Dialogs.DialogOpen() && (d.URIs || d.Plain || d.URIList != "" || d.Text != "") {
		return DropCopy
	}
	if d.leaveToField() {
		return DropField
	}
	return DropNone
}

// HandleDrop is App.svelte's onDrop: with no dialog open, the http(s) links of the uri-list (or
// the text), comment lines left out, open the Add dialog; everything else is refused, except
// text a field takes. It reports what was done.
func (m *Main) HandleDrop(d Drop) DropEffect {
	if !m.App.Dialogs.DialogOpen() {
		t := d.URIList
		if t == "" {
			t = d.Text
		}
		if urls := webLinks(commentLine.ReplaceAllString(t, "")); len(urls) > 0 {
			m.App.Open.Add(views.AddRequest{URLs: strings.Join(urls, "\n")})
			return DropCopy
		}
	}
	if d.leaveToField() {
		return DropField
	}
	return DropNone
}
