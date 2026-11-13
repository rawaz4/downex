package add

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/views/viewstest"
)

// The Confirm dialog's File name is today's <input dir="auto">: its native field takes its
// text's direction, so a Kurdish or Arabic name sits at the right and reads right to left
// (ui/win's TestNativeFieldAutoDirection covers the EDIT itself).
func TestConfirmFileNameIsDirAuto(t *testing.T) {
	for _, name := range []string{"confirm-video-6-ku", "confirm-video-6-ar"} {
		h := viewstest.New(t, viewstest.Options{Fixture: name})
		c := confirmState(t, h, name)
		f := c.Name.Native()
		if f == nil {
			t.Fatalf("%s: the File name has no native field", name)
		}
		o := c.Name.Options()
		if !o.AutoDirection {
			t.Errorf("%s: the File name field is not dir=auto", name)
		}
		if d := o.ResolveDirection(c.Name.Text()); d != kit.DirRTL || !o.AlignsRightAt(d) {
			t.Errorf("%s: name %q resolves to direction %v (right-aligned %v), want right to left", name, c.Name.Text(), d, o.AlignsRightAt(d))
		}
	}
}
