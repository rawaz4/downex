package platform

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Power actions after a queue finished (docs/M6-SPEC.md §1): Sleep, Hibernate and
// Shutdown ask the operating system the way its own menu does. None of them forces
// other applications to close: one that refuses (unsaved work) stops a shutdown.
// They return an error wrapping errors.ErrUnsupported where the system offers no way
// (hibernation on macOS, everything on Linux until logind is wired, other systems).
// CanHibernate reports whether Hibernate can work here. Only the user's own daemon
// calls them; tests use fakes (daemon.Config.Power) and never these functions.

// ErrPowerUnsupported wraps errors.ErrUnsupported for a power action this system
// cannot perform.
var ErrPowerUnsupported = fmt.Errorf("platform: power action %w on this system", errors.ErrUnsupported)

// Mac power commands (docs/M6-SPEC.md §1). Sleep uses pmset; shutting down goes
// through System Events, which asks every application to quit (the first time, macOS
// asks the user to allow Downex to control System Events).
const (
	macPmset     = "/usr/bin/pmset"
	macOsascript = "/usr/bin/osascript"
	macShutdown  = `tell application "System Events" to shut down`
)

// macPowerCommand returns the program and arguments of a macOS power action ("sleep"
// or "shutdown"); ok is false for any other action (hibernation is unsupported).
func macPowerCommand(action string) (exe string, args []string, ok bool) {
	switch action {
	case "sleep":
		return macPmset, []string{"sleepnow"}, true
	case "shutdown":
		return macOsascript, []string{"-e", macShutdown}, true
	}
	return "", nil, false
}

// macCaffeinate keeps a Mac from sleeping while idle (AwakeRequest): -i asserts that
// idle sleep is prevented, -w ends it with the given process, so that a daemon that dies
// never leaves the assertion behind.
const macCaffeinate = "/usr/bin/caffeinate"

// macCaffeinateCommand returns the program and arguments of the caffeinate process that
// holds a Mac awake for process pid.
func macCaffeinateCommand(pid int) (exe string, args []string) {
	return macCaffeinate, []string{"-i", "-w", strconv.Itoa(pid)}
}

// fileTimeOf converts t to a Windows FILETIME value (100-nanosecond intervals since
// 1601-01-01 UTC), the absolute due time of a waitable timer.
func fileTimeOf(t time.Time) int64 {
	const epochDiff = 116444736000000000 // 1601-01-01 to 1970-01-01 in 100 ns
	return t.UnixNano()/100 + epochDiff
}
