package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// selfShadowed paints a shadow outside its box by itself (kit.PaintOverflower).
type selfShadowed struct{ kit.Element }

func (s *selfShadowed) PaintOverflow() kit.Insets {
	return kit.Insets{Top: 10, Right: 20, Bottom: 30, Left: 40}
}

// TestPaintOverflowRepainted: an element's PaintOverflow joins the area repainted with it
// (and the 1 DIP the kit adds to a visual overflow) on every host: both backends keep their
// pixels between frames, so a card that shrank left its old shadow behind on Windows too.
func TestPaintOverflowRepainted(t *testing.T) {
	s := &selfShadowed{}
	s.Style = kit.Style{Width: kit.Px(100), Height: kit.Px(50)}
	root := box(kit.Style{Padding: kit.EdgesAll(kit.Px(100))}, s)
	_, loop := open(t, root, kit.Size{W: 400, H: 300}, env100)
	loop.Host.Invalidations = nil
	s.Invalidate()
	if len(loop.Host.Invalidations) != 1 {
		t.Fatalf("%d invalidations", len(loop.Host.Invalidations))
	}
	if got, want := loop.Host.Invalidations[0], kit.R(100-40-1, 100-10-1, 200+20+1, 150+30+1); got != want {
		t.Errorf("repainted %v, want %v", got, want)
	}
}

// TestShadowsOverflow: the backends' Gaussian reaches 1.5 × the blur, plus offset and spread;
// inset shadows reach nothing outside.
func TestShadowsOverflow(t *testing.T) {
	ss := []kit.Shadow{{Offset: kit.Pt(0, 24), Blur: 64}, {Spread: 1}, {Inset: true, Blur: 500}}
	if got := kit.ShadowsOverflow(ss); got != 24+96 {
		t.Errorf("got %v, want 120", got)
	}
}
