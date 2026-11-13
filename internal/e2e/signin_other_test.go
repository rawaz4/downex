//go:build !windows

package e2e

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// signInEntry returns the user's real sign-in entry for Downex
// ($XDG_CONFIG_HOME/autostart/downex.desktop, docs/DESKTOP-SPEC.md §5), "" when there
// is none. Tests only read it.
func signInEntry() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil // no home directory: no entry either
		}
		base = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(base, "autostart", "downex.desktop"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}
