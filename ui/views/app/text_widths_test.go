package app_test

import (
	"fmt"
	"sort"
	"testing"

	"downex/ui/views/viewstest"
)

// TestTextWidths: every text and control box of the chrome is within 1% of the reference's width
// in every main shot, or within 0.25 DIP where the reference box is a few DIPs wide (the page
// title cut by its ellipsis at 1190 px with 20 px text). It logs the largest share per probe.
func TestTextWidths(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	worst := map[string]string{}
	share := map[string]float64{}
	for _, s := range refs.Shots {
		if s.Kind != "main" {
			continue
		}
		o := viewstest.Options{Width: s.CSS.W, Height: s.CSS.H, Scale: float32(s.Scale), Font: s.Font, Theme: s.Theme, Real: true}
		h, m := mainWindow(t, o)
		if s.Font != 16 {
			m.Downloads.Grid().Table().FixRem(16)
		}
		shotTheme(h, s.Theme)
		for _, d := range viewstest.BoxDeltas(s, chromeProbes(m)) {
			if !d.Found || d.Ref.Width() == 0 {
				continue
			}
			dw := float64(d.DW)
			if dw < 0 {
				dw = -dw
			}
			r := dw / float64(d.Ref.Width())
			if r > 0.01 && dw > 0.25 {
				t.Errorf("%s at %dx%d %vx %dpx %s: width %.2f off of %.1f", d.Name, s.CSS.W, s.CSS.H, s.Scale, s.Font, s.Theme, d.DW, d.Ref.Width())
			}
			if r > share[d.Name] {
				share[d.Name] = r
				worst[d.Name] = fmt.Sprintf("%dx%d %vx %dpx %s: dw %.2f of %.1f", s.CSS.W, s.CSS.H, s.Scale, s.Font, s.Theme, d.DW, d.Ref.Width())
			}
		}
	}
	var names []string
	for n := range share {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return share[names[i]] > share[names[j]] })
	for _, n := range names[:min(14, len(names))] {
		t.Logf("%-14s %.2f%%  %s", n, 100*share[n], worst[n])
	}
}
