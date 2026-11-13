//go:build darwin && cgo

package mac

// [fields] A window's fields follow its backing scale: they are placed again on the new
// device pixels when it changes (another screen), though the kit's rectangles stay the same.

import (
	"testing"

	"downex/ui/kit"
)

func TestFieldRescale(t *testing.T) {
	a := testApp(t)
	root := &recRoot{ground: kit.RGB(0xffffff)}
	var w *Window
	var err error
	mainDo(func() {
		w, err = a.NewWindow(root, WindowOptions{Title: "field scale", Size: kit.Size{W: 300, H: 120}})
	})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	defer mainDo(func() { w.Close() })
	mainDo(func() {
		nf, err := w.NewTextField(kit.TextFieldOptions{Style: kit.TextStyle{Family: "system-ui", Size: 13}}, kit.TextFieldCallbacks{})
		if err != nil {
			t.Errorf("field: %v", err)
			return
		}
		f := nf.(*macField)
		b := kit.R(10.25, 20.25, 200.25, 38.75) // off the grid at 1x and at 2x
		nf.SetBounds(b)
		real := w.ppd
		other := float32(2)
		if real == 2 {
			other = 1
		}
		at := func(ppd float32, what string) {
			got, hidden := f.nativeFrame()
			if want := fldRound(b, ppd); got != want || hidden {
				t.Errorf("%s: the field is on %v (hidden %v), want %v (%vx)", what, got, hidden, want, ppd)
			}
		}
		at(real, "placed")
		w.ppd = other
		w.fldRescale()
		at(other, "rescaled")
		// The window hears AppKit's backing change: environmentChanged reads the real scale
		// again, which differs from the one the fields were last placed for.
		w.environmentChanged()
		at(real, "after environmentChanged")
		nf.Release()
	})
}
