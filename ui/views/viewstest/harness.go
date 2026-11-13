// Package viewstest is the views' test harness: an App over a headless window (kittest's loop
// on hostcaptest's host: popups, native fields of every kind and tooltips, faked), the
// simulated daemon of ui/state/scene in the reference's state, and the main window's stores
// started on it; it renders a screen offscreen (Windows: Direct2D and DirectWrite into a WIC
// bitmap) at a reference's width, scale and theme, and compares it with the 0.9.2 references
// (refs.go): reference | native | difference side by side, with a box-delta report from the
// references' index.
package viewstest

import (
	"context"
	"testing"
	"time"

	"downex/ui/core/format"
	"downex/ui/core/locale"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
	"downex/ui/views"
)

// Options are a harness's window and scene: a reference's size, scale, text size and theme.
type Options struct {
	// Width and Height are the window's client size in CSS px (DIPs); 1200 × 760 when 0.
	Width, Height int
	// Scale is the device scale (1 or 1.25); 1 when 0.
	Scale float32
	// Font is the root text size in px (16 or 20, the references' "root text px"); 16 when 0.
	Font int
	// Theme is "light" or "dark" ("" light). The system's mode is taken as light, as in the
	// references, whatever this PC's is.
	Theme string
	// Fixture is a reference state of scene.Fixtures ("remove", "confirm-file", …): its scene
	// options are used. "" is the main window's Studio scene.
	Fixture string
	// Scene changes the scene's options (after the fixture's).
	Scene func(*scene.Options)
	// Now is the clocks' start (zero: RefNow).
	Now time.Time
	// Real lays text out with the platform's text engine (DirectWrite on Windows), as
	// rendering needs; otherwise kittest's fake metrics. Render turns it on.
	Real bool
	// Frameless gives the window a custom title bar (Capabilities.CustomTitleBar), as the
	// references' demo had; nil is true.
	Frameless *bool
	// NoStart leaves the stores unstarted.
	NoStart bool
	// WebKit draws as the macOS window does (kit.Environment.WebKit).
	WebKit bool
	// Connected shows the simulated daemon as a real one (no demo mark in the status bar), for
	// screenshots meant for people (the README's).
	Connected bool
	// Daemon, when set, is the daemon in place of the scene (ui/state/statetest's fakeApi
	// port, for a test's own downloads); the platform is then a FakePlatform.
	Daemon *statetest.Daemon
}

// RefNow is the reference run's time (2026-11-02, the main window shots' capture), the
// harness's default clock.
var RefNow = time.Date(2026, 11, 2, 23, 32, 45, 0, time.UTC)

// Harness is an App over a headless window and the simulated daemon.
type Harness struct {
	T    testing.TB
	Opts Options

	App  *views.App
	Mock *scene.Mock // nil with Options.Daemon
	Main *state.Main
	// Platform is the App's platform: a *ScenePlatform over the scene, or a *FakePlatform.
	Platform views.Platform
	Storage  *statetest.Memory

	Window *kit.Window
	// Root is the window's content: a widgets.Scope of the App's theme that holds the screen
	// (Show).
	Root  *widgets.Scope
	Host  *hostcaptest.Host
	Loop  *kittest.Loop
	Clock *kittest.Clock
	// StoreClock is the stores' clock; Advance moves both.
	StoreClock *statetest.Clock
	// Calls records what the App's Openers and Actions were asked (Record fills them).
	Calls *Calls

	release func()
	closed  bool
}

// realHost is the hostcaptest host with the platform's resources, for a layout that is the
// real one (DirectWrite's text).
type realHost struct {
	*hostcaptest.Host
	res kit.Resources
}

func (h *realHost) Resources() kit.Resources { return h.res }

// New makes a harness: the scene, the stores (started unless NoStart), the App, a window of the
// size asked for, and the openers recorded (Calls).
func New(t testing.TB, o Options) *Harness {
	t.Helper()
	if o.Width == 0 {
		o.Width, o.Height = 1200, 760
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if o.Font == 0 {
		o.Font = 16
	}
	if o.Theme == "" {
		o.Theme = "light"
	}
	if o.Now.IsZero() {
		o.Now = RefNow
	}
	h := &Harness{T: t, Opts: o}
	useRefLocale(t)
	h.Clock = kittest.NewClock() // the window's motion clock; wall time is the stores' (App.Clock)
	h.StoreClock = statetest.NewClock(o.Now.UnixMilli())

	frameless := o.Frameless == nil || *o.Frameless
	var (
		daemon   host.Daemon
		events   state.Events
		files    state.FileActions
		copyText func(context.Context, string) error
		caps     host.Capabilities
		build    host.BuildInfo
	)
	h.Storage = statetest.NewMemory(nil)
	if o.Daemon != nil {
		fp := &FakePlatform{}
		h.Platform, daemon, events, files, copyText = fp, o.Daemon, &o.Daemon.Events, fp, fp.CopyText
		caps = host.Capabilities{OpenWith: true, Hibernate: true, WakeTimers: false, CustomTitleBar: frameless}
	} else {
		h.newScene(o, frameless)
		sp := &ScenePlatform{Mock: h.Mock}
		h.Platform, daemon, events, files, copyText = sp, h.Mock, h.Mock, sp, sp.CopyText
		startup := h.Mock.Startup()
		caps = host.Capabilities{OpenWith: startup.OpenWith, Hibernate: startup.Hibernate, WakeTimers: startup.WakeTimers, CustomTitleBar: startup.CustomTitleBar}
		if startup.Build != nil {
			build = *startup.Build
		}
	}

	sync := func(f func()) { f() }
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	h.Main = state.NewMain(state.MainOptions{
		Daemon: daemon, Events: events, Files: files, CopyText: copyText,
		Clock: h.StoreClock, Frames: state.TimerFrames{Clock: h.StoreClock}, Call: call, Go: sync,
		Storage: h.Storage,
	})
	h.buildApp(o, daemon, caps, build, call, sync)
	if !o.NoStart {
		h.Main.Start()
		h.Main.Downloads.RefreshStatus()
		h.Main.LoadSettings()
		h.Main.Queues.Load()
		h.Main.Tools.Load()
	}
	h.Settle()
	t.Cleanup(h.Close)
	return h
}

// The references' formatting locale: the page's language and time zone (index.json
// engine.page: en-GB, Asia/Baghdad), what today's window formats dates with there ("2 Nov").
const (
	RefLanguage = "en-GB"
	RefTimeZone = "Asia/Baghdad"
)

// useRefLocale formats with the references' locale for the rest of the test, as the real
// window formats with the user's (state.UseUserLocale); the process's default is en-US
// ("Nov 2"). A locale the test set itself before New is kept. The previous formatter comes
// back at Cleanup.
func useRefLocale(t testing.TB) {
	if !format.IsDefault() {
		return // the test chose a locale itself (a table of locales)
	}
	f, err := locale.New(RefLanguage, RefTimeZone)
	if err != nil {
		t.Logf("viewstest: the references' locale: %v", err)
		return
	}
	prev := format.Locale()
	format.SetLocale(f)
	t.Cleanup(func() { format.SetLocale(prev) })
}

// newScene makes the simulated daemon of a fixture state (or the Studio scene).
func (h *Harness) newScene(o Options, frameless bool) {
	t := h.T
	so := scene.Options{}
	if o.Fixture != "" {
		fx, ok := scene.Fixture(o.Fixture)
		if !ok {
			t.Fatalf("viewstest: no fixture %q", o.Fixture)
		}
		so = fx.Options()
	}
	so.Now = h.StoreClock.Now
	view := scene.Startup{OpenWith: true, Hibernate: true, WakeTimers: true, CustomTitleBar: frameless}
	if so.View != nil {
		view = *so.View
		view.CustomTitleBar = frameless
	}
	so.View = &view
	if o.Scene != nil {
		o.Scene(&so)
	}
	mock, err := scene.New(so)
	if err != nil {
		t.Fatalf("viewstest: the scene: %v", err)
	}
	h.Mock = mock
}

// buildApp makes the window and the App over the stores.
func (h *Harness) buildApp(o Options, daemon host.Daemon, caps host.Capabilities, build host.BuildInfo, call state.CallContext, goFn func(func())) {
	t := h.T
	theme := state.NewTheme(state.ThemeOptions{Storage: h.Storage})
	choice := host.ThemeLight
	if o.Theme == "dark" {
		choice = host.ThemeDark
	}
	theme.Set(choice)
	density := state.NewDensity(h.Storage)

	env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: o.Scale, Text: float32(o.Font) / 16}, HoverTime: 400 * time.Millisecond}
	env.WebKit = o.WebKit
	size := kit.Size{W: kit.DIP(o.Width), H: kit.DIP(o.Height)}
	var app *views.App
	h.Root = widgets.NewScope(nil)
	h.Root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	systemLight := false
	h.Window = kit.NewWindow(h.Root, kit.WindowOptions{
		Title: "Downex",
		Palette: func(env kit.Environment) kit.Palette {
			if app == nil {
				env.Dark = false
				return tokens.For(tokensChoice(o.Theme), env).Palette()
			}
			return app.WidgetTheme.Palette(env)
		},
		Now: h.Clock.Now, Scheduler: h.Clock,
		OnKey: func(e kit.KeyEvent) bool {
			if app == nil {
				return false
			}
			return app.Commands.HandleKey(e, views.KeyContextOf(app.Window))
		},
	})
	app, err := views.NewApp(views.Options{
		Window: h.Window, Daemon: daemon, Platform: h.Platform, Main: h.Main, Theme: theme, Density: density,
		Storage: h.Storage, Clock: h.StoreClock, Caps: caps, Build: build, Demo: h.Mock != nil && !o.Connected, Call: call, Go: goFn,
		SystemDark: &systemLight, StatusBarHeight: 32,
	})
	if err != nil {
		t.Fatalf("viewstest: NewApp: %v", err)
	}
	h.App = app
	h.Root.Theme = app.WidgetTheme
	h.Calls = Record(app)

	h.Host = hostcaptest.NewHost(size, env, h.Clock)
	var attach kit.Host = h.Host
	if o.Real {
		res, release, err := RealResources()
		if err != nil {
			t.Skipf("viewstest: no text engine here: %v", err)
		}
		h.release = release
		attach = &realHost{Host: h.Host, res: res}
	}
	h.Loop = &kittest.Loop{Host: h.Host.Host, Root: h.Window, Canvas: &kittest.Canvas{}, Clock: h.Clock, Refresh: time.Second / 60}
	h.Window.Attach(attach)
}

func tokensChoice(theme string) tokens.Choice {
	if theme == "dark" {
		return tokens.ChoiceDark
	}
	return tokens.ChoiceLight
}

// Show puts a screen in the window (in place of the one shown) and lets it settle.
func (h *Harness) Show(screen kit.Widget) {
	for _, c := range h.Root.Children() {
		h.Root.Remove(c.Widget())
	}
	if screen != nil {
		h.Root.Add(screen)
	}
	h.Settle()
}

// Settle runs what was posted and the frames asked for until the window is idle, the clock
// moving a refresh a step (at most 2 s of frames: a running animation ends or is cut).
func (h *Harness) Settle() {
	h.Loop.Settle(120)
	for _, p := range h.Host.Open() {
		p.Loop.Settle(120)
	}
}

// Advance moves both clocks by d (the stores' timers and the window's motion) and settles.
func (h *Harness) Advance(d time.Duration) {
	h.StoreClock.Advance(d)
	h.Loop.Run(d)
	h.Settle()
}

// Key presses k (down and up) with mods as the host sends it, and settles; it reports whether
// the window used the key down.
func (h *Harness) Key(k kit.Key, mods kit.Modifiers) bool {
	used := h.Loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: k, Mods: mods})
	h.Loop.Send(kit.KeyEvent{Action: kit.KeyUp, Key: k, Mods: mods})
	h.Settle()
	return used
}

// Click presses and releases the left button over e's centre, and settles.
func (h *Harness) Click(e *kit.Element) { h.ClickAt(e.WindowBounds().Center()) }

// ClickAt clicks at p (window DIPs).
func (h *Harness) ClickAt(p kit.Point) {
	h.Loop.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: p})
	h.Loop.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: p, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	h.Loop.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: p, Button: kit.ButtonLeft})
	h.Settle()
}

// Close disposes of the App, the stores and the scene, and detaches the window (Cleanup does
// it at the end of the test).
func (h *Harness) Close() {
	if h.closed {
		return
	}
	h.closed = true
	h.App.Dialogs.DismissAll()
	h.App.Dispose()
	h.Main.Dispose()
	if h.Mock != nil {
		h.Mock.Close()
	}
	h.Window.Detach()
	if h.release != nil {
		h.release()
	}
}
