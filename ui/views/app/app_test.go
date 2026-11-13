package app_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format/formattest"
	"downex/ui/core/selection"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
)

const MiB = formattest.MiB
const GiB = 1024 * MiB

func dl(id int64, st downexv1.DownloadState, edit func(d *downexv1.Download)) *downexv1.Download {
	return formattest.Download(id, func(d *downexv1.Download) {
		d.State = st
		if edit != nil {
			edit(d)
		}
	})
}

// opened is a screen the composition opened: its kind and request.
type opened struct {
	Kind views.ModalKind
	Req  any
	Dlg  *views.Dialog
}

// fixture is the composed main window over statetest's fakeApi port, with stand-in screens
// (the lanes' dialogs are theirs to test) that record what was asked.
type fixture struct {
	t      *testing.T
	h      *viewstest.Harness
	d      *statetest.Daemon
	m      *app.Main
	fp     *platform
	mu     sync.Mutex
	opened []opened
	// pal is the lanes' palette last opened (with setup.lanes).
	pal *palette.Palette
}

// platform is the harness's FakePlatform with a free-space answer of its own.
type platform struct {
	*viewstest.FakePlatform
	mu      sync.Mutex
	free    int64
	freeErr error
	reads   []string
	opened  []int64
	openErr error
}

func (p *platform) FreeSpace(dir string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads = append(p.reads, dir)
	return p.free, p.freeErr
}

func (p *platform) OpenProgressWindow(id int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opened = append(p.opened, id)
	return p.openErr
}

func (p *platform) freeReads() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.reads) }
func (p *platform) progress() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.opened)
}

type setup struct {
	// lanes shows the lanes' real dialogs (App.test.ts), not stand-ins.
	lanes     bool
	frameless bool
	settings  func(s *downexv1.Settings)
	daemon    func(d *statetest.Daemon)
}

func newFixture(t *testing.T, s setup, list ...*downexv1.Download) *fixture {
	t.Helper()
	f := &fixture{t: t, d: statetest.NewDaemon(list...)}
	if s.settings != nil {
		st := f.d.Settings()
		s.settings(st)
		f.d.SetSettings(st)
	}
	if s.daemon != nil {
		s.daemon(f.d)
	}
	fl := s.frameless
	f.h = viewstest.New(t, viewstest.Options{Daemon: f.d, Frameless: &fl})
	f.fp = &platform{FakePlatform: f.h.Platform.(*viewstest.FakePlatform), free: 230 * 1000 * 1000 * 1000}
	f.h.App.Platform = f.fp
	scr := f.screens()
	if s.lanes {
		scr = app.Screens{Palette: func(a *views.App) views.Modal {
			f.pal = palette.New(a, palette.Options{})
			return f.pal
		}}
	}
	m, err := app.New(f.h.App, app.Options{Screens: scr})
	if err != nil {
		t.Fatal(err)
	}
	f.m = m
	t.Cleanup(func() {
		f.closeMenus()
		m.Dispose()
	})
	f.h.Show(m.Root())
	f.h.Settle()
	return f
}

func (f *fixture) closeMenus() {
	for i := 0; i < 4 && len(f.h.Host.Open()) > 0; i++ {
		f.h.Key(kit.KeyEscape, 0)
	}
}

// screens are stand-ins: a dialog of the kind with the request's title and a Cancel button.
func (f *fixture) screens() app.Screens {
	mk := func(kind views.ModalKind, title string, req any) views.Modal {
		a := f.h.App
		var d *views.Dialog
		d = a.NewDialog(kind, widgets.DialogOptions{Title: title, Size: widgets.DialogSm})
		d.SetFooter(nil, widgets.NewButton("Cancel", "", widgets.Secondary, widgets.SizeMd, func() { d.Close() }))
		f.mu.Lock()
		f.opened = append(f.opened, opened{Kind: kind, Req: req, Dlg: d})
		f.mu.Unlock()
		return d
	}
	return app.Screens{
		Add: func(a *views.App, r views.AddRequest) views.Modal { return mk(views.ModalAdd, "Add downloads", r) },
		Confirm: func(a *views.App, r views.ConfirmRequest) views.Modal {
			return mk(views.ModalConfirm, "Confirm download", r)
		},
		Remove: func(a *views.App, r views.RemoveRequest) views.Modal {
			return mk(views.ModalRemove, "Remove download?", r)
		},
		Restart: func(a *views.App, r views.RestartRequest) views.Modal {
			return mk(views.ModalRestart, "Restart download?", r)
		},
		Open: func(a *views.App, r views.OpenRequest) views.Modal {
			return mk(views.ModalOpen, "Run this program?", r)
		},
		Move:     func(a *views.App, r views.MoveRequest) views.Modal { return mk(views.ModalMove, "Move to queue", r) },
		Settings: func(a *views.App, r views.SettingsRequest) views.Modal { return mk(views.ModalSettings, "Settings", r) },
		Queues:   func(a *views.App, r views.QueuesRequest) views.Modal { return mk(views.ModalQueues, "Queues", r) },
		Palette:  func(a *views.App) views.Modal { return mk(views.ModalPalette, "Command palette", nil) },
	}
}

// last is the last screen opened (zero when none).
func (f *fixture) last() opened {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.opened) == 0 {
		return opened{}
	}
	return f.opened[len(f.opened)-1]
}

func (f *fixture) count(kind views.ModalKind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.opened {
		if o.Kind == kind {
			n++
		}
	}
	return n
}

func (f *fixture) ds() *state.DownloadsStore { return f.h.App.Main.Downloads }

// sel selects downloads as clicks on their rows would.
func (f *fixture) sel(ids ...int64) {
	f.ds().SelectOnly(ids[0])
	for _, id := range ids[1:] {
		f.ds().Click(id, selectionToggle)
	}
	f.h.Settle()
}

func (f *fixture) key(k kit.Key, mods kit.Modifiers) bool {
	used := f.m.HandleKey(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: mods})
	f.h.Settle()
	return used
}

func (f *fixture) toasts() []string {
	var out []string
	for _, t := range f.h.App.Toasts.Items() {
		out = append(out, t.Message)
	}
	return out
}

func (f *fixture) hasToast(sub string) bool {
	for _, s := range f.toasts() {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (f *fixture) notice(title string) (widgets.Notice, bool) {
	for _, n := range f.h.App.Notices.Items() {
		if n.Title == title {
			return n, true
		}
	}
	return widgets.Notice{}, false
}

func (f *fixture) advance(d time.Duration) { f.h.Advance(d) }

// ---- App.shell.test.ts -------------------------------------------------------------------------

func TestShellTitleBar(t *testing.T) {
	t.Run("draws the window controls only for a frameless host window", func(t *testing.T) {
		f := newFixture(t, setup{frameless: true}, dl(1, state.StateQueued, nil))
		parts := f.m.Shell.TitleBar().Parts()
		var names []string
		for _, k := range []string{"minimise", "maximise", "close"} {
			names = append(names, parts[k].El.Semantics.Name)
		}
		if !slices.Equal(names, []string{"Minimise", "Maximise", "Close"}) || parts["winctl"].El.Semantics.Name != "Window" {
			t.Fatalf("controls %q", names)
		}
		f.h.Click(parts["minimise"].El)
		f.h.Click(parts["maximise"].El)
		if got := f.h.Host.Commands; !slices.Equal(got, []kit.WindowCommand{kit.WindowMinimize, kit.WindowMaximize}) {
			t.Errorf("commands %v", got)
		}
		f.h.Host.State.Maximized = true
		f.h.Loop.Send(kit.WindowStateEvent{State: kit.WindowState{Maximized: true}})
		f.h.App.Window.InvalidateRect(kit.R(0, 0, 10, 10))
		f.m.Shell.TitleBar().SetMaximized(true)
		f.h.Settle()
		if parts["maximise"].El.Semantics.Name != "Restore" {
			t.Errorf("after maximising: %q", parts["maximise"].El.Semantics.Name)
		}
		if f.h.Window.FrameHit(kit.Pt(60, 22)) != kit.FrameCaption {
			t.Error("the bar is not the window's caption (drag region)")
		}
	})
	t.Run("keeps the drag region and the window controls usable above an open dialog", func(t *testing.T) {
		f := newFixture(t, setup{frameless: true}, dl(1, state.StateQueued, nil))
		f.key(kit.KeyComma, kit.ModCtrl)
		if f.last().Kind != views.ModalSettings || !f.h.App.Dialogs.DialogOpen() {
			t.Fatal("Ctrl+, did not open Settings")
		}
		tb := f.m.Shell.TitleBar()
		parts := tb.Parts()
		if parts["winctl"].El.Inert || !f.m.Shell.Card().Inert || !tb.Modal() {
			t.Errorf("winctl inert %v, card inert %v, modal %v", parts["winctl"].El.Inert, f.m.Shell.Card().Inert, tb.Modal())
		}
		for _, e := range tb.Inert() {
			if !e.Inert {
				t.Error("the bar's search or buttons are not inert while a dialog is open")
			}
		}
		if parts["winctl"].El.Exempt != kit.ExemptTab {
			t.Error("the window controls are not reachable with Tab from a dialog")
		}
		if f.h.App.ModalTop() != 44 {
			t.Errorf("modal top %v", f.h.App.ModalTop())
		}
		f.h.Click(parts["minimise"].El)
		if !slices.Equal(f.h.Host.Commands, []kit.WindowCommand{kit.WindowMinimize}) || !f.h.App.Dialogs.DialogOpen() {
			t.Errorf("commands %v", f.h.Host.Commands)
		}
	})
	t.Run("keeps the system frame (no controls) by default, with the search and the palette key", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil), dl(2, state.StateQueued, func(d *downexv1.Download) { d.Filename = "other.iso" }))
		if _, ok := f.m.Shell.TitleBar().Parts()["winctl"]; ok {
			t.Error("window controls without a custom title bar")
		}
		if f.h.Window.FrameHit(kit.Pt(60, 22)) == kit.FrameCaption {
			t.Error("a drag region without a custom title bar")
		}
		f.key(kit.KeyF, kit.ModCtrl)
		if f.h.Window.Focused() != f.m.Shell.TitleBar().Search().Base() {
			t.Fatal("Ctrl+F did not focus the search")
		}
		f.m.Shell.TitleBar().Search().OnChange("other")
		f.h.Settle()
		if ids := f.ds().VisibleIDs(); !slices.Equal(ids, []int64{2}) {
			t.Errorf("visible %v", ids)
		}
		f.h.Click(f.m.Shell.TitleBar().Parts()["palette key"].El)
		if f.last().Kind != views.ModalPalette {
			t.Error("the palette key did not open the palette")
		}
	})
}

func TestShellPageHeader(t *testing.T) {
	t.Run("names the filter shown, counts it, and filters by its chips", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateDownloading, nil), dl(2, state.StateQueued, nil),
			dl(3, state.StateFailed, func(d *downexv1.Download) { d.Error = "HTTP 503" }), dl(4, state.StateCompleted, nil))
		hd := f.m.Shell.Header()
		if hd.Title().Text() != "All downloads" || hd.CountText().Text() != "4" {
			t.Fatalf("%q %q", hd.Title().Text(), hd.CountText().Text())
		}
		for st, name := range map[categories.Status]string{categories.Active: "1 downloading", categories.Queued: "1 queued", categories.Failed: "1 failed"} {
			if hd.Chip(st).Semantics.Name != name {
				t.Errorf("chip %q", hd.Chip(st).Semantics.Name)
			}
		}
		f.h.Click(hd.Chip(categories.Active).Base())
		if hd.Title().Text() != "Active" || hd.CountText().Text() != "1" || !slices.Equal(f.ds().VisibleIDs(), []int64{1}) || !hd.Chip(categories.Active).Pressed {
			t.Errorf("after the chip: %q %q %v", hd.Title().Text(), hd.CountText().Text(), f.ds().VisibleIDs())
		}
		f.h.Click(hd.Chip(categories.Active).Base())
		if len(f.ds().VisibleIDs()) != 4 {
			t.Error("a second click did not show every status")
		}
	})
	t.Run("switches the list density from the More menu, and remembers it", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		more := f.m.Shell.Header().More()
		f.h.Click(more.Trigger.Base())
		clickMenuItem(t, f.h, more.Menu(), "Compact")
		stored, _ := f.h.Storage.Value(state.DensityKey)
		if f.h.App.DensityChoice() != state.Compact || stored != "compact" || !more.Trigger.Base().Focused() {
			t.Errorf("density %v, stored %q, focus back %v", f.h.App.DensityChoice(), stored, more.Trigger.Base().Focused())
		}
		f.h.Click(more.Trigger.Base())
		if it := more.Items[1]; !it.Checked {
			t.Error("Compact is not checked the next time")
		}
	})
	t.Run("keeps the window shortcuts out of an open menu", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateDownloading, nil))
		f.sel(1)
		more := f.m.Shell.Header().More()
		f.h.Click(more.Trigger.Base())
		if more.Menu() == nil {
			t.Fatal("no menu")
		}
		// the window's unused keys go to Main.HandleKey (kit.WindowOptions.OnKey)
		f.key(kit.KeyDelete, 0)
		f.key(kit.KeyA, kit.ModCtrl)
		if f.count(views.ModalRemove) != 0 || f.d.Count("remove") != 0 {
			t.Error("Delete reached the window through an open menu")
		}
	})
	t.Run("opens Add in either mode from its split button", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		sp := f.m.Shell.Header().AddButton()
		f.h.Click(sp.More.Trigger.Base())
		clickMenuItem(t, f.h, sp.More.Menu(), "Add video or stream…")
		if r, _ := f.last().Req.(views.AddRequest); f.last().Kind != views.ModalAdd || r.Mode != views.AddStream {
			t.Fatalf("opened %+v", f.last())
		}
		f.h.App.Dialogs.DismissAll()
		f.h.Click(sp.Main.Base())
		if r, _ := f.last().Req.(views.AddRequest); r.Mode != views.AddDefault || f.count(views.ModalAdd) != 2 {
			t.Errorf("opened %+v", f.last())
		}
	})
}

func TestShellDetailsPane(t *testing.T) {
	// App.shell.test.ts "is not there while nothing is selected"
	f := newFixture(t, setup{}, dl(1, state.StatePaused, nil), dl(2, state.StateQueued, nil))
	details := f.m.Downloads.Details().Base()
	if f.m.Shell.DetailsShown() || details.Parent() != nil {
		t.Fatal("the details pane is there with nothing selected")
	}
	f.sel(1)
	if !f.m.Shell.DetailsShown() || details.Parent() != f.m.Shell.Card() {
		t.Fatal("selecting did not bring the details pane")
	}
	f.ds().ClearSelection()
	f.h.Settle()
	if f.m.Shell.DetailsShown() || details.Parent() != nil {
		t.Error("the pane stayed after the selection was cleared")
	}
}

func TestShellSidebarFooter(t *testing.T) {
	t.Run("says honestly what browser downloads do and opens Settings → Integration", func(t *testing.T) {
		f := newFixture(t, setup{settings: func(s *downexv1.Settings) { s.DefaultDir = "/srv/dl"; s.ConfirmBrowserDownloads = false }}, dl(1, state.StateQueued, nil))
		sb := f.m.Shell.Sidebar()
		if !strings.HasPrefix(sb.Handoff().Semantics.Name, "Browser downloads: start at once") {
			t.Fatalf("footer %q", sb.Handoff().Semantics.Name)
		}
		if cats, _, _ := sb.Items(); !strings.HasPrefix(cats[0].Semantics.Name, "All downloads") {
			t.Errorf("first button %q", cats[0].Semantics.Name)
		}
		f.h.Click(sb.Handoff())
		if r, _ := f.last().Req.(views.SettingsRequest); f.last().Kind != views.ModalSettings || r.Page != validate.PageIntegration {
			t.Errorf("opened %+v", f.last())
		}
	})
	t.Run("puts the toasts under an open dialog, so they never cover its buttons", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		a := f.h.App
		if z, _ := a.Layers.Z(a.ToastLayer.Plain()); z != views.ZToasts {
			t.Fatalf("toasts at %d", z)
		}
		f.h.Click(f.m.Shell.Sidebar().Handoff())
		if z, _ := a.Layers.Z(a.ToastLayer.Plain()); z != views.ZToastsBehind {
			t.Errorf("toasts at %d under a dialog", z)
		}
		if z, _ := a.Layers.Z(a.ToastLayer.Notices()); z != views.ZNotices || a.ToastLayer.Notices().Base().Exempt != kit.ExemptTab {
			t.Errorf("notices at %d", z)
		}
		f.last().Dlg.Close()
		f.advance(time.Second)
		if z, _ := a.Layers.Z(a.ToastLayer.Plain()); z != views.ZToasts {
			t.Errorf("toasts at %d after the dialog closed", z)
		}
	})
	t.Run("asks first by default", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		if !strings.HasPrefix(f.m.Shell.Sidebar().Handoff().Semantics.Name, "Browser downloads: ask first") {
			t.Errorf("footer %q", f.m.Shell.Sidebar().Handoff().Semantics.Name)
		}
	})
}

func TestShellStatusBar(t *testing.T) {
	limit := func(f *fixture) *widgets.MenuButton { return f.m.Shell.StatusBar().LimitMenu() }
	t.Run("sets the global speed limit with a full settings round-trip", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		lm := limit(f)
		if lm.Trigger.Base().Disabled || !strings.Contains(lm.Trigger.Base().Semantics.Name, "Unlimited") {
			t.Fatalf("trigger %q disabled %v", lm.Trigger.Base().Semantics.Name, lm.Trigger.Base().Disabled)
		}
		f.h.Click(lm.Trigger.Base())
		reads := f.d.Count("getSettings")
		clickMenuItem(t, f.h, lm.Menu(), "10 MB/s")
		f.h.Settle()
		if f.d.Count("getSettings") != reads+1 || f.d.Count("updateSettings") != 1 {
			t.Fatalf("reads %d (was %d), writes %d", f.d.Count("getSettings"), reads, f.d.Count("updateSettings"))
		}
		if got := f.d.Settings().GetGlobalSpeedLimit(); got != 10*1000*1000 {
			t.Errorf("written limit %d", got)
		}
		if !strings.Contains(lm.Trigger.Base().Semantics.Name, "Limit 10 MB/s") || !f.hasToast("Global speed limit: 10.0 MB/s") {
			t.Errorf("trigger %q toasts %q", lm.Trigger.Base().Semantics.Name, f.toasts())
		}
	})
	t.Run("opens Settings → Downloads for a custom limit", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		f.h.Click(limit(f).Trigger.Base())
		clickMenuItem(t, f.h, limit(f).Menu(), "Custom…")
		if r, _ := f.last().Req.(views.SettingsRequest); r.Page != validate.PageDownloads || r.FocusField != validate.FieldGlobalSpeedLimit || f.d.Count("updateSettings") != 0 {
			t.Errorf("opened %+v", f.last())
		}
	})
	t.Run("changes what happens when the main queue is done", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		wm := f.m.Shell.StatusBar().WhenDoneMenu()
		if wm.Style.Hidden || wm.Trigger.Base().Semantics.Name != "When done: keep running" {
			t.Fatalf("when done %q", wm.Trigger.Base().Semantics.Name)
		}
		f.h.Click(wm.Trigger.Base())
		clickMenuItem(t, f.h, wm.Menu(), "Shut down")
		f.h.Settle()
		args := f.d.Args("updateQueue")
		if len(args) == 0 {
			t.Fatal("no updateQueue")
		}
		if wm.Trigger.Base().Semantics.Name != "When done: shut down" {
			t.Errorf("trigger %q", wm.Trigger.Base().Semantics.Name)
		}
	})
	// App.shell.test.ts: hides "When done" for a downexd without queues
	t.Run("hides \"When done\" for a downexd without queues", func(t *testing.T) {
		f := newFixture(t, setup{daemon: func(d *statetest.Daemon) {
			d.Before = func(name string, _ ...any) error {
				if name == "listQueues" {
					return statetest.Err(connect.CodeUnimplemented, "downex.v1.QueueService.ListQueues is not implemented")
				}
				return nil
			}
		}}, dl(1, state.StateQueued, nil))
		if !f.m.Shell.StatusBar().WhenDoneMenu().Style.Hidden {
			t.Error("When done shows without queues")
		}
	})
	t.Run("shows the free space of the download drive, warns when it is too small, and refreshes it after a completion", func(t *testing.T) {
		f := newFixture(t, setup{settings: func(s *downexv1.Settings) { s.DefaultDir = `D:\Downloads` }},
			dl(1, state.StateQueued, func(d *downexv1.Download) { d.Size = 3 * GiB }),
			dl(2, state.StateDownloading, func(d *downexv1.Download) { d.Size = 4 * GiB; d.Received = GiB }))
		f.fp.mu.Lock()
		f.fp.free = 200 * GiB
		f.fp.mu.Unlock()
		f.m.WindowFocused()
		f.h.Settle()
		f.d.Emit(statetest.Updated(dl(9, state.StateCompleted, nil)))
		f.h.Settle()
		sb := f.m.Shell.StatusBar()
		if !strings.Contains(sb.Free().Semantics.Name, "215 GB free") || !strings.Contains(sb.Free().Semantics.Name, "D:") || sb.Short() {
			t.Fatalf("free %q", sb.Free().Semantics.Name)
		}
		if f.fp.reads[0] != `D:\Downloads` {
			t.Errorf("read %q", f.fp.reads)
		}
		f.fp.mu.Lock()
		f.fp.free = 5 * GiB
		f.fp.mu.Unlock()
		reads := f.fp.freeReads()
		f.d.Emit(statetest.Updated(dl(3, state.StateCompleted, nil)))
		f.h.Settle()
		if f.fp.freeReads() != reads+1 || !sb.Short() || sb.Free().Tooltip != "Not enough free space for the active and queued downloads (about 1.1 GB more needed)" {
			t.Errorf("reads %d→%d short %v title %q", reads, f.fp.freeReads(), sb.Short(), sb.Free().Tooltip)
		}
	})
	t.Run("hides the free space when the host cannot tell, and stops asking", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		f.fp.mu.Lock()
		f.fp.freeErr = statetest.Err(connect.CodeUnimplemented, "the FreeSpace binding is not available")
		f.fp.mu.Unlock()
		f.d.SetSettings(f.d.Settings()) // a new read
		f.h.App.Main.LoadSettings()
		f.m.WindowFocused()
		f.h.Settle()
		f.d.Emit(statetest.Updated(dl(1, state.StateCompleted, nil)))
		f.h.Settle()
		reads := f.fp.freeReads()
		f.d.Emit(statetest.Updated(dl(5, state.StateCompleted, nil)))
		f.h.Settle()
		if !f.m.Shell.StatusBar().Free().Style.Hidden || f.fp.freeReads() != reads {
			t.Errorf("free shown %v, reads %d→%d", !f.m.Shell.StatusBar().Free().Style.Hidden, reads, f.fp.freeReads())
		}
	})
	t.Run("re-reads the free space every minute", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		before := f.fp.freeReads()
		if before == 0 {
			t.Fatal("no first read")
		}
		f.advance(app.FreeSpaceInterval)
		if f.fp.freeReads() <= before {
			t.Errorf("reads %d after a minute (%d before)", f.fp.freeReads(), before)
		}
	})
}

func TestShellCommandPalette(t *testing.T) {
	t.Run("offers density, speed limit and when-done as commands", func(t *testing.T) {
		f := newFixture(t, setup{}, dl(1, state.StateQueued, nil))
		c := f.h.App.Commands
		if !c.Run("density-compact") || f.h.App.DensityChoice() != state.Compact {
			t.Error("density-compact")
		}
		if !c.Run("limit") || f.m.Shell.StatusBar().LimitMenu().Menu() == nil {
			t.Fatal("the limit command did not open the menu")
		}
		f.h.Key(kit.KeyEscape, 0)
		if !c.Run("when-done") || f.m.Shell.StatusBar().WhenDoneMenu().Menu() == nil {
			t.Error("the when-done command did not open the menu")
		}
	})
	t.Run("lists running downloads; Enter shows a running one, Ctrl+Enter its folder", func(t *testing.T) {
		// the palette is the palette lane's; its Enter is Act.OpenDownload and Ctrl+Enter
		// Act.OpenFolder, which the composition fills
		f := newFixture(t, setup{}, dl(1, state.StateDownloading, nil), dl(2, state.StateCompleted, nil))
		f.key(kit.KeyK, kit.ModCtrl)
		if f.last().Kind != views.ModalPalette {
			t.Fatal("Ctrl+K did not open the palette")
		}
		f.h.App.Dialogs.DismissAll()
		f.h.App.Act.OpenDownload(1)
		f.h.Settle()
		if !slices.Equal(f.fp.progress(), []int64{1}) {
			t.Errorf("progress windows %v", f.fp.progress())
		}
		f.h.App.Act.OpenFolder(2)
		f.h.Settle()
		if !slices.Contains(f.fp.Calls, "OpenFolder 2") || slices.Contains(f.fp.Calls, "OpenFile 2") {
			t.Errorf("platform calls %v", f.fp.Calls)
		}
	})
}

// clickMenuItem clicks the open menu's item named name.
func clickMenuItem(t *testing.T, h *viewstest.Harness, m *widgets.Menu, name string) {
	t.Helper()
	if m == nil {
		t.Fatalf("no menu to choose %q in", name)
	}
	tree := m.Root().Semantics()
	var found *kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok || found != nil {
			return
		}
		switch n.Role {
		case kit.RoleMenuItem, kit.RoleMenuItemRadio, kit.RoleMenuItemCheckBox:
			if n.Name == name || strings.HasPrefix(n.Name, name+" ") {
				found = &n
				return
			}
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	if found == nil {
		t.Fatalf("no menu item %q", name)
	}
	h.Host.Top().Click(found.Bounds.Center())
	h.Settle()
}

var errBoom = errors.New("boom")

var selectionToggle = selection.Modifiers{Toggle: true}
