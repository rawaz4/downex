package app_test

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/app"
)

// TestPaste is App.svelte's onWindowPaste through the window's keys: outside a text field and
// with no dialog, menu or palette open, the clipboard's http(s) links (splitUrls, de-duplicated,
// in order) open the Add dialog with them one per line; anything else is left alone.
func TestPaste(t *testing.T) {
	for _, c := range []struct {
		name, clip string
		key        kit.Key
		mods       kit.Modifiers
		want       string // the Add dialog's URLs; "" none opens
	}{
		{"one link", "https://example.com/a.zip", kit.KeyV, kit.ModCtrl, "https://example.com/a.zip"},
		{"links one per line", "https://example.com/a.zip\nhttp://example.org/b.iso", kit.KeyV, kit.ModCtrl, "https://example.com/a.zip\nhttp://example.org/b.iso"},
		{"links among words, case-insensitive scheme", "get https://example.com/a.zip and HTTP://EXAMPLE.ORG/B.ISO\tthen ftp://example.net/c", kit.KeyV, kit.ModCtrl, "https://example.com/a.zip\nHTTP://EXAMPLE.ORG/B.ISO"},
		{"duplicates once, in order", "https://x.example/1 https://x.example/2 https://x.example/1", kit.KeyV, kit.ModCtrl, "https://x.example/1\nhttps://x.example/2"},
		{"no web link", "magnet:?xt=urn:btih:abc ftp://example.net/c plain words", kit.KeyV, kit.ModCtrl, ""},
		{"empty clipboard", "", kit.KeyV, kit.ModCtrl, ""},
		{"Shift+Insert", "https://example.com/a.zip", kit.KeyInsert, kit.ModShift, "https://example.com/a.zip"},
		{"Ctrl+Shift+V is not a paste", "https://example.com/a.zip", kit.KeyV, kit.ModCtrl | kit.ModShift, ""},
		{"Ctrl+Alt+V is not a paste", "https://example.com/a.zip", kit.KeyV, kit.ModCtrl | kit.ModAlt, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
			f.h.Window.Focus(f.m.Downloads.Grid().FocusTarget())
			f.h.Host.Clipboard = c.clip
			f.h.Key(c.key, c.mods)
			r, ok := f.last().Req.(views.AddRequest)
			switch {
			case c.want == "" && f.count(views.ModalAdd) != 0:
				t.Errorf("Add opened with %q", r.URLs)
			case c.want != "" && (!ok || r.URLs != c.want || r.Mode != views.AddDefault):
				t.Errorf("Add %+v, want %q", r, c.want)
			}
		})
	}
	t.Run("a text field takes its own paste", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
		f.h.App.Act.FocusSearch()
		f.h.Settle()
		f.h.Host.Clipboard = "https://example.com/a.zip"
		f.h.Key(kit.KeyV, kit.ModCtrl)
		if f.count(views.ModalAdd) != 0 {
			t.Error("a paste into the search opened Add")
		}
	})
	t.Run("not while a dialog, a menu or the palette is open", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
		f.h.App.Open.Settings(views.SettingsRequest{})
		f.h.Settle()
		if f.m.Paste("https://example.com/a.zip") || f.count(views.ModalAdd) != 0 {
			t.Error("taken over a dialog")
		}
		f.last().Dlg.Remove()
		f.h.Settle()
		f.sel(1)
		f.menu()
		if f.m.Paste("https://example.com/a.zip") || f.count(views.ModalAdd) != 0 {
			t.Error("taken over the menu")
		}
		f.closeMenus()
		f.h.App.Open.Palette()
		f.h.Settle()
		if f.m.Paste("https://example.com/a.zip") || f.count(views.ModalAdd) != 0 {
			t.Error("taken over the palette")
		}
	})
	t.Run("unavailable while downexd is unreachable", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
		f.d.SetDown(true)
		f.advance(3 * time.Second)
		used := f.m.Paste("https://example.com/a.zip")
		f.h.Settle()
		if !used || f.count(views.ModalAdd) != 0 || !f.hasToast(views.UnavailableText) {
			t.Errorf("opened %d, toasts %q", f.count(views.ModalAdd), f.toasts())
		}
	})
}

// TestDrop is App.svelte's onDragOver and onDrop through the drop contract (app.Drop, which
// ui/win's DropHandler maps its DropEvent onto: URIList, Text, Files, the offered types and
// the field under the pointer).
func TestDrop(t *testing.T) {
	for _, c := range []struct {
		name       string
		d          app.Drop
		over, drop app.DropEffect
		want       string
	}{
		{"a link from Chrome or Edge", app.Drop{URIs: true, Plain: true, URIList: "https://example.com/a.zip", Text: "https://example.com/a.zip"}, app.DropCopy, app.DropCopy, "https://example.com/a.zip"},
		{"a uri-list with comment lines", app.Drop{URIs: true, URIList: "# from a page\nhttps://example.com/a.zip\r\nhttps://example.com/b.zip"}, app.DropCopy, app.DropCopy, "https://example.com/a.zip\nhttps://example.com/b.zip"},
		{"plain text with links", app.Drop{Plain: true, Text: "see https://example.com/a.zip"}, app.DropCopy, app.DropCopy, "https://example.com/a.zip"},
		{"text without a link", app.Drop{Plain: true, Text: "just words"}, app.DropCopy, app.DropNone, ""},
		{"files from Explorer", app.Drop{Files: true}, app.DropNone, app.DropNone, ""},
		{"a local file link", app.Drop{URIs: true, URIList: "file:///C:/Users/me/notes.txt"}, app.DropCopy, app.DropNone, ""},
		{"text into a field", app.Drop{Plain: true, Text: "iso", OnField: true}, app.DropCopy, app.DropField, ""},
		{"files onto a field", app.Drop{Files: true, OnField: true}, app.DropNone, app.DropNone, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
			if got := f.m.DragOver(c.d); got != c.over {
				t.Errorf("drag over: %v, want %v", got, c.over)
			}
			if got := f.m.HandleDrop(c.d); got != c.drop {
				t.Errorf("drop: %v, want %v", got, c.drop)
			}
			f.h.Settle()
			r, _ := f.last().Req.(views.AddRequest)
			if (c.want == "") != (f.count(views.ModalAdd) == 0) || r.URLs != c.want {
				t.Errorf("Add %d times, %q; want %q", f.count(views.ModalAdd), r.URLs, c.want)
			}
		})
	}
	t.Run("with a dialog open only a field takes a drop", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StatePaused, nil))
		f.h.App.Open.Settings(views.SettingsRequest{})
		f.h.Settle()
		link := app.Drop{URIs: true, URIList: "https://example.com/a.zip"}
		if f.m.DragOver(link) != app.DropNone || f.m.HandleDrop(link) != app.DropNone || f.count(views.ModalAdd) != 0 {
			t.Error("a link over a dialog is taken")
		}
		text := app.Drop{Plain: true, Text: "x", OnField: true}
		if f.m.DragOver(text) != app.DropField || f.m.HandleDrop(text) != app.DropField {
			t.Error("a dialog's field does not take text")
		}
	})
}
