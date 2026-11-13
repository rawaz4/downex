//go:build darwin && cgo

package glue

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/state"
	"downex/ui/state/prefs"
)

// RunOptions are how Run opens the windows.
type RunOptions struct {
	// App is the application's choices; Run sets Regular (the role's) and the hooks itself.
	App mac.AppOptions
	// Open opens w's window on a (see the Windows host's RunOptions.Open).
	Open func(a *mac.App, w *Window) (*mac.Window, error)
	// Done runs once the last window has closed, before the App closes.
	Done func(a *mac.App)
	// About is the About dialog's text (title, message): ui/menu.go's aboutText.
	AboutTitle, AboutMessage string
}

// Window is one window of the host: what it shows and its own session, prefs, theme, glue.
type Window struct {
	View    host.View
	Args    []string
	LockID  string
	Files   Files
	Log     *slog.Logger
	Session *host.Session

	h        *Host
	instance *fileLock // a progress window's download lock (Wails' starts find it)
	observe  func()    // its Wails notification
	win      *mac.Window
	prefs    *prefs.File
	defers   []func()
	shown    bool
	ended    bool
	menu     func(command string)

	theme *themeLink
	ui    *uiStorage
	glue  *Glue
}

// Run opens the window the start asked for, then those later starts ask for, and runs the
// main thread's event loop until the last window has closed.
func (h *Host) Run(o RunOptions) error {
	if h == nil || h.closed {
		return ErrClosed
	}
	if o.Open == nil {
		return errorf("Run without Open")
	}
	ao := o.App
	ao.Regular = h.Role != RoleProgress
	ao.OnPanic, ao.OnWindowClosed, ao.OnWindowMade = h.panicked, h.windowClosed, h.windowMade
	a, err := mac.NewApp(ao)
	if err != nil {
		return err
	}
	defer a.Close()
	host.YieldActivation = mac.YieldActivation // the main window yields before a progress window opens
	a.InstallProgram(mac.ProgramOptions{
		Settings: h.Role.hostsMain(), AboutTitle: o.AboutTitle, AboutMessage: o.AboutMessage,
		OnMenu: h.menuCommand, OnTerminate: h.terminate,
	})
	h.run, h.app = o, a
	defer func() { h.app = nil }()
	h.mu.Lock()
	h.postWake = func() { a.Post(h.drainPending) }
	h.mu.Unlock()
	if err := h.request(h.launch); err != nil {
		return err
	}
	h.drainPending()
	for {
		if err := a.Run(); err != nil {
			return err
		}
		// The last window has closed: a start that comes now is told HostClosing and tries
		// again (it becomes the host once this process has gone); those taken before open.
		h.mu.Lock()
		h.accepting = false
		h.mu.Unlock()
		h.drainPending()
		if len(h.windows) == 0 {
			break
		}
		h.mu.Lock()
		h.accepting = true
		h.mu.Unlock()
	}
	h.mu.Lock()
	h.postWake = nil
	h.mu.Unlock()
	h.stopListening()
	if o.Done != nil {
		o.Done(a)
	}
	return nil
}

// Request shows what args ask for (main thread, while Run runs).
func (h *Host) Request(args []string) {
	if err := h.request(args); err != nil {
		h.Log.Warn("cannot open the window", "progress", host.ViewOf(args).Progress, "error", err)
	}
}

func (h *Host) request(args []string) error {
	if h.app == nil {
		return ErrClosed
	}
	v := host.ViewOf(args)
	if v.IsProgress() {
		if w := h.progress[v.Progress]; w != nil {
			w.front()
			return nil
		}
		if !h.Role.hostsProgress() {
			return h.startApp(h.otherExe(true), args...)
		}
		return h.openProgress(v, args)
	}
	if h.Legacy {
		return nil
	}
	if !h.Role.hostsMain() {
		return h.startApp(h.otherExe(false), args...)
	}
	if w := h.main; w != nil {
		w.Session.SecondInstance(args, w.front)
		return nil
	}
	return h.open(h.newWindow(v, args, nil))
}

// otherExe is the other bundle's executable (the progress app's, or Downex.app's).
func (h *Host) otherExe(progress bool) string {
	l, err := clientLayout(h.exe)
	if err != nil {
		return h.exe
	}
	if progress {
		return l.Progress
	}
	return l.Window
}

// openProgress opens the progress window of v under that download's own lock (Wails' name),
// which today's starts look for; when a Wails progress process holds it, that process shows
// its window.
func (h *Host) openProgress(v host.View, args []string) error {
	name := InstanceID(h.LockID, v)
	inst := h.launchLock
	h.launchLock = nil
	if inst == nil && h.lockDir != "" {
		got, held, err := claimLock(lockPath(h.lockDir, name))
		if held {
			if b, err := handoffJSON(args, ""); err == nil {
				h.post(name, string(b))
				h.Log.Info("handed over to an older build's progress window", "progress", v.Progress)
				return nil
			}
		} else if err != nil {
			h.Log.Warn("single-instance lock", "progress", v.Progress, "error", err)
		}
		inst = got
	}
	w := h.newWindow(v, args, inst)
	w.observe = mac.ObserveDistributed(name, func(string) { w.front() })
	return h.open(w)
}

// newWindow is the part of a window that is not drawn yet: its session and lock.
func (h *Host) newWindow(v host.View, args []string, inst *fileLock) *Window {
	open := h.openOther
	if h.Role == RoleAll && !h.Legacy {
		open = h.OpenProgress // one host for every window: in this process
	}
	w := &Window{
		View: v, Args: args, LockID: h.LockID, Files: h.Files, Log: h.Log, h: h, instance: inst,
		Session: host.NewSession(host.SessionOptions{
			Args: args, Version: h.version, Daemon: h.daemon, Log: h.Log, ThemePath: h.Files.Theme, OpenWindow: open,
		}),
	}
	h.windows = append(h.windows, w)
	if v.IsProgress() {
		h.progress[v.Progress] = w
	} else {
		h.main = w
	}
	return w
}

// open draws w (RunOptions.Open); a failure, a panic too, lets go of what it holds, the
// window Open made included.
func (h *Host) open(w *Window) (err error) {
	before := map[*mac.Window]bool{}
	for _, ww := range h.app.Windows() {
		before[ww] = true
	}
	outer := h.opening
	h.opening = w
	defer func() {
		h.opening = outer
		if v := recover(); v != nil {
			err = fmt.Errorf("glue: panic while opening the window: %v\n%s", v, debug.Stack())
		}
		if err != nil {
			for _, ww := range h.app.Windows() {
				if !before[ww] {
					ww.Discard()
				}
			}
			w.end()
		}
	}()
	ww, err := h.run.Open(h.app, w)
	if err == nil && ww == nil {
		err = errorf("Open returned no window")
	}
	if err != nil {
		return err
	}
	w.win = ww
	if !h.noActivate {
		// dock_darwin.m at each window's launch: ask for the activation as macOS 14+
		// expects, and put the window in front of other apps' when it is not granted.
		mac.RequestActivation()
		mac.RaiseIfInactive(0.5)
	}
	return nil
}

// OpenProgress opens (or brings to the front) the progress window of download id from any
// goroutine (a window's own request, in a process that hosts every window).
func (h *Host) OpenProgress(id int64) error {
	a := h.app
	if a == nil {
		return ErrClosed
	}
	a.Post(func() { h.Request([]string{"--" + host.ProgressFlag, strconv.FormatInt(id, 10)}) })
	return nil
}

func (h *Host) windowOf(ww *mac.Window) *Window {
	for _, w := range h.windows {
		if w.win == ww {
			return w
		}
	}
	return nil
}

// windowMade is the App's: the window Open is making is w's from now on, before its root is
// attached, so that a panic in Attach closes it (panicked) and its close lets w go (a
// request a modal loop inside Open lets in is that request's own: opening nests).
func (h *Host) windowMade(ww *mac.Window) {
	if w := h.opening; w != nil && w.win == nil {
		w.win = ww
	}
}

// windowClosed is the App's: w's window is gone, so its part is let go.
func (h *Host) windowClosed(ww *mac.Window) {
	if w := h.windowOf(ww); w != nil {
		w.end()
	}
}

// panicked is the App's: a panic in a window's call closes that window, and only that one.
func (h *Host) panicked(v any) {
	h.Log.Error("a window failed", "error", fmt.Sprintf("%v\n%s", v, debug.Stack()))
	if h.app == nil {
		return
	}
	if ww := h.app.PanicWindow(); ww != nil && h.windowOf(ww) != nil {
		ww.Close() // no CloseEvent: no state is stored from a failed window
	}
}

// menuCommand carries out a command of the menu bar. Quit and Close Window close the key
// window as its own close does (today each window was a process of its own, which they
// ended); Settings… goes to the main window.
func (h *Host) menuCommand(c mac.MenuCommand, ww *mac.Window) {
	switch c {
	case mac.MenuSettings:
		if w := h.main; w != nil && w.menu != nil {
			w.menu("settings")
		}
	case mac.MenuQuit, mac.MenuCloseWindow:
		for _, w := range menuCloseTargets(c, h.windowOf(ww), h.main, h.windows) {
			w.command(kit.WindowClose)
		}
	}
}

// menuCloseTargets are the windows Quit or Close Window (c) closes: the key window key,
// else, when no window is key, main or visible (every one minimised), what today's process
// of the active app would have ended: the main window, else every progress window (Quit) or
// the latest one (Close Window).
func menuCloseTargets(c mac.MenuCommand, key, main *Window, windows []*Window) []*Window {
	if c != mac.MenuQuit && c != mac.MenuCloseWindow {
		return nil
	}
	switch {
	case key != nil:
		return []*Window{key}
	case main != nil:
		return []*Window{main}
	case len(windows) == 0:
		return nil
	case c == mac.MenuQuit:
		return slices.Clone(windows)
	}
	return []*Window{windows[len(windows)-1]}
}

// terminate is the system's request to quit (logout, restart, shut down, the Dock's Quit):
// as today's window does, each window's session shuts down (its prefs written) and then the
// termination goes on. Like today, the windows do not store their size here.
func (h *Host) terminate(reply func(ok bool)) {
	h.Log.Info("quit requested by the system")
	h.ending = true
	var sessions []*host.Session
	for _, w := range slices.Clone(h.windows) {
		if w.prefs != nil {
			if err := w.prefs.Flush(); err != nil {
				h.Log.Warn("the prefs at the end of the session", "error", err)
			}
		}
		sessions = append(sessions, w.Session)
	}
	go func() {
		var wg sync.WaitGroup
		for _, s := range sessions {
			wg.Add(1)
			go func() { defer wg.Done(); s.Close() }()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		reply(true)
	}()
}

// CloseAll closes every window as its own close does (the main window stores its size).
func (h *Host) CloseAll() {
	for _, w := range slices.Clone(h.windows) {
		w.command(kit.WindowClose)
	}
}

// ForeignClose: macOS has no other program's WM_CLOSE (the system's quit is terminate).
func (w *Window) ForeignClose() bool { return false }

// SetMenu sets what the menu bar's commands for this window do (the main window's
// views/app.Main.MenuCommand, run on its UI thread).
func (w *Window) SetMenu(f func(command string)) { w.menu = f }

// front shows w's window again in front (a later start asked for it), and activates.
func (w *Window) front() {
	if w.win == nil {
		return
	}
	w.win.BringToFront(!w.h.noActivate)
}

func (w *Window) command(c kit.WindowCommand) {
	if w.win != nil {
		w.win.Command(c)
	}
}

// Defer adds f to what is let go once w's window has closed, the last added first.
func (w *Window) Defer(f func()) { w.defers = append(w.defers, f) }

// end lets go of w: its defers, its glue, its session and its lock.
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
	if w.observe != nil {
		w.observe()
	}
	w.instance.Close()
	h := w.h
	h.windows = slices.DeleteFunc(h.windows, func(o *Window) bool { return o == w })
	if h.main == w {
		h.main = nil
	}
	if h.progress[w.View.Progress] == w {
		delete(h.progress, w.View.Progress)
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

// FirstFrame tells the host w drew its first frame (nothing to trim on macOS).
func (w *Window) FirstFrame() {
	if w != nil {
		w.shown = true
	}
}

// OpenPrefs opens the window's ui-prefs.json (see the Windows host's).
func (w *Window) OpenPrefs() (f *prefs.File, err error) {
	if w.Files.Prefs == "" {
		return nil, prefs.ErrNoPath
	}
	f, err = prefs.Open(w.Files.Prefs, prefs.Options{Import: webKitImport(w.h.webview2, w.Files.Prefs), Log: w.Log, Poll: prefsPoll(w.Files)})
	if err == nil {
		w.prefs = f
	}
	return f, err
}

// webKitImport starts a new prefs file with what today's Mac window (WebKit) kept in its
// localStorage: from dir (Config.WebView2Dir) when set, else from ~/Library/WebKit, but
// only for this user's own prefs file (a test's or another data folder's never reads the
// user's WebKit data).
func webKitImport(dir, path string) prefs.Importer {
	if dir == "" && path != prefs.DefaultPath() {
		return nil
	}
	return prefs.WebKitImport(dir)
}

// prefsPoll is the prefs file's own poll [mac e2e]: none when the window's file watch
// (followFiles: a kqueue on the folder, else its own poll) reloads it, as it does whenever
// ui-prefs.json lies in the folder it watches; else prefs' default. Each window polling the
// file 5 times a second kept the progress app awake (about 1% CPU with 5 idle windows).
func prefsPoll(f Files) time.Duration {
	dir := filepath.Dir(f.Theme)
	if f.Theme == "" {
		dir = filepath.Dir(f.Prefs)
	}
	if f.Prefs != "" && filepath.Dir(f.Prefs) == dir {
		return -1
	}
	return 0
}

// UIStorage is s for the window's stores: notifications on the UI thread once attached.
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

// NewTheme is the window's theme store (see the Windows host's); System follows macOS's
// appearance.
func (w *Window) NewTheme(storage state.Storage) *state.Theme {
	w.theme = newThemeLink(storage, w.Files.Theme, w.Session.SaveTheme)
	t := state.NewTheme(state.ThemeOptions{Storage: w.theme, SystemDark: mac.SystemDark})
	w.theme.attach(t)
	return t
}

// Detach lets the window's glue go once its window has closed.
func (w *Window) Detach() {
	if w == nil || w.glue == nil {
		return
	}
	w.glue.close()
	w.glue = nil
}
