package app_test

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/views/app"
	"downex/ui/views/viewstest"
)

// mainWindow composes the main window over the harness's Studio scene.
func mainWindow(t *testing.T, o viewstest.Options) (*viewstest.Harness, *app.Main) {
	t.Helper()
	h := viewstest.New(t, o)
	m, err := app.New(h.App, app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Dispose)
	h.Show(m.Root())
	h.Settle()
	// The scene's budget-plan.xlsx awaits confirmation, so its dialog opens by itself; the
	// references' main state shows the window without it (closed: the download stays waiting).
	for i := 0; i < 3 && h.App.Dialogs.DialogOpen(); i++ {
		h.Key(kit.KeyEscape, 0)
		h.Settle()
	}
	if h.App.Dialogs.DialogOpen() {
		t.Fatalf("a %v dialog stays open", h.App.Dialogs.Kind())
	}
	h.Window.Blur() // the references have the focus nowhere
	h.Settle()
	return h, m
}

// TestRenderPeek writes the native main window and the reference side by side into
// $DOWNEX_PEEK (a scratch folder) for a quick look.
func TestRenderPeek(t *testing.T) {
	dir := os.Getenv("DOWNEX_PEEK")
	if dir == "" {
		t.Skip("DOWNEX_PEEK not set")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	spec := os.Getenv("DOWNEX_PEEK_SPEC") // e.g. 1200x760@1:16:light
	if spec == "" {
		spec = "1200x760@1:16:light"
	}
	var w, hh, font int
	var sc float32
	var theme string
	if _, err := fmt.Sscanf(strings.NewReplacer("x", " ", "@", " ", ":", " ").Replace(spec), "%d %d %g %d %s", &w, &hh, &sc, &font, &theme); err != nil {
		t.Fatal(err)
	}
	o := viewstest.Options{Width: w, Height: hh, Scale: sc, Font: font, Theme: theme, Real: true}
	h, _ := mainWindow(t, o)
	img, err := h.Render()
	if err != nil {
		t.Fatal(err)
	}
	shot, ok := refs.For(o)
	if !ok {
		t.Fatal("no reference")
	}
	ref, err := refs.Image(shot)
	if err != nil {
		t.Fatal(err)
	}
	c := viewstest.Compare(ref, img, kit.R(0, 0, kit.DIP(w), kit.DIP(hh)), kit.Point{}, sc)
	t.Logf("mean %.2f differing %.3f", c.Mean, c.Differing)
	if err := viewstest.WritePNG(filepath.Join(dir, "peek.png"), c.Sheet); err != nil {
		t.Fatal(err)
	}
	// Crops: the reference over the native, 2x.
	crops := map[string][4]int{"tl": {0, 0, w / 2, 110}, "tr": {w / 2, 0, w, 110}, "bl": {0, hh - 160, w / 2, hh}, "br": {w / 2, hh - 160, w, hh}}
	for name, b := range crops {
		for i := range b {
			b[i] = int(float32(b[i]) * sc)
		}
		writeCrop(t, filepath.Join(dir, "c_"+name+".png"), ref, img, b)
	}
}

func writeCrop(t *testing.T, path string, ref, nat image.Image, b [4]int) {
	cw, ch := b[2]-b[0], b[3]-b[1]
	out := image.NewRGBA(image.Rect(0, 0, cw*2, (ch*2+2)*2))
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			r := ref.At(b[0]+x, b[1]+y)
			n := nat.At(b[0]+x, b[1]+y)
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					out.Set(x*2+dx, y*2+dy, r)
					out.Set(x*2+dx, (ch+2)*2+y*2+dy, n)
				}
			}
		}
	}
	if err := viewstest.WritePNG(path, out); err != nil {
		t.Fatal(err)
	}
}
