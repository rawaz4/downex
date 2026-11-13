//go:build darwin && cgo

package fieldtest

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"downex/ui/kit"
)

// TestFieldMatchesWebKit compares the fields with today's window, WebKit's inputs and
// textarea of the same CSS: DOWNEX_FIELD_WEBKIT names a 2x WKWebView snapshot (340 x 330
// points) of testdata/input.html. Without it the test is skipped.
func TestFieldMatchesWebKit(t *testing.T) {
	ref := os.Getenv("DOWNEX_FIELD_WEBKIT")
	if ref == "" {
		t.Skip("DOWNEX_FIELD_WEBKIT not set")
	}
	onMain(func() {
		wk := readPNG(t, ref)
		if wk == nil {
			return
		}
		r := &root{}
		w, d := open(t, r, kit.Size{W: 340, H: 330})
		if d == nil {
			return
		}
		defer w.Close()
		if ppd := r.host.Environment().Scale.PixelsPerDIP; ppd != 2 {
			t.Logf("the screen is %vx, the reference 2x", ppd)
			return
		}
		black, white := kit.RGB(0x000000), kit.RGB(0xffffff)
		o := kit.TextFieldOptions{Style: kit.TextStyle{Family: "system-ui", Size: 13, LineHeight: 1.4}, Color: black, Background: white,
			PlaceholderColor: black, Placeholder: "File name"}
		// The inputs' content boxes: 280 x 36 at (20, y), 1 px border, 11 px padding; the
		// textarea's 8 px padding and three 18 px lines.
		boxes := []kit.Rect{kit.R(32, 21, 288, 55), kit.R(32, 71, 288, 105), kit.R(32, 121, 288, 155), kit.R(32, 179, 288, 233), kit.R(32, 271, 288, 305)}
		f1, _ := w.NewTextField(o, kit.TextFieldCallbacks{})
		f1.SetText("budget-plan.xlsx")
		f2, _ := w.NewTextField(o, kit.TextFieldCallbacks{})
		o3 := o
		o3.AutoDirection = true
		f3, _ := w.NewTextField(o3, kit.TextFieldCallbacks{})
		f3.SetText("سڵاو.mp4")
		o4 := o
		o4.Placeholder = ""
		o4.Style = kit.TextStyle{Family: "ui-monospace", Size: 12, Weight: 350, LineHeight: 1.55, LetterSpacing: -0.01}
		f4, _ := w.NewField(o4, kit.FieldOptions{Kind: kit.FieldMultiline, LineHeight: 1.55}, kit.TextFieldCallbacks{})
		f4.SetText("first line\nsecond line")
		o5 := o
		o5.Placeholder = ""
		f5, _ := w.NewField(o5, kit.FieldOptions{Kind: kit.FieldPassword}, kit.TextFieldCallbacks{})
		f5.SetText("secret1")
		for i, f := range []kit.NativeField{f1, f2, f3, f4, f5} {
			f.SetBounds(boxes[i])
		}
		Pump(0.1)
		check := func(name string) {
			path := filepath.Join(t.TempDir(), name+".png")
			if !d.Snapshot(path) {
				t.Error("no snapshot")
				return
			}
			keep(t, path)
			img := readPNG(t, path)
			if img == nil {
				return
			}
			for i, b := range boxes {
				area := image.Rect(int(b.Min.X*2), int(b.Min.Y*2), int(b.Max.X*2), int(b.Max.Y*2))
				want := inkBox(wk, area)
				if !want.Empty() {
					// The text's columns only: a key window draws the caret at the field's
					// insertion point.
					area.Min.X, area.Max.X = want.Min.X-1, want.Max.X+1
				}
				got := inkBox(img, area)
				var sum, n float64
				for y := area.Min.Y; y < area.Max.Y; y++ {
					for x := area.Min.X; x < area.Max.X; x++ {
						a, _, _, _ := img.At(x, y).RGBA()
						c, _, _, _ := wk.At(x, y).RGBA()
						sum += float64(abs(int(a>>8) - int(c>>8)))
						n++
					}
				}
				t.Logf("%s field %d: native ink %v, WebKit ink %v, mean difference %.3f", name, i+1, got, want, sum/n)
				if got.Min.Y != want.Min.Y || got.Max.Y != want.Max.Y || abs(got.Min.X-want.Min.X) > 1 || abs(got.Max.X-want.Max.X) > 1 {
					t.Errorf("%s field %d: native ink %v, WebKit's %v", name, i+1, got, want)
				}
			}
		}
		check("native")
		f1.Focus()
		Pump(0.05)
		check("native-edited")
		f4.Focus()
		Pump(0.05)
		check("native-area-edited")
		f3.Focus()
		Pump(0.05)
		check("native-rtl-edited")
		f5.Focus()
		Pump(0.05)
		t.Logf("secure editor: %s", d.EditorInfo())
		check("native-password-edited")
	})
}
