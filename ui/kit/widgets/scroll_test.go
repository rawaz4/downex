package widgets_test

import (
	"testing"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

// tall is a column of n 40 DIP rows.
func tall(n int) (*kit.Element, []*kit.Element) {
	col := &kit.Element{Style: kit.Style{Display: kit.DisplayBlock}}
	rows := make([]*kit.Element, n)
	for i := range rows {
		rows[i] = &kit.Element{Style: kit.Style{Height: kit.Px(40)}}
		col.Add(rows[i])
	}
	return col, rows
}

// The pane uses kit's scroll bar (stage 2: the copy of the bar's drawing is gone): the bar is
// a child laid out over the gutter only while the content overflows, and the pane is a
// kit.ScrollShower that Window.ScrollIntoView reaches.
func TestScrollPaneUsesKitBar(t *testing.T) {
	col, rows := tall(20) // 800 DIPs
	pane := widgets.NewScrollPane(col)
	pane.Style.Height = kit.Px(200)
	r := newRig(t, kit.Size{W: 400, H: 400}, pane)
	bar := pane.Scrollbar()
	if bar == nil || bar.Base().Parent() != &pane.Element {
		t.Fatal("the pane's bar is not its child")
	}
	if !pane.Overflows() || bar.Style.Hidden {
		t.Fatalf("overflowing pane: Overflows %v, bar hidden %v", pane.Overflows(), bar.Style.Hidden)
	}
	pb, bb := pane.WindowBounds(), bar.WindowBounds()
	if bb.Width() != kit.FluentScroll.Thickness || bb.Max.X != pb.Max.X || bb.Height() != pb.Height() {
		t.Errorf("bar at %v in a pane at %v: want the 15 DIP gutter at the end", bb, pb)
	}
	if w := rows[0].WindowBounds().Width(); w != pb.Width()-kit.FluentScroll.Thickness {
		t.Errorf("content width %v beside the gutter, want %v", w, pb.Width()-kit.FluentScroll.Thickness)
	}
	// ScrollIntoView reaches the pane.
	r.win.ScrollIntoView(rows[15])
	r.settle()
	if got, want := pane.Offset(), kit.DIP(16*40-200); got != want {
		t.Errorf("offset after ScrollIntoView(row 15) = %v, want %v", got, want)
	}
	// The bar scrolls its pane: a click on the up arrow is a 40 DIP line.
	before := pane.Offset()
	r.clickAt(kit.Pt(bb.Min.X+7, bb.Min.Y+5))
	if got := pane.Offset(); got != before-40 {
		t.Errorf("offset after an up-arrow click = %v, want %v", got, before-40)
	}
	// Content that fits hides the bar and gives the gutter back.
	short, _ := tall(2)
	p2 := widgets.NewScrollPane(short)
	p2.Style.Height = kit.Px(200)
	r2 := newRig(t, kit.Size{W: 400, H: 400}, p2)
	_ = r2
	if p2.Overflows() || !p2.Scrollbar().Style.Hidden {
		t.Errorf("fitting pane: Overflows %v, bar hidden %v", p2.Overflows(), p2.Scrollbar().Style.Hidden)
	}
}

// The text area follows its box after every layout pass (kit.LayoutFollower), so a scrolled
// pane moves its native field with no paint in between.
func TestTextAreaFollowsScroll(t *testing.T) {
	col, _ := tall(10)
	area := widgets.NewTextArea("", 4, widgets.InputOptions{Name: "Links"}, nil)
	col.Add(area)
	pane := widgets.NewScrollPane(col)
	pane.Style.Height = kit.Px(200)
	r := newRig(t, kit.Size{W: 400, H: 400}, pane)
	if len(r.host.Fields) != 1 {
		t.Fatalf("%d native fields, want 1", len(r.host.Fields))
	}
	f := r.host.Fields[0]
	before := f.Bounds
	pane.ScrollTo(120)
	r.win.Frame(r.clock.Now()) // the layout pass, no paint
	if got := f.Bounds.Min.Y; got != before.Min.Y-120 {
		t.Errorf("field top after scrolling 120 = %v, want %v", got, before.Min.Y-120)
	}
}
