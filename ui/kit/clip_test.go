package kit_test

import (
	"testing"

	"downex/ui/kit"
)

// OverflowClipInline clips at the padding box's sides only: the glyphs of a cut name that
// reach above or below its line box are drawn, where OverflowHidden cuts them.
func TestOverflowClipInline(t *testing.T) {
	for _, c := range []struct {
		name     string
		overflow kit.Overflow
		inline   bool
	}{{"hidden", kit.OverflowHidden, false}, {"clip-inline", kit.OverflowClipInline, true}} {
		t.Run(c.name, func(t *testing.T) {
			// A 100 × 10 cell with 6 px of inline padding, its child 40 px tall: it runs 30 px
			// below the cell.
			child := leaf(20, 40)
			cell := box(kit.Style{Width: kit.Px(100), Height: kit.Px(10), Padding: kit.EdgesInline(kit.Px(6)),
				Overflow: c.overflow, MinWidth: kit.Px(0)}, child)
			root := box(kit.Style{Padding: kit.EdgesOf(kit.Px(50), kit.Px(50))}, cell)
			w, loop := open(t, root, kit.Size{W: 300, H: 200}, env100)
			cb := cell.WindowBounds()
			var clip kit.Rect
			n := 0
			for _, op := range loop.Canvas.Ops {
				if op.Kind == "PushClip" {
					clip, n = op.Rect, n+1
				}
			}
			if n != 1 {
				t.Fatalf("%d clips pushed", n)
			}
			if !approx(clip.Min.X, cb.Min.X) || !approx(clip.Max.X, cb.Max.X) {
				t.Errorf("clip %v: its sides are not the padding box's %v", clip, cb)
			}
			if c.inline {
				if clip.Min.Y > cb.Min.Y-10000 || clip.Max.Y < cb.Max.Y+10000 {
					t.Errorf("an inline clip %v stops above or below the content", clip)
				}
			} else if !approxRect(clip, cb) {
				t.Errorf("a hidden overflow clips to %v, want the padding box %v", clip, cb)
			}
			// The pointer finds the child where it is drawn, below the cell, only when the
			// clip lets it be drawn there.
			at := kit.Pt(cb.Min.X+10, cb.Max.Y+20)
			if hit := w.ElementAt(at) == child; hit != c.inline {
				t.Errorf("the child below the cell is hit: %v", hit)
			}
		})
	}
}

// An inline clip makes no scroll container (CSS overflow: clip): a flex item keeps its
// content-based minimum size, which OverflowHidden drops; MinWidth 0 lets it shrink.
func TestOverflowClipInlineMinimumSize(t *testing.T) {
	for _, c := range []struct {
		name     string
		overflow kit.Overflow
		min      kit.Length
		want     float32
	}{
		{"visible", kit.OverflowVisible, kit.Length{}, 100},
		{"clip-inline", kit.OverflowClipInline, kit.Length{}, 100},
		{"clip-inline, min-width 0", kit.OverflowClipInline, kit.Px(0), 50},
		{"hidden", kit.OverflowHidden, kit.Length{}, 50},
	} {
		item := box(kit.Style{Overflow: c.overflow, MinWidth: c.min}, leaf(100, 10))
		row := box(kit.Style{Display: kit.DisplayFlex, Width: kit.Px(50)}, item)
		open(t, box(kit.Style{}, row), kit.Size{W: 300, H: 100}, env100)
		if got := item.WindowBounds().Width(); !approx(got, c.want) {
			t.Errorf("%s: the item is %v wide, want %v", c.name, got, c.want)
		}
	}
}

// A label's glyphs reach past its box, so a change repaints half an em above and below it
// and a quarter at its sides, and a
// paint that reaches only its glyphs draws it.
func TestLabelInkIsRepainted(t *testing.T) {
	l := kit.NewLabel("gypsy", font13, kit.TextOptions{})
	root := box(kit.Style{Padding: kit.EdgesOf(kit.Px(40), kit.Px(40))}, l)
	w, loop := open(t, root, kit.Size{W: 300, H: 200}, env100)
	lb := l.WindowBounds()
	l.SetText("jumpy")
	loop.Step()
	// ceil(13 × 0.5) above and below, ceil(13 × 0.25) at the sides
	if want := lb.Inset(kit.Insets{Top: -7, Right: -4, Bottom: -7, Left: -4}); loop.Painted.Intersect(want) != want {
		t.Errorf("a new text repainted %v; its glyphs may reach %v", loop.Painted, want)
	}
	// A paint of the band just under the label's box (its descenders) draws the label.
	loop.Canvas.Reset()
	band := kit.R(lb.Min.X, lb.Max.Y+1, lb.Max.X, lb.Max.Y+4)
	w.Paint(loop.Canvas, band)
	if n := loop.Canvas.Count("DrawText"); n != 1 {
		t.Errorf("a paint of the descender band drew %d texts", n)
	}
}
