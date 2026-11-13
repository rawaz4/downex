package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
	"downex/ui/state/scene"
	"downex/ui/views/viewstest"
)

// TestReadmeShots writes the README's screenshots (the Studio scene's demo downloads, never real
// ones) into $README_SHOTS: the main window in both themes, Settings and the command palette.
// It asserts nothing.
func TestReadmeShots(t *testing.T) {
	dir := os.Getenv("README_SHOTS")
	if dir == "" {
		t.Skip("README_SHOTS not set")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	shots := []struct {
		name, theme string
		open        func(*viewstest.Harness)
	}{
		{"main-dark", "dark", nil},
		{"main-light", "light", nil},
		{"settings-dark", "dark", func(h *viewstest.Harness) { h.Key(kit.KeyComma, kit.ModCtrl) }},
		{"palette-dark", "dark", func(h *viewstest.Harness) { h.Key(kit.KeyK, kit.ModCtrl) }},
	}
	for _, s := range shots {
		// "telemetry" reads wrong beside "no telemetry": the picture shows another name
		rename := func(o *scene.Options) { o.Rename = map[string]string{"telemetry-183.dat": "session-backup-183.dat"} }
		h, _ := mainWindow(t, viewstest.Options{Width: 1280, Height: 800, Scale: 1.5, Theme: s.theme, Real: true, Connected: true, Scene: rename})
		shotTheme(h, s.theme)
		if s.open != nil {
			s.open(h)
			h.Settle()
		}
		img, err := h.Render()
		if err != nil {
			t.Fatal(err)
		}
		if err := viewstest.WritePNG(filepath.Join(dir, s.name+".png"), img); err != nil {
			t.Fatal(err)
		}
	}
}
