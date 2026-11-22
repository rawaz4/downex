package widgets_test

import (
	"math"
	"testing"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.02 }

// TestSegmentBarGlides: while a download streams, the fill moves from what it drew to each new
// report at an even pace over the time since the previous report, instead of jumping; a report
// that goes back, a new state or reduced motion shows at once.
func TestSegmentBarGlides(t *testing.T) {
	seg := widgets.NewSegmentBar(widgets.SegLg)
	r := newRig(t, kit.Size{W: 600, H: 200}, seg)
	report := func(f float64, st widgets.BarState) {
		seg.Set([]widgets.SegCell{{Fraction: f, Live: true}}, st)
	}
	report(0.10, widgets.BarDownloading)
	if got := seg.Drawn()[0].Fraction; got != 0.10 {
		t.Fatalf("first report drawn at %v, want 0.10 at once", got)
	}
	r.wait(500 * time.Millisecond)
	report(0.20, widgets.BarDownloading) // 500 ms after the last: glides over 500 ms
	if got := seg.Drawn()[0].Fraction; !near(got, 0.10) {
		t.Fatalf("drawn %v right after the report, want it to start from 0.10", got)
	}
	r.wait(250 * time.Millisecond)
	if got := seg.Drawn()[0].Fraction; !near(got, 0.15) {
		t.Fatalf("drawn %v halfway, want about 0.15", got)
	}
	if got := seg.Shown()[0].Fraction; got != 0.20 {
		t.Fatalf("Shown %v, want the report 0.20", got)
	}
	r.wait(300 * time.Millisecond)
	if got := seg.Drawn()[0].Fraction; got != 0.20 {
		t.Fatalf("drawn %v after the glide, want 0.20", got)
	}
	// A report while gliding starts from where the fill is.
	r.wait(200 * time.Millisecond)
	report(0.40, widgets.BarDownloading)
	r.wait(100 * time.Millisecond)
	report(0.50, widgets.BarDownloading)
	if got := seg.Drawn()[0].Fraction; got <= 0.20 || got >= 0.40 {
		t.Fatalf("drawn %v when the next report came mid-glide, want between 0.20 and 0.40", got)
	}
	r.wait(2 * time.Second)
	// Going back (a restart) and another state show at once.
	report(0.05, widgets.BarDownloading)
	if got := seg.Drawn()[0].Fraction; got != 0.05 {
		t.Fatalf("a report that went back drawn at %v, want 0.05 at once", got)
	}
	r.wait(300 * time.Millisecond)
	report(0.30, widgets.BarPaused)
	if got := seg.Drawn()[0].Fraction; got != 0.30 {
		t.Fatalf("a paused report drawn at %v, want 0.30 at once", got)
	}
}
