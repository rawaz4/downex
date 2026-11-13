package settings_test

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"downex/ui/kit"
	"downex/ui/views"
	"downex/ui/views/settings"
	"downex/ui/views/viewstest"
)

func TestExplore(t *testing.T) {
	if os.Getenv("SETTINGS_EXPLORE") == "" {
		t.Skip()
	}
	refs, err := viewstest.LoadRefs()
	if err != nil {
		t.Skip(err)
	}
	o := viewstest.Options{Width: atoi(os.Getenv("W"), 1200), Height: atoi(os.Getenv("H"), 760), Scale: float32(atoi(os.Getenv("S100"), 100)) / 100, Font: atoi(os.Getenv("F"), 16), Theme: "light", Fixture: os.Getenv("SETTINGS_FIX"), Real: true}
	h := viewstest.New(t, o)
	d := settings.New(h.App, views.SettingsRequest{}, settings.Options{})
	h.App.Dialogs.Show(d)
	h.Settle()
	h.Window.SetFocusVisible(true)
	h.Settle()
	if pg := os.Getenv("SETTINGS_PAGE"); pg != "" {
		h.Click(tab(t, d, pg))
	}
	if b := os.Getenv("SETTINGS_BUTTON"); b != "" {
		press(h, buttonNamed(t, d.Card(), b))
	}
	h.Settle()
	img, err := h.Render()
	if err != nil {
		t.Fatal(err)
	}
	_ = viewstest.WritePNG(filepath.Join(os.Getenv("SHOTS"), "native.png"), img)
	shot, _ := refs.For(o)
	ref, _ := refs.Image(shot)
	card := d.Card().WindowBounds()
	t.Log("card", card)
	for _, k := range d.Card().Children() {
		t.Logf("kid %T %v", k.Widget(), k.WindowBounds())
		for _, k2 := range k.Children() {
			t.Logf("   kid %T %v", k2.Widget(), k2.WindowBounds())
		}
	}
	if pat := os.Getenv("DUMP"); pat != "" {
		walk(d.Pane().Base(), func(e *kit.Element) bool {
			if txt := textOf(e); strings.Contains(txt, pat) || strings.Contains(e.Semantics.Name, pat) {
				for x := e; x != nil && x != d.Pane().Base(); x = x.Parent() {
					t.Logf("dump %T %v %q", x.Widget(), x.WindowBounds(), x.Semantics.Name)
				}
				return false
			}
			return true
		})
	}
	c := viewstest.Compare(ref, img, kit.R(card.Min.X, card.Min.Y, card.Max.X, card.Max.Y), card.Min, 1)
	t.Log(c.Mean, c.Differing)
	_ = viewstest.WritePNG(filepath.Join(os.Getenv("SHOTS"), "explore.png"), c.Sheet)
	if z := os.Getenv("ZOOM"); z != "" {
		var r [4]int
		fmt.Sscanf(z, "%d,%d,%d,%d", &r[0], &r[1], &r[2], &r[3])
		zr := kit.R(kit.DIP(r[0]), kit.DIP(r[1]), kit.DIP(r[2]), kit.DIP(r[3]))
		zc := viewstest.Compare(ref, img, zr, zr.Min, o.Scale)
		b := zc.Sheet.Bounds()
		k := 3
		out := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
		for y := 0; y < b.Dy()*k; y++ {
			for x := 0; x < b.Dx()*k; x++ {
				out.Set(x, y, zc.Sheet.At(b.Min.X+x/k, b.Min.Y+y/k))
			}
		}
		_ = viewstest.WritePNG(filepath.Join(os.Getenv("SHOTS"), "zoom.png"), out)
		dx, dy, best, plain := viewstest.BestShift(ref, img, zr, zr.Min, o.Scale, 4)
		t.Logf("zoom shift %d,%d best %.2f plain %.2f", dx, dy, best, plain)
	}
}

func atoi(s string, d int) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return d
	}
	return n
}
