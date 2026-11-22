//go:build windows

package main

import (
	"context"
	"log/slog"
	"runtime"
	"testing"
	"time"

	"downex/ui/host"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/fixture"
	"downex/ui/win"
)

// The standalone Confirm download window gives its backend a way onto its UI thread (b.post),
// as the main and progress windows do: downexd's quit event (the user quit Downex: the tray's
// Quit, the menu-bar icon's Quit, ⌘Q) reaches it through b.post and closes it with the others
// (productBackend's start). Before, it had none, and a Confirm download window open alone
// stayed open after Quit.
func TestTheConfirmWindowHearsTheQuit(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fx, ok := scene.Fixture("confirm-file")
	if !ok {
		t.Fatal("no confirm-file fixture")
	}
	mock, err := scene.New(fx.Options())
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	list, err := mock.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var view host.View
	for _, d := range list.GetDownloads() {
		if views.IsAwaiting(d.GetState()) {
			view.Confirm = d.GetId()
			break
		}
	}
	if view.Confirm == 0 {
		t.Fatal("the fixture has no download awaiting confirmation")
	}
	fp := &fixture.Platform{Mock: mock}
	var undo []func()
	b := &backend{
		view: view, daemon: fixtureDaemon{Daemon: mock, version: "1.0.0"}, events: mock, files: fp, platform: fp,
		theme: state.NewTheme(state.ThemeOptions{}), caps: view.Capabilities(), build: host.CurrentBuild("1.0.0"),
		log: slog.New(slog.DiscardHandler), demo: true,
		later: func(f func()) { undo = append(undo, f) },
	}
	defer b.theme.Close()
	a, err := win.NewApp(win.AppOptions{ClassName: "DownexNativeConfirmQuitTest"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}()
	const backstop = 10 * time.Second // the window closes by itself then, should the quit not reach it
	w, err := openConfirm(a, b, options{noActivate: true, software: true, exitAfter: backstop})
	if err != nil {
		t.Fatal(err)
	}
	if b.post == nil {
		t.Fatal("the Confirm download window gave its backend no way onto its UI thread")
	}
	// What the quit event does (gw.QuitApp: every window's close, CloseAll), on the window's
	// UI thread, a little after it opened (its download is still awaiting confirmation).
	var quit time.Time // set on the UI thread
	time.AfterFunc(300*time.Millisecond, func() {
		if err := b.post(func() { quit = time.Now(); w.Close() }); err != nil {
			t.Error(err)
		}
	})
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	if quit.IsZero() {
		t.Fatal("the quit did not reach the Confirm download window")
	}
	if d := time.Since(quit); d > backstop/2 {
		t.Fatalf("the window closed %v after the quit, not on it", d)
	}
}
