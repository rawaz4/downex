//go:build windows

package glue

import (
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// dropFixture is the composed main window (views/app.Main) behind the drop bridge, its Add
// dialog a stand-in that records what it was opened with.
type dropFixture struct {
	h      *viewstest.Harness
	m      *app.Main
	b      dropBridge
	adds   []views.AddRequest
	dialog *views.Dialog
}

func newDropFixture(t *testing.T) *dropFixture {
	t.Helper()
	fl := true
	f := &dropFixture{h: viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon(), Frameless: &fl})}
	m, err := app.New(f.h.App, app.Options{Screens: app.Screens{
		Add: func(a *views.App, r views.AddRequest) views.Modal {
			f.adds = append(f.adds, r)
			d := a.NewDialog(views.ModalAdd, widgets.DialogOptions{Title: "Add downloads", Size: widgets.DialogSm})
			f.dialog = d
			return d
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Dispose)
	f.h.Show(m.Root())
	f.h.Settle()
	f.m, f.b = m, dropBridge{t: m}
	return f
}

// over and drop are a drag at a point of the window: over an editable field or not.
func (f *dropFixture) over(d win.DropData, field bool) win.DropAction {
	return f.b.DragOver(win.DropEvent{At: kit.Pt(600, 400), Data: &d, Field: field})
}

func (f *dropFixture) drop(d win.DropData, field bool) win.DropAction {
	a := f.b.Drop(win.DropEvent{At: kit.Pt(600, 400), Data: &d, Field: field})
	f.h.Settle()
	return a
}

// A drag of today's DataTransfer types: files (Explorer), a link (text/uri-list), text.
var (
	dragFile  = win.DropData{Files: true}
	dragLocal = win.DropData{URIList: "file:///C:/Users/me/notes.txt"}
	dragISO   = win.DropData{Text: "iso"}
	dragLink  = win.DropData{URIList: "https://example.com/a.zip", Text: "https://example.com/a.zip"}
)

// App.test.ts 'never leaves a dropped file or link to the webview, which would replace the
// app': in the native window, "left to the webview" is a drop the window does not handle; a
// field takes only text, never files; links open the Add dialog.
func TestDropNeverLeavesAFileOrLinkToTheWindow(t *testing.T) {
	f := newDropFixture(t)
	t.Run("a dropped file is refused (dropEffect none)", func(t *testing.T) {
		if a := f.over(dragFile, false); a != win.DropRefuse {
			t.Fatalf("drag over: %v", a)
		}
		if a := f.drop(dragFile, false); a != win.DropRefuse {
			t.Fatalf("drop: %v", a)
		}
	})
	t.Run("a dropped file: URL opens nothing", func(t *testing.T) {
		if a := f.drop(dragLocal, false); a != win.DropRefuse || len(f.adds) != 0 {
			t.Fatalf("drop: %v, Add opened %d times", a, len(f.adds))
		}
	})
	t.Run("text dropped into a field is left to it", func(t *testing.T) {
		if a := f.drop(dragISO, true); a != win.DropToField {
			t.Fatalf("drop: %v", a)
		}
	})
	t.Run("a file dropped onto a field is refused", func(t *testing.T) {
		if a := f.drop(dragFile, true); a != win.DropRefuse {
			t.Fatalf("drop: %v", a)
		}
	})
	t.Run("links still open the Add dialog", func(t *testing.T) {
		if a := f.over(dragLink, false); a != win.DropCopy {
			t.Fatalf("drag over: %v", a)
		}
		if a := f.drop(dragLink, false); a != win.DropCopy {
			t.Fatalf("drop: %v", a)
		}
		if len(f.adds) != 1 || f.adds[0].URLs != "https://example.com/a.zip" || !f.h.App.Dialogs.DialogOpen() {
			t.Fatalf("Add opened with %+v (dialog open %v)", f.adds, f.h.App.Dialogs.DialogOpen())
		}
	})
}

// App.svelte's onDragOver and onDrop rules, through the bridge: the links of a uri-list (its
// comment lines left out) or of the text open Add with no dialog open; with a dialog open a
// drag is refused unless a field of the dialog takes the text; a link that is not http(s)
// opens nothing.
func TestDropRulesAreAppSvelte(t *testing.T) {
	f := newDropFixture(t)
	for _, c := range []struct {
		name     string
		data     win.DropData
		field    bool
		over     win.DropAction
		dropped  win.DropAction
		wantURLs string
	}{
		{"a uri-list with a comment line", win.DropData{URIList: "#Example\r\nhttps://example.com/b.iso"}, false, win.DropCopy, win.DropCopy, "https://example.com/b.iso"},
		{"two links as text", win.DropData{Text: "https://a.example/1.zip\nhttp://b.example/2.zip"}, false, win.DropCopy, win.DropCopy, "https://a.example/1.zip\nhttp://b.example/2.zip"},
		{"an ftp link", win.DropData{URIList: "ftp://example.com/x"}, false, win.DropCopy, win.DropRefuse, ""},
		{"plain text over the grid", win.DropData{Text: "hello"}, false, win.DropCopy, win.DropRefuse, ""},
		{"plain text over a field", win.DropData{Text: "hello"}, true, win.DropCopy, win.DropToField, ""},
		{"files with a link (a shortcut)", win.DropData{Files: true, URIList: "https://example.com/c.zip"}, false, win.DropCopy, win.DropCopy, "https://example.com/c.zip"},
		{"nothing readable", win.DropData{}, false, win.DropRefuse, win.DropRefuse, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f.adds = nil
			if a := f.over(c.data, c.field); a != c.over {
				t.Fatalf("drag over: %v, want %v", a, c.over)
			}
			if a := f.drop(c.data, c.field); a != c.dropped {
				t.Fatalf("drop: %v, want %v", a, c.dropped)
			}
			got := ""
			if len(f.adds) == 1 {
				got = f.adds[0].URLs
			}
			if len(f.adds) > 1 || got != c.wantURLs {
				t.Fatalf("Add opened with %+v, want %q", f.adds, c.wantURLs)
			}
			if f.dialog != nil {
				f.dialog.Remove()
				f.dialog = nil
				f.h.Settle()
			}
		})
	}
	t.Run("with a dialog open", func(t *testing.T) {
		f.adds = nil
		if a := f.drop(dragLink, false); a != win.DropCopy || !f.h.App.Dialogs.DialogOpen() {
			t.Fatal("no dialog opened")
		}
		f.adds = nil
		if a := f.over(dragLink, false); a != win.DropRefuse {
			t.Fatalf("drag over the dialog's scrim: %v", a)
		}
		if a := f.drop(dragLink, false); a != win.DropRefuse || len(f.adds) != 0 {
			t.Fatalf("drop: %v, Add %d", a, len(f.adds))
		}
		if a := f.over(dragLink, true); a != win.DropToField {
			t.Fatalf("drag over the dialog's field: %v", a)
		}
		if a := f.drop(dragLink, true); a != win.DropToField {
			t.Fatalf("drop into the dialog's field: %v", a)
		}
	})
	t.Run("a drag without data over a field is the field's (it takes nothing)", func(t *testing.T) {
		if a := f.b.DragOver(win.DropEvent{Field: true}); a != win.DropToField {
			t.Fatalf("drag over: %v", a)
		}
		f.b.DragLeave()
	})
}

type refuseAll struct{ overs, drops int }

func (r *refuseAll) DragOver(app.Drop) app.DropEffect   { r.overs++; return app.DropNone }
func (r *refuseAll) HandleDrop(app.Drop) app.DropEffect { r.drops++; return app.DropNone }

// EnableDrop registers the window with OLE (RegisterDragDrop), a later call replaces the
// target, and a nil one is refused; the glue's Close revokes it.
func TestEnableDropOnTheWindow(t *testing.T) {
	_ = onTestDesktop(t)
	p, _, err := start(t, testLock(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.h.Close()
	tw := openWindow(t, kit.WindowOptions{})
	g, err := p.Attach(tw.w, tw.kw, AttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.EnableDrop(nil); err == nil {
		t.Fatal("a nil target accepted")
	}
	if err := g.EnableDrop(&refuseAll{}); err != nil {
		t.Fatal(err)
	}
	if err := g.EnableDrop(&refuseAll{}); err != nil {
		t.Fatalf("a second target: %v", err)
	}
	tw.close()
	tw.run(t, 10*time.Second)
}
