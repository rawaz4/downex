//go:build windows || (darwin && cgo)

package main

import (
	"sync"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/native/glue"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/downloads"
	"downex/ui/views/progress"
	"downex/ui/views/settings"
	"downex/ui/views/shell"
)

// statusBarHeight is the main window's --statusbar-h (the toasts sit 16 px above it).
const statusBarHeight kit.DIP = 32

// openMain opens the main window (App.svelte) on a: the stores, the views.App and the
// composition (views/app) in a kit window, opened in a Win32 window with the custom frame at
// the size it was left at, then connected to the window's glue (the pickers, the risky-file
// box, dropped links, the theme and prefs files) and to downexd. What it holds is let go by
// b.later's functions once it has closed (or when it fails to open).
func openMain(a *osApp, b *backend, o options) (*osWindow, error) {
	spec := b.view.Window()
	var (
		err     error
		va      *views.App
		m       *app.Main
		w       *osWindow
		g       *glue.Glue
		last    host.WindowState
		hasLast bool
	)
	if b.statePath != "" {
		last, hasLast = host.LoadWindowState(b.statePath)
	}
	vt := &views.Theme{State: b.theme, Fixed: o.fixed, SystemDark: o.systemDark}
	widgetTheme := func() *views.Theme {
		if va != nil {
			return va.WidgetTheme
		}
		return vt
	}
	density := state.NewDensity(b.storage)
	b.later(density.Close)
	var quit func() // closes the main window (set once it exists)
	var updated func(*downexv1.UpdateInfo)
	if b.updated != nil {
		updated = func(*downexv1.UpdateInfo) {
			b.updated(func() {
				if quit != nil {
					quit()
				}
			})
		}
	}
	stores := state.NewMain(state.MainOptions{
		Daemon: b.daemon, Events: b.events, Files: b.files, CopyText: b.platform.CopyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock}, Call: b.call,
		Collator: userLocale(), Storage: b.storage, UpdateInstalled: updated,
	})
	holder := newMainContent(vt)
	frame := mainFrame()
	minSize := mainMinContent(spec, frame)
	kw := kit.NewWindow(holder, kit.WindowOptions{
		Title:       spec.Title,
		MinSize:     minSize,
		Palette:     func(env kit.Environment) kit.Palette { return widgetTheme().Palette(env) },
		FrameColors: func(env kit.Environment) kit.FrameColors { return widgetTheme().MainFrame(env) },
		OnKey:       func(e kit.KeyEvent) bool { return m != nil && m.HandleKey(e) },
		OnWindowState: func(s kit.WindowState) {
			if m != nil {
				m.Shell.TitleBar().SetMaximized(s.Maximized)
			}
		},
		OnEnvironment: func(env kit.Environment) {
			g.Environment(env) // the System theme follows the system's light or dark apps
			if m != nil {
				m.Shell.TitleBar().ThemeChanged()
			}
		},
		// Every close but -exit-after's (the title bar's Close, Alt+F4, WM_CLOSE to the window
		// or to its hand-off window) stores the size here, while the window can be measured.
		OnClose: func() bool {
			if b.statePath != "" && w != nil {
				if err := host.SaveWindowState(b.statePath, closingState(w, last, hasLast, spec)); err != nil {
					b.log.Warn("cannot store the window's size", "error", err)
				}
			}
			return false
		},
	})
	quit = func() { _ = kw.Post(kw.Close) }
	b.post = kw.Post
	va, m, err = composeMain(holder, views.Options{
		Window: kw, Daemon: b.daemon, Platform: b.platform, Main: stores, Theme: b.theme, Density: density,
		Storage: b.storage, Clock: state.SystemClock, Caps: b.caps, Build: b.build, Demo: b.demo, Call: b.call,
		Log: b.log, FixedTheme: o.fixed, SystemDark: o.systemDark, StatusBarHeight: statusBarHeight,
	})
	if err != nil {
		return nil, err
	}
	b.later(va.Dispose)
	b.later(m.Dispose)

	ws := windowSpec{
		Title: spec.Title, Size: kit.Size{W: float32(spec.Width), H: float32(spec.Height)}, MinSize: minSize,
		CustomFrame: spec.Frameless,
	}
	switch {
	case o.size != (kit.Size{}):
		ws.Size = o.size
	case hasLast:
		ws.Size = mainStartContent(last, spec, frame)
		ws.Maximized = last.Maximised
	}
	w, err = openWindow(a, &mainRoot{Window: kw, m: m}, ws, o, b)
	if err != nil {
		return nil, err
	}
	firstFrame := b.since()
	if b.shown != nil {
		b.shown()
	}
	m.Shell.TitleBar().SetMaximized(w.WindowState().Maximized)
	if b.attach != nil {
		g = b.attach(w, kw)
	}
	if g != nil {
		if err := g.EnableDrop(m); err != nil {
			b.log.Warn("links cannot be dropped on the window", "error", err)
		}
	}
	stores.Start()
	b.later(stores.Dispose)
	if b.start != nil {
		b.start()
	}
	if b.mainStarted != nil {
		b.mainStarted()
	}
	if b.gw != nil {
		// The standalone Confirm download window confirms the browser's downloads: this
		// window does not show them too (docs/DESKTOP-SPEC.md §1).
		m.SetConfirmElsewhere(b.gw.ConfirmWindowOpen)
		b.gw.SetConfirmHooks(m.ConfirmShownElsewhere, m.ConfirmElsewhereChanged)
	}
	if b.confirm != nil {
		b.confirm(m.RequestConfirm)
	}
	if b.renew != nil {
		b.renew(m.RequestRenew)
	}
	if b.menu != nil {
		b.menu(func(c string) { _ = kw.Post(func() { m.MenuCommand(c) }) })
	}
	if o.report != "" {
		writeReport(o.report, w, firstFrame)
	}
	b.later(startStats(o.stats, kw))
	if o.exitAfter > 0 {
		time.AfterFunc(o.exitAfter, func() { _ = kw.Post(kw.Close) })
	}
	return w, nil
}

// releaseIcons releases the icons every window of a drew with (a's windows share them).
func releaseIcons(a *osApp) {
	res := a.Resources()
	widgets.ReleaseIcons(res)
	shell.ReleaseIcons(res)
	downloads.ReleaseIcons(res)
	settings.ReleaseIcons(res)
	progress.ReleaseIcons(res)
}

// userLocale sets numbers, sizes and times in the user's locale, as today's main.ts sets
// them for every window, once for the process (the formatter is the process's: a later
// window does not set it again under the windows already open), and is the collator of the
// main window's lists.
var userLocale = sync.OnceValue(state.UseUserLocale)

// newMainContent is the main window's kit content: a theme scope that holds the composition.
// The window's layers (dialogs and their scrim, toasts, notices, the palette) are not inside
// the content, so widgets.ThemeOf gives them the content's theme: a plain element there made
// every layer draw the System theme whatever the choice (a dark dialog under Light on a
// dark-mode PC). t is the theme until composeMain sets the App's.
func newMainContent(t widgets.Theme) *widgets.Scope {
	s := widgets.NewScope(t)
	s.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	return s
}

// composeMain makes the views.App over o (o.Window's content is holder) and the composition
// (views/app) in holder, under the App's theme. The caller disposes of both.
func composeMain(holder *widgets.Scope, o views.Options) (*views.App, *app.Main, error) {
	va, err := views.NewApp(o)
	if err != nil {
		return nil, nil, err
	}
	holder.Theme = va.WidgetTheme
	m, err := app.New(va, app.Options{})
	if err != nil {
		va.Dispose()
		return nil, nil, err
	}
	holder.Add(m.Root())
	return va, m, nil
}

// mainRoot is the kit window as the backend hosts it, hearing the window's activation: the
// settings are read again when it gets the focus (App.svelte's onWindowFocus).
type mainRoot struct {
	*kit.Window
	m *app.Main
}

// Event is kit.Root's.
func (r *mainRoot) Event(e kit.Event) bool {
	used := r.Window.Event(e)
	if f, ok := e.(kit.FocusEvent); ok && f.In {
		r.m.WindowFocused()
	}
	return used
}

// closingState is what the closing main window stores: its placement (its restored size and
// whether it is maximised), or, when that cannot be read, today's rule from its size and
// state.
func closingState(w *osWindow, last host.WindowState, hasLast bool, spec host.WindowSpec) host.WindowState {
	if pl, err := w.Placement(); err == nil {
		return mainPlacedState(pl.Normal, pl.Maximized, spec, mainFrame())
	}
	s, st, f := w.Size(), w.WindowState(), mainFrame()
	return host.ClosingState(last, hasLast, spec.Width, spec.Height, int(s.W+f.W+0.5), int(s.H+f.H+0.5), st.Maximized, st.Minimized)
}

// The main window's sizes and its stored state, with frame what the system's title bar
// and borders add to the content (mainFrame): none on Windows, where the window draws its
// own title bar and the stored size and the minimum are the client area's. On macOS today's
// window (Wails) gave the 900 × 600 minimum to the frame and stored the frame's size
// (WindowGetSize) in ui-window.json: the content may shrink to 900 × (600 − title bar), and
// a stored size, written by either window, is the frame's.

// mainMinContent is the content area's minimum.
func mainMinContent(spec host.WindowSpec, frame kit.Size) kit.Size {
	return kit.Size{W: float32(spec.MinWidth) - frame.W, H: float32(spec.MinHeight) - frame.H}
}

// mainStartContent is the content size to open at for the stored state last.
func mainStartContent(last host.WindowState, spec host.WindowSpec, frame kit.Size) kit.Size {
	sw, sh := last.StartSize(spec.MinWidth, spec.MinHeight)
	return kit.Size{W: float32(sw) - frame.W, H: float32(sh) - frame.H}
}

// mainPlacedState is what to store for the restored content size normal.
func mainPlacedState(normal kit.Size, maximized bool, spec host.WindowSpec, frame kit.Size) host.WindowState {
	return host.PlacedState(normal.W+frame.W, normal.H+frame.H, maximized, spec.MinWidth, spec.MinHeight)
}
