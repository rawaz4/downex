//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/hostcap/hostcaptest"
	"downex/ui/kit/kittest"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/fixture"
)

// The main window's layers draw the chosen theme, not the System one: with Light chosen on a
// dark-mode PC, a dialog, the toasts and the notices outside the content take the light
// tokens through the window's content (the composition the preview runs: newMainContent and
// composeMain, the content wrapper of runMain). Before, the content was a plain element and
// every layer drew the dark System theme (the Confirm dialog dark, its scrim the dark .55
// black).
func TestLayersDrawTheChosenThemeOverADarkSystem(t *testing.T) {
	for _, c := range []struct {
		name   string
		choice tokens.Choice
		dark   bool // the system's apps
	}{
		{"light chosen, system dark", tokens.ChoiceLight, true},
		{"dark chosen, system light", tokens.ChoiceDark, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			mock, err := scene.New(scene.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			fp := &fixture.Platform{Mock: mock}
			theme := state.NewTheme(state.ThemeOptions{})
			defer theme.Close()
			clock := kittest.NewClock()
			call := func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Second)
			}
			sync := func(f func()) { f() }
			stores := state.NewMain(state.MainOptions{
				Daemon: mock, Events: mock, Files: fp, CopyText: fp.CopyText, Clock: state.SystemClock,
				Frames: state.TimerFrames{Clock: state.SystemClock}, Call: call, Go: sync,
			})
			defer stores.Dispose()
			fixed, systemDark := c.choice, c.dark
			vt := &views.Theme{State: theme, Fixed: &fixed, SystemDark: &systemDark}
			holder := newMainContent(vt)
			kw := kit.NewWindow(holder, kit.WindowOptions{Title: "Downex", Now: clock.Now, Scheduler: clock})
			va, m, err := composeMain(holder, views.Options{
				Window: kw, Daemon: mock, Platform: fp, Main: stores, Theme: theme, Density: state.NewDensity(nil),
				Clock: state.SystemClock, Caps: host.Capabilities{CustomTitleBar: true}, Demo: true, Call: call, Go: sync,
				FixedTheme: &fixed, SystemDark: &systemDark, StatusBarHeight: statusBarHeight,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer va.Dispose()
			defer m.Dispose()
			env := kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, Dark: c.dark}
			h := hostcaptest.NewHost(kit.Size{W: 1200, H: 760}, env, clock)
			loop := &kittest.Loop{Host: h.Host, Root: kw, Canvas: &kittest.Canvas{}, Clock: clock, Refresh: time.Second / 60}
			kw.Attach(h)
			defer kw.Detach()
			loop.Settle(120)

			if _, ok := kw.Content().(widgets.ThemeSource); !ok {
				t.Fatalf("the window's content %T is not a theme source", kw.Content())
			}
			d := va.NewDialog(views.ModalRemove, widgets.DialogOptions{Title: "Remove download?"})
			va.Dialogs.Show(d)
			va.Toast("Moved to Night")
			loop.Settle(120)
			want := tokens.For(c.choice, env)
			for _, l := range []struct {
				what string
				el   *kit.Element
			}{{"the dialog", d.Card()}, {"the toasts", va.ToastLayer.Plain().Base()}, {"the notices", va.ToastLayer.Notices().Base()}} {
				if l.el.Window() != kw {
					t.Fatalf("%s is not in the window", l.what)
				}
				got := widgets.ThemeOf(l.el).Tokens(env)
				if got.Surface != want.Surface || got.Ink != want.Ink || got.Dim != want.Dim {
					t.Errorf("%s draws surface %v, ink %v, scrim %v; the %v choice is %v, %v, %v",
						l.what, got.Surface, got.Ink, got.Dim, c.choice, want.Surface, want.Ink, want.Dim)
				}
			}
			va.Dialogs.DismissAll()
		})
	}
}
