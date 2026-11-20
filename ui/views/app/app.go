// Package app is the main window's composition (App.svelte): it builds the shell
// (views/shell) around the downloads lane's slots, fills the App's openers and actions with
// App.svelte's logic, routes the dialogs to the lanes' constructors (Screens), runs the
// window's effects (the confirm queue, arrivals, the tools toast, the free space, the
// post-queue countdown, the update pop-up, the stale and offline modes) and takes the window's input that no
// element used (the shortcuts through the command registry, a pasted link, a dropped one).
// It is platform-free; ui/native shows it in a Win32 window.
package app

import (
	"errors"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/categories"
	"downex/ui/core/format"
	"downex/ui/core/queues"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/dialogs"
	"downex/ui/views/downloads"
	"downex/ui/views/shell"
)

// Screens are the lanes' constructors the composition opens its dialogs with (views-base's
// rule: a lane's constructor takes the App and the request it serves). A nil one is a screen
// not built yet: its opener does nothing.
type Screens struct {
	Add      func(*views.App, views.AddRequest) views.Modal
	Confirm  func(*views.App, views.ConfirmRequest) views.Modal
	Remove   func(*views.App, views.RemoveRequest) views.Modal
	Restart  func(*views.App, views.RestartRequest) views.Modal
	Renew    func(*views.App, views.RenewRequest) views.Modal
	Rescue   func(*views.App, views.RescueRequest) views.Modal
	Open     func(*views.App, views.OpenRequest) views.Modal
	Move     func(*views.App, views.MoveRequest) views.Modal
	Settings func(*views.App, views.SettingsRequest) views.Modal
	Queues   func(*views.App, views.QueuesRequest) views.Modal
	Palette  func(*views.App) views.Modal
}

// Options are what New composes the main window from.
type Options struct {
	// Slots are the download grid, the details pane and the status bar; a nil Grid is the
	// downloads lane's grid and details pane (downloads.New), a nil StatusBar the shell's.
	Slots views.Slots
	// Screens are the dialogs' constructors; nil fields are the lanes' (laneScreens).
	Screens Screens
	// FreeSpaceEvery is how often the free space is read (0: FreeSpaceInterval).
	FreeSpaceEvery time.Duration
}

// FreeSpaceInterval is how often the free space of the download folder's drive is read while
// the window is open (App.svelte's FREE_SPACE_MS).
const FreeSpaceInterval = 60 * time.Second

// ErrNoMain is New's answer for an App without the main window's stores.
var ErrNoMain = errors.New("views/app: the main window needs App.Main")

// Main is the composed main window.
type Main struct {
	App   *views.App
	Shell *shell.Shell
	// Downloads is the downloads lane's grid and details pane (nil when Options gave a grid).
	Downloads *downloads.Downloads

	opts    Options
	screens Screens
	root    *keyRoot
	menu    *widgets.Menu

	// the confirm queue (App.svelte's dismissed and preferred)
	dismissed  map[int64]struct{}
	preferred  int64
	confirmKey confirmKey
	confirmGen int
	confirmID  int64
	confirmOn  *confirmModal
	// elsewhere reports the standalone Confirm download window open (SetConfirmElsewhere).
	elsewhere func() bool

	arrivals  arrivals
	toolsWere bool

	// the update pop-up shown (nil: none)
	updateOn *dialogs.Update

	// free space (App.svelte's freeBytes, freeUnsupported, freeReads)
	freeBytes       int64
	freeKnown       bool
	freeUnsupported bool
	freeReads       int
	freeDir         string
	freeLoaded      bool
	lastCompleted   int
	freeTimer       state.Timer

	postKey string
	closed  bool
}

// New composes the main window in app (whose Main must be set) and fills app.Open and
// app.Act. Show Root() as the window's content.
func New(app *views.App, o Options) (*Main, error) {
	if app == nil || app.Main == nil {
		return nil, ErrNoMain
	}
	m := &Main{App: app, opts: o, dismissed: map[int64]struct{}{}, lastCompleted: -1}
	m.screens = m.withLanes(o.Screens)
	m.fill()
	slots := o.Slots
	if slots.Grid == nil {
		dl, err := downloads.New(app)
		if err != nil {
			return nil, err
		}
		m.Downloads = dl
		slots = dl.Slots(o.Slots.StatusBar)
	}
	sh, err := shell.New(shell.Options{
		App: app, Slots: slots, Frameless: app.Caps.CustomTitleBar,
		TitleBar: shell.TitleBarOptions{
			OnSearch:   func(s string) { app.Main.Downloads.SetSearch(s) },
			OnCommands: func() { app.Open.Palette() },
			OnTheme: func() {
				if app.Theme != nil {
					app.Theme.Cycle()
				}
			},
			OnSettings: func() { app.Open.Settings(views.SettingsRequest{}) },
		},
		Sidebar: shell.SidebarOptions{
			OnCategory:     func(f categories.Filter) { app.Main.Downloads.SetCategory(f) },
			OnStatus:       func(s categories.Status) { app.Main.Downloads.SetStatusFilter(s) },
			OnQueue:        func(id int64) { app.Main.Downloads.SetQueueFilter(id) },
			OnManageQueues: func() { m.openQueues() },
			OnIntegration:  func() { app.Open.Settings(views.SettingsRequest{Page: "integration"}) },
		},
		Header: shell.HeaderOptions{
			OnAdd:      func() { app.Open.Add(views.AddRequest{}) },
			OnAddVideo: func() { app.Open.Add(views.AddRequest{Mode: views.AddStream}) },
			OnRescue:   func() { app.Open.Rescue(views.RescueRequest{}) },
			OnResume:   func() { app.Act.Resume(nil) },
			OnPause:    func() { app.Act.Pause(nil) },
			OnRemove:   func() { app.Open.Remove(views.RemoveRequest{IDs: m.selectedIDs()}) },
			OnPauseAll: func() { app.Act.PauseAll() }, OnResumeAll: func() { app.Act.ResumeAll() },
			OnChip: func(f categories.Status) {
				ds := app.Main.Downloads
				if ds.StatusFilter() == f {
					ds.SetStatusFilter(categories.AnyStatus)
				} else {
					ds.SetStatusFilter(f)
				}
			},
			OnDensity: func(compact bool) {
				if app.Density == nil {
					return
				}
				if compact {
					app.Density.Set(state.Compact)
				} else {
					app.Density.Set(state.Comfortable)
				}
			},
			OnToggleDetails: func() { app.Act.ToggleDetails() },
			OnManageQueues:  func() { m.openQueues() },
			OnSelectAll:     func() { app.Act.SelectAll() },
		},
		Status: shell.StatusOptions{
			OnLimit:      func(c views.LimitChoice) { app.Act.SetGlobalLimit(c) },
			OnPostAction: func(c views.WhenDoneChoice) { app.Act.SetWhenDone(c) },
			OnPolite:     func(on bool) { app.Act.SetPolite(on) },
		},
		OnRetry: func() { app.Go(app.Main.Downloads.RetryNow) },
	})
	if err != nil {
		if m.Downloads != nil {
			m.Downloads.Dispose()
		}
		return nil, err
	}
	m.Shell = sh
	m.root = &keyRoot{m: m}
	m.root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	m.root.Add(m.Shell.Root())

	ma := app.Main
	app.Watch(&ma.Downloads.Changes, m.refresh)
	app.Watch(&ma.Queues.Changes, m.refresh)
	app.Watch(&ma.Tools.Changes, m.refresh)
	app.Watch(&ma.Update.Changes, m.refresh)
	app.Watch(&ma.Polite.Changes, m.refresh)
	app.Watch(&ma.Changes, m.refresh)
	app.Watch(&app.Dialogs.Changes, m.refresh)
	app.Watch(&app.Details.Changes, m.refresh)
	if app.Theme != nil {
		app.Watch(&app.Theme.Changes, func() {
			m.Shell.ThemeChanged()
			m.refresh()
		})
	}
	if app.Density != nil {
		app.Watch(&app.Density.Changes, m.refresh)
	}
	m.refresh()
	m.scheduleFreeSpace()
	return m, nil
}

// Root is the window's content: the shell in its theme scope, under the composition's key
// handler (a pasted link).
func (m *Main) Root() kit.Widget { return m.root }

// Dispose stops the composition's timers (the App and the stores are disposed by their
// owner).
func (m *Main) Dispose() {
	m.closed = true
	if m.freeTimer != nil {
		m.freeTimer.Stop()
	}
	m.closeMenu(false)
	if m.Downloads != nil {
		m.Downloads.Dispose()
	}
}

// WindowFocused is the window getting the focus: the settings are read again unless downexd is
// unreachable (App.svelte's onWindowFocus).
func (m *Main) WindowFocused() { m.App.Main.Focus() }

// MenuCommand is the macOS menu bar's command: Settings… opens Settings unless another dialog
// is open (App.svelte's onMenuCommand).
func (m *Main) MenuCommand(command string) {
	if command != "settings" || m.App.Dialogs.DialogOpen() {
		return
	}
	m.App.Open.Settings(views.SettingsRequest{})
}

// RequestConfirm takes the host's --confirm ids (Session.Confirmations().Ready, a second
// launch): each one's dialog is asked for again (App.svelte's onConfirmRequest). Any goroutine
// may call it.
func (m *Main) RequestConfirm(ids []int64) {
	m.App.UI(func() {
		for _, id := range ids {
			m.requestConfirm(id, false)
		}
		m.refresh()
	})
}

// RequestRenew shows the New link dialogs of ids ("--renew <id>": a progress window's
// "New link…", docs/DESKTOP-SPEC.md §10), on the UI thread.
func (m *Main) RequestRenew(ids []int64) {
	m.App.UI(func() {
		for _, id := range ids {
			m.App.Open.Renew(views.RenewRequest{ID: id})
		}
		m.refresh()
	})
}

func (m *Main) selectedIDs() []int64 { return views.SelectedIDs(m.App.Main.Downloads.Selected()) }

// settings are the daemon's settings as last read and whether they are current.
func (m *Main) settings() (*downexv1.Settings, bool) { return m.App.Settings() }

// whenDone is the main queue's post-queue action, known only while downexd has queues and they
// were read (App.svelte's whenDone).
func (m *Main) whenDone() (downexv1.PostAction, bool) {
	q := m.App.Main.Queues
	if !q.Supported() || !q.Loaded() {
		return 0, false
	}
	main, ok := q.Get(queues.MainQueueID)
	if !ok {
		return 0, false
	}
	return main.GetPostAction(), true
}

// refresh shows the stores' state in the shell and runs the window's effects; it runs on the UI
// thread after every change of a store, the dialogs or a preference.
func (m *Main) refresh() {
	if m.closed {
		return
	}
	a := m.App
	ma := a.Main
	ds := ma.Downloads
	stale := ds.Stale()
	settings, loaded := m.settings()
	ab := a.Abilities()
	visible := ds.Visible()
	counts := ds.Counts()
	sel := ds.Selected()
	choices := a.QueueChoices()
	sh := m.Shell

	// the title bar
	sh.SetModal(a.Dialogs.Modal())
	// The palette (z 70) covers the title bar; its native search field would draw over it.
	sh.TitleBar().Search().SetCovered(a.Dialogs.PaletteOpen())
	sh.TitleBar().SetSearchText(ds.Search())

	// the sidebar
	browser := shell.BrowserUnknown
	if loaded {
		browser = shell.BrowserStart
		if settings.GetConfirmBrowserDownloads() {
			browser = shell.BrowserAsk
		}
	}
	sh.Sidebar().Update(shell.SidebarState{
		Index: ds.Index(), Category: ds.Category(), Status: ds.StatusFilter(), Counts: counts,
		Queues: choices, Queue: ds.QueueFilter(), QueueCounts: ds.QueueCounts(), Browser: browser,
	})

	// the page header
	ix := ds.Index()
	sh.Header().Update(shell.HeaderState{
		Abilities: ab,
		Title:     views.PageTitle(ds.Category(), ix.LabelOf, ds.QueueFilter(), ma.Queues.NameOf, ds.StatusFilter()),
		Count:     len(visible),
		Chips: shell.HeaderChips{
			Downloading: counts.Statuses[categories.Active], Queued: counts.Statuses[categories.Queued], Failed: counts.Statuses[categories.Failed],
		},
		Status: ds.StatusFilter(),
		More:   shell.MoreState{Compact: a.Compact(), Details: !a.Details.Collapsed(), Queues: len(choices) > 0},
	})

	// the connection banner and the details pane (the grid draws the empty list itself)
	sh.ConnectionBanner().Update(shell.ConnState{Daemon: ds.Daemon(), LoadError: ds.LoadError(), Checking: ds.Checking(), NextCheckAt: ds.NextCheckAt()})
	sh.SetDetails(len(sel) > 0)

	// the status bar
	if sb := sh.StatusBar(); sb != nil {
		wd, wdKnown := m.whenDone()
		cur := wd
		if !wdKnown {
			cur = 0
		}
		sb.Update(shell.StatusState{
			Daemon: ds.Daemon(), Demo: a.Demo, Totals: ds.Totals(), Shown: len(visible), Selected: len(sel),
			History: ds.TotalHistory(), Stale: stale, Limit: settings.GetGlobalSpeedLimit(), LimitKnown: loaded,
			PostAction: wd, PostActionKnown: wdKnown, PostActionChoices: queues.PostActionChoices(a.QueueCaps(), cur),
			FreeBytes: m.freeBytes, FreeKnown: m.freeKnown, DriveLabel: format.DriveLabel(settings.GetDefaultDir()),
			NeedBytes: views.NeedBytes(ds.All()),
			Polite:    m.politeStatus(), PoliteOn: settings.GetPoliteMode(),
		})
	}

	// the effects
	m.freeSpaceEffects(settings, loaded, ds.All())
	m.postActionEffect(stale)
	m.toolsEffect(stale)
	m.arrivalsEffect()
	m.confirmEffect()
	m.updateEffect(stale)
}

// politeStatus is Polite mode's status as downexd last reported it (nil: none yet).
func (m *Main) politeStatus() *downexv1.PoliteStatus {
	st, _ := m.App.Main.Polite.Status()
	return st
}
