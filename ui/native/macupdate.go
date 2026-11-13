//go:build windows || (darwin && cgo)

package main

import (
	"strconv"

	"downex/internal/client"
)

// What the macOS main window does beside downexd's update and Setup's registration, which
// Windows has from Setup: it starts again from the new Downex.app after an update, and it
// registers Downex with the browsers when it starts. The rules are pure, so that every
// system tests them; os_darwin.go wires them.

// reopenScript waits (at most a minute) for the window's process to exit, then opens
// Downex.app again through Launch Services. Its arguments are positional ($1 the pid, $2 the
// bundle): nothing is interpolated into the script.
const reopenScript = `pid=$1; app=$2; i=0
while kill -0 "$pid" 2>/dev/null; do
	i=$((i+1))
	[ "$i" -gt 600 ] && break
	sleep 0.1
done
exec /usr/bin/open "$app"
`

// reopenCommand is the helper that opens the main window of l again once the process pid
// has exited: after downexd replaced Downex.app (InstallUpdate), this process still runs the
// old bundle's code. ok is false outside a bundle (a development build: nothing was
// replaced) and for a copy that App Translocation made (downexd does not update one).
func reopenCommand(l client.Layout, pid int) (exe string, args []string, ok bool) {
	if l.Bundle == "" || l.Translocated() {
		return "", nil, false
	}
	return "/bin/sh", []string{"-c", reopenScript, "downex-reopen", strconv.Itoa(pid), l.Bundle}, true
}

// registersBrowsers reports that the main window of l on goos registers Downex with the
// browsers when it starts (downex-nmhost register --keep, for the browsers this user has: it
// writes the missing manifests, rewrites those that name another downex-nmhost or lack a
// built-in extension ID, keeps every ID a manifest allows already and never replaces a .bak;
// Settings → Integration's "Register with browsers again" is plain register). That is macOS
// inside Downex.app: the first start after copying it, after an update, after moving it.
// Never from a copy that App Translocation made, whose path goes away when Downex quits;
// Windows Setup registers.
func registersBrowsers(goos string, l client.Layout) bool {
	return goos == "darwin" && l.Bundle != "" && !l.Translocated()
}
