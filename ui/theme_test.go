package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

// The app's theme choice reaches the native parts of every window (docs/DESIGN-SYSTEM.md
// §6.2): the stored choice, read before a window is created, and SetTheme.

func TestThemeFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Downex", themeFileName)
	if got := loadThemeChoice(path); got != themeSystem {
		t.Fatalf("missing file reads as %q, want system", got)
	}
	for _, c := range []themeChoice{themeLight, themeDark, themeSystem} {
		if err := saveThemeChoice(path, c); err != nil {
			t.Fatalf("save %q: %v", c, err)
		}
		if got := loadThemeChoice(path); got != c {
			t.Fatalf("round trip of %q = %q", c, got)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != `{"theme":"system"}` {
		t.Fatalf("file = %s, %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("the folder holds %d entries, want only the file (no temporary left)", len(entries))
	}
	for name, content := range map[string]string{
		"garbage": "not json", "unknown value": `{"theme":"purple"}`, "wrong type": `{"theme":1}`, "empty": "",
		"too long": `{"theme":"dark","pad":"` + strings.Repeat("x", 8<<10) + `"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := loadThemeChoice(path); got != themeSystem {
			t.Errorf("%s: reads as %q, want system", name, got)
		}
	}
	if got := loadThemeChoice(""); got != themeSystem {
		t.Errorf("no path reads as %q", got)
	}
	if err := saveThemeChoice("", themeDark); err == nil {
		t.Error("saving without a path succeeded")
	}
}

func TestThemeChoiceMapping(t *testing.T) {
	for _, tc := range []struct {
		in        string
		ok        bool
		c         themeChoice
		win       windows.Theme
		mac       mac.AppearanceType
		darkOnLit bool // dark while the system is light
		darkOnDim bool // dark while the system is dark
	}{
		{"system", true, themeSystem, windows.SystemDefault, mac.DefaultAppearance, false, true},
		{"light", true, themeLight, windows.Light, mac.NSAppearanceNameAqua, false, false},
		{"dark", true, themeDark, windows.Dark, mac.NSAppearanceNameDarkAqua, true, true},
	} {
		c, ok := parseThemeChoice(tc.in)
		if ok != tc.ok || c != tc.c {
			t.Errorf("parse %q = %q, %v", tc.in, c, ok)
		}
		if got := c.windowsTheme(); got != tc.win {
			t.Errorf("%q: windows theme %v, want %v", tc.in, got, tc.win)
		}
		if got := macOptions(c).Appearance; got != tc.mac {
			t.Errorf("%q: mac appearance %q, want %q", tc.in, got, tc.mac)
		}
		if c.isDark(false) != tc.darkOnLit || c.isDark(true) != tc.darkOnDim {
			t.Errorf("%q: dark on a light system %v, on a dark one %v", tc.in, c.isDark(false), c.isDark(true))
		}
	}
	for _, bad := range []string{"", "System", "auto", "dark ", "purple"} {
		if _, ok := parseThemeChoice(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestBackgroundByChoice: a window opens in the ground its page then paints, in the
// app's theme (not the system's when the app forces one): no dark flash in a light app.
func TestBackgroundByChoice(t *testing.T) {
	for _, tc := range []struct {
		v          view
		c          themeChoice
		systemDark bool
		want       rgb
	}{
		{view{}, themeSystem, false, chromeLight},
		{view{}, themeSystem, true, chromeDark},
		{view{}, themeLight, true, chromeLight},
		{view{}, themeDark, false, chromeDark},
		{view{progress: 4}, themeSystem, false, surfaceLight},
		{view{progress: 4}, themeSystem, true, surfaceDark},
		{view{progress: 4}, themeLight, true, surfaceLight},
		{view{progress: 4}, themeDark, false, surfaceDark},
	} {
		if got := tc.v.window().background(tc.c.isDark(tc.systemDark)); got != tc.want {
			t.Errorf("view %+v, %q, system dark %v: background %+v, want %+v", tc.v, tc.c, tc.systemDark, got, tc.want)
		}
	}
}

// TestWindowsOptionsByChoice: every view shares the WebView2 profile and environment;
// the theme follows the choice, and the native caption of a progress window takes the
// page's --surface and ink in both themes (the frameless main window draws its own).
func TestWindowsOptionsByChoice(t *testing.T) {
	for _, c := range []themeChoice{themeSystem, themeLight, themeDark} {
		main := windowsOptions(c, view{}.windowOn("windows"))
		progress := windowsOptions(c, view{progress: 2}.windowOn("windows"))
		for _, o := range []*windows.Options{main, progress} {
			if o.Theme != c.windowsTheme() || o.WebviewUserDataPath != webviewDataDir() || !o.DisablePinchZoom {
				t.Errorf("%q: options %+v", c, o)
			}
		}
		if main.CustomTheme != nil {
			t.Errorf("%q: the frameless main window got caption colours", c)
		}
		ct := progress.CustomTheme
		if ct == nil {
			t.Fatalf("%q: the progress window has no caption colours", c)
		}
		want := windows.ThemeSettings{
			LightModeTitleBar: windows.RGB(0xff, 0xff, 0xff), LightModeTitleBarInactive: windows.RGB(0xff, 0xff, 0xff),
			LightModeTitleText: windows.RGB(0x0f, 0x16, 0x13), LightModeTitleTextInactive: windows.RGB(0x65, 0x70, 0x6a),
			LightModeBorder: windows.RGB(0xc9, 0xd1, 0xcd), LightModeBorderInactive: windows.RGB(0xdf, 0xe5, 0xe2),
			DarkModeTitleBar: windows.RGB(0x17, 0x1e, 0x1b), DarkModeTitleBarInactive: windows.RGB(0x17, 0x1e, 0x1b),
			DarkModeTitleText: windows.RGB(0xe8, 0xef, 0xeb), DarkModeTitleTextInactive: windows.RGB(0x8b, 0x98, 0x91),
			DarkModeBorder: windows.RGB(0x3d, 0x49, 0x43), DarkModeBorderInactive: windows.RGB(0x30, 0x3b, 0x36),
		}
		if *ct != want {
			t.Errorf("%q: caption colours %+v, want %+v", c, *ct, want)
		}
	}
}

func TestSetThemeBinding(t *testing.T) {
	host := &fakeHost{}
	a, _ := newTestApp(t, newFakeDaemon(), host)
	if err := a.SetTheme(json.RawMessage(`{"theme":"dark"}`)); err != nil {
		t.Fatalf("SetTheme: %v", err)
	}
	if got := host.themesSet(); len(got) != 1 || got[0] != themeDark {
		t.Fatalf("host themes %v, want [dark]", got)
	}
	if got := loadThemeChoice(a.themePath); got != themeDark {
		t.Fatalf("stored choice %q, want dark", got)
	}
	if err := a.SetTheme(json.RawMessage(`{"theme":"system"}`)); err != nil {
		t.Fatalf("SetTheme: %v", err)
	}
	if got := loadThemeChoice(a.themePath); got != themeSystem {
		t.Fatalf("stored choice %q, want system", got)
	}

	for _, bad := range []string{`{"theme":"purple"}`, `{"theme":""}`, `{}`, `null`, `{"theme":"dark","extra":1}`, `{"theme":3}`, `[1]`} {
		err := a.SetTheme(json.RawMessage(bad))
		if err == nil || !strings.HasPrefix(err.Error(), "invalid_argument: ") {
			t.Errorf("SetTheme(%s) = %v, want invalid_argument", bad, err)
		}
	}
	if got := host.themesSet(); len(got) != 2 {
		t.Errorf("refused requests reached the window: %v", got)
	}
	if got := loadThemeChoice(a.themePath); got != themeSystem {
		t.Errorf("a refused request changed the stored choice to %q", got)
	}

	// Before the window exists the choice is still stored for the next window.
	b, _ := newTestApp(t, newFakeDaemon(), nil)
	if err := b.SetTheme(json.RawMessage(`{"theme":"light"}`)); err != nil {
		t.Fatalf("SetTheme without a window: %v", err)
	}
	if got := loadThemeChoice(b.themePath); got != themeLight {
		t.Errorf("stored choice %q, want light", got)
	}
}
