package app_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

// TestHeaderWidthRules: Toolbar.svelte's media queries in the composed window, at the widths the
// plan names (1100, 1200, 1366, 1920) and either side of each breakpoint: the selection's
// Resume, Pause and Remove are icon-only (30 px) below 1180 px, the bulk pair below 1366 px, the
// chips leave below 960 px; the header never wraps (one row, the references' 58 px).
func TestHeaderWidthRules(t *testing.T) {
	for _, c := range []struct {
		w            int
		sel, bulk    bool // labelled
		chips        bool
		sidebarWidth kit.DIP
	}{
		{900, false, false, false, 56}, {959, false, false, false, 56}, {960, false, false, true, 56},
		{1100, false, false, true, 224}, {1179, false, false, true, 224}, {1180, true, false, true, 224},
		{1200, true, false, true, 224}, {1365, true, false, true, 224}, {1366, true, true, true, 224},
		{1920, true, true, true, 224},
	} {
		for _, font := range []int{16, 20} {
			h := viewstest.New(t, viewstest.Options{Width: c.w, Height: 760, Font: font})
			m, err := app.New(h.App, app.Options{})
			if err != nil {
				t.Fatal(err)
			}
			h.Show(m.Root())
			h.Settle()
			for h.App.Dialogs.DialogOpen() {
				h.Key(kit.KeyEscape, 0)
				h.Settle()
			}
			hd := m.Shell.Header()
			labelled := func(e *kit.Element) bool { return e.WindowBounds().Width() > 30.5 }
			for _, b := range []*kit.Element{hd.Resume().Base(), hd.Pause().Base(), hd.RemoveButton().Base()} {
				if labelled(b) != c.sel {
					t.Errorf("%d px, %d px text: %q labelled %v (%v)", c.w, font, b.Semantics.Name, labelled(b), b.WindowBounds())
				}
			}
			for _, b := range []*kit.Element{hd.ResumeAll().Base(), hd.PauseAll().Base()} {
				if labelled(b) != c.bulk {
					t.Errorf("%d px, %d px text: %q labelled %v (%v)", c.w, font, b.Semantics.Name, labelled(b), b.WindowBounds())
				}
			}
			if shown := !hd.Chips().WindowBounds().Empty() && !hd.Chips().Style.Hidden; shown != c.chips {
				t.Errorf("%d px: chips shown %v", c.w, shown)
			}
			if hh := hd.Base().WindowBounds().Height(); hh != 58 {
				t.Errorf("%d px, %d px text: the header is %v tall (wraps?)", c.w, font, hh)
			}
			want := c.sidebarWidth
			if font == 20 && want == 224 {
				want = 280 // 14rem
			}
			if sw := m.Shell.Sidebar().Base().WindowBounds().Width(); sw != want {
				t.Errorf("%d px, %d px text: sidebar %v wide, want %v", c.w, font, sw, want)
			}
			m.Dispose()
			h.Close()
		}
	}
}
