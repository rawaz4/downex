package views_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format/formattest"
	"downex/ui/core/selection"
	"downex/ui/kit"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
)

const MiB = formattest.MiB

func dl(id int64, st downexv1.DownloadState, edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.Download(id, func(d *downexv1.Download) {
		d.State = st
		if edit != nil {
			edit(d)
		}
	})
}

// fake is a harness over statetest's fakeApi port with the downloads given.
func fake(t *testing.T, list ...*downexv1.Download) (*viewstest.Harness, *statetest.Daemon) {
	t.Helper()
	d := statetest.NewDaemon(list...)
	return viewstest.New(t, viewstest.Options{Daemon: d}), d
}

// mainContent is a stand-in for the shell's content: a column with a button that holds the
// focus before a dialog opens.
func mainContent(h *viewstest.Harness) (*kit.Element, *widgets.Button) {
	b := widgets.NewButton("Remove", "remove", widgets.Secondary, widgets.SizeMd, nil)
	e := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Padding: kit.EdgesAll(kit.Px(20))}}
	e.Add(b)
	h.Show(e)
	return e, b
}

func TestNewAppNeedsAWindow(t *testing.T) {
	if _, err := views.NewApp(views.Options{}); err != views.ErrNoWindow {
		t.Fatalf("NewApp without a window: %v, want ErrNoWindow", err)
	}
}

func TestLayersStackByZ(t *testing.T) {
	h, _ := fake(t)
	ls := h.App.Layers
	a, b, c := &kit.Element{}, &kit.Element{}, &kit.Element{}
	ls.Set(a, views.ZPalette)
	ls.Set(b, views.ZToastsBehind)
	ls.Set(c, views.ZDialog)
	h.Settle()
	order := ls.Order()
	plain, notices := h.App.ToastLayer.Plain(), h.App.ToastLayer.Notices()
	want := []kit.Widget{b, c, plain, notices, a}
	if !slices.Equal(order, want) {
		t.Fatalf("Order() = %v, want behind, dialog, toasts, notices, palette", order)
	}
	got := h.Window.Layers()
	if !slices.Equal(got[len(got)-len(want):], want) {
		t.Errorf("window layers %v do not end with the managed order", got)
	}
	// Moving one keeps the order of equal Zs as first set.
	ls.Set(plain, views.ZToastsBehind)
	if o := ls.Order(); o[0] != plain || o[1] != b {
		t.Errorf("after moving the toasts behind: %v", o)
	}
	ls.Remove(a)
	if slices.Contains(h.Window.Layers(), kit.Widget(a)) {
		t.Error("Remove left the layer in the window")
	}
}

func TestDialogHostOneAtATime(t *testing.T) {
	h, _ := fake(t, dl(1, state.StatePaused, nil))
	app := h.App
	content, opener := mainContent(h)
	app.Dialogs.SetMain(content)
	app.Window.Focus(opener)
	var closed []string
	mk := func(title string) *views.Dialog {
		d := app.NewDialog(views.ModalRemove, widgets.DialogOptions{Title: title, Size: widgets.DialogSm, OnClose: func() { closed = append(closed, title) }})
		d.SetFooter(nil, widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, nil))
		return d
	}
	first := mk("First")
	app.Dialogs.Show(first)
	h.Settle()
	if !app.Dialogs.DialogOpen() || app.Dialogs.Kind() != views.ModalRemove || !app.Dialogs.Modal() {
		t.Fatal("the dialog is not shown")
	}
	if !content.Inert {
		t.Error("the main content is not inert while a dialog is open")
	}
	// App.shell.test.ts "puts the toasts under an open dialog, so they never cover its
	// buttons": the plain toasts go behind, the notices stay above and Tab reaches them.
	plain, notices := app.ToastLayer.Plain(), app.ToastLayer.Notices()
	if z, _ := app.Layers.Z(plain); z != views.ZToastsBehind {
		t.Errorf("plain toasts at %d while a dialog is open, want %d", z, views.ZToastsBehind)
	}
	order := h.Window.Layers()
	if slices.Index(order, plain) > slices.Index(order, kit.Widget(first.Dialog)) || slices.Index(order, notices) < slices.Index(order, kit.Widget(first.Dialog)) {
		t.Errorf("layers %v: want the plain toasts under the dialog and the notices above it", order)
	}
	if notices.Base().Exempt != kit.ExemptTab {
		t.Error("the notices are not exempt from the dialog's trap")
	}
	if ok := app.Dialogs.ShowIfFree(mk("Later")); ok {
		t.Error("ShowIfFree showed a dialog over another")
	}
	// A new dialog replaces the one shown without its OnClose (App.svelte assigns `dialog`).
	second := mk("Second")
	app.Dialogs.Show(second)
	h.Settle()
	if first.Shown() || !second.Shown() || app.Dialogs.Dialog() != views.Modal(second) {
		t.Fatal("the second dialog did not replace the first")
	}
	if len(closed) != 0 {
		t.Errorf("OnClose ran for a replaced dialog: %v", closed)
	}
	// Escape closes it after its motion, runs OnClose, frees the host and gives the focus back.
	h.Key(kit.KeyEscape, 0)
	h.Advance(time.Second)
	if app.Dialogs.DialogOpen() || second.Shown() {
		t.Fatal("Escape did not close the dialog")
	}
	if !slices.Equal(closed, []string{"Second"}) {
		t.Errorf("OnClose ran for %v, want [Second]", closed)
	}
	if content.Inert {
		t.Error("the main content stayed inert")
	}
	if z, _ := app.Layers.Z(plain); z != views.ZToasts {
		t.Errorf("plain toasts at %d after the dialog closed, want %d", z, views.ZToasts)
	}
	if app.Window.Focused() != &opener.Element {
		t.Errorf("focus after closing is on %v, want the opener", app.Window.Focused())
	}
	// Remove (after a save) frees the host at once, without OnClose.
	third := mk("Third")
	app.Dialogs.Show(third)
	third.Remove()
	if app.Dialogs.DialogOpen() || len(closed) != 1 {
		t.Errorf("after Remove: open %v, closed %v", app.Dialogs.DialogOpen(), closed)
	}
}

// paletteModal is a stand-in for the palette lane's modal.
type paletteModal struct {
	layer *kit.Element
	w     *kit.Window
	done  func()
}

func (p *paletteModal) ModalKind() views.ModalKind { return views.ModalPalette }
func (p *paletteModal) ShowModal(w *kit.Window, done func()) kit.Widget {
	p.w, p.done = w, done
	p.layer = &kit.Element{}
	return p.layer
}
func (p *paletteModal) DismissModal() {
	p.w.RemoveLayer(p.layer)
	p.done()
}

func TestPaletteBesideTheDialog(t *testing.T) {
	h, _ := fake(t)
	app := h.App
	content, _ := mainContent(h)
	app.Dialogs.SetMain(content)
	p := &paletteModal{}
	app.Dialogs.Show(p)
	h.Settle()
	if !app.Dialogs.PaletteOpen() || app.Dialogs.DialogOpen() || !app.Dialogs.Modal() || !content.Inert {
		t.Fatal("the palette is not shown as a modal beside the dialog's place")
	}
	if z, _ := app.Layers.Z(app.ToastLayer.Plain()); z != views.ZToasts {
		t.Error("the palette put the toasts behind (only a dialog does)")
	}
	order := app.Layers.Order()
	if order[len(order)-1] != kit.Widget(p.layer) {
		t.Errorf("the palette is not above every toast: %v", order)
	}
	// A dialog that opens by itself goes under the palette.
	d := app.NewDialog(views.ModalConfirm, widgets.DialogOptions{Title: "Confirm download"})
	app.Dialogs.Show(d)
	h.Settle()
	order = app.Layers.Order()
	if order[len(order)-1] != kit.Widget(p.layer) || slices.Index(order, kit.Widget(d.Dialog)) < 0 {
		t.Errorf("dialog and palette: %v", order)
	}
	app.Dialogs.DismissAll()
	if app.Dialogs.Modal() || content.Inert {
		t.Error("DismissAll left a modal")
	}
}

func TestMissingAndRecord(t *testing.T) {
	if got := views.Missing(views.Openers{}); len(got) != 10 || got[0] != "Add" {
		t.Errorf("Missing(Openers{}) = %v", got)
	}
	h, _ := fake(t)
	if m := views.Missing(&h.App.Open); len(m) != 0 {
		t.Errorf("Record left openers: %v", m)
	}
	if m := views.Missing(&h.App.Act); len(m) != 0 {
		t.Errorf("Record left actions: %v", m)
	}
}

func TestCommands(t *testing.T) {
	h, _ := fake(t,
		dl(1, state.StateDownloading, func(d *downexv1.Download) { d.Received, d.SpeedBps = MiB, MiB }),
		dl(2, state.StateCompleted, func(d *downexv1.Download) { d.Filename, d.Received = "notes.pdf", 10*MiB }),
	)
	app, calls := h.App, h.Calls
	ds := app.Main.Downloads

	t.Run("nothing selected: Pause selected is disabled", func(t *testing.T) {
		if app.Commands.Run("pause") {
			t.Error("Pause selected ran with nothing selected")
		}
	})
	t.Run("a selected active download pauses", func(t *testing.T) {
		calls.Reset()
		ds.SelectOnly(1)
		if !app.Commands.Run("pause") || !calls.Has("Pause") {
			t.Errorf("Pause selected: calls %v", calls.Names())
		}
	})
	// App.shell.test.ts "offers density, speed limit and when-done as commands"
	t.Run("offers density, speed limit and when-done as commands", func(t *testing.T) {
		calls.Reset()
		if !app.Commands.Run("density-compact") || !app.Density.Compact() {
			t.Error("Density: compact did not apply")
		}
		if app.Commands.Run("density-compact") {
			t.Error("Density: compact ran while in use")
		}
		if !app.Commands.Run("limit") || !calls.Has("OpenLimitMenu") {
			t.Errorf("Set global speed limit…: %v", calls.Names())
		}
		if !app.Commands.State().WhenDone {
			t.Fatal("When done… is not offered with a main queue")
		}
		if !app.Commands.Run("when-done") || !calls.Has("OpenWhenDoneMenu") {
			t.Errorf("When done…: %v", calls.Names())
		}
	})
	// App.test.ts "skips the choice when the file is gone, and offers it in the details pane
	// and the palette": Download again… asks to restart the completed ones.
	t.Run("offers Download again for the selected completed downloads", func(t *testing.T) {
		calls.Reset()
		ds.SelectOnly(2)
		if !app.Commands.Run("download-again") {
			t.Fatal("Download again… did not run")
		}
		c, _ := calls.Last("Restart")
		if r := c.Args[0].(views.RestartRequest); !slices.Equal(r.IDs, []int64{2}) || r.Reason != views.RestartManual {
			t.Errorf("Restart request %+v", r)
		}
	})
	t.Run("go to: category, status and queue", func(t *testing.T) {
		app.Commands.Run("cat-other")
		if ds.Category() != categories.OtherFilter {
			t.Errorf("category %q", ds.Category())
		}
		app.Commands.Run("status-paused")
		if ds.StatusFilter() != categories.Paused {
			t.Errorf("status %q", ds.StatusFilter())
		}
		app.Commands.Run("queue-1")
		if ds.QueueFilter() != 1 {
			t.Errorf("queue %d", ds.QueueFilter())
		}
	})
	t.Run("theme", func(t *testing.T) {
		app.Commands.Run("theme-dark")
		h.Settle()
		if app.ThemeChoice() != "dark" || app.WidgetTheme.Choice() != tokens.ChoiceDark {
			t.Errorf("theme %q", app.ThemeChoice())
		}
	})
	t.Run("a lane's own command", func(t *testing.T) {
		ran := false
		app.Commands.Handle("x-test", func() { ran = true })
		if !app.Commands.Run("x-test") || !ran {
			t.Error("Handle's command did not run")
		}
	})
}

// App.test.ts "offers Resume all only for paused downloads, as the daemon resumes only those"
func TestResumeAllOnlyForPaused(t *testing.T) {
	h, _ := fake(t, dl(1, state.StateFailed, nil), dl(2, state.StateCanceled, nil))
	if h.App.Abilities().CanResumeAll {
		t.Error("Resume all offered for failed and canceled downloads")
	}
	for _, c := range h.App.Commands.List() {
		if c.ID == "resume-all" && !c.Disabled {
			t.Error("the palette's Resume all is enabled")
		}
	}
}

// App.test.ts "shows a stale list with a retry banner, blocks actions…": nothing that changes
// downloads is offered while downexd is unreachable.
func TestStaleBlocksActions(t *testing.T) {
	h, d := fake(t, dl(1, state.StateDownloading, func(d *downexv1.Download) { d.Received = MiB }))
	h.App.Main.Downloads.SelectOnly(1)
	if ab := h.App.Abilities(); !ab.CanPause || !ab.CanAdd {
		t.Fatalf("reachable: %+v", ab)
	}
	d.SetDown(true)
	h.App.Main.Downloads.RefreshStatus()
	h.Settle()
	if ab := h.App.Abilities(); ab != (views.Abilities{Stale: true}) {
		t.Errorf("stale abilities %+v, want none", ab)
	}
	for _, c := range h.App.Commands.List() {
		if c.ID == "pause" && (!c.Disabled || c.DisabledReason != "downexd is not reachable") {
			t.Errorf("stale Pause selected: %+v", c)
		}
	}
}

func TestShortcuts(t *testing.T) {
	h, _ := fake(t, dl(1, state.StateDownloading, nil))
	app, calls := h.App, h.Calls
	app.Main.Downloads.SelectOnly(1)
	ctrl, shift := kit.ModCtrl, kit.ModShift
	cases := []struct {
		name string
		key  kit.Key
		mods kit.Modifiers
		ctx  views.KeyContext
		want string // the call, "" none
	}{
		{"Ctrl+K opens the palette", kit.KeyK, ctrl, views.KeyContext{}, "Palette"},
		{"Ctrl+K from a field too", kit.KeyK, ctrl, views.KeyContext{Editable: true}, "Palette"},
		{"Ctrl+Shift+K does nothing", kit.KeyK, ctrl | shift, views.KeyContext{}, ""},
		{"Ctrl+N opens Add", kit.KeyN, ctrl, views.KeyContext{}, "Add"},
		{"Ctrl+, opens Settings", kit.KeyComma, ctrl, views.KeyContext{}, "Settings"},
		{"Ctrl+F focuses the search", kit.KeyF, ctrl, views.KeyContext{}, "FocusSearch"},
		{"Ctrl+A selects all", kit.KeyA, ctrl, views.KeyContext{}, "SelectAll"},
		{"Ctrl+A in a field is the field's", kit.KeyA, ctrl, views.KeyContext{Editable: true}, ""},
		{"Delete removes", kit.KeyDelete, 0, views.KeyContext{}, "Remove"},
		{"Delete in a field is the field's", kit.KeyDelete, 0, views.KeyContext{Editable: true}, ""},
		{"Space pauses or resumes", kit.KeySpace, 0, views.KeyContext{}, "TogglePauseResume"},
		{"Space on a button is the button's", kit.KeySpace, 0, views.KeyContext{OnControl: true}, ""},
		// App.shell.test.ts "keeps the window shortcuts out of an open menu"
		{"keeps the window shortcuts out of an open menu: Delete", kit.KeyDelete, 0, views.KeyContext{InMenu: true}, ""},
		{"keeps the window shortcuts out of an open menu: Ctrl+A", kit.KeyA, ctrl, views.KeyContext{InMenu: true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls.Reset()
			used := app.Commands.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: c.key, Mods: c.mods}, c.ctx)
			names := calls.Names()
			if c.want == "" {
				if used || len(names) > 0 {
					t.Errorf("used %v, calls %v; want nothing", used, names)
				}
				return
			}
			if !used || !slices.Equal(names, []string{c.want}) {
				t.Errorf("used %v, calls %v; want %s", used, names, c.want)
			}
		})
	}
	t.Run("on the Mac, ⌫ and ⌘⌫ remove (⇧⌫ with the files); on Windows Backspace does not", func(t *testing.T) {
		defer func(old bool) { kit.MacKeys = old }(kit.MacKeys)
		for _, c := range []struct {
			mac    bool
			mods   kit.Modifiers
			ctx    views.KeyContext
			remove bool
			files  bool
		}{
			{false, 0, views.KeyContext{}, false, false},
			{true, 0, views.KeyContext{}, true, false},
			{true, kit.ModMeta, views.KeyContext{}, true, false},
			{true, shift, views.KeyContext{}, true, true},
			{true, ctrl, views.KeyContext{}, false, false},
			{true, 0, views.KeyContext{Editable: true}, false, false},
			{true, 0, views.KeyContext{InMenu: true}, false, false},
		} {
			kit.MacKeys = c.mac
			calls.Reset()
			used := app.Commands.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyBackspace, Mods: c.mods}, c.ctx)
			r, ok := calls.Last("Remove")
			got := ok && used
			if got != c.remove {
				t.Errorf("mac %v mods %v ctx %+v: removed %v, want %v", c.mac, c.mods, c.ctx, got, c.remove)
				continue
			}
			if got {
				if req, _ := r.Args[0].(views.RemoveRequest); req.DeleteFile != c.files {
					t.Errorf("mac %v mods %v: with the files %v, want %v", c.mac, c.mods, req.DeleteFile, c.files)
				}
			}
		}
	})
	t.Run("Shift+Delete removes with the files", func(t *testing.T) {
		calls.Reset()
		app.Commands.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyDelete, Mods: shift}, views.KeyContext{})
		c, ok := calls.Last("Remove")
		if r, _ := c.Args[0].(views.RemoveRequest); !ok || !r.DeleteFile || !slices.Equal(r.IDs, []int64{1}) {
			t.Errorf("Shift+Delete: %v", calls.List)
		}
	})
	t.Run("nothing while a dialog is open; Escape closes the menu", func(t *testing.T) {
		d := app.NewDialog(views.ModalSettings, widgets.DialogOptions{Title: "Settings"})
		app.Dialogs.Show(d)
		calls.Reset()
		if app.Commands.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyN, Mods: ctrl}, views.KeyContext{}) {
			t.Error("Ctrl+N used over a dialog")
		}
		d.Remove()
		app.Dialogs.SetMenuOpen(true)
		if !app.Commands.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyEscape}, views.KeyContext{}) || !calls.Has("CloseMenu") {
			t.Errorf("Escape with the menu open: %v", calls.Names())
		}
		app.Dialogs.SetMenuOpen(false)
	})
	t.Run("through the window: an unused key reaches the registry", func(t *testing.T) {
		calls.Reset()
		h.Show(&kit.Element{})
		h.Key(kit.KeyK, ctrl)
		if !calls.Has("Palette") {
			t.Errorf("Ctrl+K through the window: %v", calls.Names())
		}
	})
}

func TestKeyContextOf(t *testing.T) {
	h, _ := fake(t)
	b := widgets.NewButton("Go", "", widgets.Secondary, widgets.SizeMd, nil)
	in := widgets.NewInput("", widgets.InputOptions{Name: "Name"}, nil)
	e := &kit.Element{}
	e.Add(b, in)
	h.Show(e)
	h.Window.Focus(b)
	if k := views.KeyContextOf(h.Window); !k.OnControl || k.Editable {
		t.Errorf("a button: %+v", k)
	}
	h.Window.Focus(in)
	if k := views.KeyContextOf(h.Window); !k.Editable {
		t.Errorf("a field: %+v", k)
	}
}

func menuLabels(items []widgets.MenuItem) map[string]widgets.MenuItem {
	m := map[string]widgets.MenuItem{}
	for _, it := range items {
		m[it.ID] = it
	}
	return m
}

func TestMenuItems(t *testing.T) {
	h, d := fake(t,
		dl(1, state.StateCompleted, func(d *downexv1.Download) { d.Filename, d.Received = "notes.pdf", 10*MiB }),
		dl(2, state.StatePaused, nil),
		dl(4, state.StateFailed, func(d *downexv1.Download) {
			d.ErrorCode, d.Url = "auth_required", "https://private.example.net/files/report.pdf"
		}),
	)
	app := h.App
	ds := app.Main.Downloads
	// App.test.ts "names the menu item after the selection and offers it for completed
	// downloads"
	t.Run("names the menu item after the selection and offers it for completed downloads", func(t *testing.T) {
		ds.SelectOnly(1)
		m := menuLabels(app.Commands.MenuItems())
		if it := m["restart"]; it.Label != "Download again…" || it.Disabled {
			t.Errorf("completed: %+v", it)
		}
		if it := m["open"]; it.Disabled {
			t.Error("Open is disabled for a completed download")
		}
		ds.Click(2, selection.Modifiers{Toggle: true})
		m = menuLabels(app.Commands.MenuItems())
		if it := m["restart"]; it.Label != "Restart…" {
			t.Errorf("mixed: %+v", it)
		}
		if it := m["copy"]; it.Label != "Copy URLs" {
			t.Errorf("several: %+v", it)
		}
	})
	// App.m6.test.ts "offers a site login for a server that asked for credentials"
	t.Run("offers a site login for a server that asked for credentials", func(t *testing.T) {
		ds.SelectOnly(4)
		m := menuLabels(app.Commands.MenuItems())
		if _, ok := m["add-login"]; !ok {
			t.Error("no Add site login…")
		}
		dd, _ := ds.Get(4)
		if p := views.LoginPatternFor(dd); p != "https://private.example.net/*" {
			t.Errorf("LoginPatternFor = %q", p)
		}
		if it := m["progress"]; it.Label != "Show progress" {
			t.Errorf("a failed download has a progress: %+v", it)
		}
	})
	// App.m6.test.ts "moves downloads to another queue from the context menu"
	t.Run("moves downloads to another queue from the context menu", func(t *testing.T) {
		if _, ok := menuLabels(app.Commands.MenuItems())["move-queue"]; ok {
			t.Error("Move to queue… offered with one queue")
		}
		d.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night" }))
		app.Main.Queues.Load()
		h.Settle()
		ds.SelectOnly(2)
		if _, ok := menuLabels(app.Commands.MenuItems())["move-queue"]; !ok {
			t.Fatal("no Move to queue… with two queues")
		}
		h.Calls.Reset()
		app.Commands.RunMenuItem("move-queue")
		if names := h.Calls.Names(); !slices.Equal(names, []string{"CloseMenu", "MoveToQueue"}) {
			t.Errorf("calls %v", names)
		}
	})
	t.Run("Remove and delete file… closes the menu, then asks", func(t *testing.T) {
		ds.SelectOnly(2)
		h.Calls.Reset()
		app.Commands.RunMenuItem("remove-delete")
		c, _ := h.Calls.Last("Remove")
		if r := c.Args[0].(views.RemoveRequest); !r.DeleteFile || !slices.Equal(r.IDs, []int64{2}) || h.Calls.Names()[0] != "CloseMenu" {
			t.Errorf("calls %v", h.Calls.List)
		}
	})
}

func TestHelpers(t *testing.T) {
	name := func(id int64) string { return map[int64]string{1: "a.zip", 2: "b.zip"}[id] }
	fail := func(id int64, msg string) state.Outcome {
		return state.Outcome{ID: id, Err: state.ErrorOf(statetest.Err(connect.CodeUnavailable, "%s", msg))}
	}
	cases := []struct {
		name     string
		outcomes []state.Outcome
		want     string
	}{
		{"every one worked", []state.Outcome{{ID: 1}}, ""},
		{"one failed", []state.Outcome{{ID: 1}, fail(2, "gone")}, "Could not pause ⁨b.zip⁩: ⁨gone⁩"},
		{"several failed", []state.Outcome{fail(1, "gone"), fail(2, "busy")}, "Could not pause 2 downloads: ⁨gone⁩"},
	}
	for _, c := range cases {
		if got := views.ReportText(c.outcomes, "pause", name); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if !views.NeedsRestart(dl(1, state.StateFailed, func(d *downexv1.Download) { d.ErrorCode = "validator_changed" })) {
		t.Error("NeedsRestart: validator_changed")
	}
	if views.StartsNow(dl(1, state.StateQueued, func(d *downexv1.Download) { d.QueuePosition = 2 })) {
		t.Error("StartsNow: a download waiting in the queue")
	}
	if !views.StartsNow(dl(1, state.StateQueued, nil)) {
		t.Error("StartsNow: a queued download about to start")
	}
	label := func(f categories.Filter) string { return "Video" }
	queue := func(int64) string { return "Main queue" }
	if got := views.PageTitle(categories.All, label, 0, queue, categories.AnyStatus); got != "All downloads" {
		t.Errorf("PageTitle all = %q", got)
	}
	if got := views.PageTitle(categories.Key("video"), label, 0, queue, categories.Paused); got != "Video · Paused" {
		t.Errorf("PageTitle = %q", got)
	}
	if got := views.PageTitle(categories.All, label, 1, queue, categories.AnyStatus); got != "Main queue" {
		t.Errorf("PageTitle queue = %q", got)
	}
}

func TestDetailsStateIsKept(t *testing.T) {
	h, _ := fake(t)
	d := h.App.Details
	if d.Collapsed() {
		t.Fatal("collapsed at first")
	}
	d.Toggle()
	if !d.Collapsed() || !state.DetailsCollapsed(h.Storage) {
		t.Error("Toggle was not kept in the prefs")
	}
}

func TestThemeChangeRepaints(t *testing.T) {
	h, _ := fake(t)
	h.App.Theme.Set("dark")
	h.Settle()
	want := tokens.Dark.Palette()
	if got := h.Window.Palette(); got.Ground != want.Ground {
		t.Errorf("palette ground %v after Theme: dark, want %v", got.Ground, want.Ground)
	}
	fc := h.Host.FrameColors
	if len(fc) == 0 || !fc[len(fc)-1].Dark {
		t.Errorf("frame colours %v: want the dark frame last", fc)
	}
}

func TestStylesSizes(t *testing.T) {
	h, _ := fake(t)
	hint := views.NewHint("Others wait in the queue (1–10).")
	field := views.NewField("Downloads at once", widgets.NewInput("3", widgets.InputOptions{Name: "Downloads at once"}, nil), hint)
	tag := views.NewTag("hls", nil)
	codec := views.NewCodec("av1")
	sep := views.NewVSep()
	spin := views.NewSpinner()
	caps := views.NewCaps("Running downloads")
	row := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, AlignItems: kit.AlignCenter}}
	row.Add(tag, codec, sep, spin, caps)
	note := views.NewNote(views.NoteWarn, "yt-dlp could not be set up.")
	col := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Width: kit.Px(400)}}
	col.Add(field, row, note)
	h.Show(col)
	ib := field.Children()[1].WindowBounds()
	lb := field.Label.WindowBounds()
	if gap := ib.Min.Y - lb.Max.Y; gap != 6 {
		t.Errorf(".field gap %v, want 6 (--sp-3)", gap)
	}
	for name, c := range map[string]struct {
		e    *kit.Element
		w, h kit.DIP
	}{
		"tag height": {tag.Base(), -1, 17}, "codec height": {codec.Base(), -1, 17},
		"vsep": {sep.Base(), 1, 20}, "spinner": {spin.Base(), 14, 14},
	} {
		b := c.e.WindowBounds()
		if (c.w >= 0 && b.Width() != c.w) || b.Height() != c.h {
			t.Errorf("%s: %v, want %v × %v", name, b.Size(), c.w, c.h)
		}
	}
	if caps.Text() != "RUNNING DOWNLOADS" || caps.Semantics.Name != "Running downloads" {
		t.Errorf("caps %q named %q", caps.Text(), caps.Semantics.Name)
	}
	if h.Window.Animating() == 0 {
		t.Error("the spinner does not turn")
	}
}

// A note paints as CSS paints .note's background and border: the ground over the whole
// (border) box, then the see-through border colour over it as a 1 px ring, in both themes.
// The border colour filled under the whole box before tinted the ground, so the dark info,
// warn and fail notes drew too bright (confirm-file-fail dark: a pinkish band).
func TestNotePaintsTheGroundThenTheBorderRing(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		for _, scale := range []float32{1, 1.25} {
			h := viewstest.New(t, viewstest.Options{Daemon: statetest.NewDaemon(), Theme: theme, Scale: scale})
			tones := []views.NoteTone{views.NotePlain, views.NoteInfo, views.NoteWarn, views.NoteFail}
			col := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Width: kit.Px(400)}}
			var notes []*views.Note
			for _, tone := range tones {
				n := views.NewNote(tone, "yt-dlp could not be set up.")
				notes = append(notes, n)
				col.Add(n)
			}
			h.Show(col)
			h.Loop.Canvas.Reset()
			h.Window.InvalidateRect(kit.Rect{Max: kit.Point{X: h.Window.Size().W, Y: h.Window.Size().H}})
			h.Settle()
			th := views.TokensOf(col)
			grounds := []kit.Color{th.Surface2, th.InfoSoft, th.WarnSoft, th.DangerSoft}
			borders := []kit.Color{th.Line, th.InfoLine, th.WarnLine, th.DangerLine}
			w := 1 / scale // SnapWidth(1): one device pixel
			if scale == 1 {
				w = 1
			}
			for i, n := range notes {
				box := n.WindowBounds()
				var fill, ring *kittest.Op
				for k := range h.Loop.Canvas.Ops {
					op := &h.Loop.Canvas.Ops[k]
					if op.Kind == "FillRoundedRect" && op.Color == grounds[i] && near(op.Rect, box, 0.5) && fill == nil {
						fill = op
					}
					if op.Kind == "StrokeRoundedRect" && op.Color == borders[i] && fill != nil && ring == nil {
						ring = op
					}
				}
				name := fmt.Sprintf("%s %vx tone %d", theme, scale, tones[i])
				if fill == nil {
					t.Errorf("%s: no ground fill of the whole box %v", name, box)
					continue
				}
				if ring == nil {
					t.Errorf("%s: no border ring after the ground", name)
					continue
				}
				if !near(ring.Rect, fill.Rect.Inset(kit.UniformInsets(ring.Stroke.Width/2)), 0.01) || ring.Stroke.Width <= 0 || ring.Stroke.Width > w+0.01 {
					t.Errorf("%s: ring %v width %v, want the box %v inset by half of 1 px", name, ring.Rect, ring.Stroke.Width, fill.Rect)
				}
				for _, op := range h.Loop.Canvas.Ops {
					if op.Kind == "FillRoundedRect" && op.Color == borders[i] {
						t.Errorf("%s: the border colour fills a box (%v)", name, op.Rect)
					}
				}
			}
			h.Close()
		}
	}
}

func near(a, b kit.Rect, d kit.DIP) bool {
	abs := func(x kit.DIP) kit.DIP {
		if x < 0 {
			return -x
		}
		return x
	}
	return abs(a.Min.X-b.Min.X) <= d && abs(a.Min.Y-b.Min.Y) <= d && abs(a.Max.X-b.Max.X) <= d && abs(a.Max.Y-b.Max.Y) <= d
}
