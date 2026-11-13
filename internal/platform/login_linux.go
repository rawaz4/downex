//go:build linux

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// loginEntryName is the autostart entry SetLaunchAtLogin manages (docs/DESKTOP-SPEC.md §5).
const loginEntryName = "downex.desktop"

// SetLaunchAtLogin makes the desktop session start exe with args when the current user
// signs in (enabled) or stops it from doing so: it writes or removes the XDG autostart
// entry $XDG_CONFIG_HOME/autostart/downex.desktop (default ~/.config/autostart). It
// changes nothing when the entry is already as requested, and removing an entry that
// does not exist succeeds. exe must be an absolute path.
func SetLaunchAtLogin(enabled bool, exe string, args ...string) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	return setAutostartEntry(path, enabled, exe, args...)
}

// autostartPath locates the autostart entry from the environment.
func autostartPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("platform: locate the home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", loginEntryName), nil
}

// setAutostartEntry implements SetLaunchAtLogin for the entry at path.
func setAutostartEntry(path string, enabled bool, exe string, args ...string) error {
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("platform: remove the login entry: %w", err)
		}
		return nil
	}
	data, err := desktopEntry(exe, args)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
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

// desktopEntry is the autostart entry that runs exe with args.
func desktopEntry(exe string, args []string) ([]byte, error) {
	if !filepath.IsAbs(exe) {
		return nil, fmt.Errorf("platform: login entry: %q is not an absolute executable path", exe)
	}
	words := make([]string, 0, 1+len(args))
	for _, w := range append([]string{exe}, args...) {
		if strings.ContainsFunc(w, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, fmt.Errorf("platform: login entry: %q contains a control character", w)
		}
		words = append(words, execQuote(w))
	}
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Downex\n")
	b.WriteString("Comment=Start the Downex download manager\n")
	b.WriteString("Exec=" + strings.Join(words, " ") + "\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("NoDisplay=true\n")
	b.WriteString("X-GNOME-Autostart-enabled=true\n")
	return []byte(b.String()), nil
}

// execQuote quotes one word of an Exec key (Desktop Entry Specification, "The Exec
// key"): inside double quotes, `"`, '`', '$' and '\' are escaped with a backslash, '%'
// is doubled (field codes), and the value's own string escaping then doubles every
// backslash.
func execQuote(w string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range w {
		switch r {
		case '"', '`', '$', '\\':
			b.WriteString(`\\`) // the quoting backslash, itself escaped for the string value
			if r == '\\' {
				b.WriteString(`\\`)
			} else {
				b.WriteRune(r)
			}
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
