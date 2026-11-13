//go:build windows

package e2e

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// signInEntry returns the user's real sign-in entry for Downex
// (HKCU\...\Run\Downex, docs/DESKTOP-SPEC.md §5), "" when there is none. Tests only
// read it.
func signInEntry() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue("Downex")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return v, err
}
