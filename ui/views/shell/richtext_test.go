package shell

import (
	"strings"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/views/viewstest"
)

// The connection banner's paragraph keeps the spaces between its words when the platform's
// text engine lays it out (DirectWrite leaves a text's trailing spaces out of its width):
// each word after one that ends in a space starts that space's width after it, and the
// paragraph's one-line width counts the spaces.
func TestRichTextKeepsSpacesWithTheRealTextEngine(t *testing.T) {
	for _, real := range []bool{true, false} {
		h := viewstest.New(t, viewstest.Options{Real: real, Width: 1190, Height: 752})
		b := NewConnectionBanner(time.Now, func() {})
		box := &kit.Element{Style: kit.Style{Display: kit.DisplayFlex, Axis: kit.AxisColumn, Width: kit.Pct(100), Height: kit.Pct(100)}}
		box.Add(b)
		h.Show(box)
		b.Update(ConnState{Daemon: &state.DaemonState{Error: "dial: connection refused"}, NextCheckAt: time.Now().Add(3 * time.Second)})
		h.Settle()
		p := b.para
		if !strings.Contains(p.Text(), "The list shows") {
			t.Fatalf("paragraph %q", p.Text())
		}
		var gaps int
		for i := 1; i < len(p.words); i++ {
			prev, cur := p.words[i-1], p.words[i]
			pr, cr := prev.Bounds(), cur.Bounds()
			if cr.Min.Y >= pr.Max.Y-0.5 {
				continue // a new line
			}
			gap := cr.Min.X - pr.Max.X
			size := prev.Label.Font().Style(kit.Scale{PixelsPerDIP: 1, Text: 1}).Size
			switch {
			case prev.space && gap < 0.2*size:
				t.Errorf("real %v: %q then %q: %.2f DIP apart, want a space (~%.1f)", real, prev.Text.Text(), cur.Text.Text(), gap, 0.27*size)
			case !prev.space && (gap < -0.01 || gap > 0.01):
				t.Errorf("real %v: %q then %q: %.2f DIP apart, want none", real, prev.Text.Text(), cur.Text.Text(), gap)
			}
			gaps++
		}
		if gaps < 10 {
			t.Errorf("real %v: only %d word pairs on a line", real, gaps)
		}
	}
}
