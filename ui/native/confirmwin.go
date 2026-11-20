//go:build windows || (darwin && cgo)

package main

import (
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/views"
	"downex/ui/views/confirmwin"
)

// openConfirm opens the standalone Confirm download window (docs/DESKTOP-SPEC.md §1) on a: a
// download the user just started in the browser is confirmed in a compact window of its own,
// with the system's frame and caption in the app's theme, and the main window is not shown.
// It holds the downloads' stores (the dialog's queues, categories and settings) and shows the
// "--confirm <id>" requests one at a time, oldest first, then closes. What it holds is let go
// by b.later's functions once it has closed.
func openConfirm(a *osApp, b *backend, o options) (*osWindow, error) {
	userLocale()
	spec := b.view.Window()
	stores := state.NewMain(state.MainOptions{
		Daemon: b.daemon, Events: b.events, Files: b.files, CopyText: b.platform.CopyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock}, Call: b.call,
		Collator: userLocale(), Storage: b.storage,
	})
	theme := &views.Theme{State: b.theme, Fixed: o.fixed, SystemDark: o.systemDark}
	scope := widgets.NewScope(theme)
	scope.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	minSize := kit.Size{W: kit.DIP(spec.MinWidth), H: kit.DIP(spec.MinHeight)}
	var va *views.App
	kw := kit.NewWindow(scope, kit.WindowOptions{
		Title:       spec.Title,
		MinSize:     minSize,
		Palette:     theme.Palette,
		FrameColors: theme.CaptionFrame,
	})
	va, err := views.NewApp(views.Options{
		Window: kw, Daemon: b.daemon, Platform: b.platform, Main: stores, Storage: b.storage,
		Clock: state.SystemClock, Caps: b.caps, Build: b.build, Demo: b.demo, Call: b.call, Log: b.log,
		FixedTheme: o.fixed, SystemDark: o.systemDark, NoToasts: true,
	})
	if err != nil {
		stores.Dispose()
		return nil, err
	}
	// The theme store goes to the App once it is made, as in a progress window: the caption
	// keeps the system frame's colours (themeChanged below).
	va.Theme = b.theme
	va.WidgetTheme = theme
	screen := confirmwin.New(va, confirmwin.Options{
		// The window's own "--confirm <id>", asked for at once: the list may be read before
		// b.confirm's request runs, and the window must not close on an empty queue then.
		IDs:    []int64{b.view.Confirm},
		OnShow: b.gw.ConfirmShown,
		Close:  func() { _ = kw.Post(kw.Close) },
	})
	scope.Add(screen)
	b.later(stores.Dispose)
	b.later(va.Dispose)
	b.later(screen.Dispose)
	if b.theme != nil {
		va.Watch(&b.theme.Changes, func() {
			t := va.Tokens()
			kw.SetPalette(t.Palette())
			if h := kw.Host(); h != nil {
				h.SetFrameColors(t.CaptionFrame())
			}
			kw.InvalidateRect(kit.RectAt(kit.Point{}, kw.Size()))
		})
	}
	size := kit.Size{W: kit.DIP(spec.Width), H: kit.DIP(spec.Height)}
	if o.size != (kit.Size{}) {
		size = o.size
	}
	w, err := openWindow(a, kw, windowSpec{Title: spec.Title, Size: size, MinSize: minSize, Dialog: true}, o, b)
	if err != nil {
		return nil, err
	}
	firstFrame := b.since()
	if b.shown != nil {
		b.shown()
	}
	if b.attach != nil {
		b.attach(w, kw) // the folder picker, later starts, the theme followed
	}
	stores.Start()
	if b.start != nil {
		b.start()
	}
	if b.confirm != nil {
		b.confirm(func(ids []int64) { va.UI(func() { screen.Request(ids) }) })
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
