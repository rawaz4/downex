package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// The app's theme for what the page does not draw (docs/DESIGN-SYSTEM.md §6.2): the native
// title bar of the progress and download complete windows (they keep the system frame),
// the window's ground before the page paints, and macOS's appearance. The page keeps
// its own choice (Settings → Appearance) and tells the host with SetTheme; the host
// stores it in a small per-user file that every window process reads before its window
// is created, so a light app never opens a dark window, also when downexd starts the
// window.

// themeChoice is the app's theme: follow the system, or force light or dark.
type themeChoice string

const (
	themeSystem themeChoice = "system"
	themeLight  themeChoice = "light"
	themeDark   themeChoice = "dark"
)

// themeFileName is the stored choice: {"theme": "system" | "light" | "dark"}.
const themeFileName = "ui-theme.json"

// maxThemeFile bounds what is read of the file; anything longer is not one.
const maxThemeFile = 4 << 10

// parseThemeChoice accepts exactly "system", "light" and "dark".
func parseThemeChoice(s string) (themeChoice, bool) {
	switch c := themeChoice(s); c {
	case themeSystem, themeLight, themeDark:
		return c, true
	}
	return themeSystem, false
}

// isDark reports whether the window is dark when the system's apps are (systemDark).
func (c themeChoice) isDark(systemDark bool) bool {
	switch c {
	case themeLight:
		return false
	case themeDark:
		return true
	}
	return systemDark
}

// windowsTheme is the Windows title bar theme of the choice: System follows Windows and
// its changes (Wails re-applies it on WM_SETTINGCHANGE "ImmersiveColorSet").
func (c themeChoice) windowsTheme() windows.Theme {
	switch c {
	case themeLight:
		return windows.Light
	case themeDark:
		return windows.Dark
	}
	return windows.SystemDefault
}

// themePath is the stored choice of this user: next to the WebView2 folder on Windows
// (%LOCALAPPDATA%\Downex\ui-theme.json), in the user's configuration folder elsewhere;
// "" when there is none (the choice is then not kept).
func themePath() string {
	var (
		base string
		err  error
	)
	if runtime.GOOS == "windows" {
		base, err = os.UserCacheDir() // %LOCALAPPDATA%
	} else {
		base, err = os.UserConfigDir()
	}
	if err != nil || base == "" {
		return ""
	}
	return filepath.Join(base, "Downex", themeFileName)
}

type themeFile struct {
	Theme string `json:"theme"`
}

// loadThemeChoice reads the stored choice at path. A missing, unreadable, oversized or
// malformed file, or an unknown value, reads as system.
func loadThemeChoice(path string) themeChoice {
	if path == "" {
		return themeSystem
	}
	f, err := os.Open(path)
	if err != nil {
		return themeSystem
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxThemeFile+1))
	if err != nil || len(b) > maxThemeFile {
		return themeSystem
	}
	var tf themeFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return themeSystem
	}
	c, ok := parseThemeChoice(tf.Theme)
	if !ok {
		return themeSystem
	}
	return c
}

// saveThemeChoice stores c at path atomically: a temporary file in the same folder
// (0600, synced) renamed over the old one, so a reader never sees half a file. It
// creates the folder (0700) when needed.
func saveThemeChoice(path string, c themeChoice) error {
	if path == "" {
		return errors.New("no folder for the theme file")
	}
	if _, ok := parseThemeChoice(string(c)); !ok {
		return fmt.Errorf("unknown theme %q", c)
	}
	b, err := json.Marshal(themeFile{Theme: string(c)})
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
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

// The page's colours of the native caption of a progress window (docs/DESIGN-SYSTEM.md
// §2.1), so that it reads as part of the page on Windows 11 (older systems ignore them):
// --surface ground, --ink title (--faint while inactive), --line-strong border (--line
// while inactive), each in the light and the dark theme.
var (
	inkLight, faintLight       = rgb{0x0f, 0x16, 0x13}, rgb{0x65, 0x70, 0x6a}
	lineLight, lineStrongLight = rgb{0xdf, 0xe5, 0xe2}, rgb{0xc9, 0xd1, 0xcd}
	inkDark, faintDark         = rgb{0xe8, 0xef, 0xeb}, rgb{0x8b, 0x98, 0x91}
	lineDark, lineStrongDark   = rgb{0x30, 0x3b, 0x36}, rgb{0x3d, 0x49, 0x43}
)

func (c rgb) colorref() int32 { return windows.RGB(c.r, c.g, c.b) }

// captionTheme is the native caption of a window with the system frame in the page's
// colours; Wails applies the light or the dark half as the theme is.
func captionTheme() *windows.ThemeSettings {
	return &windows.ThemeSettings{
		LightModeTitleBar: surfaceLight.colorref(), LightModeTitleBarInactive: surfaceLight.colorref(),
		LightModeTitleText: inkLight.colorref(), LightModeTitleTextInactive: faintLight.colorref(),
		LightModeBorder: lineStrongLight.colorref(), LightModeBorderInactive: lineLight.colorref(),
		DarkModeTitleBar: surfaceDark.colorref(), DarkModeTitleBarInactive: surfaceDark.colorref(),
		DarkModeTitleText: inkDark.colorref(), DarkModeTitleTextInactive: faintDark.colorref(),
		DarkModeBorder: lineStrongDark.colorref(), DarkModeBorderInactive: lineDark.colorref(),
	}
}

// windowsOptions are the Windows options of a window of spec in theme c. Every view uses
// one WebView2 user-data folder and the same environment options (browser arguments,
// such as the features Wails turns off, follow from these and from options.App fields
// that every view leaves at their defaults; Frameless, which only the main window sets,
// adds none; the theme and caption colours add none), so that WebView2 runs one browser
// process for all of them instead of one per window. The title bar's theme is c; a window
// with the system frame (progress windows) also takes the page's caption colours.
// DisableFramelessWindowDecorations stays false: the frameless main window keeps the
// system's shadow, rounded corners and resize borders.
func windowsOptions(c themeChoice, spec windowSpec) *windows.Options {
	o := &windows.Options{
		Theme:               c.windowsTheme(),
		WebviewUserDataPath: webviewDataDir(),
		DisablePinchZoom:    true,
	}
	if !spec.frameless {
		o.CustomTheme = captionTheme()
	}
	return o
}

// themeRequest is the argument of SetTheme.
type themeRequest struct {
	Theme string `json:"theme"`
}

// decodeStrict parses a JSON object argument into v, refusing unknown fields, trailing
// data and anything that is not an object (an absent argument too).
func decodeStrict(raw json.RawMessage, v any) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return errors.New("the argument must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the object")
	}
	return nil
}
