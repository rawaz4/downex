package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

// TestSkinShots writes every page under both skins to $SKIN_SHOTS (a look for the owner; it
// asserts nothing).
func TestSkinShots(t *testing.T) {
	dir := os.Getenv("SKIN_SHOTS")
	if dir == "" {
		t.Skip("SKIN_SHOTS not set")
	}
	pages := []string{"general", "downloads", "network", "integration", "video", "appearance", "updates", "about"}
	for _, mac := range []bool{false, true} {
		for _, theme := range []string{"dark", "light"} {
			name := "win"
			if mac {
				name = "mac"
			}
			h := viewstest.New(t, viewstest.Options{Width: 1280, Height: 860, Theme: theme, Real: true, WebKit: mac})
			d := settings.New(h.App, views.SettingsRequest{}, settings.Options{})
			h.App.Dialogs.Show(d)
			h.Settle()
			for _, pg := range pages {
				h.Click(tab(t, d, pg))
				h.Settle()
				img, err := h.Render()
				if err != nil {
					t.Fatal(err)
				}
				if err := viewstest.WritePNG(filepath.Join(dir, name+"-"+theme+"-"+pg+".png"), img); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
