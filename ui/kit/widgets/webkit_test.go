package widgets_test

// [mac webkit] Widgets under kit.Environment.WebKit (the Mac window), and the same widgets
// without it (Windows), which must draw exactly as before.

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
	"downex/ui/kit/widgets"
)

func webkitEnv(on bool) kit.Environment {
	return kit.Environment{Scale: kit.Scale{PixelsPerDIP: 1, Text: 1}, WebKit: on}
}

// paintOps repaints the whole window and returns what was drawn.
func (r *rig) paintOps() []kittest.Op {
	c := &kittest.Canvas{}
	full := kit.RectAt(kit.Point{}, r.win.Size())
	r.win.Paint(c, full)
	return c.Ops
}

func opsWithin(ops []kittest.Op, b kit.Rect) (kinds []string, all []kittest.Op) {
	for _, o := range ops {
		if o.Kind == "FillPath" || o.Kind == "StrokePath" || (!o.Rect.Empty() && !b.Intersect(o.Rect).Empty()) {
			kinds = append(kinds, o.Kind)
			all = append(all, o)
		}
	}
	return kinds, all
}

func count(kinds []string, k string) int {
	n := 0
	for _, v := range kinds {
		if v == k {
			n++
		}
	}
	return n
}

// Without WebKit the check box is Chromium's (a rounded rectangle and its border, or the
// accent square with a stroked check); with it, macOS's (paths: the ground and the ring,
// or the fill and the round-capped check).
func TestCheckBoxWebKitLook(t *testing.T) {
	off := widgets.NewCheckbox("Off", false, nil)
	on := widgets.NewCheckbox("On", true, nil)
	r := newRig(t, kit.Size{W: 300, H: 200}, off, on)

	kinds, _ := opsWithin(r.paintOps(), off.Box.WindowBounds())
	if count(kinds, "FillPath") != 0 || count(kinds, "StrokePath") != 1 || count(kinds, "FillRoundedRect") < 1 {
		t.Fatalf("Chromium look: ops %v", kinds)
	}

	r.loop.Send(kit.EnvironmentEvent{Env: webkitEnv(true)})
	r.settle()
	kinds, all := opsWithin(r.paintOps(), off.Box.WindowBounds())
	// off: ground + ring; on: ground + fill (paths) and the check (a round stroke)
	if count(kinds, "FillPath") != 4 || count(kinds, "StrokePath") != 1 {
		t.Fatalf("WebKit look: ops %v", kinds)
	}
	for _, o := range all {
		if o.Kind == "StrokePath" && (o.Stroke.Cap != kit.CapRound || o.Stroke.Join != kit.JoinRound) {
			t.Errorf("the check's stroke %+v, want round caps and joins", o.Stroke)
		}
	}
	// the light ring is black at half alpha over a white ground
	var ring, ground bool
	for _, o := range all {
		if o.Kind == "FillPath" && o.Color == (kit.Color{A: 0.5}) {
			ring = true
		}
		if o.Kind == "FillPath" && o.Color == kit.RGB(0xffffff) {
			ground = true
		}
	}
	if !ring || !ground {
		t.Errorf("WebKit look: ring %v ground %v in %+v", ring, ground, all)
	}

	r.loop.Send(kit.EnvironmentEvent{Env: webkitEnv(false)})
	r.settle()
	kinds, _ = opsWithin(r.paintOps(), off.Box.WindowBounds())
	if count(kinds, "FillPath") != 0 {
		t.Fatalf("Chromium look again: ops %v", kinds)
	}
}

// The primary button's --primary-lift starts with an inset highlight (inset 0 1px 0 rgb(255
// 255 255 / 0.18)), drawn over the fill in both looks: Edge draws it (the 0.9.2 references:
// the row under a light primary button's top border is #3b976f over #10804f) and so does
// WebKit.
func TestPrimaryInsetHighlight(t *testing.T) {
	b := widgets.NewButton("Save", "", widgets.Primary, widgets.SizeMd, nil)
	r := newRig(t, kit.Size{W: 300, H: 200}, b)
	insets := func() (n int, afterFill bool) {
		filled := false
		for _, o := range r.paintOps() {
			if b.WindowBounds().Intersect(o.Rect).Empty() {
				continue
			}
			if o.Kind == "FillRoundedRect" {
				filled = true
			}
			if o.Kind == "DrawShadow" && o.Color == kit.RGBA(255, 255, 255, 0.18) {
				n++
				afterFill = filled
			}
		}
		return n, afterFill
	}
	for _, on := range []bool{false, true} {
		r.loop.Send(kit.EnvironmentEvent{Env: webkitEnv(on)})
		r.settle()
		if n, after := insets(); n != 1 || !after {
			t.Errorf("WebKit %v: %d inset shadows, after the fill %v", on, n, after)
		}
	}
}
