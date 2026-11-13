package main

import (
	"errors"
	"log/slog"
	"os"
	"strconv"

	"downex/internal/ipc"
	"downex/internal/platform"
)

// desktop are the desktop integrations of a daemon (docs/DESKTOP-SPEC.md §1, §3–§5).
type desktop struct {
	// tray shows the notification-area icon and its notifications (tray.go).
	tray bool
	// launchAtLogin applies Settings.launch_at_login (daemon.Config.LaunchAtLogin);
	// nil leaves the sign-in entry alone.
	launchAtLogin func(enabled bool) error
	// openConfirmation starts "downex-ui --confirm <id>" (daemon.Config.OpenConfirmation);
	// nil when there is no window to ask.
	openConfirmation func(id int64) error
	// openProgress starts "downex-ui --progress <id>" (daemon.Config.OpenProgress; in the
	// macOS bundle the progress app); nil when there is no window.
	openProgress func(id int64) error
}

// setLaunchAtLogin writes or removes the sign-in entry (platform.SetLaunchAtLogin);
// tests replace it.
var setLaunchAtLogin = platform.SetLaunchAtLogin

// errTranslocated refuses launch at login from the copy that macOS runs of a quarantined
// Downex.app opened where the Finder did not move it (App Translocation): that copy goes
// away, and the login item would name it.
var errTranslocated = errors.New("Downex runs from a temporary copy that macOS made of Downex.app " +
	"(App Translocation): move Downex.app to Applications with the Finder and open it from there")

// ownDaemon reports whether o (with the environment of getenv) describes the user's
// own daemon: the default database on the per-user default endpoint. A daemon given
// another --db, --endpoint or $DOWNEX_ENDPOINT is a private instance (tests, scripts,
// other profiles): it leaves the sign-in entry alone and shows no icon.
func ownDaemon(o options, getenv func(string) string) bool {
	return o.db == "" && o.endpoint == "" && getenv(ipc.EnvEndpoint) == ""
}

// desktopHooks returns the integrations for the daemon described by o.
func desktopHooks(o options, log *slog.Logger) desktop {
	var d desktop
	own := ownDaemon(o, os.Getenv)
	d.tray = own && !o.noTray
	l, lerr := installLayout()
	if own {
		if lerr != nil {
			log.Warn("launch at login is unavailable: cannot locate downexd", "err", lerr)
		} else {
			// The sign-in entry starts this executable, or the macOS login item
			// (docs/DESKTOP-SPEC.md §5, docs/MACOS-SPEC.md §2).
			exe := loginProgram(l)
			d.launchAtLogin = func(enabled bool) error { return setLaunchAtLogin(enabled, exe, "--background") }
			if l.Translocated() {
				// A temporary copy of the bundle must not start at login; an entry of the
				// installed app is left as it is, and removing one is still allowed.
				d.launchAtLogin = func(enabled bool) error {
					if enabled {
						return errTranslocated
					}
					return setLaunchAtLogin(false, exe)
				}
			}
		}
	}
	// The window finds the daemon by itself on the default endpoint or the inherited
	// $DOWNEX_ENDPOINT; it cannot reach one given another --endpoint.
	if o.endpoint == "" && lerr == nil {
		if regularFile(l.Window) {
			d.openConfirmation = func(id int64) error { return openUI("--confirm", strconv.FormatInt(id, 10)) }
		}
		if regularFile(l.Progress) {
			d.openProgress = func(id int64) error { return openProgressApp("--progress", strconv.FormatInt(id, 10)) }
		}
	}
	return d
}
