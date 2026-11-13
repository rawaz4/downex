package app_test

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"downex/ui/core/selection"
	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

// composedStates are the whole-window states compared by TestComposedWindow: the main window
// and one state of each dialog, opened through the composition as the references' steps open
// them (the openers, the window's shortcuts, the confirm queue), so that the sheets show what
// the window shows around a dialog: the title bar, the selection's details pane, the notices.
var composedStates = []struct {
	name    string
	fixture string
	open    func(t testing.TB, h *viewstest.Harness, m *app.Main)
}{
	{name: "main"},
	{name: "remove", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		selectRows(t, h, "podcast-episode-41.mp3")
		h.Key(kit.KeyDelete, 0)
	}},
	{name: "restart", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		ids := selectRows(t, h, "photo-archive-277.7z")
		h.App.Open.Restart(views.RestartRequest{IDs: ids, Reason: views.RestartManual})
	}},
	// The steps search for "editor-5" (the list is virtual), then double-click its row.
	{name: "open-program", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		m.Shell.TitleBar().SetSearchText("editor-5")
		h.Main.Downloads.SetSearch("editor-5")
		h.Settle()
		for _, id := range h.Main.Downloads.VisibleIDs() {
			if h.Main.Downloads.NameOf(id) == "editor-5.AppImage" {
				h.App.Act.OpenDownload(id)
				return
			}
		}
		t.Fatal("editor-5.AppImage is not listed")
	}},
	{name: "move-queue", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		ids := selectRows(t, h, "setup-x64-312.exe")
		h.App.Open.MoveToQueue(views.MoveRequest{IDs: ids})
	}},
	{name: "queues-main", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		h.App.Open.Queues(views.QueuesRequest{})
	}},
	{name: "settings-general", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		h.Key(kit.KeyComma, kit.ModCtrl)
	}},
	{name: "palette", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		h.Key(kit.KeyK, kit.ModCtrl)
	}},
	{name: "add-files", open: func(t testing.TB, h *viewstest.Harness, m *app.Main) {
		h.Key(kit.KeyN, kit.ModCtrl)
	}},
	// The Confirm dialog of a file opens by itself (the confirm queue).
	{name: "confirm-file", fixture: "confirm-file"},
}

// ownDesign are the states whose dialog has its own design (Settings: Windows 11's on
// Windows, DESIGN-SYSTEM §3.20), so its box is not compared with the WebView reference's;
// ui/views/settings checks it (TestStatesFitTheWindow, TestSkinRules).
var ownDesign = map[string]bool{"settings-general": true}

// selectRows selects the downloads named as the steps' clicks do (the first a plain click,
// the others Ctrl+clicks) and returns their ids in the list's order.
func selectRows(t testing.TB, h *viewstest.Harness, names ...string) []int64 {
	t.Helper()
	s := h.Main.Downloads
	var ids []int64
	for _, id := range s.VisibleIDs() {
		if slices.Contains(names, s.NameOf(id)) {
			ids = append(ids, id)
		}
	}
	if len(ids) != len(names) {
		t.Fatalf("found %d of the downloads %v", len(ids), names)
	}
	for i, id := range ids {
		s.Click(id, selection.Modifiers{Toggle: i > 0})
	}
	h.Settle()
	return ids
}

// modalCard is the box of the dialog or palette shown.
func modalCard(a *views.App) (kit.Rect, bool) {
	var m views.Modal
	if a.Dialogs.DialogOpen() {
		m = a.Dialogs.Dialog()
	} else if a.Dialogs.PaletteOpen() {
		m = a.Dialogs.Palette()
	}
	for {
		u, ok := m.(interface{ Unwrap() views.Modal })
		if !ok {
			break
		}
		m = u.Unwrap()
	}
	if c, ok := m.(interface{ Card() *kit.Element }); ok && c.Card() != nil {
		return c.Card().WindowBounds(), true
	}
	return kit.Rect{}, false
}

// composedSizes are the widths the composition is compared at (the 0.9.2 references exist at
// 1200 × 760 and 1920 × 1080 among them; 1100, 1366 and 1470 are rendered without one).
var composedSizes = []struct{ w, h int }{{1100, 760}, {1200, 760}, {1366, 768}, {1470, 900}, {1920, 1080}}

// TestComposedWindow renders the composed window (views/app over the Studio scene) in each
// composed state at 1100, 1200, 1366, 1470 and 1920 px, 1x and 1.25x, light and dark, and
// compares it with the 0.9.2 reference where one exists: the whole-window difference, the
// chrome's boxes (main) and the dialog's box (dialogs). With DOWNEX_VIEWS_WRITE=1 it writes
// native-s2-compose-<state>-<theme>-<width>-<scale>.png: reference | native | difference, or
// the native window alone where 0.9.2 has no reference.
func TestComposedWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("renders 200 windows")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	write := os.Getenv("DOWNEX_VIEWS_WRITE") != ""
	only := os.Getenv("DOWNEX_COMPOSE_ONLY")
	var summary []string
	for _, st := range composedStates {
		for _, sz := range composedSizes {
			for _, scale := range []float32{1, 1.25} {
				for _, theme := range []string{"light", "dark"} {
					name := fmt.Sprintf("%s-%dx%d-%vx-%s", st.name, sz.w, sz.h, scale, theme)
					if only != "" && !strings.Contains(name, only) {
						continue
					}
					t.Run(name, func(t *testing.T) {
						o := viewstest.Options{Width: sz.w, Height: sz.h, Scale: scale, Font: 16, Theme: theme, Real: true, Fixture: st.fixture}
						line := renderComposed(t, refs, o, st.name, st.open, write)
						summary = append(summary, name+": "+line)
					})
				}
			}
		}
	}
	sort.Strings(summary)
	for _, l := range summary {
		t.Log(l)
	}
}

func renderComposed(t *testing.T, refs *viewstest.Refs, o viewstest.Options, state string,
	open func(testing.TB, *viewstest.Harness, *app.Main), write bool,
) string {
	var (
		h *viewstest.Harness
		m *app.Main
	)
	if state == "confirm-file" {
		h = viewstest.New(t, o)
		var err error
		if m, err = app.New(h.App, app.Options{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Dispose)
		h.Show(m.Root())
		h.Settle()
		h.Advance(1500 * time.Millisecond) // the confirm queue opens the waiting download's dialog
		if !h.App.Dialogs.DialogOpen() {
			t.Fatal("the Confirm dialog did not open by itself")
		}
		h.Window.Blur()
		h.Settle()
	} else {
		h, m = mainWindow(t, o)
		if open != nil {
			h.Window.SetFocusVisible(true) // the script opened every dialog from the keyboard or a click
			open(t, h, m)
			h.Settle()
			if !h.App.Dialogs.Modal() {
				t.Fatalf("%s: no dialog opened", state)
			}
		}
	}
	shotTheme(h, o.Theme)
	img, err := h.Render()
	if err != nil {
		t.Fatal(err)
	}
	kind := "state"
	if state == "main" {
		kind = "main"
	}
	shot, ok := refs.Find(kind, state, o.Width, o.Height, float64(o.Scale), o.Font, o.Theme)
	file := filepath.Join(viewstest.ShotDir(), viewstest.NativeName("compose", state, o.Theme, o.Width, float64(o.Scale)))
	if !ok {
		if write {
			if err := viewstest.WritePNG(file, img); err != nil {
				t.Fatal(err)
			}
		}
		return "no 0.9.2 reference at this size (native only)"
	}
	ref, err := refs.Image(shot)
	if err != nil {
		t.Fatal(err)
	}
	c := viewstest.Compare(ref, img, kit.R(0, 0, kit.DIP(o.Width), kit.DIP(o.Height)), kit.Point{}, o.Scale)
	line := fmt.Sprintf("whole window mean %.2f, %.1f%% differ by more than %d", c.Mean, 100*c.Differing, c.Threshold)
	if kind == "main" {
		worst, found := 0.0, 0
		for _, d := range viewstest.BoxDeltas(shot, chromeProbes(m)) {
			if d.Found {
				found++
				worst = math.Max(worst, float64(d.Max()))
			}
		}
		line += fmt.Sprintf("; %d chrome boxes, largest delta %.2f DIP", found, worst)
		if worst > 1 {
			t.Errorf("chrome box delta %.2f DIP", worst)
		}
	} else if ownDesign[state] {
		// a dialog with its own design since the WebView window was retired: it opened over
		// the window (the window behind is the reference's); its box is not the reference's
		if _, ok := modalCard(h.App); !ok {
			t.Fatal("no dialog card")
		}
	} else if want, ok := shot.DialogBox(); ok {
		got, ok := modalCard(h.App)
		if !ok {
			t.Fatal("no dialog card")
		}
		d := [4]float64{float64(got.Min.X - want.Min.X), float64(got.Min.Y - want.Min.Y), float64(got.Width() - want.Width()), float64(got.Height() - want.Height())}
		worst := 0.0
		for _, v := range d {
			worst = math.Max(worst, math.Abs(v))
		}
		line += fmt.Sprintf("; dialog box dx %.2f dy %.2f dw %.2f dh %.2f DIP", d[0], d[1], d[2], d[3])
		if worst > 1 {
			t.Errorf("dialog box off by %.2f DIP: native %v, reference %v", worst, got, want)
		}
		// The backdrop beside the dialog: the window under the scrim, blurred as
		// Dialog.svelte's backdrop-filter: blur(1.5px) blurs it.
		if bd := kit.R(0, 64, want.Min.X-24, kit.DIP(o.Height)-40); bd.Width() > 32 {
			b := viewstest.Compare(ref, img, bd, bd.Min, o.Scale)
			line += fmt.Sprintf("; backdrop mean %.2f, %.1f%% differ", b.Mean, 100*b.Differing)
		}
	}
	if write {
		if err := viewstest.WritePNG(file, c.Sheet); err != nil {
			t.Fatal(err)
		}
	}
	return line
}

// TestPaletteCoversTheSearchField: the command palette (z 70) covers the title bar, and the
// title bar's search is a native child window that would draw over it (the real window's
// palette shot showed the search box through the palette's field), so it hides while the
// palette is open and comes back when it closes. A dialog (z 40) stays under the title bar.
func TestPaletteCoversTheSearchField(t *testing.T) {
	h, m := mainWindow(t, viewstest.Options{})
	search := m.Shell.TitleBar().Search()
	if search.Covered() {
		t.Fatal("covered before the palette opened")
	}
	h.Key(kit.KeyK, kit.ModCtrl)
	h.Settle()
	if !h.App.Dialogs.PaletteOpen() || !search.Covered() {
		t.Fatalf("palette open %v, search covered %v", h.App.Dialogs.PaletteOpen(), search.Covered())
	}
	h.Key(kit.KeyEscape, 0)
	h.Settle()
	if h.App.Dialogs.PaletteOpen() || search.Covered() {
		t.Fatalf("after Escape: palette open %v, search covered %v", h.App.Dialogs.PaletteOpen(), search.Covered())
	}
	h.Key(kit.KeyComma, kit.ModCtrl) // Settings: a dialog under the title bar
	h.Settle()
	if !h.App.Dialogs.DialogOpen() || search.Covered() {
		t.Fatalf("Settings open %v, search covered %v", h.App.Dialogs.DialogOpen(), search.Covered())
	}
}
