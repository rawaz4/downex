package views

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/queues"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
)

// Platform is what a window does through its host beyond the daemon's calls: today's api
// methods that are not RPCs (openFile, openWith, openFolder, fileExists, freeSpace,
// openAppFolder, openProgressWindow, pickFolder, pickFile, openExternal, copyText,
// diagnostics, thirdPartyNotices), and the browser extension's folders and registration. Every call may block (a file dialog, the shell): make them
// off the UI thread (App.Go) with a context from App.CallContext. Errors are the host's
// Connect errors (host.ErrorOf reads them; "unimplemented" where the platform cannot).
type Platform interface {
	OpenFile(ctx context.Context, id int64) error
	// OpenWith asks for the app to open the file with (Windows); canceled when the dialog
	// closes without a choice.
	OpenWith(ctx context.Context, id int64) error
	OpenFolder(ctx context.Context, id int64) error
	FileExists(ctx context.Context, id int64) (bool, error)
	// FreeSpace is the bytes free on the volume of an absolute folder.
	FreeSpace(dir string) (int64, error)
	OpenAppFolder(ctx context.Context, kind host.FolderKind) error
	// ExtensionFolder is the absolute folder of the browser extension that came with this
	// copy of Downex (kind host.FolderExtensionChrome or host.FolderExtensionFirefox), for
	// the browser's "Load unpacked"; it does not block.
	ExtensionFolder(kind host.FolderKind) (string, error)
	// RegisterBrowsers registers Downex with the browsers again: the relay installed next to
	// this program runs "register" (no shell), as Setup does.
	RegisterBrowsers(ctx context.Context) error
	// OpenProgressWindow opens (or raises) the progress window of a download.
	OpenProgressWindow(id int64) error
	// PickFolder and PickFile show the system's pickers (titles host.FolderPickerTitle and
	// host.FilePickerTitleOf(title), starting in host.ExistingDir(initial)); "" with a nil
	// error is a cancelled picker (today's null).
	PickFolder(ctx context.Context, initial string) (string, error)
	PickFile(ctx context.Context, title, initial string) (string, error)
	// OpenExternal opens a fixed https address in the system's browser.
	OpenExternal(ctx context.Context, url string) error
	// CopyText puts text on the clipboard.
	CopyText(ctx context.Context, text string) error
	// Diagnostics is Settings → About's "Copy diagnostics" text.
	Diagnostics(ctx context.Context) string
	// ThirdPartyNotices is the open-source notices' text.
	ThirdPartyNotices() string
}

// Options are what NewApp builds an App from. The composition (views/app in the main window,
// views/progress in a progress window, viewstest in tests) makes the stores and starts them;
// NewApp adds the window's side.
type Options struct {
	// Window is the window the screens are in (required). Its content is the screen's root,
	// wrapped in a widgets.Scope of App.WidgetTheme.
	Window *kit.Window
	// Daemon is downexd (host.Session.Daemon, or a fake).
	Daemon host.Daemon
	// Platform is the host's other calls (HostPlatform over a host.Session, or a fake).
	Platform Platform
	// Main is the main window's stores (nil in a progress window).
	Main *state.Main
	// Theme and Density are the shared view preferences (nil: System and Comfortable for
	// the session).
	Theme   *state.Theme
	Density *state.Density
	// Storage is the prefs file (ui-prefs.json: column widths, the details pane, the
	// existing-file choice); nil keeps them for the session.
	Storage state.Storage
	// Clock is the stores' clock (nil: the system's).
	Clock state.Clock
	// Caps and Build are the host's Startup facts.
	Caps  host.Capabilities
	Build host.BuildInfo
	// Demo marks the simulated daemon (the status bar's demo mark, today's api.kind 'mock').
	Demo bool
	// Call makes a call's context (nil: state.DefaultCallContext, host.CallTimeout).
	Call state.CallContext
	// Go runs blocking work off the UI thread (nil: a goroutine). Tests may run it at once.
	Go func(func())
	// Log is the window's log (nil: slog's default).
	Log *slog.Logger
	// FixedTheme, when set, is the theme drawn whatever Theme says (references and tests);
	// SystemDark, when set, stands in for the system's app mode.
	FixedTheme *tokens.Choice
	SystemDark *bool
	// NoToasts leaves the toast and notice layers out (a window that shows none).
	NoToasts bool
	// StatusBarHeight is how far above the window's bottom the toasts sit, less 16 px
	// (--statusbar-h, 32 px in the main window; 0 where there is no status bar).
	StatusBarHeight kit.DIP
}

// App is what every screen is handed: the window and its stores, the daemon and the host,
// the toasts and notices, the preferences, the clock, the capabilities, the dialog host and
// the function fields the composition fills in. Its methods belong to the UI thread unless
// they say otherwise; a store's Changes reach the UI thread through Watch.
type App struct {
	Window   *kit.Window
	Daemon   host.Daemon
	Platform Platform
	// Main is the main window's stores: Downloads, Queues, Tools and the settings (nil in a
	// progress window).
	Main    *state.Main
	Theme   *state.Theme
	Density *state.Density
	Storage state.Storage
	Clock   state.Clock
	Caps    host.Capabilities
	Build   host.BuildInfo
	Demo    bool
	Log     *slog.Logger

	// WidgetTheme is the theme the screens draw with (the Theme store's choice, or the
	// fixed one): wrap the window's content in widgets.NewScope(app.WidgetTheme, ...).
	WidgetTheme *Theme
	// Toasts and Notices are the window's toast stores (Toasts.svelte), shown by ToastLayer.
	// Use App.Toast, Success, Error and Notice from any goroutine.
	Toasts     *widgets.Toasts
	Notices    *widgets.Notices
	ToastLayer *widgets.ToastLayer
	// Layers stacks the window's layers as today's z-indexes do; Dialogs is the dialog host.
	Layers  *Layers
	Dialogs *DialogHost
	// Open and Act are filled in by the composition (views/app); Commands dispatches the
	// palette's commands, the shortcuts and the context menu to them.
	Open     Openers
	Act      Actions
	Commands *Commands
	// Details is the details pane's collapsed state (App.svelte's detailsCollapsed).
	Details *DetailsState
	// FallbackFocus is where focus goes when a dialog closes and its opener is gone
	// (App.svelte's gridElement): the shell sets it to the download grid.
	FallbackFocus func() kit.Widget

	call  state.CallContext
	goFn  func(func())
	mu    sync.Mutex
	stops []func()
}

// ErrNoWindow is NewApp's answer without a window.
var ErrNoWindow = errors.New("views: an App needs a window")

// NewApp makes the window's side of an App: the widget theme, the toast stores and layers,
// the layers, the dialog host, the command registry and the details state. It starts nothing
// in the stores. Theme changes repaint the window with the new palette and frame colours.
func NewApp(o Options) (*App, error) {
	if o.Window == nil {
		return nil, ErrNoWindow
	}
	a := &App{
		Window: o.Window, Daemon: o.Daemon, Platform: o.Platform, Main: o.Main, Theme: o.Theme,
		Density: o.Density, Storage: o.Storage, Clock: o.Clock, Caps: o.Caps, Build: o.Build,
		Demo: o.Demo, Log: o.Log, call: o.Call, goFn: o.Go,
	}
	if a.Clock == nil {
		a.Clock = state.SystemClock
	}
	if a.call == nil {
		a.call = state.DefaultCallContext
	}
	if a.goFn == nil {
		a.goFn = func(f func()) { go f() }
	}
	if a.Log == nil {
		a.Log = slog.Default()
	}
	a.WidgetTheme = &Theme{State: o.Theme, Fixed: o.FixedTheme, SystemDark: o.SystemDark}
	a.Layers = NewLayers(o.Window)
	a.Toasts = widgets.NewToasts(o.Window)
	a.Notices = widgets.NewNotices(o.Window)
	a.ToastLayer = widgets.NewToastLayer(a.Toasts, a.Notices)
	a.ToastLayer.StatusBarHeight = o.StatusBarHeight
	a.Dialogs = newDialogHost(a)
	a.Commands = newCommands(a)
	a.Details = newDetailsState(o.Storage)
	if !o.NoToasts {
		a.Layers.Set(a.ToastLayer.Plain(), ZToasts)
		a.Layers.Set(a.ToastLayer.Notices(), ZNotices)
	}
	if o.Theme != nil {
		a.Watch(&o.Theme.Changes, a.themeChanged)
	}
	return a, nil
}

// themeChanged repaints the window in the new theme: its palette, frame colours and every
// widget (they read their colours when they paint).
func (a *App) themeChanged() {
	w := a.Window
	t := a.Tokens()
	w.SetPalette(t.Palette())
	if h := w.Host(); h != nil {
		h.SetFrameColors(t.MainFrame())
	}
	w.InvalidateRect(kit.RectAt(kit.Point{}, w.Size()))
}

// Tokens is the theme in effect in the window's environment.
func (a *App) Tokens() *tokens.Theme { return a.WidgetTheme.Tokens(a.Window.Environment()) }

// Dispose stops what Watch subscribed and clears the toasts' timers.
func (a *App) Dispose() {
	a.mu.Lock()
	stops := a.stops
	a.stops = nil
	a.mu.Unlock()
	for _, s := range stops {
		s()
	}
	a.Notices.Clear()
}

// Watch runs f on the UI thread after every change sig tells of (a store's Changes, which
// are notified on any goroutine); it stops at Dispose or when the returned cancel runs.
func (a *App) Watch(sig *state.Signal, f func()) (cancel func()) {
	stop := sig.Subscribe(func() { a.UI(f) })
	a.mu.Lock()
	a.stops = append(a.stops, stop)
	a.mu.Unlock()
	return stop
}

// UI runs f on the UI thread after the calls in progress there (any goroutine may call it);
// it does nothing once the window is closed.
func (a *App) UI(f func()) { _ = a.Window.Post(f) }

// Go runs f off the UI thread (a daemon call, a picker); f reports back through UI.
func (a *App) Go(f func()) { a.goFn(f) }

// CallContext is a call's context (30 s, ended when the session closes).
func (a *App) CallContext() (context.Context, context.CancelFunc) { return a.call() }

// Toast, Success and Error show a toast (toasts.show, toasts.success, toasts.error); any
// goroutine may call them.
func (a *App) Toast(message string)   { a.UI(func() { a.Toasts.Show(message, widgets.ToastInfo) }) }
func (a *App) Success(message string) { a.UI(func() { a.Toasts.Success(message) }) }
func (a *App) Error(message string)   { a.UI(func() { a.Toasts.Error(message) }) }

// Notice shows a notice (a toast with actions: notices.show); DismissNotice takes one away.
// Any goroutine may call them.
func (a *App) Notice(n widgets.Notice)  { a.UI(func() { a.Notices.Show(n) }) }
func (a *App) DismissNotice(key string) { a.UI(func() { a.Notices.Dismiss(key) }) }

// UnavailableText is App.svelte's unavailable() toast.
const UnavailableText = "Not available while downexd is unreachable."

// Unavailable shows the toast of an action downexd cannot take while it is unreachable.
func (a *App) Unavailable() { a.Toast(UnavailableText) }

// Stale reports that downexd is unreachable: the list is the last known state and nothing
// that changes downloads is offered (App.svelte's stale). False without a Main.
func (a *App) Stale() bool { return a.Main != nil && a.Main.Downloads.Stale() }

// Settings are downexd's settings as last read and whether they were read since it last
// became reachable; before the first read they are state.DefaultSettings (App.svelte's
// DEFAULT_SETTINGS).
func (a *App) Settings() (s *downexv1.Settings, loaded bool) {
	if a.Main != nil {
		s, loaded = a.Main.Settings()
	}
	if s == nil {
		s = state.DefaultSettings()
	}
	return s, loaded
}

// QueueCaps is what the system can do after a queue (Capabilities' hibernate and wakeTimers).
func (a *App) QueueCaps() queues.Capabilities {
	return queues.Capabilities{Hibernate: a.Caps.Hibernate, WakeTimers: a.Caps.WakeTimers}
}

// QueueChoices are the queues the dialogs offer: none while downexd has no queues
// (App.svelte's queueChoices).
func (a *App) QueueChoices() []*downexv1.Queue {
	if a.Main == nil || !a.Main.Queues.Supported() {
		return nil
	}
	return a.Main.Queues.List()
}

// Compact reports the compact density.
func (a *App) Compact() bool { return a.Density != nil && a.Density.Compact() }

// ThemeChoice is the theme setting in effect (Theme's, else System).
func (a *App) ThemeChoice() host.ThemeChoice {
	if a.Theme == nil {
		return host.ThemeSystem
	}
	return a.Theme.Choice()
}

// DensityChoice is the density setting in effect (Density's, else Comfortable).
func (a *App) DensityChoice() state.DensityChoice {
	if a.Density == nil {
		return state.Comfortable
	}
	return a.Density.Choice()
}

// ModalTop is where a dialog's scrim starts: below a frameless window's 44 px title bar,
// which stays usable as the caption (TitleBar's --modal-top), else at the top.
func (a *App) ModalTop() kit.DIP {
	if a.Caps.CustomTitleBar {
		return TitleBarHeight
	}
	return 0
}

// TitleBarHeight is --titlebar-h.
const TitleBarHeight kit.DIP = 44

// Fallback is the focus a closed dialog falls back to (FallbackFocus's, or nothing).
func (a *App) Fallback() kit.Widget {
	if a.FallbackFocus == nil {
		return nil
	}
	return a.FallbackFocus()
}

// Abilities are what the toolbar, the menu and the palette may do now (App.svelte's canAdd,
// canPause, canResume, canRemove, canPauseAll, canResumeAll).
type Abilities struct {
	Stale                                                             bool
	CanAdd, CanPause, CanResume, CanRemove, CanPauseAll, CanResumeAll bool
}

// Abilities reads them from the stores.
func (a *App) Abilities() Abilities {
	if a.Main == nil {
		return Abilities{}
	}
	ds := a.Main.Downloads
	stale := ds.Stale()
	sel := ds.Selected()
	var ab Abilities
	ab.Stale = stale
	if stale {
		return ab
	}
	ab.CanAdd = true
	ab.CanRemove = len(sel) > 0
	for _, d := range sel {
		ab.CanPause = ab.CanPause || state.IsActive(d.GetState())
		ab.CanResume = ab.CanResume || state.IsResumable(d.GetState())
	}
	t := ds.Totals()
	ab.CanPauseAll = t.Active+t.Queued > 0
	for _, d := range ds.All() {
		if d.GetState() == state.StatePaused {
			ab.CanResumeAll = true
			break
		}
	}
	return ab
}
