//go:build windows

// Command native is downex-ui, Downex's window, drawn with Direct2D and DirectWrite from pure
// Go in Win32 windows (the native-ui decision, plan steps 11 and 17). With no
// arguments it is the main window: the title bar, sidebar, page header, download grid,
// details pane and status bar of ui/views/shell and ui/views/downloads, composed by
// ui/views/app, with every dialog of the lanes (Add, Confirm, Remove, Restart, Open, Move to
// queue, Settings, Queues and the command palette). "--progress <id>" is that download's
// progress window (ui/views/progress). "--confirm <id>" asks the main window for that
// download's confirmation. One process hosts every window: a later start hands its
// arguments to the running one and exits, and the process ends with its last window (the
// launch contract of ui/native/glue, which hands over to and from today's Wails window).
//
// build.ps1 builds it: as the opt-in preview (downex-ui-native-preview.exe, the default) it
// has its own single-instance lock and its own files beside today's
// (%LOCALAPPDATA%\Downex\ui-window-native-preview.json, ui-prefs-native-preview.json,
// ui-theme-native-preview.json and ui-native-preview.log) and talks to the same downexd as
// today's window ($DOWNEX_ENDPOINT, else this user's); with -Product it is downex-ui.exe with
// today's locks and files.
//
// Measuring flags (the product is started with none of them; they are taken out of the
// command line before the launch contract reads it):
//
//	-size WxH            the client size in DIPs (the window neither reads nor stores the size it was left at)
//	-dpi N               draw at N DPI whatever the monitor's (also leaves the stored size alone)
//	-theme C             draw theme C (system, light or dark) whatever the stored choice
//	-system-theme M      what "system" means: auto (this PC's), light or dark
//	-render hw|sw        Direct2D on the GPU, or its software renderer
//	-noactivate          show the window without taking the focus
//	-report FILE         write the window's handle, process id and first-frame time as JSON
//	-stats FILE          write the window's counters and the process's memory once a second (JSON lines)
//	-exit-after D        close the window after D, without storing its size
//	-fixture studio|random  the simulated daemon (ui/state/scene) instead of downexd: no
//	                     lock, no per-user file; -rows N rows for random (10000), -live moves
//	                     the simulation on 4 times a second
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"
	"unsafe"

	"downex/ui/host"
	"downex/ui/native/glue"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views/fixture"
	"downex/ui/win"

	"golang.org/x/sys/windows"
)

// The UI thread is the main thread: Win32 windows and COM's apartment belong to it.
func init() { runtime.LockOSThread() }

// version is the build's version (build.ps1: -X main.version=...).
var version = "0.0.0-dev"

const (
	// iconID is the icon's resource id in the executable (winres/main.go).
	iconID = 1
	// The window classes: the preview's own, so that FindWindow of one never finds the other.
	previewClass = "DownexNativePreview"
	productClass = "DownexNativeWindow"
)

func main() {
	o, args, err := parseArgs(os.Args[1:])
	if err != nil {
		fail(err)
	}
	if o.fixture {
		err = runFixture(o, args)
	} else {
		err = runProduct(o, args)
	}
	if errors.Is(err, errHandoffFailed) {
		os.Exit(1) // logged in ui.log
	}
	if err != nil {
		fail(err)
	}
}

// errHandoffFailed: the running window did not take a later start's arguments in time.
var errHandoffFailed = errors.New("the running window did not take the hand-off")

// runProduct is the windows on downexd: the launch contract first (a later start hands its
// arguments to the running host and exits 0), then the host's windows, each with its own
// prefs, theme and session, until the last one has closed.
func runProduct(o options, args []string) error {
	h, handedOver, err := glue.Start(glue.Config{Args: args, Version: version, NoActivate: o.noActivate})
	if handedOver {
		if err != nil {
			return errHandoffFailed
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer h.Close()
	class := productClass
	if glue.IsPreview(h.LockID) {
		class = previewClass
	}
	n := 0
	return h.Run(glue.RunOptions{
		App: win.AppOptions{ClassName: class, Icon: iconID},
		Open: func(a *win.App, gw *glue.Window) (*win.Window, error) {
			n++
			wo := o.forWindow(n)
			b := productBackend(gw, wo)
			if n > 1 {
				b.asked = time.Now()
			}
			switch {
			case gw.View.IsProgress():
				return openProgress(a, b, wo)
			case gw.View.IsConfirm():
				return openConfirm(a, b, wo)
			}
			return openMain(a, b, wo)
		},
		Done: releaseIcons,
	})
}

// runFixture is a window on the simulated daemon, for measurements and screenshots: nothing
// is locked, read from or stored in the user's files.
func runFixture(o options, args []string) error {
	so := scene.Options{Scene: o.scene, AutoTick: o.live, TickMs: 250}
	if o.scene == scene.Random {
		so.Count = o.rows
	}
	mock, err := scene.New(so)
	if err != nil {
		return err
	}
	defer mock.Close()
	view := host.ViewOf(args)
	fp := &fixture.Platform{Mock: mock}
	var undo []func()
	letGo := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		undo = nil
	}
	b := &backend{
		view: view, daemon: fixtureDaemon{Daemon: mock, version: version}, events: mock, files: fp, platform: fp,
		theme: state.NewTheme(state.ThemeOptions{}), caps: view.Capabilities(), build: host.CurrentBuild(version),
		log: slog.New(slog.DiscardHandler), demo: true,
		later: func(f func()) { undo = append(undo, f) },
	}
	defer b.theme.Close()
	a, err := win.NewApp(win.AppOptions{ClassName: previewClass, Icon: iconID})
	if err != nil {
		return err
	}
	defer a.Close()
	defer letGo() // the window's parts go before the App
	switch {
	case view.IsProgress():
		_, err = openProgress(a, b, o)
	case view.IsConfirm():
		_, err = openConfirm(a, b, o)
	default:
		_, err = openMain(a, b, o)
	}
	if err == nil {
		err = a.Run()
	}
	letGo()
	releaseIcons(a)
	return err
}

var (
	user32          = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

// fail shows err in a message box (a GUI program has no console) and exits 1.
func fail(err error) {
	text := windows.StringToUTF16Ptr("Downex: " + err.Error())
	caption := windows.StringToUTF16Ptr("Downex")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(caption)), 0x10 /* MB_ICONERROR */)
	fmt.Fprintln(os.Stderr, "downex-ui:", err)
	os.Exit(1)
}
