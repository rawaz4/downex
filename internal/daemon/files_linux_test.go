//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// The settings file is readable by the current user only (the lock files' modes are
// tested in internal/dlock).
func TestPrivateFileModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg", SettingsFileName)
	if err := saveSettings(path, Settings{MaxConcurrentDownloads: 1, DefaultDir: dir, DefaultMaxConnections: 8}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings mode = %v, want 0600", perm)
	}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("settings directory mode = %v (%v)", fi.Mode(), err)
	}
}
