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

// A browser add whose "--confirm <id> --raise" comes while the Confirm download window's close
// is on its way (it asked to close: MarkClosing) is not handed to that window, which would drop
// it: a new Confirm download window opens and takes it. The old window's end leaves the new
// one the host's Confirm download window, and the main window sees one open throughout.
func TestConfirmWhileTheConfirmWindowCloses(t *testing.T) {
	t.Run("a request opens a new window", func(t *testing.T) {
		h, _ := inprocHost(t, t.TempDir())
		var views []host.View
		asked := map[int64][][]int64{} // each confirm window's requests, by its first id
		var openSeen []bool
		newIsCurrent := false
		err := h.Run(RunOptions{
			App: win.AppOptions{ClassName: "DownexGlueTestConfirmClosing1"},
			Open: func(a *win.App, w *Window) (*win.Window, error) {
				views = append(views, w.View)
				ww, kw, err := labelWindow(a, "window")
				if err != nil {
					return nil, err
				}
				if w.View.IsConfirm() {
					first := w.View.Confirm
					w.Session.Confirmations().Ready(func(ids []int64) { asked[first] = append(asked[first], ids) })
				}
				switch w.View.Confirm {
				case 0:
					w.SetConfirmHooks(nil, func() { openSeen = append(openSeen, w.ConfirmWindowOpen()) })
					_ = kw.Post(func() { h.Request([]string{"--confirm", "7", "--raise"}) })
					time.AfterFunc(900*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				case 7:
					// As confirmwin.Screen's finish does: closing, then the close posted; the
					// next browser add comes before the close has run.
					_ = kw.Post(func() {
						w.MarkClosing()
						_ = kw.Post(kw.Close)
						h.Request([]string{"--confirm", "8", "--raise"})
					})
				case 8:
					time.AfterFunc(300*time.Millisecond, func() {
						_ = kw.Post(func() {
							newIsCurrent = h.confirm == w && w.ConfirmWindowOpen()
							kw.Close()
						})
					})
				}
				return ww, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []host.View{{}, {Confirm: 7}, {Confirm: 8}}; !slices.Equal(views, want) {
			t.Fatalf("opened %+v, want the main window, the confirm window, then a new one %+v", views, want)
		}
		if want := [][]int64{{7}}; !slices.EqualFunc(asked[7], want, slices.Equal) {
			t.Fatalf("the closing confirm window was asked for %v, want %v", asked[7], want)
		}
		if want := [][]int64{{8}}; !slices.EqualFunc(asked[8], want, slices.Equal) {
			t.Fatalf("the new confirm window was asked for %v, want %v", asked[8], want)
		}
		if !newIsCurrent {
			t.Fatal("once the old confirm window had closed, the new one was not the host's confirm window")
		}
		if want := []bool{true, true, false}; !slices.Equal(openSeen, want) {
			t.Fatalf("the main window saw the confirm window open %v, want %v", openSeen, want)
		}
	})

	// A request the closing window took just before (its screen's Request after the close:
	// ReopenConfirm) opens a new window too, also when the closing window is the process's
	// last one: the new window opens before the close, so the process does not end first.
	// ReopenConfirm marks the window closing itself, so the request never comes back to it.
	t.Run("reopen as the last window", func(t *testing.T) {
		h, _ := inprocHost(t, t.TempDir(), "--confirm", "5", "--raise")
		var views []host.View
		asked := map[int64][][]int64{}
		err := h.Run(RunOptions{
			App: win.AppOptions{ClassName: "DownexGlueTestConfirmClosing2"},
			Open: func(a *win.App, w *Window) (*win.Window, error) {
				views = append(views, w.View)
				ww, kw, err := labelWindow(a, "window")
				if err != nil {
					return nil, err
				}
				first := w.View.Confirm
				w.Session.Confirmations().Ready(func(ids []int64) { asked[first] = append(asked[first], ids) })
				switch first {
				case 5:
					_ = kw.Post(func() {
						_ = kw.Post(kw.Close)
						w.ReopenConfirm([]int64{6, 9})
					})
				default:
					time.AfterFunc(200*time.Millisecond, func() { _ = kw.Post(kw.Close) })
				}
				return ww, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []host.View{{Confirm: 5}, {Confirm: 6}}; !slices.Equal(views, want) {
			t.Fatalf("opened %+v, want the confirm window, then a new one %+v", views, want)
		}
		if want := [][]int64{{5}}; !slices.EqualFunc(asked[5], want, slices.Equal) {
			t.Fatalf("the closing confirm window was asked for %v, want %v", asked[5], want)
		}
		if want := [][]int64{{6, 9}}; !slices.EqualFunc(asked[6], want, slices.Equal) {
			t.Fatalf("the new confirm window was asked for %v, want %v", asked[6], want)
		}
	})
}
