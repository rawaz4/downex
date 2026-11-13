package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// The app's theme for what a window draws before it knows the page's choice
// (docs/DESIGN-SYSTEM.md §6.2): the native title bar of the windows with the system frame,
// the window's ground before it paints, and macOS's appearance. The choice is the user's
// (Settings → Appearance); every window process reads it from a small per-user file before
// its window is created, so a light app never opens a dark window, also when downexd
// starts the window. Today's window (ui/theme.go) and the native window share the file.

// ThemeChoice is the app's theme: follow the system, or force light or dark.
type ThemeChoice string

const (
	ThemeSystem ThemeChoice = "system"
	ThemeLight  ThemeChoice = "light"
	ThemeDark   ThemeChoice = "dark"
)

// ThemeFileName is the stored choice: {"theme": "system" | "light" | "dark"}.
const ThemeFileName = "ui-theme.json"

// maxThemeFile bounds what is read of the file; anything longer is not one.
const maxThemeFile = 4 << 10

// ParseThemeChoice accepts exactly "system", "light" and "dark".
func ParseThemeChoice(s string) (ThemeChoice, bool) {
	switch c := ThemeChoice(s); c {
	case ThemeSystem, ThemeLight, ThemeDark:
		return c, true
	}
	return ThemeSystem, false
}

// IsDark reports whether the window is dark when the system's apps are (systemDark).
func (c ThemeChoice) IsDark(systemDark bool) bool {
	switch c {
	case ThemeLight:
		return false
	case ThemeDark:
		return true
	}
	return systemDark
}

// DefaultThemePath is this user's stored choice: next to the WebView2 folder on Windows
// (%LOCALAPPDATA%\Downex\ui-theme.json), in the user's configuration folder elsewhere; ""
// when there is none (the choice is then not kept).
func DefaultThemePath() string { return themePathOn(runtime.GOOS, os.UserCacheDir, os.UserConfigDir) }

func themePathOn(goos string, cacheDir, configDir func() (string, error)) string {
	dir := configDir
	if goos == "windows" {
		dir = cacheDir // %LOCALAPPDATA%
	}
	base, err := dir()
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "Downex", ThemeFileName)
}

type themeFile struct {
	Theme string `json:"theme"`
}

// LoadThemeChoice reads the stored choice at path. A missing, unreadable, oversized or
// malformed file, or an unknown value, reads as system.
func LoadThemeChoice(path string) ThemeChoice {
	if path == "" {
		return ThemeSystem
	}
	b, ok := readSmallFile(path, maxThemeFile)
	if !ok {
		return ThemeSystem
	}
	var tf themeFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return ThemeSystem
	}
	c, ok := ParseThemeChoice(tf.Theme)
	if !ok {
		return ThemeSystem
	}
	return c
}

// SaveThemeChoice stores c at path atomically (writeFileAtomic).
func SaveThemeChoice(path string, c ThemeChoice) error {
	if path == "" {
		return errors.New("no folder for the theme file")
	}
	if _, ok := ParseThemeChoice(string(c)); !ok {
		return fmt.Errorf("unknown theme %q", c)
	}
	b, err := json.Marshal(themeFile{Theme: string(c)})
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// readSmallFile reads the file at path when it holds at most limit bytes.
func readSmallFile(path string, limit int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, false
	}
	return b, true
}

// writeFileAtomic writes b and a newline to path through a temporary file in the same
// folder (0600, synced) renamed over the old one, so a reader never sees half a file. It
// creates the folder (0700) when needed.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(append(b, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}
