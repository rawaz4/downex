package app

import (
	"slices"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/queues"
	"downex/ui/core/text"
	"downex/ui/core/validate"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// fill gives every field of App.Open and App.Act App.svelte's logic.
func (m *Main) fill() {
	a := m.App
	a.Open = views.Openers{
		Add:         m.openAdd,
		Confirm:     func(r views.ConfirmRequest) { m.requestConfirm(r.ID, r.Requested); m.refresh() },
		Remove:      m.requestRemove,
		Restart:     m.requestRestart,
		Open:        func(r views.OpenRequest) { m.show(m.screen(func(s Screens) views.Modal { return call(s.Open, a, r) })) },
		MoveToQueue: m.openMove,
		Settings:    m.openSettings,
		Queues: func(r views.QueuesRequest) {
			m.closeMenu(false)
			m.show(m.screen(func(s Screens) views.Modal { return call(s.Queues, a, r) }))
		},
		Palette: m.openPalette,
		About:   func() { m.openSettings(views.SettingsRequest{Page: validate.PageAbout}) },
	}
	a.Act = views.Actions{
		Pause:             m.pause,
		Resume:            m.resume,
		TogglePauseResume: m.togglePauseResume,
		PauseAll:          m.pauseAll,
		ResumeAll:         m.resumeAll,
		OpenDownload:      m.openDownload,
		OpenFolder:        m.openFolder,
		OpenProgress: func(id int64) {
			if a.Stale() {
				a.Unavailable()
				return
			}
			m.openProgress(id)
		},
		CopyURLs:     m.copyURLs,
		ShowDownload: m.showDownload,
		ClearFilters: m.clearFilters,
		SelectAll: func() {
			a.Main.Downloads.SelectAll()
			m.focusList()
		},
		FocusSearch:      func() { m.Shell.TitleBar().FocusSearch() },
		FocusList:        m.focusList,
		ToggleDetails:    func() { a.Details.Toggle() },
		SetGlobalLimit:   m.setGlobalLimit,
		SetWhenDone:      m.setWhenDone,
		OpenLimitMenu:    func() { m.Shell.StatusBarSlot().OpenLimitMenu() },
		OpenWhenDoneMenu: func() { m.Shell.StatusBarSlot().OpenWhenDoneMenu() },
		SetDownloadLimit: m.setDownloadLimit,
		AddSiteLogin:     m.addSiteLogin,
		CancelPostAction: func(q int64) { m.cancelPostAction(q, nil) },
		OpenMenu:         m.openMenu,
		CloseMenu:        func() { m.closeMenu(true) },
	}
}

// call runs a screen constructor when it is set.
func call[R any](f func(*views.App, R) views.Modal, a *views.App, r R) views.Modal {
	if f == nil {
		return nil
	}
	return f(a, r)
}

// screen makes a modal with the Screens.
func (m *Main) screen(f func(Screens) views.Modal) views.Modal { return f(m.screens) }

// show shows a modal (a nil one is a screen not built yet).
func (m *Main) show(md views.Modal) {
	if md == nil {
		return
	}
	m.App.Dialogs.Show(md)
}

// name is a download's display name, bidi-isolated for a sentence.
func (m *Main) name(id int64) string { return text.Isolate(m.App.Main.Downloads.NameOf(id)) }

// errText is a call error's message, bidi-isolated (toApiError(err).message).
func errText(err error) string { return text.Isolate(views.ErrorMessage(err)) }

// do runs f off the UI thread with a call context; then runs on the UI thread with nothing.
func (m *Main) do(f func(ctx contextT)) {
	a := m.App
	a.Go(func() {
		ctx, cancel := a.CallContext()
		defer cancel()
		f(ctx)
	})
}

// ---- openers ----------------------------------------------------------------------------------

func (m *Main) openAdd(r views.AddRequest) {
	m.closeMenu(false)
	if m.App.Stale() {
		m.App.Unavailable()
		return
	}
	m.show(m.screen(func(s Screens) views.Modal { return call(s.Add, m.App, r) }))
}

// openSettings opens the Settings dialog (on a page, with a field focused).
func (m *Main) openSettings(r views.SettingsRequest) {
	m.closeMenu(false)
	m.show(m.screen(func(s Screens) views.Modal { return call(s.Settings, m.App, r) }))
}

// openQueues opens the Queues dialog on the queue filter (openQueues(id = store.queue)).
func (m *Main) openQueues() {
	m.App.Open.Queues(views.QueuesRequest{Queue: m.App.Main.Downloads.QueueFilter()})
}

func (m *Main) openPalette() {
	m.closeMenu(false)
	if m.screens.Palette == nil {
		return
	}
	m.show(m.screens.Palette(m.App))
}

func (m *Main) openMove(r views.MoveRequest) {
	if len(r.IDs) == 0 {
		return
	}
	m.show(m.screen(func(s Screens) views.Modal { return call(s.Move, m.App, r) }))
}

// requestRemove is App.svelte's requestRemove: nothing for no ids, unavailable while stale.
func (m *Main) requestRemove(r views.RemoveRequest) {
	if len(r.IDs) == 0 {
		return
	}
	if m.App.Stale() {
		m.App.Unavailable()
		return
	}
	r.IDs = slices.Clone(r.IDs)
	m.show(m.screen(func(s Screens) views.Modal { return call(s.Remove, m.App, r) }))
}

// requestRestart is App.svelte's requestRestart: the restartable ones; for the completed ones
// the host is asked first whether their files are still there (a host that cannot tell is
// taken as "there"); the dialog shows only while no other came first.
func (m *Main) requestRestart(r views.RestartRequest) {
	a := m.App
	ds := a.Main.Downloads
	var targets, completed []int64
	for _, id := range r.IDs {
		d, ok := ds.Get(id)
		if !ok || !views.Restartable(d) {
			continue
		}
		targets = append(targets, id)
		if d.GetState() == state.StateCompleted {
			completed = append(completed, id)
		}
	}
	if len(targets) == 0 {
		return
	}
	if a.Stale() {
		a.Unavailable()
		return
	}
	reason := r.Reason
	if reason == views.RestartCompleted {
		reason = views.RestartManual
	}
	if len(completed) == len(targets) {
		reason = views.RestartCompleted
	}
	names := make([]string, len(completed))
	sites := make([]bool, len(completed))
	for i, id := range completed {
		names[i] = ds.NameOf(id)
		if d, ok := ds.Get(id); ok {
			sites[i] = d.GetKind() == downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA && isSite(d)
		}
	}
	m.do(func(ctx contextT) {
		again := make([]views.AgainFile, len(completed))
		for i, id := range completed {
			exists, err := a.Platform.FileExists(ctx, id)
			if err != nil {
				exists = true
			}
			again[i] = views.AgainFile{Name: names[i], Exists: exists, Site: sites[i]}
		}
		a.UI(func() {
			req := views.RestartRequest{IDs: targets, Reason: reason, Again: again}
			md := call(m.screens.Restart, a, req)
			if md != nil {
				a.Dialogs.ShowIfFree(md) // another dialog came first: this one is not shown
			}
		})
	})
}

// ---- actions ----------------------------------------------------------------------------------

func (m *Main) ids(ids []int64) []int64 {
	if ids == nil {
		return m.selectedIDs()
	}
	return ids
}

// report is App.svelte's report: a toast for a bulk action's failures.
func (m *Main) report(outcomes []state.Outcome, verb string) {
	if s := views.ReportText(outcomes, verb, m.App.Main.Downloads.NameOf); s != "" {
		m.App.Error(s)
	}
}

func (m *Main) pause(ids []int64) {
	ids = m.ids(ids)
	m.do(func(ctx contextT) {
		out := m.App.Main.Downloads.Pause(ctx, ids)
		m.App.UI(func() { m.report(out, "pause") })
	})
}

// resume resumes the plain ones and asks to restart those downexd cannot resume; one download
// resumed on its own gets its progress window when that is on and it starts now.
func (m *Main) resume(ids []int64) {
	ids = m.ids(ids)
	ds := m.App.Main.Downloads
	var restart, plain []int64
	for _, id := range ids {
		if d, ok := ds.Get(id); ok && views.NeedsRestart(d) {
			restart = append(restart, id)
		} else {
			plain = append(plain, id)
		}
	}
	if len(plain) > 0 {
		single := len(ids) == 1
		m.do(func(ctx contextT) {
			out := ds.Resume(ctx, plain, state.ResumeOptions{})
			m.App.UI(func() {
				m.report(out, "resume")
				if single && len(out) == 1 && out[0].OK() {
					m.showProgressIfStarted(out[0].ID)
				}
			})
		})
	}
	if len(restart) > 0 {
		m.requestRestart(views.RestartRequest{IDs: restart, Reason: views.RestartValidatorChanged})
	}
}

func (m *Main) togglePauseResume() {
	ab := m.App.Abilities()
	switch {
	case ab.CanPause:
		m.pause(nil)
	case ab.CanResume:
		m.resume(nil)
	}
}

func (m *Main) pauseAll() {
	m.do(func(ctx contextT) {
		n, err := m.App.Main.Downloads.PauseAll(ctx)
		switch {
		case err != nil:
			m.App.Error("Could not pause all downloads: " + errText(err))
		case n == 0:
			m.App.Toast("Nothing to pause")
		case n == 1:
			m.App.Success("1 download paused")
		default:
			m.App.Success(format.Count(float64(n)) + " downloads paused")
		}
	})
}

func (m *Main) resumeAll() {
	m.do(func(ctx contextT) {
		n, err := m.App.Main.Downloads.ResumeAll(ctx)
		switch {
		case err != nil:
			m.App.Error("Could not resume all downloads: " + errText(err))
		case n == 0:
			m.App.Toast("Nothing to resume")
		case n == 1:
			m.App.Success("1 download resumed")
		default:
			m.App.Success(format.Count(float64(n)) + " downloads resumed")
		}
	})
}

// openDownload is a row's Enter or double-click: the dialog of one awaiting confirmation, the
// progress window of an unfinished one, the file of a completed one (programs and names with
// hidden bidi controls are confirmed first: opening them can run code).
func (m *Main) openDownload(id int64) {
	a := m.App
	d, ok := a.Main.Downloads.Get(id)
	if !ok {
		return
	}
	switch {
	case views.IsAwaiting(d.GetState()):
		if a.Stale() {
			a.Unavailable()
			return
		}
		m.requestConfirm(id, true)
		m.refresh()
		return
	case d.GetState() != state.StateCompleted:
		if a.Stale() {
			a.Unavailable()
			return
		}
		m.openProgress(id)
		return
	}
	raw := categories.RawName(d)
	if categories.IsExecutableName(raw) || text.HasBidiControls(raw) {
		a.Open.Open(views.OpenRequest{ID: id})
		return
	}
	m.openFile(id)
}

func (m *Main) openFile(id int64) {
	name := m.name(id)
	m.do(func(ctx contextT) {
		if err := m.App.Main.Downloads.OpenFile(ctx, id); err != nil {
			m.App.Error("Could not open " + name + ": " + errText(err))
		}
	})
}

func (m *Main) openFolder(id int64) {
	m.do(func(ctx contextT) {
		if err := m.App.Main.Downloads.OpenFolder(ctx, id); err != nil {
			m.App.Error("Could not open the folder: " + errText(err))
		}
	})
}

// openProgress opens a download's progress window (one per download, by the host).
func (m *Main) openProgress(id int64) {
	name := m.name(id)
	m.App.Go(func() {
		if err := m.App.Platform.OpenProgressWindow(id); err != nil {
			m.App.Error("Could not open the progress window of " + name + ": " + errText(err))
		}
	})
}

// showProgressIfStarted: after the user resumed or restarted exactly one download in this
// window, its progress window when that is on and downexd starts it now (DESKTOP-SPEC §7).
func (m *Main) showProgressIfStarted(id int64) {
	d, ok := m.App.Main.Downloads.Get(id)
	s, _ := m.settings()
	if ok && s.GetShowProgressWindow() && views.StartsNow(d) {
		m.openProgress(id)
	}
}

func (m *Main) copyURLs(ids []int64) {
	ids = m.ids(ids)
	m.do(func(ctx contextT) {
		n, err := m.App.Main.Downloads.CopyURLs(ctx, ids)
		switch {
		case err != nil:
			m.App.Error("Could not copy: " + errText(err))
		case n == 1:
			m.App.Success("URL copied")
		case n > 1:
			m.App.Success(format.Count(float64(n)) + " URLs copied")
		}
	})
}

// showDownload shows a download in the list: every filter cleared, selected, the list focused.
func (m *Main) showDownload(id int64) {
	ds := m.App.Main.Downloads
	if _, ok := ds.Get(id); !ok {
		return
	}
	m.clearFilters()
	ds.SelectOnly(id)
	m.focusList()
}

// clearFilters shows every download again (the list's empty state).
func (m *Main) clearFilters() {
	ds := m.App.Main.Downloads
	if ds.Search() != "" {
		ds.SetSearch("")
	}
	if ds.Category() != categories.All {
		ds.SetCategory(categories.All)
	}
	if ds.StatusFilter() != categories.AnyStatus {
		ds.SetStatusFilter(categories.AnyStatus)
	}
	if ds.QueueFilter() != 0 {
		ds.SetQueueFilter(0)
	}
}

func (m *Main) focusList() {
	if g := m.Shell.Grid(); g != nil {
		g.FocusList()
	}
}

// setGlobalLimit sets the global speed limit with a full settings round-trip; Custom… opens
// Settings → Downloads with the limit focused.
func (m *Main) setGlobalLimit(c views.LimitChoice) {
	a := m.App
	if c.Custom {
		m.openSettings(views.SettingsRequest{Page: validate.PageDownloads, FocusField: validate.FieldGlobalSpeedLimit})
		return
	}
	if a.Stale() {
		a.Unavailable()
		return
	}
	m.do(func(ctx contextT) {
		saved, err := host.EditSettings(ctx, a.Daemon, func(s *downexv1.Settings) error {
			s.GlobalSpeedLimit = c.BytesPerSecond
			return nil
		})
		if err != nil {
			a.Error("Could not change the global speed limit: " + errText(err))
			return
		}
		a.Main.SetSettings(saved)
		if l := saved.GetGlobalSpeedLimit(); l > 0 {
			a.Success("Global speed limit: " + format.Speed(float64(l)))
		} else {
			a.Success("Global speed limit: unlimited")
		}
	})
}

// setWhenDone sets the main queue's post-queue action; Other queues… opens Queues on the first
// other queue (else the main one).
func (m *Main) setWhenDone(c views.WhenDoneChoice) {
	a := m.App
	if c.Other {
		target := int64(queues.MainQueueID)
		for _, q := range a.QueueChoices() {
			if q.GetId() != queues.MainQueueID {
				target = q.GetId()
				break
			}
		}
		a.Open.Queues(views.QueuesRequest{Queue: target})
		return
	}
	if a.Stale() {
		a.Unavailable()
		return
	}
	m.do(func(ctx contextT) {
		if _, err := a.Main.Queues.SetPostAction(ctx, queues.MainQueueID, c.Action); err != nil {
			a.Error("Could not change what happens when the main queue is done: " + errText(err))
			return
		}
		a.Success(queues.WhenDoneText(c.Action) + " (main queue)")
	})
}

// setDownloadLimit is the details pane's speed limit of one download (SetDownloadOptions).
func (m *Main) setDownloadLimit(id, bps int64) {
	a := m.App
	if a.Stale() {
		a.Unavailable()
		return
	}
	name := m.name(id)
	m.do(func(ctx contextT) {
		limit := bps
		_, err := a.Daemon.SetDownloadOptions(ctx, &downexv1.SetDownloadOptionsRequest{Id: id, SpeedLimit: &limit})
		switch {
		case err != nil:
			a.Error("Could not change the speed limit of " + name + ": " + errText(err))
		case bps > 0:
			a.Success(name + ": limited to " + format.Speed(float64(bps)))
		default:
			a.Success(name + ": no speed limit")
		}
	})
}

// addSiteLogin opens Settings → Network with a new site login for the server that asked for
// credentials.
func (m *Main) addSiteLogin(id int64) {
	a := m.App
	if a.Stale() {
		a.Unavailable()
		return
	}
	d, ok := a.Main.Downloads.Get(id)
	if !ok {
		return
	}
	m.openSettings(views.SettingsRequest{Page: validate.PageNetwork, AddLogin: views.LoginPatternFor(d)})
}

// cancelPostAction cancels a queue's countdown; done hears the daemon's error (the banner
// shows it).
func (m *Main) cancelPostAction(q int64, done func(error)) {
	a := m.App
	m.do(func(ctx contextT) {
		cancelled, err := a.Main.Queues.CancelPostAction(ctx, q)
		a.UI(func() {
			if done != nil {
				done(err)
			}
		})
		switch {
		case err != nil:
		case cancelled:
			a.Success("Cancelled: nothing happens after the queue this time.")
		default:
			a.Toast("The countdown had already ended: nothing left to cancel.")
		}
	})
}

// ---- the context menu -------------------------------------------------------------------------

// openMenu opens the download context menu at a point for the selection (App.svelte's menu).
func (m *Main) openMenu(at kit.Point) {
	m.closeMenu(false)
	a := m.App
	owner := kit.Widget(m.root)
	if g := m.Shell.Grid(); g != nil {
		owner = g
	}
	menu, err := widgets.OpenMenu(owner, widgets.MenuOptions{
		Items: a.Commands.MenuItems(), Label: views.MenuLabel, At: at,
		OnSelect: func(id string) { a.Commands.RunMenuItem(id) },
		OnClose:  func() { m.closeMenu(true) },
	})
	if err != nil {
		a.Log.Warn("the download menu did not open", "err", err)
		return
	}
	m.menu = menu
	a.Dialogs.SetMenuOpen(true)
}

// closeMenu closes the context menu; focus goes to the list when asked (closeMenu).
func (m *Main) closeMenu(focus bool) {
	if m.menu == nil {
		return
	}
	menu := m.menu
	m.menu = nil
	menu.Close()
	m.App.Dialogs.SetMenuOpen(false)
	if focus {
		m.focusList()
	}
}

// MenuOpen reports the context menu (tests).
func (m *Main) MenuOpen() bool { return m.menu != nil }

// Menu is the open context menu, or nil.
func (m *Main) Menu() *widgets.Menu { return m.menu }
