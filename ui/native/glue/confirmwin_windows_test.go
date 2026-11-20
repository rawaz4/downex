//go:build windows

package glue

import (
	"slices"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/win"
)

// "--confirm <id> --raise" (a download the user just started in the browser) opens the
// standalone Confirm download window only: the main window is neither opened nor shown, a
// later request goes to the same window, and the main window, when it is open, hears of it.
func TestConfirmRaiseOpensOnlyTheConfirmWindow(t *testing.T) {
	t.Run("no main window", func(t *testing.T) {
		h, _ := inprocHost(t, t.TempDir(), "--confirm", "5", "--raise")
		var views []host.View
		var confirmIDs [][]int64
		err := h.Run(RunOptions{
			App: win.AppOptions{ClassName: "DownexGlueTestConfirm1"},
			Open: func(a *win.App, w *Window) (*win.Window, error) {
				views = append(views, w.View)
				ww, kw, err := labelWindow(a, "window")
				if err != nil {
					return nil, err
				}
				if w.View.IsConfirm() {
					w.Session.Confirmations().Ready(func(ids []int64) { confirmIDs = append(confirmIDs, ids) })
					// A later browser add asks again: the same window takes it.
					_ = kw.Post(func() { h.Request([]string{"--confirm", "6", "--raise"}) })
				}
				time.AfterFunc(400*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				return ww, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []host.View{{Confirm: 5}}; !slices.Equal(views, want) {
			t.Fatalf("opened %+v, want only the confirm window %+v", views, want)
		}
		if want := [][]int64{{5}, {6}}; !slices.EqualFunc(confirmIDs, want, slices.Equal) {
			t.Fatalf("the confirm window was asked for %v, want %v", confirmIDs, want)
		}
	})

	// Start download in a process that holds the confirm window alone: the window asks for
	// the progress window and then closes, and the progress window opens before Run returns
	// (the process does not end with the confirm window, its last one).
	t.Run("start as the last window", func(t *testing.T) {
		h, _ := inprocHost(t, t.TempDir(), "--confirm", "5", "--raise")
		var views []host.View
		err := h.Run(RunOptions{
			App: win.AppOptions{ClassName: "DownexGlueTestConfirm3"},
			Open: func(a *win.App, w *Window) (*win.Window, error) {
				views = append(views, w.View)
				ww, kw, err := labelWindow(a, "window")
				if err != nil {
					return nil, err
				}
				if w.View.IsConfirm() {
					// As confirmwin.Screen does on Start download: the request first, then the close.
					_ = kw.Post(func() {
						if err := w.Session.OpenProgressWindow(5); err != nil {
							t.Error(err)
						}
						_ = kw.Post(kw.Close)
					})
				} else {
					time.AfterFunc(200*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				}
				return ww, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []host.View{{Confirm: 5}, {Progress: 5}}; !slices.Equal(views, want) {
			t.Fatalf("opened %+v, want the confirm window, then the progress window", views)
		}
	})

	t.Run("main window open", func(t *testing.T) {
		h, _ := inprocHost(t, t.TempDir())
		var views []host.View
		var shown []int64
		changes, openSeen := 0, []bool{}
		var mainSecond int
		err := h.Run(RunOptions{
			App: win.AppOptions{ClassName: "DownexGlueTestConfirm2"},
			Open: func(a *win.App, w *Window) (*win.Window, error) {
				views = append(views, w.View)
				ww, kw, err := labelWindow(a, "window")
				if err != nil {
					return nil, err
				}
				switch {
				case w.View.IsMain():
					w.SetConfirmHooks(func(id int64) { shown = append(shown, id) }, func() {
						changes++
						openSeen = append(openSeen, w.ConfirmWindowOpen())
					})
					w.Session.Confirmations().Ready(func([]int64) { mainSecond++ })
					_ = kw.Post(func() { h.Request([]string{"--confirm", "7", "--raise"}) })
					time.AfterFunc(600*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				case w.View.IsConfirm():
					w.ConfirmShown(7)
					time.AfterFunc(200*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				}
				return ww, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []host.View{{}, {Confirm: 7}}; !slices.Equal(views, want) {
			t.Fatalf("opened %+v, want the main window, then the confirm window", views)
		}
		if mainSecond != 0 {
			t.Fatal("the main window was asked to show the confirmation")
		}
		if !slices.Equal(shown, []int64{7}) || !slices.Equal(openSeen, []bool{true, false}) || changes != 2 {
			t.Fatalf("the main window heard shown %v, open %v (%d changes)", shown, openSeen, changes)
		}
	})
}
