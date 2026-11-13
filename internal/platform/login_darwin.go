//go:build darwin

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// SetLaunchAtLogin makes launchd start exe with args when the current user logs in
// (enabled) or stops it from doing so (docs/MACOS-SPEC.md §2): it writes or removes the
// LaunchAgent ~/Library/LaunchAgents/com.downex.agent.plist (launchAgentPlist; the
// Downex agent passes its own executable, Downex Agent.app/Contents/MacOS/downexd-agent,
// and --background). launchctl is not called: the change takes effect at the next
// login, and a running daemon is left alone.
//
// The property list is written atomically with mode 0644 and belongs to the user, as
// launchd expects: it is never written as root (effective uid 0). It changes nothing
// when the file is already as requested, and removing a file that does not exist
// succeeds. exe must be an absolute path.
func SetLaunchAtLogin(enabled bool, exe string, args ...string) error {
	dirs, err := userDirs()
	if err != nil {
		return err
	}
	return setLaunchAgent(dirs.launchAgent, enabled, exe, args...)
}

// setLaunchAgent implements SetLaunchAtLogin for the property list at path.
func setLaunchAgent(path string, enabled bool, exe string, args ...string) error {
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("platform: remove the login entry: %w", err)
		}
		return nil
	}
	data, err := launchAgentPlist(exe, args)
	if err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return fmt.Errorf("platform: login entry: refusing to write %s as root: a LaunchAgent must belong to the user", path)
	}
	if launchAgentCurrent(path, data) {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("platform: create %s: %w", dir, err)
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("platform: write the login entry: %w", err)
	}
	return nil
}

// launchAgentCurrent reports whether path is a regular file holding data, with mode 0644
// and owned by this process's effective user, so that nothing needs to be written.
func launchAgentCurrent(path string, data []byte) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		return false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int64(st.Uid) != int64(os.Geteuid()) {
		return false
	}
	cur, err := os.ReadFile(path)
	return err == nil && bytes.Equal(cur, data)
}
