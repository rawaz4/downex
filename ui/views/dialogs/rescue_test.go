package dialogs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/display"
	"downex/ui/kit"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/viewstest"
)

// sizes stands in for the file system: the size of each path ("" missing: unreadable).
func sizes(m map[string]int64) func(string) (int64, error) {
	return func(p string) (int64, error) {
		n, ok := m[p]
		if !ok {
			return 0, os.ErrNotExist
		}
		return n, nil
	}
}

func rescueHarness(t *testing.T) (*viewstest.Harness, *statetest.Daemon, *viewstest.FakePlatform) {
	t.Helper()
	h, d := harness(t)
	return h, d, h.Platform.(*viewstest.FakePlatform)
}

// TestRescueDialog: docs/DESKTOP-SPEC.md §11, the Rescue partial file dialog.
func TestRescueDialog(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"home", "you", "Downloads")
	chrome := filepath.Join(dir, "disk.iso.crdownload")
	t.Run("texts, the pick, the prefill and a rescue", func(t *testing.T) {
		h, d, fp := rescueHarness(t)
		fp.Picked = chrome
		n := dialogs.NewRescue(h.App, views.RescueRequest{}, dialogs.RescueOptions{Size: sizes(map[string]int64{chrome: 412_000_000})})
		show(h, n)
		if s := n.Card().Semantics.Name; s != "Rescue partial file" {
			t.Errorf("title %q", s)
		}
		if s := n.Card().Semantics.Description; s != dialogs.RescueDescription {
			t.Errorf("description %q", s)
		}
		cancel, submit := n.Buttons()
		if cancel.Label().Text() != "Cancel" || submit.Label().Text() != "Rescue" || !submit.Disabled {
			t.Errorf("buttons %q %q disabled %v", cancel.Label().Text(), submit.Label().Text(), submit.Disabled)
		}
		if o := n.Address().InputOptions(); o.Placeholder != "https://…" || o.Name != "Address" {
			t.Errorf("address %+v", o)
		}
		if o := n.SaveAs().InputOptions(); o.Placeholder != "Named by the server" {
			t.Errorf("save as %+v", o)
		}
		click(h, n.Choose().Base())
		h.Settle()
		if !called(fp.Calls, "PickFile "+fmt.Sprint(dialogs.RescuePickTitle, "/home/you/Downloads")) {
			t.Fatalf("picker calls %v", fp.Calls)
		}
		if n.Path() != chrome || n.SaveAs().Text() != "disk.iso" {
			t.Fatalf("path %q, save as %q", n.Path(), n.SaveAs().Text())
		}
		if s := n.FileHint(); s != "412 MB saved · Chrome partial file (.crdownload)" {
			t.Errorf("hint %q", s)
		}
		if _, submit := n.Buttons(); !submit.Disabled {
			t.Error("Rescue enabled without an address")
		}
		n.Address().OnChange("https://cdn.example/disk.iso")
		if _, submit := n.Buttons(); submit.Disabled {
			t.Fatal("Rescue disabled with a file and an address")
		}
		n.Submit()
		h.Settle()
		if n.Shown() {
			t.Fatal("the dialog stayed open")
		}
		args := d.Args("rescue")
		req := args[0].(*downexv1.RescueRequest)
		if req.GetPartialPath() != chrome || req.GetUrl() != "https://cdn.example/disk.iso" || req.GetFilename() != "disk.iso" ||
			req.GetSource() != downexv1.AddSource_ADD_SOURCE_UI {
			t.Fatalf("request %v", req)
		}
		if got := lastToast(h); got != display.RescuedToast("disk.iso", 40_000_000) {
			t.Errorf("toast %q", got)
		}
		sel := h.Main.Downloads.Selected()
		if len(sel) != 1 || sel[0].GetFilename() != "disk.iso" {
			t.Errorf("selection %v", sel)
		}
		// show_progress_window is on by default and a queued download starts now.
		opened := false
		for _, c := range h.Calls.List {
			opened = opened || c.String() == fmt.Sprint("OpenProgress", []any{sel[0].GetId()})
		}
		if !opened {
			t.Errorf("no progress window: %v", h.Calls.List)
		}
	})
	t.Run("an unconfirmed partial is named by the server; Firefox's kind", func(t *testing.T) {
		h, _, _ := rescueHarness(t)
		un := filepath.Join(dir, "Unconfirmed 4711.crdownload")
		n := dialogs.NewRescue(h.App, views.RescueRequest{Partial: un}, dialogs.RescueOptions{Size: sizes(map[string]int64{un: 5})})
		show(h, n)
		if n.SaveAs().Text() != "" {
			t.Errorf("save as %q", n.SaveAs().Text())
		}
		ff := filepath.Join(dir, "movie.mkv.part")
		n = dialogs.NewRescue(h.App, views.RescueRequest{Partial: ff}, dialogs.RescueOptions{Size: sizes(map[string]int64{ff: 2_000})})
		show(h, n)
		if n.SaveAs().Text() != "movie.mkv" || !strings.HasSuffix(n.FileHint(), " saved · Firefox partial file (.part)") {
			t.Errorf("save as %q, hint %q", n.SaveAs().Text(), n.FileHint())
		}
	})
	t.Run("unreadable and empty files", func(t *testing.T) {
		h, d, _ := rescueHarness(t)
		gone := filepath.Join(dir, "x.part")
		n := dialogs.NewRescue(h.App, views.RescueRequest{Partial: gone}, dialogs.RescueOptions{Size: sizes(nil)})
		show(h, n)
		if n.FileError() != "Downex cannot read this file." {
			t.Errorf("unreadable: %q", n.FileError())
		}
		empty := filepath.Join(dir, "e.part")
		n = dialogs.NewRescue(h.App, views.RescueRequest{Partial: empty}, dialogs.RescueOptions{Size: sizes(map[string]int64{empty: 0})})
		show(h, n)
		n.Address().OnChange("https://cdn.example/e")
		n.Submit()
		h.Settle()
		if n.FileError() != "This file is empty: there is nothing to finish." || d.Count("rescue") != 0 {
			t.Errorf("empty: %q, %d calls", n.FileError(), d.Count("rescue"))
		}
	})
	t.Run("what Enter says without a file or an address", func(t *testing.T) {
		h, _, _ := rescueHarness(t)
		n := dialogs.NewRescue(h.App, views.RescueRequest{}, dialogs.RescueOptions{})
		show(h, n)
		n.Submit()
		if n.FileError() != "Choose the partial file." {
			t.Errorf("no file: %q", n.FileError())
		}
		ok := filepath.Join(dir, "ok.part")
		n = dialogs.NewRescue(h.App, views.RescueRequest{Partial: ok}, dialogs.RescueOptions{Size: sizes(map[string]int64{ok: 9})})
		show(h, n)
		n.Address().OnChange("ftp://x")
		n.Submit()
		if n.AddressError() != "Enter the download's address (http or https)." {
			t.Errorf("no address: %q", n.AddressError())
		}
		n.Address().OnChange("https://x.example/a")
		if n.AddressError() != "" {
			t.Errorf("an edit kept %q", n.AddressError())
		}
	})
	t.Run("refusals under their field, Checking… while waiting", func(t *testing.T) {
		for _, c := range []struct {
			url, file string
			code      string
			onFile    bool
		}{
			{"https://cdn.example/changed", "a.part", "rescue_changed", false},
			{"https://cdn.example/noranges", "a.part", "rescue_no_ranges", false},
			{"https://cdn.example/locked", "a.part", "rescue_locked", true},
			{"https://cdn.example/ok", "gone.part", "rescue_not_found", true},
		} {
			h, d, _ := rescueHarness(t)
			p := filepath.Join(dir, c.file)
			var seen string
			var n *dialogs.Rescue
			// The harness runs the call at once (App.Go is synchronous): the call sees the
			// dialog as it is while downexd checks the file.
			n = dialogs.NewRescue(h.App, views.RescueRequest{Partial: p}, dialogs.RescueOptions{Size: sizes(map[string]int64{p: 9}),
				Call: func(req *downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
					_, submit := n.Buttons()
					seen = submit.Label().Text()
					if !submit.Disabled {
						seen += " (enabled)"
					}
					return d.Rescue(context.Background(), req)
				}})
			show(h, n)
			n.Address().OnChange(c.url)
			n.Submit()
			h.Settle()
			if seen != "Checking…" {
				t.Errorf("%s: busy button %q", c.code, seen)
			}
			want := display.RescueErrorText(c.code, runtime.GOOS)
			got := n.AddressError()
			if c.onFile {
				got = n.FileError()
			}
			if !n.Shown() || got != want {
				t.Errorf("%s: shown %v, error %q (file %q, address %q)", c.code, n.Shown(), got, n.FileError(), n.AddressError())
			}
		}
	})
	t.Run("another error shows downexd's message", func(t *testing.T) {
		h, _, _ := rescueHarness(t)
		p := filepath.Join(dir, "a.part")
		n := dialogs.NewRescue(h.App, views.RescueRequest{Partial: p}, dialogs.RescueOptions{Size: sizes(map[string]int64{p: 9}),
			Call: func(*downexv1.RescueRequest) (*downexv1.RescueResponse, error) {
				return nil, statetest.RenewErr("disk_full", "size part file: no space left")
			}})
		show(h, n)
		n.Address().OnChange("https://cdn.example/a")
		n.Submit()
		h.Settle()
		if n.AddressError() != "size part file: no space left" {
			t.Errorf("error %q", n.AddressError())
		}
	})
}

// TestRescueDialogFits: the dialog never scrolls and stays inside the window at the
// reference sizes and text sizes, light and dark, with a long path and errors shown.
func TestRescueDialogFits(t *testing.T) {
	long := filepath.Join(string(filepath.Separator)+"home", "you", strings.Repeat("a very long folder name ", 8), "Unconfirmed 123456.crdownload")
	for _, sz := range []struct{ w, h, font int }{{1190, 752, 16}, {1190, 752, 20}, {900, 600, 16}, {900, 600, 20}} {
		for _, theme := range []string{"light", "dark"} {
			t.Run(fmt.Sprintf("%dx%d@%d/%s", sz.w, sz.h, sz.font, theme), func(t *testing.T) {
				d := statetest.NewDaemon()
				h := viewstest.New(t, viewstest.Options{Width: sz.w, Height: sz.h, Font: sz.font, Theme: theme, Daemon: d})
				n := dialogs.NewRescue(h.App, views.RescueRequest{Partial: long}, dialogs.RescueOptions{Size: sizes(nil)})
				show(h, n)
				n.Address().OnChange("ftp://x")
				n.Submit()
				h.Settle()
				card := n.Card().WindowBounds()
				if card.Min.Y < h.App.ModalTop()-0.5 || card.Max.Y > kit.DIP(sz.h)+0.5 || card.Min.X < -0.5 || card.Max.X > kit.DIP(sz.w)+0.5 {
					t.Errorf("the dialog %v leaves the window", card)
				}
				if pane, ok := n.BodyPane(); ok && pane.Overflows() {
					t.Errorf("the dialog scrolls")
				}
			})
		}
	}
}

// called reports whether list holds s.
func called(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
