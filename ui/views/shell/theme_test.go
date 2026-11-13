package shell_test

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/query"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state/statetest"
	"downex/ui/views/shell"
	"downex/ui/views/viewstest"
)

// labelOf is the first label with text inside e (a nav item's name, a menu trigger's text).
func labelOf(e *kit.Element) *kit.Label {
	for _, k := range e.Children() {
		if l, ok := k.Widget().(*kit.Label); ok && l.Text() != "" {
			return l
		}
		if l := labelOf(k); l != nil {
			return l
		}
	}
	return nil
}

// A theme change shows the sidebar's labels and the status bar's menus in the new theme's
// colours at once, in both directions, whether or not anyone calls ThemeChanged: the items
// notice the new theme when they paint (a choice from the title bar, the system's light or
// dark apps, a contrast theme). Before, the labels kept the previous theme's ink until a
// relaunch: after light→dark the categories stayed light-theme grey, after dark→light "All
// downloads" was drawn in near-white on white.
func TestThemeChangeRecolours(t *testing.T) {
	for _, call := range []bool{false, true} {
		name := "noticed when painting"
		if call {
			name = "ThemeChanged"
		}
		t.Run(name, func(t *testing.T) {
			s := shell.NewSidebar(shell.SidebarOptions{
				OnCategory: func(categories.Filter) {}, OnStatus: func(categories.Status) {},
				OnQueue: func(int64) {}, OnManageQueues: func() {}, OnIntegration: func() {},
			})
			bar := shell.NewStatusBar(shell.StatusOptions{})
			page := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Width: kit.Pct(100), Height: kit.Pct(100)}}
			side := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Flex: kit.Flex1, MinHeight: kit.Px(0), Width: kit.Px(224)}}
			s.Style.Width = kit.Pct(100) // the shell's grid gives the sidebar its column
			side.Add(s)
			page.Add(side, bar)
			if err := viewstest.CanRender(); err != nil {
				t.Skipf("no offscreen renderer: %v", err)
			}
			h := viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon(), Real: true})
			h.Show(page)
			ix := categories.NewIndex([]*downexv1.Category{{Name: "Video", Extensions: []string{"mp4"}}})
			s.Update(shell.SidebarState{
				Index: ix, Category: categories.All, Status: categories.AnyStatus,
				Counts:      query.Counts{Categories: map[categories.Filter]int{categories.All: 3}, Statuses: map[categories.Status]int{categories.Queued: 2}},
				Queues:      []*downexv1.Queue{statetest.Queue(1, nil)},
				QueueCounts: map[int64]int{1: 2}, Browser: shell.BrowserStart,
			})
			bar.Update(barState(nil))
			h.Settle()

			check := func(when string, th *tokens.Theme) {
				t.Helper()
				// a frame of the window: the colours a paint shows
				if _, err := h.Render(); err != nil {
					t.Fatalf("render: %v", err)
				}
				cats, qs, sts := s.Items()
				for i, group := range [][]*kit.Element{cats, qs, sts} {
					for j, e := range group {
						l := labelOf(e)
						if l == nil {
							if i == 1 { // a queue item's name may be its glyph's
								continue
							}
							t.Fatalf("%s: item %d/%d has no label", when, i, j)
						}
						want := th.Ink2
						if i == 0 && j == 0 { // "All downloads" is the active filter: --ink
							want = th.Ink
						}
						if got := l.Color(); got != want {
							t.Errorf("%s: %q drawn in %v, want %v", when, l.Text(), got, want)
						}
					}
				}
				for _, part := range []string{"limit", "whendone"} {
					e := bar.Parts()[part]
					if e == nil || e.Style.Hidden {
						continue
					}
					l := labelOf(e)
					if l == nil {
						t.Fatalf("%s: %s has no label", when, part)
					}
					if got := l.Color(); got != th.Muted {
						t.Errorf("%s: status bar %s %q drawn in %v, want --muted %v", when, part, l.Text(), got, th.Muted)
					}
				}
			}
			check("light", &tokens.Light)
			for _, step := range []struct {
				choice host.ThemeChoice
				th     *tokens.Theme
			}{{host.ThemeDark, &tokens.Dark}, {host.ThemeLight, &tokens.Light}, {host.ThemeDark, &tokens.Dark}} {
				h.App.Theme.Set(step.choice)
				h.Settle()
				if call {
					s.ThemeChanged()
					bar.ThemeChanged()
					h.Settle()
				}
				check("after "+string(step.choice), step.th)
			}
		})
	}
}
