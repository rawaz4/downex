//go:build windows

package platform

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// runKey is the per-user list of programs Windows starts when the user signs in.
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// loginValueName is the value of runKey that SetLaunchAtLogin manages
// (docs/DESKTOP-SPEC.md §5).
const loginValueName = "Downex"

// SetLaunchAtLogin makes Windows start exe with args when the current user signs in
// (enabled) or stops it from doing so: it writes or deletes the value "Downex" of
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run, whose data is the quoted exe
// followed by args. It changes nothing when the value is already as requested, and
// removing a value that does not exist succeeds. exe must be an absolute path.
func SetLaunchAtLogin(enabled bool, exe string, args ...string) error {
	return setRunValue(loginValueName, enabled, exe, args...)
}

// setRunValue implements SetLaunchAtLogin for the value name (tests use their own).
func setRunValue(name string, enabled bool, exe string, args ...string) error {
	if !enabled {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("platform: open HKCU\\%s: %w", runKey, err)
		}
		defer k.Close()
		if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("platform: remove the login entry %s: %w", name, err)
		}
		return nil
	}
	cmd, err := runCommandLine(exe, args)
	if err != nil {
		return err
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("platform: open HKCU\\%s: %w", runKey, err)
	}
	defer k.Close()
	if cur, typ, err := k.GetStringValue(name); err == nil && typ == registry.SZ && cur == cmd {
		return nil
	}
	if err := k.SetStringValue(name, cmd); err != nil {
		return fmt.Errorf("platform: write the login entry %s: %w", name, err)
	}
	return nil
}

// runValue returns the data of the Run value name ("" and false when it is missing).
func runValue(name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return v, err == nil, err
}

// runCommandLine is the command line of a Run value: the executable always in double
// quotes, then each argument quoted as CommandLineToArgvW expects.
func runCommandLine(exe string, args []string) (string, error) {
	if !filepath.IsAbs(exe) || strings.ContainsAny(exe, "\"\x00\r\n") {
		return "", fmt.Errorf("platform: login entry: %q is not an absolute executable path", exe)
	}
	parts := []string{`"` + exe + `"`}
	for _, a := range args {
		if strings.ContainsAny(a, "\x00\r\n") {
			return "", fmt.Errorf("platform: login entry: argument %q contains a control character", a)
		}
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " "), nil
}
