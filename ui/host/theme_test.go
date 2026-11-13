package host

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The app's theme choice reaches every window before it exists (docs/DESIGN-SYSTEM.md §6.2):
// the stored choice, read before a window is created.

func TestThemeFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Downex", ThemeFileName)
	if got := LoadThemeChoice(path); got != ThemeSystem {
		t.Fatalf("missing file reads as %q, want system", got)
	}
	for _, c := range []ThemeChoice{ThemeLight, ThemeDark, ThemeSystem} {
		if err := SaveThemeChoice(path, c); err != nil {
			t.Fatalf("save %q: %v", c, err)
		}
		if got := LoadThemeChoice(path); got != c {
			t.Fatalf("round trip of %q = %q", c, got)
		}
	}
	// Today's window reads the same file: {"theme":"…"} and a newline.
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "{\"theme\":\"system\"}\n" {
		t.Fatalf("file = %q, %v", b, err)
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
		if got := LoadThemeChoice(path); got != ThemeSystem {
			t.Errorf("%s: reads as %q, want system", name, got)
		}
	}
	if got := LoadThemeChoice(""); got != ThemeSystem {
		t.Errorf("no path reads as %q", got)
	}
	if err := SaveThemeChoice("", ThemeDark); err == nil {
		t.Error("saving without a path succeeded")
	}
	if err := SaveThemeChoice(path, "purple"); err == nil {
		t.Error("saving an unknown choice succeeded")
	}
}

func TestThemeChoiceMapping(t *testing.T) {
	for _, tc := range []struct {
		in        string
		ok        bool
		c         ThemeChoice
		darkOnLit bool // dark while the system is light
		darkOnDim bool // dark while the system is dark
	}{
		{"system", true, ThemeSystem, false, true},
		{"light", true, ThemeLight, false, false},
		{"dark", true, ThemeDark, true, true},
	} {
		c, ok := ParseThemeChoice(tc.in)
		if ok != tc.ok || c != tc.c {
			t.Errorf("parse %q = %q, %v", tc.in, c, ok)
		}
		if c.IsDark(false) != tc.darkOnLit || c.IsDark(true) != tc.darkOnDim {
			t.Errorf("%q: dark on a light system %v, on a dark one %v", tc.in, c.IsDark(false), c.IsDark(true))
		}
	}
	for _, bad := range []string{"", "System", "auto", "dark ", "purple"} {
		if c, ok := ParseThemeChoice(bad); ok || c != ThemeSystem {
			t.Errorf("%q accepted as %q", bad, c)
		}
	}
}

// TestBackgroundByChoice: a window opens in the ground it then paints, in the app's theme
// (not the system's when the app forces one): no dark flash in a light app.
func TestBackgroundByChoice(t *testing.T) {
	for _, tc := range []struct {
		v          View
		c          ThemeChoice
		systemDark bool
		want       RGB
	}{
		{View{}, ThemeSystem, false, ChromeLight},
		{View{}, ThemeSystem, true, ChromeDark},
		{View{}, ThemeLight, true, ChromeLight},
		{View{}, ThemeDark, false, ChromeDark},
		{View{Progress: 4}, ThemeSystem, false, SurfaceLight},
		{View{Progress: 4}, ThemeSystem, true, SurfaceDark},
		{View{Progress: 4}, ThemeLight, true, SurfaceLight},
		{View{Progress: 4}, ThemeDark, false, SurfaceDark},
	} {
		if got := tc.v.Window().Background(tc.c.IsDark(tc.systemDark)); got != tc.want {
			t.Errorf("view %+v, %q, system dark %v: background %+v, want %+v", tc.v, tc.c, tc.systemDark, got, tc.want)
		}
	}
}

// The file lives next to the WebView2 folder on Windows, in the configuration folder
// elsewhere; without a base folder the choice is not kept.
func TestThemePath(t *testing.T) {
	cache := func() (string, error) { return `C:\Users\a\AppData\Local`, nil }
	config := func() (string, error) { return "/home/a/.config", nil }
	none := func() (string, error) { return "", errors.New("no home") }
	if got := themePathOn("windows", cache, config); got != filepath.Join(`C:\Users\a\AppData\Local`, "Downex", "ui-theme.json") {
		t.Errorf("windows: %q", got)
	}
	if got := themePathOn("linux", cache, config); got != filepath.Join("/home/a/.config", "Downex", "ui-theme.json") {
		t.Errorf("linux: %q", got)
	}
	if got := themePathOn("darwin", none, none); got != "" {
		t.Errorf("without a folder: %q", got)
	}
}

func TestSessionSaveTheme(t *testing.T) {
	s, _ := newTestSession(t, newFakeDaemon())
	if err := s.SaveTheme(ThemeDark); err != nil {
		t.Fatalf("SaveTheme: %v", err)
	}
	if got := s.LoadTheme(); got != ThemeDark {
		t.Fatalf("stored choice %q, want dark", got)
	}
	if err := s.SaveTheme("purple"); errText(err) != `invalid_argument: theme must be "system", "light" or "dark"` {
		t.Errorf("SaveTheme(purple) = %v", err)
	}
	if got := s.LoadTheme(); got != ThemeDark {
		t.Errorf("a refused choice changed the stored one to %q", got)
	}
	// A folder that cannot be made: the choice is not stored, internal.
	blocker := filepath.Join(t.TempDir(), "file")
	writeFile(t, blocker)
	b := NewSession(SessionOptions{Daemon: newFakeDaemon(), Shell: &fakeShell{}, ThemePath: filepath.Join(blocker, ThemeFileName)})
	defer b.Close()
	if err := b.SaveTheme(ThemeLight); !hasPrefix(err, "internal: cannot store the theme: ") {
		t.Errorf("SaveTheme into a file = %v", err)
	}
}
