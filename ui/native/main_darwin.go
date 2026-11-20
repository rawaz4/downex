//go:build darwin && cgo

// Command native on macOS is the native Downex window (ui/mac over the same views as the
// Windows window) in place of today's Wails window, with today's Mac behaviour: Downex.app
// shows the main window (a regular app: Dock icon, menu bar), Downex Progress.app every
// progress window (an accessory app: no Dock icon); "--progress <id>" and "--confirm <id>"
// as before; a later start hands its arguments to the running host and exits (ui/native/
// glue, start_darwin.go). scripts/build-macos.sh builds it with cgo for both
// architectures. The measuring flags are Windows' (args.go); -render and -dpi have no
// effect here.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/mac"
	"downex/ui/native/glue"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views/fixture"
)

// AppKit belongs to the main thread: the main goroutine stays on it.
func init() { runtime.LockOSThread() }

// version is the build's version (build-macos.sh: -X main.version=...).
var version = "0.0.0-dev"

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

// errHandoffFailed: the running window did not take a later start's arguments.
var errHandoffFailed = errors.New("the running window did not take the hand-off")

// aboutText is the About dialog's text (ui/menu.go's).
func aboutText() (title, message string) {
	return "Downex", "Version " + version + "\n\nA download manager."
}

// runProduct is the windows on downexd (see glue's launch contract).
func runProduct(o options, args []string) error {
	c := glue.Config{Args: args, Version: version, NoActivate: o.noActivate}
	e2eConfig(&c)
	h, handedOver, err := glue.Start(c)
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
	title, message := aboutText()
	n := 0
	return h.Run(glue.RunOptions{
		AboutTitle: title, AboutMessage: message,
		Open: func(a *mac.App, gw *glue.Window) (*mac.Window, error) {
			e2eListen(a)
			n++
			wo := o.forWindow(n)
			b := productBackend(gw, wo)
			if n > 1 {
				b.asked = time.Now()
			}
			open := openMain
			switch {
			case gw.View.IsProgress():
				open = openProgress
			case gw.View.IsConfirm():
				open = openConfirm
			}
			w, err := open(a, b, wo)
			shootLater(w)
			return w, err
		},
		Done: releaseIcons,
	})
}

// runFixture is a window on the simulated daemon: nothing locked, read or stored.
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
	var menuDo func(string)
	b := &backend{
		view: view, daemon: fixtureDaemon{Daemon: mock, version: version}, events: mock, files: fp, platform: fp,
		theme: state.NewTheme(state.ThemeOptions{SystemDark: mac.SystemDark}), caps: view.Capabilities(),
		build: host.CurrentBuild(version), log: slog.New(slog.DiscardHandler), demo: true,
		later: func(f func()) { undo = append(undo, f) },
		menu:  func(do func(string)) { menuDo = do },
	}
	defer b.theme.Close()
	a, err := mac.NewApp(mac.AppOptions{Regular: !view.IsProgress()})
	if err != nil {
		return err
	}
	defer a.Close()
	title, message := aboutText()
	a.InstallProgram(mac.ProgramOptions{
		Settings: !view.IsProgress(), AboutTitle: title, AboutMessage: message,
		OnMenu: func(c mac.MenuCommand, w *mac.Window) {
			switch {
			case c == mac.MenuSettings && menuDo != nil:
				menuDo("settings")
			case c != mac.MenuSettings && w != nil:
				w.Command(kit.WindowClose)
			}
		},
	})
	defer letGo()
	var w *mac.Window
	if view.IsProgress() {
		w, err = openProgress(a, b, o)
	} else {
		w, err = openMain(a, b, o)
	}
	shootLater(w)
	if err == nil {
		err = a.Run()
	}
	letGo()
	releaseIcons(a)
	return err
}

// shotsEnv names a file prefix: 3 s after each window opens, what it drew is written to
// "<prefix>.<window number>.png" (a measuring aid, as the Windows flags are; a window
// process started by the product never has it).
const shotsEnv = "DOWNEX_UI_SHOTS"

// shootLater writes w's picture as shotsEnv says.
func shootLater(w *mac.Window) {
	prefix := os.Getenv(shotsEnv)
	if prefix == "" || w == nil {
		return
	}
	time.AfterFunc(3*time.Second, func() {
		_ = w.Post(func() {
			name := fmt.Sprintf("%s.%d.png", prefix, w.WindowNumber())
			if err := w.WriteViewsPNG(name); err != nil {
				fmt.Fprintln(os.Stderr, "downex-ui: shot:", err)
			}
		})
	})
}

// fail shows err in an alert (a GUI program has no console) and exits 1.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "downex-ui:", err)
	mac.AlertCritical("Downex", "Downex: "+err.Error())
	os.Exit(1)
}
