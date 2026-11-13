package progress

import (
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/state"
)

// The completion view's actions sit inside its scroll pane with room for the focus ring: the
// pane clips to its viewport, so a button flush with its edge lost its ring's left and bottom
// and its own last row of pixels (seen on the Mac, the same layout on Windows). The window
// keeps its size.
func TestCompleteViewKeepsTheRingInside(t *testing.T) {
	r := show(t, running(func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil }))
	v := r.s().done
	view, open := v.WindowBounds(), v.open.WindowBounds()
	const reach = 4 // the ring's 2 px offset and 2 px width
	if open.Min.X-reach < view.Min.X-0.01 || open.Max.Y+reach > view.Max.Y+0.01 {
		t.Errorf("Open %v and its ring do not fit the pane's viewport %v", open, view)
	}
	t.Logf("window %v, Open %v, viewport %v", r.lastSize(), open, view)
	if v.Overflows() {
		t.Error("the complete view scrolls")
	}
	_ = kit.Size{}
}
