//go:build windows

package app_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// TestRealMainWindow composes the main window in a real Win32 window drawn with Direct2D and
// DirectWrite (a custom frame, shown without taking the focus) over the Studio scene in live
// mode, with the wall clock: the chrome's boxes against the reference at the window's own
// scale, the frame's parts, the live work, and nothing at all once the scene stops. It opens a
// window on the desktop, so it runs only with DOWNEX_SHELL_REAL=1.
func TestRealMainWindow(t *testing.T) {
	if os.Getenv("DOWNEX_SHELL_REAL") != "1" {
		t.Skip("set DOWNEX_SHELL_REAL=1 to open a real window")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	refs, refErr := viewstest.LoadRefs()
	mock, err := scene.New(scene.Options{AutoTick: true, TickMs: 250})
	if err != nil {
		t.Fatal(err)
	}
	sp := &viewstest.ScenePlatform{Mock: mock}
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	main := state.NewMain(state.MainOptions{
		Daemon: mock, Events: mock, Files: sp, CopyText: sp.CopyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock}, Call: call,
	})
	storage := statetest.NewMemory(nil)
	theme := state.NewTheme(state.ThemeOptions{Storage: storage})
	theme.Set(host.ThemeSystem)
	density := state.NewDensity(storage)
	light := false

	var a *views.App
	var m *app.Main
	holder := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}}
	kw := kit.NewWindow(holder, kit.WindowOptions{
		Title: "Downex main window gate", MinSize: kit.Size{W: 900, H: 600},
		Palette: func(env kit.Environment) kit.Palette {
			if a == nil {
				return tokens.For(tokens.ChoiceLight, env).Palette()
			}
			return a.WidgetTheme.Palette(env)
		},
		OnKey: func(e kit.KeyEvent) bool { return m != nil && m.HandleKey(e) },
		OnWindowState: func(s kit.WindowState) {
			if m != nil {
				m.Shell.TitleBar().SetMaximized(s.Maximized)
			}
		},
	})
	a, err = views.NewApp(views.Options{
		Window: kw, Daemon: mock, Platform: sp, Main: main, Theme: theme, Density: density,
		Storage: storage, Clock: state.SystemClock, Call: call, SystemDark: &light, StatusBarHeight: 32,
		Caps: host.Capabilities{CustomTitleBar: true, OpenWith: true}, Demo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err = app.New(a, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	holder.Add(m.Root())

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wa, err := win.NewApp(win.AppOptions{ClassName: "DownexShellLaneGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	if _, err := wa.NewWindow(kw, win.WindowOptions{Title: "Downex main window gate", Size: kit.Size{W: 1200, H: 760}, MinSize: kit.Size{W: 900, H: 600}, NoActivate: true, CustomFrame: true}); err != nil {
		t.Fatal(err)
	}
	onUI := func(f func()) {
		done := make(chan struct{})
		if kw.Post(func() { f(); close(done) }) != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	stats := func() (s kit.Stats) { onUI(func() { s = kw.Stats() }); return }
	var report []string
	go func() {
		defer func() { _ = kw.Post(kw.Close) }()
		main.Start()
		time.Sleep(3 * time.Second)
		// the chrome where the reference has it, at this window's scale
		var scale float32
		var deltas []viewstest.Delta
		var frame []string
		var confirming bool
		onUI(func() {
			scale = kw.Environment().Scale.PixelsPerDIP
			_, confirming = m.Confirming()
			if refErr == nil {
				if shot, ok := refs.Find("main", "main", 1200, 760, float64(scale), 16, "light"); ok {
					deltas = viewstest.BoxDeltas(shot, chromeProbes(m))
				}
			}
			tb := m.Shell.TitleBar()
			for name, p := range map[string]kit.Point{
				"brand": tb.Logo().WindowBounds().Center(), "search": tb.Parts()["search"].El.WindowBounds().Center(),
				"close": kit.Pt(1199, 10), "minimise": kit.Pt(1200-46*3+10, 22),
			} {
				frame = append(frame, fmt.Sprintf("%s=%v", name, kw.FrameHit(p)))
			}
		})
		worst, found := 0.0, 0
		for _, d := range deltas {
			if d.Found {
				found++
				worst = max(worst, float64(d.Max()))
				if d.Max() > 1 {
					t.Errorf("real window: %s", d)
				}
			}
		}
		report = append(report, fmt.Sprintf("scale %.2f; the confirmation opened by itself: %v; %d chrome boxes against the reference, largest delta %.2f DIP; frame %v", scale, confirming, found, worst, frame))
		a0 := stats()
		time.Sleep(5 * time.Second)
		b0 := stats()
		report = append(report, fmt.Sprintf("live 5 s: %.1f paints/s, %.1f layouts/s, %.1f frames/s, %.0f DIP² painted/s",
			float64(b0.Paints-a0.Paints)/5, float64(b0.Layouts-a0.Layouts)/5, float64(b0.Frames-a0.Frames)/5, (b0.PaintedArea-a0.PaintedArea)/5))
		mock.Close() // the simulation stops
		time.Sleep(4 * time.Second)
		if os.Getenv("DOWNEX_SHELL_REAL_TRACE") != "" {
			var changes int
			stop := main.Downloads.Changes.Subscribe(func() { changes++ })
			for i := 0; i < 5; i++ {
				x := stats()
				time.Sleep(time.Second)
				y := stats()
				var stale, dlg bool
				onUI(func() { stale, dlg = a.Stale(), a.Dialogs.DialogOpen() })
				report = append(report, fmt.Sprintf("trace %d: stale %v dialog %v store changes %d; %d paints %d frames %d layouts %d anims %d timers %d frame requests", i, stale, dlg, changes, y.Paints-x.Paints, y.Frames-x.Frames, y.Layouts-x.Layouts, y.Animations-x.Animations, y.Timers-x.Timers, y.FrameRequests-x.FrameRequests))
			}
			stop()
		}
		a1 := stats()
		time.Sleep(10 * time.Second)
		b1 := stats()
		report = append(report, fmt.Sprintf("idle 10 s: %d paints, %d frames, %d layouts", b1.Paints-a1.Paints, b1.Frames-a1.Frames, b1.Layouts-a1.Layouts))
		if b1.Paints != a1.Paints || b1.Layouts != a1.Layouts {
			t.Errorf("idle window worked: %d paints, %d layouts", b1.Paints-a1.Paints, b1.Layouts-a1.Layouts)
		}
	}()
	if err := wa.Run(); err != nil {
		t.Fatal(err)
	}
	m.Dispose()
	a.Dispose()
	main.Dispose()
	for _, l := range report {
		t.Log(l)
	}
}
