package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/views/app"
	"downex/ui/views/shell"
	"downex/ui/views/viewstest"
)

// chromeProbes are the main window's chrome boxes as the 0.9.2 index has them (layout.nodes,
// selector "tag.class"), with the native element in the same place.
func chromeProbes(m *app.Main) []viewstest.Probe {
	sh := m.Shell
	tb := sh.TitleBar()
	sb := sh.Sidebar()
	hd := sh.Header()
	st := sh.StatusBar()
	var ps []viewstest.Probe
	add := func(name, sel string, nth int, e *kit.Element) {
		if e == nil || e.Window() == nil || e.Style.Hidden {
			return
		}
		ps = append(ps, viewstest.Probe{Name: name, Selector: sel, Nth: nth, Native: e.WindowBounds()})
	}
	add("titlebar", "header.titlebar", 0, tb.Base())
	for name, p := range tb.Parts() {
		add(name, p.Sel, p.Nth, p.El)
	}
	add("sidebar", "nav.sidebar", 0, sb.Base())
	cats, qs, sts := sb.Items()
	n := 0
	for _, list := range [][]*kit.Element{cats, qs, sts} {
		for _, it := range list {
			add(fmt.Sprintf("item %d", n), "li.", n, it)
			n++
		}
	}
	for name, p := range sb.Parts() {
		add(name, p.Sel, p.Nth, p.El)
	}
	add("card", "main.card", 0, sh.Card())
	add("toolbar", "header.toolbar", 0, hd.Base())
	add("heading", "div.heading", 0, hd.Heading())
	add("title", "h1.title", 0, hd.Title().Base())
	add("count", "span.count.tnum", 0, hd.CountText().Base())
	add("chips", "div.chips", 0, hd.Chips())
	add("actions", "div.actions", 0, hd.Actions())
	add("resume", "button.btn.ghost.sel", 0, hd.Resume().Base())
	add("pause", "button.btn.ghost.sel", 1, hd.Pause().Base())
	add("remove", "button.btn.ghost.sel", 2, hd.RemoveButton().Base())
	add("resume all", "button.btn.ghost.bulk", 0, hd.ResumeAll().Base())
	add("pause all", "button.btn.ghost.bulk", 1, hd.PauseAll().Base())
	add("more", "button.icon-btn.more", 0, hd.More().Trigger.Base())
	add("split", "div.split", 0, hd.AddButton().Base())
	add("grid", "div.wrap", 0, sh.GridArea())
	add("statusbar", "footer.statusbar", 0, st.Base())
	parts := st.Parts()
	for _, p := range []struct{ name, sel, part string }{
		{"conn", "span.sb-item.conn.tone-demo", "conn"}, {"speed", "span.sb-item.speed", "speed"},
		{"speed value", "span.speed-value.tnum", "speedValue"}, {"counts", "span.sb-item.counts", "counts"},
		{"end", "span.end", "end"}, {"limit", "button.sb-item.sb-menu.limit", "limit"},
		{"whendone", "button.sb-item.sb-menu.whendone", "whendone"}, {"free", "span.sb-item.free", "free"},
		{"drive", "span.drive", "drive"}, {"free bytes", "b.tnum", "freeBytes"}, {"total", "span.sb-item.total", "total"},
	} {
		add(p.name, p.sel, 0, parts[p.part])
	}
	return ps
}

// shotTheme gives the harness the reference's theme setting: the references were taken with
// the theme set to follow the system, the system light or dark.
func shotTheme(h *viewstest.Harness, theme string) {
	h.App.Theme.Set(host.ThemeSystem)
	dark := theme == "dark"
	h.App.WidgetTheme.SystemDark = &dark
	h.Settle()
}

// TestMainWindowMatchesReferences renders the main window with the Studio scene in every
// state the 0.9.2 references have (1190×752, 1200×760, 960×608, 900×600 and 1920×1080, at 1x
// and 1.25x, 16 and 20 px text, light and dark) and compares the chrome's boxes with the
// index's; with DOWNEX_VIEWS_WRITE=1 it writes reference | native | difference sheets.
func TestMainWindowMatchesReferences(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	var shots []*viewstest.Shot
	for _, s := range refs.Shots {
		if s.Kind == "main" {
			shots = append(shots, s)
		}
	}
	sort.Slice(shots, func(i, j int) bool { return shots[i].File < shots[j].File })
	worst := map[string]float64{}
	for _, s := range shots {
		name := fmt.Sprintf("%dx%d-%vx-%dpx-%s", s.CSS.W, s.CSS.H, s.Scale, s.Font, s.Theme)
		t.Run(name, func(t *testing.T) {
			o := viewstest.Options{Width: s.CSS.W, Height: s.CSS.H, Scale: float32(s.Scale), Font: s.Font, Theme: s.Theme, Real: true}
			h, m := mainWindow(t, o)
			if s.Font != 16 {
				// The references set the root text size after the page loaded: the table had
				// measured 16 px per rem when it mounted and kept it (as the downloads lane's
				// TestRenderMainGrid does).
				m.Downloads.Grid().Table().FixRem(16)
			}
			shotTheme(h, s.Theme)
			img, err := h.Render()
			if err != nil {
				t.Fatal(err)
			}
			ds := viewstest.BoxDeltas(s, chromeProbes(m))
			var bad, missing []string
			found := 0
			for _, d := range ds {
				if !d.Found {
					missing = append(missing, d.Name)
					continue
				}
				found++
				if float64(d.Max()) > worst[d.Name] {
					worst[d.Name] = float64(d.Max())
				}
				if d.Max() > 1 {
					bad = append(bad, d.String())
				}
			}
			for _, b := range bad {
				t.Error(b)
			}
			t.Logf("%d chrome boxes compared; without a reference box: %v", found, missing)
			if write {
				ref, err := refs.Image(s)
				if err != nil {
					t.Fatal(err)
				}
				c := viewstest.Compare(ref, img, kit.R(0, 0, kit.DIP(s.CSS.W), kit.DIP(s.CSS.H)), kit.Point{}, float32(s.Scale))
				t.Logf("whole window: mean %.2f, %.1f%% differ by more than %d", c.Mean, 100*c.Differing, c.Threshold)
				state := "main"
				if s.Font != 16 {
					state = fmt.Sprintf("main-%dpx", s.Font)
				}
				if s.CSS.H != 760 && s.CSS.W == 1200 || s.CSS.W != 1200 {
					state += fmt.Sprintf("-%dx%d", s.CSS.W, s.CSS.H)
				}
				file := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("shell", state, s.Theme, s.CSS.W, s.Scale))
				if err := viewstest.WritePNG(file, c.Sheet); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	var names []string
	for n := range worst {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s %.2f; ", n, worst[n])
	}
	t.Log("largest box delta per probe (DIP): " + b.String())
}

var _ = shell.SidebarWidth
