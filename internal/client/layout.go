package client

import (
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// The macOS app bundle (docs/MACOS-SPEC.md §1):
//
//	Downex.app/Contents/MacOS/Downex                         the window
//	Downex.app/Contents/Helpers/downexd, downex, downex-nmhost
//	Downex.app/Contents/Helpers/Downex Progress.app          the progress windows
//	Downex.app/Contents/Library/LoginItems/Downex Agent.app  downexd with the menu-bar icon
//
// The bundle itself may be renamed (Downex 2.app); the names inside it are fixed.
const (
	// MacWindowName is the window's executable in Contents/MacOS of Downex.app and of
	// the progress app (the same binary).
	MacWindowName = "Downex"
	// MacProgressApp is the progress windows' app bundle in Contents/Helpers.
	MacProgressApp = "Downex Progress.app"
	// MacAgentApp is the login item in Contents/Library/LoginItems.
	MacAgentApp = "Downex Agent.app"
	// MacAgentName is the login item's executable (downexd built with the menu-bar icon).
	MacAgentName = "downexd-agent"
)

// Layout is where one installation keeps its executables, found from the running one
// (never from the current directory). Outside a macOS app bundle they are siblings:
// downex, downex-nmhost, downexd and the window downex-ui in one directory. Inside
// Downex.app the CLI, the relay and the daemon are in Contents/Helpers, and the window,
// the progress app and the login item are where the bundle layout puts them.
type Layout struct {
	// Exe is the running executable, symbolic links resolved.
	Exe string
	// Bundle is the Downex.app directory, or "" outside an app bundle.
	Bundle string
	// Dir holds downex, downex-nmhost and downexd.
	Dir string
	// Window is the main window's executable.
	Window string
	// Progress is the executable that shows a progress window ("--progress <id>"): the
	// window itself outside a bundle.
	Progress string
	// Agent is the login item's executable inside a bundle, "" outside one.
	Agent string

	goos string
}

// Daemon is the installation's downexd executable.
func (l Layout) Daemon() string {
	if l.goos == "darwin" {
		return path.Join(l.Dir, daemonName(l.goos))
	}
	return filepath.Join(l.Dir, daemonName(l.goos))
}

// autostart is the executable that clients start as the user's daemon (FindDaemon):
// inside the macOS app bundle the login item when isFile finds it, so that the daemon
// shows the menu-bar icon and its notifications however it was started (the downexd in
// Contents/Helpers is built without cgo and has neither, docs/MACOS-SPEC.md §1); else
// Daemon.
func (l Layout) autostart(isFile func(string) bool) string {
	if l.Agent != "" && isFile(l.Agent) {
		return l.Agent
	}
	return l.Daemon()
}

// Translocated reports whether the running executable is inside a copy that
// Gatekeeper's App Translocation made of a quarantined Downex.app (MacTranslocated).
func (l Layout) Translocated() bool {
	return l.Bundle != "" && MacTranslocated(l.Bundle)
}

// MacTranslocated reports whether the macOS path p is inside the copy that Gatekeeper's
// App Translocation makes of a quarantined app bundle opened where the Finder did not
// move it (below /private/var/folders/.../AppTranslocation/<UUID>/): that copy is
// read-only and goes away, so neither the browsers' manifests nor the login item may
// name it.
func MacTranslocated(p string) bool {
	return slices.Contains(strings.Split(path.Clean(p), "/"), "AppTranslocation")
}

// FindLayout returns the layout of the installation that contains the running
// executable (executable is os.Executable; symbolic links are resolved, so a CLI linked
// into $PATH finds its own installation).
func FindLayout(executable func() (string, error)) (Layout, error) {
	exe, err := executable()
	if err != nil {
		return Layout{}, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return LayoutOf(runtime.GOOS, exe), nil
}

// LayoutOf returns the layout, under the rules of goos, of the installation that
// contains exe (an absolute path, symbolic links resolved). macOS paths are handled
// with package path, so the rules can be checked on any system.
func LayoutOf(goos, exe string) Layout {
	if goos == "darwin" {
		exe = path.Clean(exe)
		if contents, ok := macContents(exe); ok {
			helpers := path.Join(contents, "Helpers")
			return Layout{
				Exe:      exe,
				Bundle:   path.Dir(contents),
				Dir:      helpers,
				Window:   path.Join(contents, "MacOS", MacWindowName),
				Progress: path.Join(helpers, MacProgressApp, "Contents", "MacOS", MacWindowName),
				Agent:    path.Join(contents, "Library", "LoginItems", MacAgentApp, "Contents", "MacOS", MacAgentName),
				goos:     goos,
			}
		}
		dir := path.Dir(exe)
		ui := path.Join(dir, uiName(goos))
		return Layout{Exe: exe, Dir: dir, Window: ui, Progress: ui, goos: goos}
	}
	dir := filepath.Dir(exe)
	ui := filepath.Join(dir, uiName(goos))
	return Layout{Exe: exe, Dir: dir, Window: ui, Progress: ui, goos: goos}
}

// macContents returns the Contents directory of the app bundle that holds exe at one of
// the places of docs/MACOS-SPEC.md §1:
//
//	<B>.app/Contents/Helpers/<exe>                                   CLI, relay, daemon
//	<B>.app/Contents/MacOS/<exe>                                     the window
//	<B>.app/Contents/Helpers/<P>.app/Contents/MacOS/<exe>            the progress app
//	<B>.app/Contents/Library/LoginItems/<A>.app/Contents/MacOS/<exe> the login item
//
// Names are compared without case, as the default (case-insensitive) APFS volume does.
func macContents(exe string) (string, bool) {
	if !path.IsAbs(exe) {
		return "", false
	}
	dir := path.Dir(exe)
	switch {
	case isNamed(dir, "Helpers"):
		if c := path.Dir(dir); isContents(c) {
			return c, true
		}
	case isNamed(dir, "MacOS"):
		own := path.Dir(dir)
		if !isContents(own) {
			return "", false
		}
		parent := path.Dir(path.Dir(own)) // the directory holding this .app
		if isNamed(parent, "Helpers") && isContents(path.Dir(parent)) {
			return path.Dir(parent), true
		}
		library := path.Dir(parent)
		if isNamed(parent, "LoginItems") && isNamed(library, "Library") && isContents(path.Dir(library)) {
			return path.Dir(library), true
		}
		return own, true
	}
	return "", false
}

// isContents reports whether dir is the Contents directory of an app bundle.
func isContents(dir string) bool {
	app := path.Base(path.Dir(dir))
	return isNamed(dir, "Contents") && len(app) > len(".app") && strings.EqualFold(app[len(app)-len(".app"):], ".app")
}

// isNamed reports whether the last element of p is name, ignoring case.
func isNamed(p, name string) bool {
	return strings.EqualFold(path.Base(p), name)
}

func daemonName(goos string) string {
	if goos == "windows" {
		return "downexd.exe"
	}
	return "downexd"
}

// UIName is the file name of the window's executable next to downexd outside a macOS
// app bundle (Downex.exe and downex.exe are one name on Windows, hence downex-ui).
func UIName() string { return uiName(runtime.GOOS) }

func uiName(goos string) string {
	if goos == "windows" {
		return "downex-ui.exe"
	}
	return "downex-ui"
}
