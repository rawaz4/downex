package store

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	appDirName = "Downex"
	dbFileName = "downex.db"
)

// DefaultPath returns the per-user database location,
// <os.UserConfigDir()>/Downex/downex.db, creating the Downex directory
// (readable only by the current user) if it does not exist.
func DefaultPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("store: locate user config directory: %w", err)
	}
	dir := filepath.Join(base, appDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("store: create %s: %w", dir, err)
	}
	return filepath.Join(dir, dbFileName), nil
}
