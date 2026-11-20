//go:build windows || (darwin && cgo)

package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/native/glue"
	"downex/ui/state"
	"downex/ui/state/prefs"
	"downex/ui/views"

	"google.golang.org/protobuf/proto"
)

// backend is what a window shows and talks to: downexd through the window's host session,
// or the simulated daemon of a fixture run.
type backend struct {
	view     host.View
	daemon   host.Daemon
	events   state.Events
	files    state.FileActions
	platform views.Platform
	storage  state.Storage
	theme    *state.Theme
	caps     host.Capabilities
	build    host.BuildInfo
	call     state.CallContext
	log      *slog.Logger
	demo     bool
	// statePath is the main window's stored size ("" keeps none).
	statePath string
	// attach connects the window to its glue (nil in a fixture run).
	attach func(w *osWindow, kw *kit.Window) *glue.Glue
	// start starts the daemon's events (Session.Start); confirm says where the main window
	// shows the "--confirm <id>" requests (nil in a fixture run).
	start   func()
	confirm func(show func(ids []int64))
	// renew says where the main window shows the "--renew <id>" requests (New link…).
	renew func(show func(ids []int64))
	// shown is told the window's first frame (the first-frame memory trim; nil in a fixture run).
	shown func()
	// later adds what is let go once the window has closed, the last added first.
	later func(f func())
	// foreignClose answers a WM_CLOSE from another program (win.WindowOptions.OnForeignClose;
	// nil: the window closes alone).
	foreignClose func() bool
	// menu is told how the menu bar's commands reach the main window (macOS: Settings…;
	// nil elsewhere).
	menu func(do func(command string))
	// updated runs, off the UI thread, once downexd has started installing an update; quit
	// closes the main window (macOS: the window starts again from the new Downex.app; nil
	// elsewhere).
	updated func(quit func())
	// mainStarted runs once the main window is up and connected (macOS: the browsers'
	// registration; nil elsewhere).
	mainStarted func()
	// asked is when the window was asked for: its first-frame time is measured from it.
	asked time.Time
	// gw is the host's window (nil in a fixture run): the main window and the standalone
	// Confirm download window keep each other's confirmations apart through it.
	gw *glue.Window
}

// since is the time since the window was asked for (the process's start for its first one).
func (b *backend) since() time.Duration {
	if b.asked.IsZero() {
		return sinceStart()
	}
	return time.Since(b.asked)
}

// productBackend is window gw's backend: its own prefs file, theme and session (the events
// bridge of its view), on the host's daemon.
func productBackend(gw *glue.Window, o options) *backend {
	pf, perr := gw.OpenPrefs()
	var storage state.Storage
	switch {
	case perr == nil:
		storage = gw.UIStorage(pf) // its changes reach the stores on the UI thread
		gw.Defer(pf.Close)
	case !errors.Is(perr, prefs.ErrNoPath):
		gw.Log.Warn("the prefs file cannot be opened; choices last for this session", "error", perr)
	}
	theme := gw.NewTheme(storage)
	gw.Defer(theme.Close)
	// Runs first of these: the window's glue (the files watch above all) goes before the
	// theme and the prefs file close, and the session and the lock after them.
	gw.Defer(gw.Detach)
	hp := &views.HostPlatform{Session: gw.Session}
	hub := &state.Hub{}
	st := gw.Session.Startup()
	b := &backend{
		view: gw.View, daemon: gw.Session.Daemon(), events: hub, files: gw.Session.Files(), platform: hp,
		storage: storage, theme: theme, caps: st.Capabilities, build: st.Build, call: gw.Session.CallContext,
		log: gw.Log,
		attach: func(w *osWindow, kw *kit.Window) *glue.Glue {
			g, err := gw.Attach(w, kw, glue.AttachOptions{Theme: theme, Prefs: pf})
			if err != nil {
				gw.Log.Warn("the window's system glue", "error", err)
				return nil
			}
			hp.Pickers = g.Pickers()
			return g
		},
		start:        func() { gw.Session.Start(hub.Emit) },
		confirm:      gw.Session.Confirmations().Ready,
		renew:        gw.Session.Confirmations().ReadyRenew,
		shown:        gw.FirstFrame,
		gw:           gw,
		later:        gw.Defer,
		foreignClose: gw.ForeignClose,
	}
	osBackend(b, gw)
	if o.remember && gw.View.IsMain() {
		b.statePath = gw.Files.WindowState
	}
	return b
}

// fixtureDaemon is the simulated daemon as a fixture run shows it: downexd reports the
// window's own version, as a real downexd of the same build does, so that Settings → About
// shows no version mismatch that only the simulation makes (the mock reports 0.0.0-mock).
type fixtureDaemon struct {
	host.Daemon
	version string
}

// Status is the simulated status with the window's version.
func (d fixtureDaemon) Status(ctx context.Context) (*downexv1.StatusResponse, error) {
	st, err := d.Daemon.Status(ctx)
	if err != nil || st == nil {
		return st, err
	}
	st = proto.Clone(st).(*downexv1.StatusResponse)
	st.Version = d.version
	return st, nil
}
