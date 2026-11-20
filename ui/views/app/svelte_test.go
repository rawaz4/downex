package app_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/validate"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/dialogs"
	"downex/ui/views/settings"
)

// The App.test.ts and App.m6.test.ts cases, over the composed window with the lanes' real
// dialogs. What a case checks inside a lane's dialog (its fields, quality cards, guard) is the
// lane's own test; here it is what the composition does with it: which dialog opens, for which
// download, what reaches downexd and the host, and what the window says.

// lanes is the composed window with the lanes' dialogs.
func lanes(t *testing.T, s setup, list ...*downexv1.Download) *fixture {
	t.Helper()
	s.lanes = true
	return newFixture(t, s, list...)
}

// find is the first semantic node of the window (with its layers) of role whose name is name,
// or starts with name and a space (a button named with its count).
func (f *fixture) find(role kit.Role, name string) (kit.SemanticNode, bool) {
	for _, n := range nodesOf(f.h.Window, role) {
		if n.Name == name || strings.HasPrefix(n.Name, name+" ") {
			return n, true
		}
	}
	return kit.SemanticNode{}, false
}

func nodesOf(w *kit.Window, role kit.Role) []kit.SemanticNode {
	tree := w.Semantics()
	var out []kit.SemanticNode
	var walk func(id kit.NodeID)
	walk = func(id kit.NodeID) {
		n, ok := tree.Node(id)
		if !ok {
			return
		}
		if n.Role == role {
			out = append(out, n)
		}
		for _, c := range tree.AppendChildren(nil, id) {
			walk(c)
		}
	}
	walk(tree.Root())
	return out
}

// dialogName is the open dialog's accessible name (its title).
func (f *fixture) dialogName() string {
	for _, role := range []kit.Role{kit.RoleAlertDialog, kit.RoleDialog} {
		if ns := nodesOf(f.h.Window, role); len(ns) > 0 {
			return ns[len(ns)-1].Name
		}
	}
	return ""
}

// radio clicks the radio whose name starts with name.
func (f *fixture) radio(name string) {
	f.t.Helper()
	for _, n := range nodesOf(f.h.Window, kit.RoleRadioButton) {
		if strings.HasPrefix(n.Name, name) {
			f.h.ClickAt(n.Bounds.Center())
			f.h.Settle()
			return
		}
	}
	f.t.Fatalf("no radio %q", name)
}

// dialogDescription is the open dialog's accessible description.
func (f *fixture) dialogDescription() string {
	for _, role := range []kit.Role{kit.RoleAlertDialog, kit.RoleDialog} {
		if ns := nodesOf(f.h.Window, role); len(ns) > 0 {
			return ns[len(ns)-1].Description
		}
	}
	return ""
}

// press clicks the button named name.
func (f *fixture) press(name string) {
	f.t.Helper()
	n, ok := f.find(kit.RoleButton, name)
	if !ok {
		f.t.Fatalf("no button %q", name)
	}
	f.h.ClickAt(n.Bounds.Center())
	f.h.Settle()
}

func disabled(e *kit.Element) bool {
	n, ok := e.Window().Semantics().Node(e.NodeID())
	return ok && n.States&kit.NodeDisabled != 0
}

// menu opens the download menu for the selection, as a right-click on a row does.
func (f *fixture) menu() {
	f.h.App.Act.OpenMenu(kit.Point{X: 600, Y: 300})
	f.h.Settle()
}

// pastGuard waits until a confirmation dialog that opened by itself accepts clicks.
func (f *fixture) pastGuard() { f.advance(650 * time.Millisecond) }

func (f *fixture) confirming() int64 {
	id, _ := f.m.Confirming()
	return id
}

func awaiting(id, createdAt int64, name string) *downexv1.Download {
	return dl(id, state.StateAwaiting, func(d *downexv1.Download) {
		d.Filename = name
		d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		d.Size = -1
		d.CreatedAt = formattest.Millis(createdAt)
		d.Category = "Video"
		d.Dir = "/home/you/Downloads/Video"
	})
}

func (f *fixture) calls(name string) int { return f.d.Count(name) }

// ---- App.test.ts -------------------------------------------------------------------------------

func TestAppUnreachable(t *testing.T) {
	t.Run("shows a stale list with a retry banner, blocks actions and announces recovery", func(t *testing.T) {
		f := lanes(t, setup{},
			dl(1, state.StateDownloading, func(d *downexv1.Download) { d.SpeedBps = 2 * MiB; d.Received = MiB }),
			dl(2, state.StatePaused, nil))
		b := f.m.Shell.ConnectionBanner()
		if !b.Base().Style.Hidden {
			t.Fatal("the banner shows while downexd answers")
		}
		f.sel(1)
		hd := f.m.Shell.Header()
		if disabled(hd.Pause().Base()) {
			t.Fatal("Pause is unavailable for a running download")
		}
		f.d.SetDown(true)
		f.advance(3 * time.Second)
		if b.Base().Style.Hidden || !strings.Contains(b.TitleText(), "can’t reach downexd") || !strings.Contains(b.Paragraph(), "last known state") {
			t.Fatalf("banner %q / %q", b.TitleText(), b.Paragraph())
		}
		if !f.h.App.Stale() {
			t.Fatal("the list is not stale")
		}
		if !strings.Contains(strings.Join(f.m.Shell.StatusBar().Texts(), " | "), "0 B/s") {
			t.Errorf("status bar %q", f.m.Shell.StatusBar().Texts())
		}
		for _, b := range []*kit.Element{hd.AddButton().Base(), hd.Pause().Base(), hd.RemoveButton().Base()} {
			n, _ := b.Window().Semantics().Node(b.NodeID())
			if n.States&kit.NodeDisabled == 0 {
				// the split button's own node is a group; its main half carries the state
				if _, ok := b.Widget().(interface{ Base() *kit.Element }); ok && b == hd.AddButton().Base() {
					if !disabled(hd.AddButton().Main.Base()) {
						t.Error("Add stays available")
					}
					continue
				}
				t.Errorf("%q stays available", n.Name)
			}
		}
		f.d.ResetCalls()
		f.h.Click(hd.Pause().Base())
		f.key(kit.KeyDelete, 0)
		f.h.Settle()
		if f.h.App.Dialogs.DialogOpen() {
			t.Error("a dialog opened while stale")
		}
		for _, c := range f.d.Calls() {
			if c != "status" {
				t.Errorf("call %q while stale", c)
			}
		}
		f.d.SetDown(false)
		f.h.Click(b.Retry().Base())
		f.h.Settle()
		if !b.Base().Style.Hidden {
			t.Error("the banner stays after Retry now")
		}
		if b.Status != "Reconnected to downexd." {
			t.Errorf("announced %q", b.Status)
		}
		if disabled(hd.Pause().Base()) {
			t.Error("Pause stays unavailable")
		}
	})
}

func TestAppOpeningFiles(t *testing.T) {
	t.Run("confirms before opening a program whose name hides its extension", func(t *testing.T) {
		f := lanes(t, setup{},
			dl(1, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "invoice\u202efdp.exe"; d.Received = 10 * MiB }),
			dl(2, state.StateCompleted, func(d *downexv1.Download) {
				d.Filename = "notes.pdf"
				d.Received = 10 * MiB
				d.CreatedAt = formattest.Millis(1)
			}))
		f.h.App.Act.OpenDownload(1)
		f.h.Settle()
		o, ok := f.h.App.Dialogs.Dialog().(*dialogs.Open)
		if !ok {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		if f.dialogName() != "Run this program?" || !strings.Contains(o.NameText().Text(), "invoice⟨U+202E⟩fdp.exe") || !strings.Contains(o.Warning().Text(), "hidden text-direction characters") {
			t.Errorf("title %q name %q warning %q", f.dialogName(), o.NameText().Text(), o.Warning().Text())
		}
		if slices.Contains(f.fp.Calls, "OpenFile 1") {
			t.Fatal("opened before the confirmation")
		}
		f.press("Run")
		if !slices.Contains(f.fp.Calls, "OpenFile 1") {
			t.Errorf("Run did not open it: %v", f.fp.Calls)
		}
		// ordinary documents open directly
		f.h.App.Act.OpenDownload(2)
		f.h.Settle()
		if !slices.Contains(f.fp.Calls, "OpenFile 2") || f.h.App.Dialogs.DialogOpen() {
			t.Errorf("calls %v, dialog %v", f.fp.Calls, f.h.App.Dialogs.Kind())
		}
	})
}

func TestAppDropping(t *testing.T) {
	t.Run("never leaves a dropped file or link to the webview, which would replace the app", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateQueued, nil))
		file := app.Drop{Files: true}
		if e := f.m.DragOver(file); e != app.DropNone {
			t.Errorf("a file dragged over: %v", e)
		}
		if e := f.m.HandleDrop(file); e != app.DropNone {
			t.Errorf("a file dropped: %v", e)
		}
		local := app.Drop{URIs: true, URIList: "file:///C:/Users/me/notes.txt"}
		if e := f.m.HandleDrop(local); e != app.DropNone {
			t.Errorf("a local file link dropped: %v", e)
		}
		if f.h.App.Dialogs.DialogOpen() {
			t.Fatal("a dialog opened")
		}
		// text dropped into a field is left to it
		if e := f.m.HandleDrop(app.Drop{Plain: true, Text: "iso", OnField: true}); e != app.DropField {
			t.Errorf("text into the search: %v", e)
		}
		if e := f.m.HandleDrop(app.Drop{Files: true, OnField: true}); e != app.DropNone {
			t.Errorf("a file into the search: %v", e)
		}
		// links still open the Add dialog
		if e := f.m.HandleDrop(app.Drop{URIs: true, URIList: "https://example.com/a.zip"}); e != app.DropCopy {
			t.Errorf("a link dropped: %v", e)
		}
		f.h.Settle()
		if f.h.App.Dialogs.Kind() != views.ModalAdd {
			t.Errorf("dialog %v", f.h.App.Dialogs.Kind())
		}
	})
}

func TestAppFocusAfterRemoving(t *testing.T) {
	t.Run("keeps keyboard focus when the toolbar Remove button is used", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateQueued, nil), dl(2, state.StateQueued, func(d *downexv1.Download) { d.CreatedAt = formattest.Millis(1) }))
		f.sel(1)
		rm := f.m.Shell.Header().RemoveButton()
		f.h.Window.Focus(rm)
		f.h.Click(rm.Base())
		r, ok := f.h.App.Dialogs.Dialog().(*dialogs.Remove)
		if !ok || f.dialogName() != "Remove download?" {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		r.Submit()
		f.h.Settle()
		f.advance(time.Second)
		if f.h.App.Dialogs.DialogOpen() {
			t.Fatal("the dialog stays")
		}
		if f.h.Window.Focused() == nil {
			t.Error("the focus went nowhere")
		}
		// the next row took the removed one's place
		if got := views.SelectedIDs(f.ds().Selected()); !slices.Equal(got, []int64{2}) {
			t.Errorf("selected %v", got)
		}
	})
}

func TestRestartDialogName(t *testing.T) {
	t.Run("isolates the name inside the sentence", func(t *testing.T) {
		txt := dialogs.RestartTexts([]string{"(final) report.pdf"}, views.RestartManual, nil)
		if !strings.Contains(txt.Description, "“\u2068(final) report.pdf\u2069”") {
			t.Errorf("description %q", txt.Description)
		}
	})
}

func TestAppConfirmingHandOffs(t *testing.T) {
	t.Run("shows one dialog per waiting download, oldest first, and remembers a closed one", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateQueued, nil), awaiting(2, 200, "newer.mp4"), awaiting(3, 100, "older.mp4"))
		if f.confirming() != 3 {
			t.Fatalf("confirming %d, want the older 3", f.confirming())
		}
		// it opened by itself: keystrokes meant for something else do not reach its form
		if fo := f.h.Window.Focused(); fo == nil || fo.Semantics.Role != kit.RoleDialog {
			t.Errorf("focus on %v", fo)
		}
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		args := f.d.Args("confirm")
		if len(args) != 2 || args[0] != int64(3) || !args[1].(*downexv1.ConfirmRequest).GetStart() {
			t.Fatalf("confirm %v", args)
		}
		f.advance(time.Second)
		// the next one follows
		if f.confirming() != 2 {
			t.Fatalf("then confirming %d", f.confirming())
		}
		// closing leaves it waiting, and the dialog does not come back by itself
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		if f.h.App.Dialogs.DialogOpen() || f.d.Item(2).GetState() != state.StateAwaiting {
			t.Fatalf("dialog %v, state %v", f.h.App.Dialogs.Kind(), f.d.Item(2).GetState())
		}
		// downexd asks again ("downex-ui --confirm 2")
		f.m.RequestConfirm([]int64{2})
		f.h.Settle()
		if f.confirming() != 2 {
			t.Fatal("the request did not show the dialog again")
		}
		f.press("Cancel download")
		if f.calls("remove") != 0 {
			t.Fatal("cancelled too soon after it appeared to be deliberate")
		}
		f.pastGuard()
		f.press("Cancel download")
		f.h.Settle()
		if a := f.d.Args("remove"); len(a) != 2 || a[0] != int64(2) || a[1] != false {
			t.Errorf("remove %v", a)
		}
		f.advance(time.Second)
		if f.h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stays")
		}
	})
	t.Run("offers the dialog again from the list and filters waiting downloads", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StatePaused, nil), awaiting(2, 100, "clip.mp4"))
		if f.confirming() != 2 {
			t.Fatal("no dialog")
		}
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		f.sel(2)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Confirm download…")
		if f.confirming() != 2 {
			t.Fatal("the menu item did not offer it again")
		}
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		// Enter (or a double-click) on the row opens it too, ready for editing the name
		f.h.App.Act.OpenDownload(2)
		f.advance(time.Second)
		if f.confirming() != 2 {
			t.Fatal("the row did not open it")
		}
		if fo := f.h.Window.Focused(); fo == nil || fo.Semantics.Name != "File name" {
			t.Errorf("focus on %+v", fo)
		}
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		_, _, sts := f.m.Shell.Sidebar().Items()
		for _, it := range sts {
			if strings.HasPrefix(it.Semantics.Name, "To confirm") {
				f.h.Click(it)
			}
		}
		f.h.Settle()
		if got := views.SelectedIDs(f.ds().Visible()); !slices.Equal(got, []int64{2}) {
			t.Errorf("shown %v", got)
		}
	})
}

func TestAppCategoriesFromSettings(t *testing.T) {
	t.Run("lists the configured categories, custom ones too, and Other", func(t *testing.T) {
		f := lanes(t, setup{settings: func(s *downexv1.Settings) {
			s.Categories = []*downexv1.Category{{Name: "Video", Extensions: []string{"mp4"}}, {Name: "Comics", Extensions: []string{"cbz"}}}
		}},
			dl(1, state.StateQueued, func(d *downexv1.Download) { d.Filename = "a.cbz"; d.Category = "Comics" }),
			dl(2, state.StateQueued, func(d *downexv1.Download) { d.Filename = "b.mp4"; d.Category = "Video" }),
			dl(3, state.StateQueued, func(d *downexv1.Download) { d.Filename = "c.bin" }))
		cats, _, _ := f.m.Shell.Sidebar().Items()
		var labels []string
		for _, c := range cats {
			labels = append(labels, strings.Fields(c.Semantics.Name)[0])
		}
		if len(labels) < 4 || !slices.Equal(labels[:4], []string{"All", "Video", "Comics", "Other"}) {
			t.Fatalf("categories %q", labels)
		}
		f.h.Click(cats[2])
		if got := views.SelectedIDs(f.ds().Visible()); !slices.Equal(got, []int64{1}) {
			t.Errorf("Comics shows %v", got)
		}
		f.h.Click(cats[3])
		if got := views.SelectedIDs(f.ds().Visible()); !slices.Equal(got, []int64{3}) {
			t.Errorf("Other shows %v", got)
		}
	})
}

func TestAppPauseAllResumeAll(t *testing.T) {
	t.Run("uses the daemon calls and reports how many changed", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateDownloading, nil), dl(2, state.StatePaused, nil))
		hd := f.m.Shell.Header()
		f.h.Click(hd.PauseAll().Base())
		if f.calls("pauseAll") != 1 || !f.hasToast("1 download paused") {
			t.Errorf("calls %v toasts %q", f.d.Calls(), f.toasts())
		}
		f.h.Settle()
		f.h.Click(hd.ResumeAll().Base())
		if f.calls("resumeAll") != 1 {
			t.Errorf("calls %v", f.d.Calls())
		}
	})
	t.Run("offers Resume all only for paused downloads, as the daemon resumes only those", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateFailed, nil), dl(2, state.StateCanceled, nil))
		if !disabled(f.m.Shell.Header().ResumeAll().Base()) {
			t.Error("Resume all is offered")
		}
	})
}

func TestAppSettingsStayCurrent(t *testing.T) {
	books := func(s *downexv1.Settings) {
		s.DefaultDir = "/srv/dl"
		s.Categories = append(s.Categories, &downexv1.Category{Name: "Books", Extensions: []string{"epub"}, Dir: "/srv/books"})
	}
	clip := func(id int64) *downexv1.Download {
		return dl(id, state.StateAwaiting, func(d *downexv1.Download) {
			d.Filename = "clip.mp4"
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
			d.Size = -1
			d.Category = "Books"
			d.Dir = "/srv/books"
		})
	}
	hasBooks := func(f *fixture) bool {
		s, ok := f.h.App.Settings()
		if !ok {
			return false
		}
		for _, c := range s.GetCategories() {
			if c.GetName() == "Books" {
				return true
			}
		}
		return false
	}
	t.Run("reads the settings once downexd is reachable, also when it was not at first", func(t *testing.T) {
		f := lanes(t, setup{settings: books, daemon: func(d *statetest.Daemon) { d.SetDown(true) }}, clip(1))
		if f.h.App.Dialogs.DialogOpen() {
			t.Fatal("a dialog while downexd is unreachable")
		}
		f.d.SetDown(false)
		f.advance(3 * time.Second)
		f.h.Settle()
		if f.confirming() != 1 || !hasBooks(f) {
			t.Fatalf("confirming %d, settings with Books %v", f.confirming(), hasBooks(f))
		}
		// the dialog's Category select and its folder proposal are the add lane's
		// (TestConfirmDialog); it was made with these settings
	})
	t.Run("tries again at the next status check after a failed read", func(t *testing.T) {
		failing := true
		f := lanes(t, setup{settings: books, daemon: func(d *statetest.Daemon) {
			d.Before = func(name string, _ ...any) error {
				if name == "getSettings" && failing {
					return statetest.Err(4, "downexd did not answer")
				}
				return nil
			}
		}}, dl(1, state.StateQueued, func(d *downexv1.Download) { d.Filename = "a.epub"; d.Category = "Books" }))
		label := func() []string {
			cats, _, _ := f.m.Shell.Sidebar().Items()
			var out []string
			for _, c := range cats {
				out = append(out, c.Semantics.Name)
			}
			return out
		}
		if slices.ContainsFunc(label(), func(s string) bool { return strings.HasPrefix(s, "Books") }) {
			t.Fatal("Books before the settings were read")
		}
		failing = false
		f.advance(3 * time.Second)
		f.h.Settle()
		if !slices.ContainsFunc(label(), func(s string) bool { return strings.HasPrefix(s, "Books") }) {
			t.Errorf("sidebar %q", label())
		}
	})
	t.Run("reads the settings again before showing a confirmation dialog", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateQueued, nil))
		reads := f.calls("getSettings")
		if reads == 0 {
			t.Fatal("no settings read")
		}
		// changed by another client ("downex settings --dir ...")
		s := f.d.Settings()
		books(s)
		f.d.SetSettings(s)
		f.d.Emit(statetest.Updated(clip(2)))
		f.h.Settle()
		f.h.Settle()
		if f.confirming() != 2 || f.calls("getSettings") <= reads || !hasBooks(f) {
			t.Errorf("confirming %d, reads %d→%d, Books %v", f.confirming(), reads, f.calls("getSettings"), hasBooks(f))
		}
	})
}

func TestAppStreamingMedia(t *testing.T) {
	t.Run("confirms a stream from the browser with the chosen quality", func(t *testing.T) {
		// The quality cards and the confirm request with videoTrack v2 are the add lane's
		// TestConfirmDialogMediaDownloads; the composition opens that dialog by itself.
		f := lanes(t, setup{}, formattest.MediaDownload(2, func(d *downexv1.Download) {
			d.State = state.StateAwaiting
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
			d.Dir = "/home/you/Downloads/Video"
		}))
		if f.confirming() != 2 || f.h.App.Dialogs.Kind() != views.ModalConfirm {
			t.Fatalf("confirming %d", f.confirming())
		}
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		if a := f.d.Args("confirm"); len(a) != 2 || a[0] != int64(2) || a[1].(*downexv1.ConfirmRequest).GetVideoTrack() == "" {
			t.Errorf("confirm %v", a)
		}
	})
	t.Run("adds a stream pasted into the window after listing its qualities", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateCompleted, nil))
		if !f.m.Paste("https://media.example.org/talk/master.m3u8") {
			t.Fatal("the paste was not taken")
		}
		f.h.Settle()
		if f.h.App.Dialogs.Kind() != views.ModalAdd {
			t.Fatalf("dialog %v", f.h.App.Dialogs.Kind())
		}
		if f.dialogName() != "Add video or stream" {
			t.Errorf("title %q", f.dialogName())
		}
		f.press("Find qualities")
		f.h.Settle()
		if f.calls("probeMedia") != 1 {
			t.Fatalf("calls %v", f.d.Calls())
		}
		f.press("Add stream")
		f.h.Settle()
		a := f.d.Args("addMedia")
		if len(a) != 1 || a[0].(*downexv1.AddMediaRequest).GetManifestUrl() != "https://media.example.org/talk/master.m3u8" || a[0].(*downexv1.AddMediaRequest).GetSource() != downexv1.AddSource_ADD_SOURCE_UI {
			t.Errorf("addMedia %v", a)
		}
		f.advance(time.Second)
		if f.h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stays")
		}
		if _, ok := f.ds().Get(2); !ok {
			t.Error("the new stream is not listed at once")
		}
	})
	t.Run("tells why an encrypted stream cannot be added", func(t *testing.T) {
		// The Video or stream mode and the "encrypted or DRM-protected" alert are the add
		// lane's (TestAddDialogStreams); here Ctrl+N opens it with nothing to add.
		f := lanes(t, setup{})
		f.key(kit.KeyN, kit.ModCtrl)
		if f.h.App.Dialogs.Kind() != views.ModalAdd || f.calls("addMedia") != 0 {
			t.Errorf("dialog %v, calls %v", f.h.App.Dialogs.Kind(), f.d.Calls())
		}
	})
}

func TestAppVideoPages(t *testing.T) {
	t.Run("confirms a video from the browser in another MP4 quality, keeping its .mp4 name", func(t *testing.T) {
		// The video card, the four MP4 qualities and the 126 MB size are the add lane's
		// TestConfirmDialogVideoPages; here the dialog opens by itself and Start download
		// reaches downexd for that download.
		f := lanes(t, setup{}, formattest.SiteDownload(3, func(d *downexv1.Download) {
			d.State = state.StateAwaiting
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
			d.Dir = "/home/you/Downloads/Video"
		}))
		if f.confirming() != 3 {
			t.Fatalf("confirming %d", f.confirming())
		}
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		if a := f.d.Args("confirm"); len(a) != 2 || a[0] != int64(3) {
			t.Errorf("confirm %v", a)
		}
	})
	t.Run("adds a video page from the Add dialog after listing its qualities", func(t *testing.T) {
		// The site_resolving retry and the "No cookies sent" card are the add lane's
		// TestAddDialogVideoPages; here Ctrl+N opens the Add dialog over the composition.
		f := lanes(t, setup{})
		f.key(kit.KeyN, kit.ModCtrl)
		if f.h.App.Dialogs.Kind() != views.ModalAdd {
			t.Errorf("dialog %v", f.h.App.Dialogs.Kind())
		}
	})
	t.Run("keeps the first download of yt-dlp and Deno and its failure out of sight", func(t *testing.T) {
		f := lanes(t, setup{daemon: func(d *statetest.Daemon) { d.SetTools(statetest.Tools(nil)) }}, dl(1, state.StateCompleted, nil))
		installing := statetest.Tools(func(s *downexv1.ToolsStatus) {
			s.Ready = false
			for _, x := range s.Tools {
				x.State = downexv1.ToolState_TOOL_STATE_INSTALLING
				x.Version = ""
				x.BytesDone = 10 * MiB
				x.BytesTotal = 40 * MiB
			}
		})
		f.d.Emit(statetest.ToolsEvent(installing))
		f.h.Settle()
		if f.m.Shell.ToolsShown() {
			t.Fatal("a tools toast")
		}
		failed := statetest.Tools(func(s *downexv1.ToolsStatus) {
			s.Ready = false
			for _, x := range s.Tools {
				x.State = downexv1.ToolState_TOOL_STATE_FAILED
				x.Version = ""
				x.Error = "could not download " + x.GetName() + " (network)"
			}
		})
		f.d.Emit(statetest.ToolsEvent(failed))
		f.h.Settle()
		if _, ok := f.notice("Downex could not set up yt-dlp/Deno"); ok {
			t.Error("a notice about yt-dlp and Deno")
		}
	})
	t.Run("shows no toast while ready copies are updated", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateCompleted, nil))
		f.d.Emit(statetest.ToolsEvent(statetest.Tools(func(s *downexv1.ToolsStatus) {
			for _, x := range s.Tools {
				x.State = downexv1.ToolState_TOOL_STATE_INSTALLING
				x.BytesTotal = 40 * MiB
			}
		})))
		f.d.Emit(statetest.ToolsEvent(statetest.Tools(func(s *downexv1.ToolsStatus) {
			for _, x := range s.Tools {
				x.State = downexv1.ToolState_TOOL_STATE_FAILED
			}
		})))
		f.h.Settle()
		if f.m.Shell.ToolsShown() {
			t.Error("a tools toast")
		}
		if _, ok := f.notice("Downex could not set up yt-dlp/Deno"); ok {
			t.Error("a failure notice")
		}
	})
}

func TestAppProgressWindows(t *testing.T) {
	addOne := func(f *fixture, urls string) {
		f.key(kit.KeyN, kit.ModCtrl)
		if f.h.App.Dialogs.Kind() != views.ModalAdd {
			f.t.Fatalf("dialog %v", f.h.App.Dialogs.Kind())
		}
		f.h.App.Dialogs.Dialog().(views.Modal).DismissModal()
		f.h.Settle()
		f.h.App.Open.Add(views.AddRequest{URLs: urls})
		f.h.Settle()
	}
	t.Run("opens the progress window of an unfinished download from the list, whatever the setting", func(t *testing.T) {
		f := lanes(t, setup{settings: func(s *downexv1.Settings) { s.ShowProgressWindow = false }},
			dl(1, state.StateDownloading, func(d *downexv1.Download) { d.SpeedBps = MiB }),
			dl(2, state.StatePaused, nil),
			dl(3, state.StateCompleted, func(d *downexv1.Download) { d.Received = 10 * MiB }))
		f.h.App.Act.OpenDownload(1)
		f.sel(2)
		f.h.App.Act.OpenDownload(2)
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("opened %v", got)
		}
		f.sel(1)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Show progress")
		if got := f.fp.progress(); !slices.Equal(got, []int64{1, 2, 1}) {
			t.Fatalf("opened %v", got)
		}
		// A completed download opens its file instead, and has no progress to show.
		f.sel(3)
		f.menu()
		for _, it := range f.h.App.Commands.MenuItems() {
			if it.ID == "progress" {
				t.Error("Show progress for a completed download")
			}
		}
		f.closeMenus()
		f.h.App.Act.OpenDownload(3)
		f.h.Settle()
		if !slices.Contains(f.fp.Calls, "OpenFile 3") || !slices.Equal(f.fp.progress(), []int64{1, 2, 1}) {
			t.Errorf("calls %v opened %v", f.fp.Calls, f.fp.progress())
		}
	})
	t.Run("opens it for a download added to start now, when the setting is on", func(t *testing.T) {
		f := lanes(t, setup{})
		addOne(f, "https://example.com/one.zip")
		f.press("Add download")
		f.h.Settle()
		if got := f.fp.progress(); len(got) != 1 {
			t.Fatalf("opened %v", got)
		}
		// a list of addresses opens none (Add paused is the add lane's More options)
		f.advance(time.Second)
		addOne(f, "https://example.com/three.zip\nhttps://example.com/four.zip")
		f.press("Add 2 downloads")
		f.h.Settle()
		if f.calls("add") != 3 || len(f.fp.progress()) != 1 {
			t.Errorf("adds %d, opened %v", f.calls("add"), f.fp.progress())
		}
	})
	// App.test.ts: opens it after "Start download" in the confirmation dialog, unless it is turned off
	t.Run("opens it after \"Start download\" in the confirmation dialog, unless it is turned off", func(t *testing.T) {
		waiting := func(id int64) *downexv1.Download {
			return dl(id, state.StateAwaiting, func(d *downexv1.Download) {
				d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
				d.Size = -1
				d.CreatedAt = formattest.Millis(id)
			})
		}
		f := lanes(t, setup{}, waiting(1), waiting(2))
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1}) {
			t.Fatalf("opened %v", got)
		}
		f.advance(time.Second)
		s := f.d.Settings()
		s.ShowProgressWindow = false
		f.d.SetSettings(s)
		reads := f.calls("getSettings")
		f.m.WindowFocused()
		f.h.Settle()
		if f.calls("getSettings") <= reads {
			t.Fatal("the window's focus did not read the settings")
		}
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		if f.calls("confirm") != 2 || !slices.Equal(f.fp.progress(), []int64{1}) {
			t.Errorf("confirms %d, opened %v", f.calls("confirm"), f.fp.progress())
		}
	})
	t.Run("opens none for a download that waits in the queue (max_concurrent_downloads reached)", func(t *testing.T) {
		// Like downexd with every slot taken: what it accepts is queued at position 1 (the
		// fake's answer keeps the download's queue position).
		f := lanes(t, setup{}, dl(1, state.StateAwaiting, func(d *downexv1.Download) {
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
			d.Size = -1
			d.QueuePosition = 1
		}))
		f.pastGuard()
		f.press("Start download")
		f.h.Settle()
		f.advance(time.Second)
		if f.calls("confirm") != 1 || f.h.App.Dialogs.DialogOpen() {
			t.Fatalf("calls %v, dialog %v", f.d.Calls(), f.h.App.Dialogs.Kind())
		}
		if got := f.fp.progress(); len(got) != 0 {
			t.Errorf("opened %v", got)
		}
		// the Add half (api.add answering queued at position 1) is the same rule:
		// progressIfStarts takes StartsNow of the answer
		if views.StartsNow(dl(9, state.StateQueued, func(d *downexv1.Download) { d.QueuePosition = 1 })) {
			t.Error("StartsNow takes a download queued behind the limit as starting")
		}
	})
	t.Run("opens it after resuming one download from the main window (toolbar, Space, row action), when the setting is on", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StatePaused, func(d *downexv1.Download) { d.Received = MiB }))
		f.sel(1)
		f.h.Click(f.m.Shell.Header().Resume().Base())
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1}) {
			t.Fatalf("opened %v", got)
		}
		f.h.App.Main.Downloads.Apply([]*downexv1.Event{statetest.Updated(f.d.Item(1))})
		f.h.Settle()
		// Space pauses (no window), then resumes (a window)
		f.h.Window.Focus(f.m.Downloads.Grid().FocusTarget())
		f.key(kit.KeySpace, 0)
		f.h.App.Main.Downloads.Apply([]*downexv1.Event{statetest.Updated(f.d.Item(1))})
		f.h.Settle()
		f.key(kit.KeySpace, 0)
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1, 1}) {
			t.Fatalf("opened %v", got)
		}
		f.h.App.Main.Downloads.Apply([]*downexv1.Event{statetest.Updated(f.d.Item(1))})
		f.h.Click(f.m.Shell.Header().Pause().Base())
		f.h.App.Main.Downloads.Apply([]*downexv1.Event{statetest.Updated(f.d.Item(1))})
		f.h.Settle()
		// the row's Resume action (DownloadRow's hover action: App.svelte's onRowAction)
		f.h.App.Act.Resume([]int64{1})
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1, 1, 1}) {
			t.Fatalf("opened %v", got)
		}
	})
	t.Run("opens none after resuming several downloads, Resume all, a resume that waits in the queue, or with the setting off", func(t *testing.T) {
		f := lanes(t, setup{},
			// downexd queues download 1 behind max_concurrent_downloads when it is resumed
			dl(1, state.StatePaused, func(d *downexv1.Download) { d.Received = MiB; d.QueuePosition = 2 }),
			dl(2, state.StatePaused, func(d *downexv1.Download) { d.Received = MiB; d.CreatedAt = formattest.Millis(1) }))
		// several selected
		f.sel(1, 2)
		f.h.Click(f.m.Shell.Header().Resume().Base())
		f.h.Settle()
		if f.calls("resume") != 2 || len(f.fp.progress()) != 0 {
			t.Fatalf("resumes %d, opened %v", f.calls("resume"), f.fp.progress())
		}
		// Resume all
		f.h.App.Act.ResumeAll()
		f.h.Settle()
		if f.calls("resumeAll") != 1 || len(f.fp.progress()) != 0 {
			t.Fatalf("resumeAll %d, opened %v", f.calls("resumeAll"), f.fp.progress())
		}
		// one download that downexd queues behind max_concurrent_downloads
		f.h.App.Act.Pause([]int64{1, 2})
		f.h.Settle()
		f.h.App.Act.Resume([]int64{1})
		f.h.Settle()
		if f.calls("resume") != 3 || len(f.fp.progress()) != 0 {
			t.Fatalf("resumes %d, opened %v", f.calls("resume"), f.fp.progress())
		}
		// the setting off
		s := f.d.Settings()
		s.ShowProgressWindow = false
		f.d.SetSettings(s)
		reads := f.calls("getSettings")
		f.m.WindowFocused()
		f.h.Settle()
		if f.calls("getSettings") <= reads {
			t.Fatal("the window's focus did not read the settings")
		}
		f.h.App.Act.Resume([]int64{2})
		f.h.Settle()
		if f.calls("resume") != 4 || len(f.fp.progress()) != 0 {
			t.Errorf("resumes %d, opened %v", f.calls("resume"), f.fp.progress())
		}
	})
	t.Run("opens it after restarting one download, and after downloading a completed one again", func(t *testing.T) {
		f := lanes(t, setup{},
			dl(1, state.StateFailed, func(d *downexv1.Download) { d.Error = "HTTP 500"; d.ErrorCode = "http_status"; d.Received = MiB }),
			dl(2, state.StateCompleted, func(d *downexv1.Download) {
				d.Filename = "notes.pdf"
				d.Received = 10 * MiB
				d.CreatedAt = formattest.Millis(1)
			}))
		f.sel(1)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Restart…")
		f.h.Settle()
		r, ok := f.h.App.Dialogs.Dialog().(*dialogs.Restart)
		if !ok || f.dialogName() != "Restart download?" {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		r.Submit()
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1}) {
			t.Fatalf("opened %v", got)
		}
		f.advance(time.Second)
		f.sel(2)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Download again…")
		f.h.Settle()
		r, ok = f.h.App.Dialogs.Dialog().(*dialogs.Restart)
		if !ok || f.dialogName() != "Download again?" {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		f.radio("Replace the current file")
		r.Submit()
		f.h.Settle()
		if got := f.fp.progress(); !slices.Equal(got, []int64{1, 2}) {
			t.Fatalf("opened %v", got)
		}
		if a := f.d.Args("resume"); len(a) != 2 || a[0] != int64(2) || a[1].(*downexv1.ResumeRequest).GetExistingFile() != downexv1.ExistingFile_EXISTING_FILE_REPLACE || !a[1].(*downexv1.ResumeRequest).GetRestart() {
			t.Errorf("resume %v", a)
		}
	})
	t.Run("reports a progress window that cannot be opened", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateDownloading, nil))
		f.fp.openErr = statetest.Err(13, "cannot open the progress window: no such file")
		f.h.App.Act.OpenDownload(1)
		f.h.Settle()
		if !f.hasToast("Could not open the progress window of") || !f.hasToast("no such file") {
			t.Errorf("toasts %q", f.toasts())
		}
	})
}

func TestAppDownloadAgain(t *testing.T) {
	t.Run("names the menu item after the selection and offers it for completed downloads", func(t *testing.T) {
		f := lanes(t, setup{},
			dl(1, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "notes.pdf"; d.Received = 10 * MiB }),
			dl(2, state.StatePaused, func(d *downexv1.Download) { d.CreatedAt = formattest.Millis(1) }))
		f.sel(1)
		labels := func() map[string]bool {
			out := map[string]bool{}
			for _, it := range f.h.App.Commands.MenuItems() {
				if !it.Disabled {
					out[it.Label] = true
				}
			}
			return out
		}
		if l := labels(); !l["Download again…"] || l["Restart…"] {
			t.Errorf("menu %v", l)
		}
		// a mixed selection: Restart… (the dialog asks only about the completed one)
		f.sel(1, 2)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Restart…")
		f.h.Settle()
		r, ok := f.h.App.Dialogs.Dialog().(*dialogs.Restart)
		if !ok || f.dialogName() != "Restart downloads?" || r.Choices() == nil || r.Choices().Semantics.Name != "The current file of “\u2068notes.pdf\u2069”" {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		r.Submit()
		f.h.Settle()
		if f.calls("resume") != 2 || len(f.fp.progress()) != 0 {
			t.Errorf("resumes %d, opened %v", f.calls("resume"), f.fp.progress())
		}
	})
	t.Run("skips the choice when the file is gone, and offers it in the details pane and the palette", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "notes.pdf"; d.Received = 10 * MiB }))
		f.fp.Gone = map[int64]bool{1: true}
		f.sel(1)
		f.press("Download again…") // the details pane's
		f.h.Settle()
		r, ok := f.h.App.Dialogs.Dialog().(*dialogs.Restart)
		if !ok || f.dialogName() != "Download again?" || r.Choices() != nil || !strings.Contains(f.dialogDescription(), "is no longer in its folder") {
			t.Fatalf("dialog %T %q choices %v gone %v", f.h.App.Dialogs.Dialog(), f.dialogName(), r.Choices() != nil, r.GoneNote() != nil)
		}
		if !slices.Contains(f.fp.Calls, "FileExists 1") {
			t.Errorf("calls %v", f.fp.Calls)
		}
		r.Cancel()
		f.advance(time.Second)
		f.fp.Gone = nil
		f.key(kit.KeyK, kit.ModCtrl)
		p := f.pal
		if p == nil || !f.h.App.Dialogs.PaletteOpen() {
			t.Fatal("no palette")
		}
		p.SetQuery("download again")
		f.h.Settle()
		if c := p.Current(); c == nil || c.Command == nil || c.Command.Label != "Download again…" {
			t.Fatalf("first %+v", c)
		}
		p.RunCurrent(false)
		f.h.Settle()
		r, ok = f.h.App.Dialogs.Dialog().(*dialogs.Restart)
		if !ok || r.Choices() == nil || len(r.Choices().Options) != 2 {
			t.Errorf("dialog %T", f.h.App.Dialogs.Dialog())
		}
	})
}

func TestAppMenuBar(t *testing.T) {
	t.Run("opens Settings from Settings…, unless another dialog is open", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StatePaused, nil))
		f.key(kit.KeyN, kit.ModCtrl)
		add := f.h.App.Dialogs.Dialog()
		f.m.MenuCommand("settings")
		f.h.Settle()
		if f.h.App.Dialogs.Kind() != views.ModalAdd || f.h.App.Dialogs.Dialog() != add {
			t.Fatalf("dialog %v", f.h.App.Dialogs.Kind())
		}
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		f.m.MenuCommand("settings")
		f.h.Settle()
		if f.h.App.Dialogs.Kind() != views.ModalSettings {
			t.Errorf("dialog %v", f.h.App.Dialogs.Kind())
		}
	})
}

func TestAppCommandPalette(t *testing.T) {
	t.Run("opens with Ctrl+K, finds a command by typing and runs it", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StatePaused, nil), dl(2, state.StateCompleted, nil))
		f.key(kit.KeyK, kit.ModCtrl)
		p := f.pal
		if p == nil || !f.h.App.Dialogs.PaletteOpen() || f.h.Window.Focused() != p.Field().Base() {
			t.Fatalf("palette %v, focus %v", p, f.h.Window.Focused())
		}
		p.SetQuery("status paused")
		f.h.Settle()
		if c := p.Current(); c == nil || c.Command == nil || c.Command.Label != "Status: Paused" {
			t.Fatalf("first %+v", c)
		}
		f.h.Key(kit.KeyEnter, 0)
		f.advance(time.Second)
		if f.h.App.Dialogs.PaletteOpen() {
			t.Fatal("the palette stays")
		}
		if got := views.SelectedIDs(f.ds().Visible()); !slices.Equal(got, []int64{1}) {
			t.Errorf("shown %v", got)
		}
		// Escape closes it without running anything
		f.key(kit.KeyK, kit.ModCtrl)
		f.h.Key(kit.KeyEscape, 0)
		f.advance(time.Second)
		if f.h.App.Dialogs.PaletteOpen() {
			t.Error("Escape left it open")
		}
	})
}

func TestAppArrivals(t *testing.T) {
	t.Run("says so in a toast that can show the download", func(t *testing.T) {
		f := lanes(t, setup{}, dl(1, state.StateQueued, nil))
		f.d.Emit(statetest.Updated(dl(7, state.StateQueued, func(d *downexv1.Download) {
			d.Filename = "handed.zip"
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		})))
		f.h.Settle()
		n, ok := f.notice("Download added from the browser")
		if !ok || !strings.Contains(n.Detail, "handed.zip") {
			t.Fatalf("notice %+v", n)
		}
		f.press("Show")
		if got := views.SelectedIDs(f.ds().Selected()); !slices.Equal(got, []int64{7}) {
			t.Errorf("selected %v", got)
		}
		if _, ok := f.notice("Download added from the browser"); ok {
			t.Error("the notice stays")
		}
	})
	t.Run("pauses a download from its row hover action", func(t *testing.T) {
		// The hover action's button is the downloads lane's (TestDownloadTable); it calls
		// App.Act.Pause with its download, which pauses that one.
		f := lanes(t, setup{}, dl(1, state.StateDownloading, func(d *downexv1.Download) { d.SpeedBps = MiB; d.Received = MiB }))
		f.h.App.Act.Pause([]int64{1})
		f.h.Settle()
		if a := f.d.Args("pause"); len(a) != 1 || a[0] != int64(1) {
			t.Errorf("pause %v", a)
		}
	})
}

// ---- App.m6.test.ts ----------------------------------------------------------------------------

func m6(t *testing.T) *fixture {
	t.Helper()
	return lanes(t, setup{daemon: func(d *statetest.Daemon) {
		d.SetQueue(statetest.Queue(1, func(q *downexv1.Queue) { q.DownloadIds = []int64{1, 2, 4} }))
		d.SetQueue(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night"; q.DownloadIds = []int64{3} }))
	}},
		dl(1, state.StateQueued, func(d *downexv1.Download) { d.Filename = "one.iso" }),
		dl(2, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "two.iso" }),
		dl(3, state.StateQueued, func(d *downexv1.Download) { d.Filename = "three.iso"; d.QueueId = 2 }),
		dl(4, state.StateFailed, func(d *downexv1.Download) {
			d.Filename = "private.zip"
			d.Url = "https://private.example.net/files/private.zip"
			d.ErrorCode = "auth_required"
			d.Error = "The server asks for a user name and password"
		}))
}

func queueItem(f *fixture, name string) *kit.Element {
	_, qs, _ := f.m.Shell.Sidebar().Items()
	for _, q := range qs {
		if strings.HasPrefix(q.Semantics.Name, name) {
			return q
		}
	}
	f.t.Fatalf("no queue %q in the sidebar", name)
	return nil
}

func TestAppQueuesM6(t *testing.T) {
	t.Run("lists the queues in the sidebar and filters the list by queue", func(t *testing.T) {
		f := m6(t)
		if f.calls("listQueues") == 0 {
			t.Fatal("queues not read")
		}
		main, night := queueItem(f, "Main queue"), queueItem(f, "Night")
		// Running state and what is left to do (completed downloads are not counted).
		if s := main.Semantics.Name; !strings.Contains(s, "Running") || !strings.Contains(s, "2") {
			t.Errorf("main %q", s)
		}
		if s := night.Semantics.Name; !strings.Contains(s, "Stopped") || !strings.Contains(s, "1") {
			t.Errorf("night %q", s)
		}
		f.h.Click(night)
		if n, _ := f.h.Window.Semantics().Node(night.NodeID()); n.Toggle != kit.ToggleOn {
			t.Error("Night is not pressed")
		}
		if got := views.SelectedIDs(f.ds().Visible()); !slices.Equal(got, []int64{3}) {
			t.Errorf("Night shows %v", got)
		}
		f.h.Click(night)
		if _, ok := slices.BinarySearch(views.SelectedIDs(f.ds().Visible()), 1); !ok && !slices.Contains(views.SelectedIDs(f.ds().Visible()), 1) {
			t.Error("the queue filter stays")
		}
		// A queue event updates the sidebar.
		f.d.Emit(statetest.QueueUpdated(statetest.Queue(2, func(q *downexv1.Queue) { q.Name = "Night"; q.Running = true; q.DownloadIds = []int64{3} })))
		f.h.Settle()
		if s := queueItem(f, "Night").Semantics.Name; !strings.Contains(s, "Running") {
			t.Errorf("night %q", s)
		}
	})
	t.Run("moves downloads to another queue from the context menu", func(t *testing.T) {
		f := m6(t)
		f.sel(1)
		f.menu()
		clickMenuItem(t, f.h, f.m.Menu(), "Move to queue…")
		mv, ok := f.h.App.Dialogs.Dialog().(*dialogs.Move)
		if !ok || mv.Choice() != 2 {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		f.press("Move")
		f.h.Settle()
		a := f.d.Args("moveDownloads")
		if len(a) != 3 || !slices.Equal(a[0].([]int64), []int64{1}) || a[1] != int64(2) || a[2] != int32(-1) {
			t.Fatalf("move %v", a)
		}
		f.advance(time.Second)
		if f.h.App.Dialogs.DialogOpen() {
			t.Error("the dialog stays")
		}
		// The queue's name is bidi-isolated in the sentence.
		if !f.hasToast("Moved to \u2068Night\u2069") {
			t.Errorf("toasts %q", f.toasts())
		}
		// The download is counted in its new queue at once.
		if s := queueItem(f, "Night").Semantics.Name; !strings.Contains(s, "2") {
			t.Errorf("night %q", s)
		}
	})
	t.Run("opens the queue manager from the sidebar", func(t *testing.T) {
		f := m6(t)
		f.h.Click(f.m.Shell.Sidebar().Manage().Base())
		if f.h.App.Dialogs.Kind() != views.ModalQueues {
			t.Fatalf("dialog %v", f.h.App.Dialogs.Kind())
		}
		// the heading and the order list ("one.iso", "private.zip") are the queues lane's
		// TestQueueDialog
	})
	t.Run("counts down a post-queue action and cancels it", func(t *testing.T) {
		f := m6(t)
		now := f.h.App.Clock.Now().UnixMilli()
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SHUTDOWN, now+60_000, false))
		f.h.Settle()
		b := f.m.Shell.PostActionBanner()
		if b == nil || !strings.HasPrefix(b.Text(), "Shutting down in") || !strings.Contains(b.Detail(), "Night finished its downloads") {
			t.Fatalf("banner %v", b)
		}
		if f.h.Window.Focused() != b.CancelButton().Base() {
			t.Error("Cancel has not the focus")
		}
		f.h.Click(b.CancelButton().Base())
		f.h.Settle()
		if a := f.d.Args("cancelPostAction"); len(a) != 1 || a[0] != int64(2) {
			t.Fatalf("cancel %v", a)
		}
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SHUTDOWN, 0, true))
		f.h.Settle()
		if f.m.Shell.PostActionBanner() != nil {
			t.Fatal("the banner stays")
		}
		// The daemon's own cancellation (another window, the tray) removes it too.
		f.d.Emit(statetest.PostAction(1, downexv1.PostAction_POST_ACTION_SLEEP, now+30_000, false))
		f.h.Settle()
		if b := f.m.Shell.PostActionBanner(); b == nil || !strings.HasPrefix(b.Text(), "Going to sleep in") {
			t.Fatalf("banner %v", b)
		}
		f.d.Emit(statetest.PostAction(1, downexv1.PostAction_POST_ACTION_SLEEP, 0, true))
		f.h.Settle()
		if f.m.Shell.PostActionBanner() != nil {
			t.Error("the banner stays")
		}
	})
	t.Run("keeps the countdown in reach of the keyboard while a dialog is open", func(t *testing.T) {
		f := m6(t)
		f.key(kit.KeyComma, kit.ModCtrl)
		dlg, ok := f.h.App.Dialogs.Dialog().(*settings.Dialog)
		if !ok {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		f.advance(time.Second)
		now := f.h.App.Clock.Now().UnixMilli()
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SHUTDOWN, now+60_000, false))
		f.h.Settle()
		b := f.m.Shell.PostActionBanner()
		if b == nil || f.h.Window.Focused() != b.CancelButton().Base() {
			t.Fatalf("banner %v, focus %v", b, f.h.Window.Focused())
		}
		// Tab goes on into the dialog, Shift+Tab from its first control back to the countdown.
		f.h.Key(kit.KeyTab, 0)
		if fo := f.h.Window.Focused(); fo == nil || !dlg.Contains(fo) {
			t.Fatalf("Tab went to %v", fo)
		}
		f.h.Key(kit.KeyTab, kit.ModShift)
		if f.h.Window.Focused() != b.CancelButton().Base() {
			t.Errorf("Shift+Tab went to %v", f.h.Window.Focused())
		}
		// Escape on the countdown cancels it, not the dialog.
		f.h.Key(kit.KeyEscape, 0)
		f.h.Settle()
		if a := f.d.Args("cancelPostAction"); len(a) == 0 {
			t.Fatal("Escape did not cancel")
		}
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SHUTDOWN, 0, true))
		f.h.Settle()
		if f.m.Shell.PostActionBanner() != nil || f.h.App.Dialogs.Dialog() != views.Modal(dlg) {
			t.Fatalf("banner %v, dialog %T", f.m.Shell.PostActionBanner(), f.h.App.Dialogs.Dialog())
		}
		// The keyboard goes back into the dialog.
		if fo := f.h.Window.Focused(); fo == nil || !dlg.Contains(fo) {
			t.Errorf("focus %v", fo)
		}
	})
	t.Run("leaves the focus on the countdown when a confirmation dialog opens by itself", func(t *testing.T) {
		f := m6(t)
		now := f.h.App.Clock.Now().UnixMilli()
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SLEEP, now+60_000, false))
		f.h.Settle()
		b := f.m.Shell.PostActionBanner()
		if b == nil || f.h.Window.Focused() != b.CancelButton().Base() {
			t.Fatal("Cancel has not the focus")
		}
		f.d.Emit(statetest.Updated(dl(9, state.StateAwaiting, func(d *downexv1.Download) {
			d.Filename = "handoff.zip"
			d.Source = downexv1.AddSource_ADD_SOURCE_BROWSER
		})))
		f.h.Settle()
		f.h.Settle()
		if f.confirming() != 9 {
			t.Fatal("no confirmation dialog")
		}
		if f.h.Window.Focused() != b.CancelButton().Base() {
			t.Errorf("focus %v", f.h.Window.Focused())
		}
	})
	t.Run("drops a countdown that already ended when it is cancelled", func(t *testing.T) {
		f := m6(t)
		f.d.Before = func(name string, _ ...any) error {
			if name == "cancelPostAction" {
				return statetest.Err(9, "queue 2 has no post-queue action pending")
			}
			return nil
		}
		now := f.h.App.Clock.Now().UnixMilli()
		f.d.Emit(statetest.PostAction(2, downexv1.PostAction_POST_ACTION_SLEEP, now+60_000, false))
		f.h.Settle()
		b := f.m.Shell.PostActionBanner()
		if b == nil {
			t.Fatal("no banner")
		}
		f.h.Click(b.CancelButton().Base())
		f.h.Settle()
		if f.m.Shell.PostActionBanner() != nil {
			t.Error("the banner stays")
		}
		if !f.hasToast("nothing left to cancel") {
			t.Errorf("toasts %q", f.toasts())
		}
	})
	// App.m6.test.ts: confirms "Download later" as the queue hint says
	t.Run("confirms \"Download later\" as the queue hint says", func(t *testing.T) {
		// The Queue select, its hint and the move-then-confirm order are the add lane's
		// TestConfirmDialogQueues; the composition hands the dialog the queues it offers.
		f := m6(t)
		if got := len(f.h.App.QueueChoices()); got != 2 {
			t.Errorf("queue choices %d", got)
		}
	})
	t.Run("offers a site login for a server that asked for credentials", func(t *testing.T) {
		f := m6(t)
		f.sel(4)
		f.press("Add site login…")
		f.h.Settle()
		sd, ok := f.h.App.Dialogs.Dialog().(*settings.Dialog)
		if !ok || sd.Page() != validate.PageNetwork {
			t.Fatalf("dialog %T", f.h.App.Dialogs.Dialog())
		}
		if !f.h.Calls.Has("Settings") && views.LoginPatternFor(f.d.Item(4)) != "https://private.example.net/*" {
			t.Errorf("pattern %q", views.LoginPatternFor(f.d.Item(4)))
		}
	})
}
