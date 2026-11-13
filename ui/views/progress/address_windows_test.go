//go:build windows

package progress

import (
	"testing"

	"downex/ui/kit"
)

// The Status tab's address at today's 520 px window, laid out by DirectWrite: the head ends
// in an ellipsis exactly where today's .url-head's text-overflow puts it
// ("https://releases.example.org/p…" before the 24-character tail "os-image-26.04-amd64.iso"),
// and the 22 px copy button sits var(--sp-2) = 4 px after the tail.
func TestAddressCutsAsToday(t *testing.T) {
	for _, scale := range []float32{1, 1.25} {
		r, _ := sceneRig(t, "pw-status", scale, 16, "light")
		v := r.s().statusV
		a := v.addr
		if got := a.tail.Text(); got != "os-image-26.04-amd64.iso" {
			t.Fatalf("%vx: tail %q", scale, got)
		}
		l := a.head.TextLayout()
		if l == nil || !l.Metrics().Truncated {
			t.Fatalf("%vx: the head is not cut", scale)
		}
		m := l.Metrics()
		kept := a.head.Text()[:l.HitTest(kit.Pt(m.Width-2, m.Height/2)).Pos] // a hit in the ellipsis: where the cut text starts
		if kept != "https://releases.example.org/p" {
			t.Errorf("%vx: the head keeps %q before its ellipsis, today %q", scale, kept, "https://releases.example.org/p")
		}
		ab, cb := a.Bounds(), v.copyA.Bounds()
		if gap := cb.Min.X - ab.Max.X; gap < 3.9 || gap > 4.1 {
			t.Errorf("%vx: the copy button is %.2f DIP after the address, today 4", scale, gap)
		}
		if w := cb.Width(); w < 21.9 || w > 22.1 {
			t.Errorf("%vx: the copy button is %.2f DIP wide, today 22", scale, w)
		}
	}
}
