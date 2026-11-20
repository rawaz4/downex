//go:build windows

package glue

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strconv"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/prefs"
	"downex/ui/win"
)

// RunOptions are how Run opens the windows.
type RunOptions struct {
	// App is the UI thread's win.App's choices (its window class and icon); Run sets the
	// hooks it needs itself.
	App win.AppOptions
	// Open opens w's window on a: the main window, or the progress window of w.View, with
	// w's session, prefs and theme (Window.OpenPrefs, UIStorage, NewTheme), connected with
	// w.Attach. It returns once the window is open; what the window holds is let go by
	// w.Defer's functions after it has closed. An error leaves the other windows open.
	Open func(a *win.App, w *Window) (*win.Window, error)
	// Done runs once the last window has closed, before the App closes (the icons the
	// windows shared are released there).
	Done func(a *win.App)
}

// Window is one window of the host: what it shows (View) and its own session, prefs,
// theme and glue, as each window process had them before the windows shared a process.
type Window struct {
	// View is what the window shows (the main window, or a progress window); Args are the
	// arguments it was asked with.
	View host.View
	Args []string
	// Names are the window's lock names: the main lock for the main window, that
	// download's for a progress window.
	Names host.HandoffNames
	// LockID, Files and Log are the host's.
	LockID string
	Files  Files
	Log    *slog.Logger
	// Session is the window's own host session: its view, its "--confirm" requests, its
	// events bridge (a progress window's watches its download only), its files' confirm
	// box; the daemon is the host's.
	Session *host.Session

	h        *Host
	instance *win.Instance // a progress window's own lock (old builds' starts find it)
	hooks    confirmHooks  // the main window's (SetConfirmHooks)
	win      *win.Window   // from the moment Open makes it (Host.windowMade)
	prefs    *prefs.File   // OpenPrefs' (flushed at the end of the session)
	defers   []func()
	shown    bool // FirstFrame ran
	ended    bool

	theme *themeLink
	ui    *uiStorage // UIStorage's
	glue  *Glue
}

// Run opens the window the process's start asked for, then those later starts ask for,
// and runs the UI thread's loop until the last window has closed (each window closes on
// its own: the main window and the progress windows do not take each other along). A
// failure to open the start's own window is returned; a later one is logged.
func (h *Host) Run(o RunOptions) error {
	if h == nil || h.closed {
		return ErrClosed
	}
	if o.Open == nil {
		return errorf("Run without Open")
	}
	ao := o.App
	ao.OnPanic, ao.OnWindowMade, ao.OnWindowClosed, ao.OnEndSession = h.panicked, h.windowMade, h.windowClosed, h.endSession
	a, err := win.NewApp(ao)
	if err != nil {
		return err
	}
	defer a.Close()
	h.run, h.accepting = o, true
	h.app.Store(a)
	defer h.app.Store(nil)
	if err := h.request(h.launch); err != nil {
		return err
	}
	for {
		if err := a.Run(); err != nil {
			return err
		}
		// The last window has closed: a host start that comes now is told HostClosing and
		// tries again (it becomes the host once this process has gone); those taken before
		// are opened.
		h.accepting = false
		h.instance.Drain()
		if len(h.windows) == 0 {
			break
		}
		h.accepting = true
	}
	// Nothing is taken from here on: the hand-off window and the main lock go before Done and
	// the App's close (which may still check messages), so that a later start, of whichever
	// build, finds no host and becomes one, rather than handing over to a host that would
	// never show what it took.
	h.instance.Close()
	if o.Done != nil {
		o.Done(a)
	}
	return nil
}

// Request shows what args ask for (a later start's, or a window's own request): no
// "--progress" is the main window (opened when it is not, else restored and brought to the
// front, its "--confirm <id>" requests shown), "--progress <id>" that download's progress
// window (one per download), "--confirm <id> --raise" the standalone Confirm download window
// (one, which takes later ids in turn; the main window is not shown). For the UI thread,
// while Run runs.
func (h *Host) Request(args []string) {
	if err := h.request(args); err != nil {
		h.Log.Warn("cannot open the window", "progress", host.ViewOf(args).Progress, "error", err)
	}
}

func (h *Host) request(args []string) (err error) {
	if h.app.Load() == nil {
		return ErrClosed
	}
	v := host.ViewOf(args)
	if v.IsProgress() {
		if w := h.progress[v.Progress]; w != nil {
			w.frontFor(args)
			return nil
		}
		return h.openProgress(v, args)
	}
	if h.Legacy {
		return nil // a legacy host shows its one progress window (nothing else reaches it)
	}
	if v.IsConfirm() {
		// A download the user just started in the browser: its own Confirm download window,
		// and the main window is left as it is (hidden, minimised or wherever it is).
		if w := h.confirm; w != nil {
			w.Session.SecondInstance(args, func() { w.frontFor(args) })
			return nil
		}
		return h.open(h.newWindow(v, args, nil))
	}
	if w := h.main; w != nil {
		w.Session.SecondInstance(args, func() { w.frontFor(args) })
		return nil
	}
	return h.open(h.newWindow(v, args, nil))
}

// openProgress opens the progress window of v under that download's own lock, which old
// builds' starts look for. When an old build's progress process holds it, that process
// shows its window (a second window of the download is never opened).
func (h *Host) openProgress(v host.View, args []string) error {
	names := HandoffNames(h.LockID, v)
	inst := h.launchLock
	h.launchLock = nil
	if inst == nil {
		got, running, err := win.ClaimInstance(win.InstanceNames(names), 0)
		if running.OK() {
			host.AllowForegroundHandoff() // that window may come to the front, as a start's would
			if err := forward(running, args, h.getwd); err == nil {
				h.Log.Info("handed over to an older build's progress window", "progress", v.Progress)
				return nil
			}
			h.Log.Warn("an older build's progress window did not take the hand-off", "progress", v.Progress, "error", err)
		} else if err != nil {
			h.Log.Warn("single-instance lock", "progress", v.Progress, "error", err)
		}
		inst = got
	}
	w := h.newWindow(v, args, inst)
	if inst != nil && !inst.Listening() {
		if err := inst.Listen(win.ListenOptions{
			Kind: host.HandoffDataKind, Max: host.MaxHandoff,
			OnData:  func([]byte) { w.front() },
			OnClose: func() { w.command(kit.WindowClose) },
		}); err != nil {
			h.Log.Warn("older builds' starts cannot find the progress window", "progress", v.Progress, "error", err)
		}
	}
	return h.open(w)
}

// newWindow is the part of a window that is not drawn yet: its session and lock.
func (h *Host) newWindow(v host.View, args []string, inst *win.Instance) *Window {
	open := h.openOther
	if !h.Legacy {
		open = h.OpenProgress
	}
	w := &Window{
		View: v, Args: args, Names: HandoffNames(h.LockID, v), LockID: h.LockID, Files: h.Files, Log: h.Log,
		h: h, instance: inst,
		Session: host.NewSession(host.SessionOptions{
			Args: args, Version: h.version, Daemon: h.daemon, Log: h.Log, ThemePath: h.Files.Theme, OpenWindow: open,
		}),
	}
	h.windows = append(h.windows, w)
	switch {
	case v.IsProgress():
		h.progress[v.Progress] = w
	case v.IsConfirm():
		h.confirm = w
		h.confirmChanged()
	default:
		h.main = w
	}
	return w
}

// open draws w (RunOptions.Open); a failure, a panic too, lets go of what it holds, the
// window Open made included (windowMade tells it, whether Attach ran or not).
func (h *Host) open(w *Window) (err error) {
	outer := h.opening
	h.opening = w
	defer func() {
		h.opening = outer
		if v := recover(); v != nil {
			err = fmt.Errorf("glue: panic while opening the window: %v\n%s", v, debug.Stack())
		}
		if err != nil {
			if w.win != nil {
				w.win.Discard() // a panic may have left calls into its root counted
			}
			w.end()
		}
	}()
	ww, err := h.run.Open(h.app.Load(), w)
	if err == nil && ww == nil {
		err = errorf("Open returned no window")
	}
	if err != nil {
		return err
	}
	w.win = ww
	if !h.noActivate && host.RaiseArg(w.Args) {
		// The window of a download the user just started in the browser: over the browser,
		// even without the foreground (never for a §8 complete dialog or a clipboard add).
		ww.Raise()
	}
	return nil
}

// OpenProgress opens (or brings to the front) the progress window of download id from any
// goroutine (a window's own request: Session.OpenProgressWindow, Platform's).
func (h *Host) OpenProgress(id int64) error {
	a := h.app.Load()
	if a == nil {
		return ErrClosed
	}
	return a.Post(func() { h.Request([]string{"--" + host.ProgressFlag, strconv.FormatInt(id, 10)}) })
}

// windowMade is the App's: the first window made while w opens is w's (a window of a
// request that a modal loop inside Open lets in is that request's own: opening nests).
func (h *Host) windowMade(ww *win.Window) {
	if w := h.opening; w != nil && w.win == nil {
		w.win = ww
	}
}

// windowClosed is the App's: w's window is gone, so its part is let go.
func (h *Host) windowClosed(ww *win.Window) {
	for _, w := range h.windows {
		if w.win == ww {
			w.end()
			return
		}
	}
}

// panicked is the App's: a panic in a window's procedure closes that window, and only that
// one (it was put back in order, but what it shows may be half updated); the others go on.
// A panic of no window is logged.
func (h *Host) panicked(ww *win.Window, err error) {
	h.Log.Error("a window failed", "error", err)
	if ww == nil {
		return
	}
	for _, w := range h.windows {
		if w.win == ww {
			ww.Close() // no CloseEvent: its state is not stored from a failed window
			return
		}
	}
}

// QuitApp closes every window of the process: the user quit Downex (the tray's Quit, the
// menu-bar icon's Quit, ⌘Q), which downexd announced. For the UI thread.
func (w *Window) QuitApp() {
	if w.h != nil {
		w.h.Log.Info("Downex quits: closing every window")
		w.h.CloseAll()
	}
}

// CloseAll closes every window as its own close does (the main window stores its size),
// so that the process ends: the answer to a WM_CLOSE another program sends (an installer's
// CloseMainWindow reaches one window of the process; the hand-off window's). For the UI
// thread.
func (h *Host) CloseAll() {
	for _, w := range slices.Clone(h.windows) {
		w.command(kit.WindowClose)
	}
}

// endSession is the App's: the user's session ends, and the process with it as soon as the
// message returns, so every window closes now (the main window storing its size) and, once
// its HWND is gone, lets go of what it holds (the prefs written). A window inside a call into
// its views (one of its dialogs is open) cannot close under that call: what it holds stays
// in use, and only its prefs are written.
func (h *Host) endSession() {
	h.Log.Info("the session ends: closing every window")
	for _, w := range slices.Clone(h.windows) {
		if w.win == nil || w.win.CloseNow() {
			w.end()
			continue
		}
		h.Log.Info("a window in a dialog stays open to the end of the session", "progress", w.View.Progress)
		if w.prefs != nil {
			if err := w.prefs.Flush(); err != nil {
				h.Log.Warn("the prefs at the end of the session", "error", err)
			}
		}
	}
}

// ForeignClose is win.WindowOptions.OnForeignClose of the host's windows: a WM_CLOSE from
// another program closes every window (CloseAll), not just the one it reached.
func (w *Window) ForeignClose() bool {
	if w.h == nil || w.h.Legacy {
		return false
	}
	w.h.CloseAll()
	return true
}

// front restores, shows and brings w's window to the front (a later start asked for it).
func (w *Window) front() {
	if w.win == nil || w.h.noActivate {
		return
	}
	w.win.BringToFront()
}

// frontFor brings w to the front for a later start with args: one with "--raise" (a
// download the user just started in the browser) puts it above the browser even when the
// foreground is not granted.
func (w *Window) frontFor(args []string) {
	if w.win == nil || w.h.noActivate {
		return
	}
	if !w.win.BringToFront() && host.RaiseArg(args) {
		w.win.Raise()
	}
}

func (w *Window) command(c kit.WindowCommand) {
	if w.win != nil {
		w.win.Command(c)
	}
}

// Defer adds f to what is let go once w's window has closed, the last added first.
func (w *Window) Defer(f func()) { w.defers = append(w.defers, f) }

// end lets go of w: its defers (the stores, the prefs, the glue), its session and its lock.
func (w *Window) end() {
	if w.ended {
		return
	}
	w.ended = true
	for i := len(w.defers) - 1; i >= 0; i-- {
		w.runDefer(w.defers[i])
	}
	w.defers = nil
	w.Detach()
	w.Session.Close()
	w.instance.Close()
	h := w.h
	h.windows = slices.DeleteFunc(h.windows, func(o *Window) bool { return o == w })
	if h.main == w {
		h.main = nil
	}
	if h.progress[w.View.Progress] == w {
		delete(h.progress, w.View.Progress)
	}
	if h.confirm == w {
		h.confirm = nil
		h.confirmChanged()
	}
}

func (w *Window) runDefer(f func()) {
	defer func() {
		if v := recover(); v != nil {
			w.Log.Error("a window's clean-up failed", "error", fmt.Sprintf("%v\n%s", v, debug.Stack()))
		}
	}()
	f()
}

// FirstFrame tells the host w drew its first frame, on the UI thread: the first-frame trim
// runs FirstFrameTrimAfter later (see StartupTrimAfter), once for each window.
func (w *Window) FirstFrame() {
	if w == nil || w.shown {
		return
	}
	w.shown = true
	w.h.firstFrame()
}

// OpenPrefs opens the per-viewer conveniences file (ui-prefs.json; the preview's own in a
// preview build) for this window: each window has its own, as each window process had, and
// follows the others' changes as it followed other processes'. A file that does not exist
// yet starts with today's window's localStorage values, read and never changed
// (prefs.WebView2Import). Without a folder for it the error is prefs.ErrNoPath, and the views
// keep their choices for the session.
func (w *Window) OpenPrefs() (f *prefs.File, err error) {
	if w.Files.Prefs == "" {
		return nil, prefs.ErrNoPath
	}
	f, err = prefs.Open(w.Files.Prefs, prefs.Options{Import: prefs.WebView2Import(w.h.webview2), Log: w.Log})
	if err == nil {
		w.prefs = f
	}
	return f, err
}

// UIStorage is s for the window's stores: their change notifications run on the UI thread of
// the window Attach connects (see uiStorage). Give it to NewTheme and the other stores.
func (w *Window) UIStorage(s state.Storage) state.Storage {
	if s == nil {
		return nil
	}
	if w.ui == nil {
		w.ui = &uiStorage{inner: s}
	} else {
		w.ui.inner = s
	}
	return w.ui
}

// NewTheme is the window's theme store over storage (UIStorage of the prefs file; nil keeps
// the choice for the session): System, Light or Dark, followed across windows and processes,
// its choice written to ui-theme.json as themeLink says. Call it once, before Attach; Attach
// follows ui-theme.json.
func (w *Window) NewTheme(storage state.Storage) *state.Theme {
	w.theme = newThemeLink(storage, w.Files.Theme, w.Session.SaveTheme)
	t := state.NewTheme(state.ThemeOptions{Storage: w.theme, SystemDark: host.SystemPrefersDark})
	w.theme.attach(t)
	return t
}

// Detach lets the window's glue go, on its UI thread, once its window has closed and before
// the prefs file and the theme close: the files watch stops (so nothing reloads a closed
// prefs file or posts to a destroyed window), the stores' notifications are dropped and the
// system dialogs close. end calls it too; a second call does nothing.
func (w *Window) Detach() {
	if w == nil || w.glue == nil {
		return
	}
	w.glue.close()
	w.glue = nil
}
