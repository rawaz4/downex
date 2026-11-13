//go:build darwin && cgo

package mac

// [popups]

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"downex/ui/kit"
	"downex/ui/mac/popuptest"
)

func sp(s string) *string { return &s }

func TestDropDataAsWebKitSawIt(t *testing.T) {
	dir := t.TempDir()
	webloc := filepath.Join(dir, "Example.webloc")
	if err := os.WriteFile(webloc, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>URL</key><string>https://example.com/file.zip</string></dict></plist>`), 0o600); err != nil {
		t.Fatal(err)
	}
	shortcut := filepath.Join(dir, "Example.url")
	if err := os.WriteFile(shortcut, []byte("[InternetShortcut]\r\nURL=https://example.org/a.iso\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name             string
		url, text, html  *string
		files            string
		wantURI, wantTxt string
		types            []string
	}{
		{name: "a link from a browser", url: sp("https://example.com/a.zip"), text: sp("https://example.com/a.zip"), html: sp(`<a href="https://example.com/a.zip">a</a>`),
			wantURI: "https://example.com/a.zip", wantTxt: "https://example.com/a.zip", types: []string{TypePlain, TypeURIList, TypeHTML}},
		{name: "a link without text", url: sp("https://example.com/b.zip"),
			wantURI: "https://example.com/b.zip", wantTxt: "https://example.com/b.zip", types: []string{TypePlain, TypeURIList}},
		{name: "plain text", text: sp("see https://x.example/1"), wantTxt: "see https://x.example/1", types: []string{TypePlain}},
		{name: "not a URL", url: sp("::nope"), text: sp("t"), wantTxt: "t", types: []string{TypePlain}},
		{name: "a file", files: plain, types: []string{TypeFiles}},
		{name: "an Internet location", files: webloc, wantURI: "https://example.com/file.zip", wantTxt: "https://example.com/file.zip",
			types: []string{TypePlain, TypeURIList, TypeFiles}},
		{name: "an Internet shortcut", files: shortcut, wantURI: "https://example.org/a.iso", wantTxt: "https://example.org/a.iso",
			types: []string{TypePlain, TypeURIList, TypeFiles}},
	}
	w := openTestWindow(t, &recRoot{ground: kit.RGBA(240, 240, 240, 1)}, kit.Size{W: 300, H: 200})
	h := &recDrop{answer: DropCopy}
	mainDo(func() {
		if err := w.EnableDrop(h); err != nil {
			t.Fatal(err)
		}
	})
	for _, c := range cases {
		var d DropData
		mainDo(func() {
			// What the drag's (private) pasteboard holds, as draggingEntered: reads it.
			popuptest.Drag(w.ns, 1, 50, 50, c.url, c.text, c.html, c.files, true)
			d = h.data
			popuptest.Drag(w.ns, 3, 0, 0, nil, nil, nil, "", true)
		})
		if d.URIList != c.wantURI || d.Text != c.wantTxt || !slices.Equal(d.Types(), c.types) {
			t.Errorf("%s: uri %q text %q types %v; want %q %q %v", c.name, d.URIList, d.Text, d.Types(), c.wantURI, c.wantTxt, c.types)
		}
	}
}

type recDrop struct {
	over, drops, leaves int
	last                DropEvent
	data                DropData
	answer              DropAction
}

func (r *recDrop) DragOver(e DropEvent) DropAction {
	r.over++
	r.last, r.data = e, *e.Data
	return r.answer
}
func (r *recDrop) Drop(e DropEvent) DropAction {
	r.drops++
	r.last, r.data = e, *e.Data
	return r.answer
}
func (r *recDrop) DragLeave() { r.leaves++ }

func TestDropTargetCalls(t *testing.T) {
	owner := &recRoot{ground: kit.RGBA(240, 240, 240, 1)}
	w := openTestWindow(t, owner, kit.Size{W: 400, H: 300})
	h := &recDrop{answer: DropCopy}
	link := sp("https://example.com/x.iso")
	mainDo(func() {
		if err := w.EnableDrop(h); err != nil {
			t.Fatal(err)
		}
		if !w.dragCall(1, kit.Pt(100, 80), link, nil, "", true) {
			t.Error("entered: refused")
		}
		if h.over != 1 || h.last.At != kit.Pt(100, 80) || h.data.URIList != *link || h.last.Field {
			t.Errorf("DragOver %d at %v data %+v", h.over, h.last.At, h.data)
		}
		w.dragCall(2, kit.Pt(110, 90), link, nil, "", true)
		if h.over != 2 || h.last.At != kit.Pt(110, 90) || h.data.URIList != *link {
			t.Errorf("updated: %d at %v uri %q", h.over, h.last.At, h.data.URIList)
		}
		// A source that does not allow a copy: refused.
		if w.dragCall(2, kit.Pt(110, 90), link, nil, "", false) {
			t.Error("a drag without copy was taken")
		}
		if !w.dragCall(4, kit.Pt(120, 95), link, nil, "", true) || h.drops != 1 || h.data.URIList != *link {
			t.Errorf("drop: %d %+v", h.drops, h.data)
		}
		// DragLeave once per entered drag only.
		w.dragCall(3, kit.Pt(0, 0), nil, nil, "", true)
		if h.leaves != 0 {
			t.Errorf("a leave after the drop: %d", h.leaves)
		}
		w.dragCall(1, kit.Pt(10, 10), link, nil, "", true)
		w.dragCall(3, kit.Pt(0, 0), nil, nil, "", true)
		w.dragCall(3, kit.Pt(0, 0), nil, nil, "", true)
		if h.leaves != 1 {
			t.Errorf("leaves %d, want 1", h.leaves)
		}
		// Refused while the window is inside a call into its root (a modal loop).
		w.call(func() {
			if w.dragCall(1, kit.Pt(10, 10), link, nil, "", true) {
				t.Error("a drag inside a root call was taken")
			}
		})
		w.dragCall(3, kit.Pt(0, 0), nil, nil, "", true)
		// DropToField without a field under the point is refused.
		h.answer = DropToField
		if w.dragCall(1, kit.Pt(10, 10), nil, sp("text"), "", true) {
			t.Error("DropToField without a field was taken")
		}
		w.dragCall(3, kit.Pt(0, 0), nil, nil, "", true)
		// Over an editable field: Field, and DropToField puts the text into it.
		f := w.addTestField(kit.R(200, 200, 380, 224), "ab")
		if !w.dragCall(1, kit.Pt(370, 212), nil, sp("XY\nZ"), "", true) || !h.last.Field {
			t.Errorf("over a field: field %v", h.last.Field)
		}
		if !w.dragCall(4, kit.Pt(370, 212), nil, sp("XY\nZ"), "", true) {
			t.Error("the field did not take the drop")
		}
		if got := f.Text(); got != "abXYZ" {
			t.Errorf("field text %q, want abXYZ (inserted at the end, one line)", got)
		}
		f.Remove()
		w.DisableDrop()
		if len(dropTargets) != 0 {
			t.Error("DisableDrop left the target")
		}
	})
}
