package progress

import (
	"errors"
	"log/slog"
	"time"

	"downex/ui/core/progress"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
)

// Options are what New builds a progress window from: the window process's session (its
// daemon, its event bridge watching only this download, its files), the shared view
// preferences, and the backend's facts.
type Options struct {
	// ID is the download the window shows (host.View.Progress).
	ID int64
	// Daemon is downexd (host.Session.Daemon); Events the bridge's batches (a state.Hub
	// that host.Session.Start feeds).
	Daemon host.Daemon
	Events state.Events
	// Platform is the host's other calls: open, open with, open folder, copy text.
	Platform views.Platform
	// Caps are the host's capabilities (OpenWith: the system's "Open with" dialog; without
	// it, no Open with…).
	Caps host.Capabilities
	// Theme is the shared theme (nil: System for the session); Storage the prefs file, where
	// the choice to show the details is kept for every progress window (nil: for the
	// session).
	Theme   *state.Theme
	Storage state.Storage
	// Clock is wall time and the store's timers (nil: the system's); Call makes a call's
	// context (nil: 30 s); Go runs blocking work off the UI thread (nil: a goroutine); Log is
	// the window's log.
	Clock state.Clock
	Call  state.CallContext
	Go    func(func())
	Log   *slog.Logger
	// FixedTheme and SystemDark stand in for the theme and the system's mode (references and
	// tests).
	FixedTheme *tokens.Choice
	SystemDark *bool
	// StatusInterval is how often downexd is checked (0: state.ProgressStatusInterval);
	// ApplyDelay how long typing in the limit fields waits before it applies them (0: 600 ms).
	StatusInterval time.Duration
	ApplyDelay     time.Duration
	// Frame is the window's frame in DIPs: its outer size less its client size (the title bar
	// and the borders). Today's sizes (520 × 300, 560, the fit rules) are the window's outer
	// size; the window's client area is that less the frame. Nil asks the host (a host with a
	// FrameSize() kit.Size method), else 0 × 40 (lib/progress.ts's Frame).
	Frame func() kit.Size
	// OpenPrompt asks before a program (or a name that hides its type) is opened (the dialogs
	// lane's OpenDialog). Nil: such a file is not opened.
	OpenPrompt OpenPrompt
	// Close closes the window (nil: the kit window's Close, which the host turns into the
	// end of the process).
	Close func()
	// Now, Scheduler and NoMotion are the kit window's (tests; nil: real time and timers).
	Now       func() time.Time
	Scheduler kit.Scheduler
	NoMotion  bool
	// Direction mirrors the layout (DirRTL).
	Direction kit.Direction
}

// OpenPrompt shows the question before a program, or a file whose name hides its type, is
// opened (OpenDialog.svelte): "Run this program?", "Open this program with another app?",
// "Open this file?". It returns the modal the window's dialog host shows.
type OpenPrompt func(app *views.App, q OpenQuestion) views.Modal

// OpenQuestion is what the prompt asks about.
type OpenQuestion struct {
	ID int64
	// Name is the display name (bidi controls made visible).
	Name string
	// Executable: the file would run code when opened; HiddenControls: its real name has
	// bidi control characters; WithApp: Open with… (the user chooses the app next).
	Executable, HiddenControls, WithApp bool
	// Confirm is the user's yes: the window opens the file (or asks for the app). Closed is
	// called when the prompt has gone, after Confirm too.
	Confirm func()
	Closed  func()
}

// Window is one progress window: its store, its App and its kit window, whose Root a
// backend hosts.
type Window struct {
	// Kit is the kit window; Root is what a backend hosts (Kit, also hearing the window's
	// activation: the settings are read again then).
	Kit  *kit.Window
	Root kit.Root
	App  *views.App
	// Store is the download's state (stores/progress.svelte.ts).
	Store *state.ProgressStore
	// Screen is the window's content.
	Screen *Screen

	o       Options
	started bool
}

// ErrNoDownload is New's answer without a download id or a daemon.
var ErrNoDownload = errors.New("progress: a progress window needs a download and a daemon")

// New builds the progress window of download o.ID: the store, the App over a kit window,
// the screen, and the window's theme (the system frame's caption in the page's colours,
// following the theme live). Nothing starts until Start.
func New(o Options) (*Window, error) {
	if o.ID <= 0 || o.Daemon == nil {
		return nil, ErrNoDownload
	}
	if o.ApplyDelay <= 0 {
		o.ApplyDelay = 600 * time.Millisecond
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	pw := &Window{o: o}
	pw.Store = state.NewProgress(state.ProgressOptions{Daemon: o.Daemon, Events: o.Events, ID: o.ID, Clock: o.Clock, Call: o.Call, Go: o.Go})

	theme := &views.Theme{State: o.Theme, Fixed: o.FixedTheme, SystemDark: o.SystemDark}
	scope := widgets.NewScope(theme)
	scope.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	frame := pw.frameOptions()
	pw.Kit = kit.NewWindow(scope, kit.WindowOptions{
		Title:         "Downex",
		MinSize:       MinSize(frame),
		Direction:     o.Direction,
		Palette:       theme.Palette,
		FrameColors:   theme.CaptionFrame,
		OnKey:         func(e kit.KeyEvent) bool { return pw.Screen != nil && pw.Screen.windowKey(e) },
		OnEnvironment: func(env kit.Environment) { pw.Screen.environment(env) },
		Now:           o.Now,
		Scheduler:     o.Scheduler,
		NoMotion:      o.NoMotion,
	})
	// The theme store goes to the App only after it is made: views.App repaints a theme change
	// with the main window's frame colours, and this window keeps the system frame's.
	app, err := views.NewApp(views.Options{
		Window: pw.Kit, Daemon: o.Daemon, Platform: o.Platform, Storage: o.Storage, Clock: o.Clock,
		Caps: o.Caps, Call: o.Call, Go: o.Go, Log: o.Log, FixedTheme: o.FixedTheme, SystemDark: o.SystemDark,
		NoToasts: true,
	})
	if err != nil {
		return nil, err
	}
	app.Theme = o.Theme
	app.WidgetTheme = theme
	pw.App = app
	pw.Screen = newScreen(pw)
	scope.Add(pw.Screen)
	pw.Root = &root{Window: pw.Kit, pw: pw}
	if o.Theme != nil {
		app.Watch(&o.Theme.Changes, pw.themeChanged)
	}
	app.Watch(&pw.Store.Changes, pw.Screen.sync)
	return pw, nil
}

// frameOptions is the frame New sizes the window with: Options.Frame's, else 0 × 40.
func (pw *Window) frameOptions() kit.Size {
	if pw.o.Frame != nil {
		if f := pw.o.Frame(); validFrame(f) {
			return f
		}
	}
	return kit.Size{H: progress.Frame}
}

func validFrame(f kit.Size) bool { return f.W >= 0 && f.W <= 200 && f.H >= 0 && f.H <= 200 }

// frameSize is the window's frame now: Options.Frame's, the host's (a FrameSize method),
// else 0 × 40.
func (pw *Window) frameSize() kit.Size {
	if pw.o.Frame != nil {
		if f := pw.o.Frame(); validFrame(f) {
			return f
		}
	}
	if h, ok := pw.Kit.Host().(interface{ FrameSize() kit.Size }); ok {
		if f := h.FrameSize(); validFrame(f) && f.H > 0 {
			return f
		}
	}
	return kit.Size{H: progress.Frame}
}

// InitialSize is the client size the window opens with: today's page, 520 px wide, in a
// 300 px high window less the frame (it opens collapsed; the screen sizes it once it knows
// the download). Today's widths are the page's (its 520 px page is 520 CSS px wide at every
// scaling), its heights the window's; the host makes the outer size from the client size
// (AdjustWindowRectExForDpi at the window's DPI).
func InitialSize(frame kit.Size) kit.Size {
	return kit.Size{W: host.ProgressWidth, H: host.ProgressHeight - frame.H}
}

// MinSize is the smallest client size: today's 480 px wide page in a 260 px high window
// less the frame.
func MinSize(frame kit.Size) kit.Size {
	return kit.Size{W: host.ProgressMinWidth, H: host.ProgressMinHeight - frame.H}
}

// Start reads the download and the settings (off the UI thread) and starts the checks;
// call it once the window is attached to its host.
func (pw *Window) Start() {
	if pw.started {
		return
	}
	pw.started = true
	interval := pw.o.StatusInterval
	pw.App.Go(func() { pw.Store.Start(interval) })
}

// Dispose stops the store, the screen's timers and the App's subscriptions.
func (pw *Window) Dispose() {
	pw.Screen.dispose()
	pw.Store.Dispose()
	pw.App.Dialogs.DismissAll()
	pw.App.Dispose()
}

// close closes the window (closeWindow): once.
func (pw *Window) close() {
	if pw.o.Close != nil {
		pw.o.Close()
		return
	}
	pw.Kit.Close()
}

// themeChanged repaints the window in the new theme, with the caption's colours.
func (pw *Window) themeChanged() {
	t := pw.App.Tokens()
	pw.Kit.SetPalette(t.Palette())
	if h := pw.Kit.Host(); h != nil {
		h.SetFrameColors(t.CaptionFrame())
	}
	pw.Kit.InvalidateRect(kit.RectAt(kit.Point{}, pw.Kit.Size()))
}

// root is the kit window as the backend hosts it, hearing the window's activation.
type root struct {
	*kit.Window
	pw *Window
}

// Event is kit.Root's: the window's own, then, when it got the focus, the settings are read
// again (onWindowFocus: the default connections may have changed in the background).
func (r *root) Event(e kit.Event) bool {
	used := r.Window.Event(e)
	if f, ok := e.(kit.FocusEvent); ok && f.In {
		r.pw.Screen.windowFocused()
	}
	return used
}
