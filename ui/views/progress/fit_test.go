package progress

import (
	"math"
	"slices"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/format/formattest"
	"downex/ui/core/progress"
	"downex/ui/kit"
	"downex/ui/state"
)

// ProgressWindow.test.ts's "the window fits what it shows (no needless scrollbar)". jsdom has
// no layout, so those tests give the page's blocks heights of their own; here the kit lays the
// page out and the fit reads its boxes. The window's frame is 39 px, as there.

func TestProgressWindowFit(t *testing.T) {
	t.Run("sizes the complete window to its fractional content plus the frame and slack, and again when the content changes", func(t *testing.T) {
		r := show(t, running(func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil }))
		content := r.s().neededHeight(progress.ViewComplete)
		wantH := kit.DIP(math.Ceil(content + 39 + progress.FitSlack))
		if s := r.lastSize(); s != (kit.Size{W: 520, H: wantH}) {
			t.Fatal(s, content)
		}
		// never a pixel short (it scrolled by 1 at 125 %)
		if float64(wantH) <= content+39+1.9 {
			t.Fatal(wantH, content)
		}
		// the window shows the content without scrolling
		if r.s().done.Overflows() {
			t.Fatal("the complete view scrolls")
		}
		// it fits again when the content grows (a message: the choice's hint)
		r.click(r.s().done.dontShow.Box)
		grown := r.s().neededHeight(progress.ViewComplete)
		if !(grown > content) {
			t.Fatal(grown, content)
		}
		if s := r.lastSize(); s != (kit.Size{W: 520, H: kit.DIP(math.Ceil(grown + 39 + progress.FitSlack))}) {
			t.Fatal(s, grown)
		}
	})

	t.Run("keeps the frame it measured first: a host that reports the new size before the page has it does not move the window", func(t *testing.T) {
		// this host resizes the page only later
		r := newRig(t, running(func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil }),
			nil, rigOptions{noResize: true})
		first := r.sizes()
		if len(first) != 1 {
			t.Fatal(first)
		}
		// more layouts of the same content: no new size
		r.s().Relayout()
		r.settle()
		r.s().queueFit(false)
		r.settle()
		if s := r.sizes(); !slices.Equal(s, first) {
			t.Fatal(s, first)
		}
	})

	t.Run("fits the collapsed progress window to its facts, never below its usual height nor smaller for the Speed limit tab", func(t *testing.T) {
		r := show(t, running(nil))
		content := r.s().neededHeight(progress.ViewCollapsed)
		wantH := math.Ceil(content + 39 + progress.FitSlack)
		if wantH <= progress.CollapsedHeight {
			// it fits the usual 300 px window: nothing to do
			if len(r.sizes()) != 0 {
				t.Fatal(r.sizes())
			}
		} else if s := r.sizes(); !slices.Equal(s, []kit.Size{{W: 520, H: kit.DIP(wantH)}}) {
			t.Fatal(s, wantH)
		}
		// more facts (a stream's fragments and quality): it grows to them
		r.update(7, func(d *downexv1.Download) {
			d.Kind = downexv1.DownloadKind_DOWNLOAD_KIND_MEDIA
			d.Media = formattest.MediaInfo(func(m *downexv1.MediaInfo) { m.FragmentsDone = 50 })
		})
		grownContent := r.s().neededHeight(progress.ViewCollapsed)
		grown := kit.DIP(math.Ceil(grownContent + 39 + progress.FitSlack))
		if !(grownContent > content) || r.lastSize() != (kit.Size{W: 520, H: grown}) {
			t.Fatal(r.sizes(), grownContent, content)
		}
		if r.s().panel.Overflows() {
			t.Fatal("the facts scroll")
		}
		// the shorter Speed limit tab does not shrink it (no jump between the tabs)
		r.click(r.tab(tabLimits))
		if l := r.s().neededHeight(progress.ViewCollapsed); !(l < grownContent) {
			t.Fatal("the Speed limit tab is not shorter", l, grownContent)
		}
		if r.lastSize() != (kit.Size{W: 520, H: grown}) {
			t.Fatal(r.sizes())
		}
		// back on Status with fewer facts (a row went away): it fits them again, at least the
		// usual height
		r.update(7, func(d *downexv1.Download) { d.Kind, d.Media = downexv1.DownloadKind_DOWNLOAD_KIND_FILE, nil })
		r.click(r.tab(tabStatus))
		back := progress.FitWindowHeight(progress.ViewCollapsed, r.s().neededHeight(progress.ViewCollapsed), 39)
		if r.lastSize() != (kit.Size{W: 520, H: kit.DIP(back)}) || back < progress.CollapsedHeight {
			t.Fatal(r.sizes(), back)
		}
	})
}

// The window never shrinks on a tab switch, at every scale and text size (the plan's gate).
func TestProgressWindowTabSwitchNeverShrinks(t *testing.T) {
	for _, scale := range []float32{1, 1.25, 1.5} {
		for _, font := range []int{16, 20} {
			r := newRig(t, running(nil), nil, rigOptions{scale: scale, font: font})
			h := r.pw.Kit.Size().H
			for i := 0; i < 6; i++ {
				tab := tabLimits
				if i%2 == 1 {
					tab = tabStatus
				}
				r.click(r.tab(tab))
				if now := r.pw.Kit.Size().H; now < h-0.01 {
					t.Fatalf("scale %v font %d: %v → %v after %s", scale, font, h, now, tab)
				}
				h = r.pw.Kit.Size().H
			}
		}
	}
}

// Today's host truncates the window's outer size to device pixels (Wails' SetSize: (px × dpi)
// / 96) and the frame is whole device pixels; the client area is what is left. At 125 % a
// 302 px window with a 40 px frame is 377 − 50 = 327 device pixels (261.6 DIPs), not 262 DIPs
// (327.5, rounded up to 328): the progress window was 1 device pixel taller than today's.
func TestProgressWindowHostSize(t *testing.T) {
	frame := kit.Size{H: progress.Frame}
	for _, c := range []struct {
		scale     float32
		out       kit.Size
		wantPx    kit.Size
		wantFrame kit.Size
	}{
		{1, kit.Size{W: 520, H: 302}, kit.Size{W: 520, H: 262}, frame},
		{1.25, kit.Size{W: 520, H: 302}, kit.Size{W: 650, H: 327}, frame},                        // pw-status
		{1.25, kit.Size{W: 520, H: 325}, kit.Size{W: 650, H: 356}, frame},                        // pw-site: 406 − 50
		{1.25, kit.Size{W: 520, H: 321}, kit.Size{W: 650, H: 351}, frame},                        // pw-complete: 401 − 50
		{1.25, kit.Size{W: 520, H: 370}, kit.Size{W: 650, H: 412}, frame},                        // pw-complete-media: 462 − 50
		{1.5, kit.Size{W: 520, H: 317}, kit.Size{W: 780, H: 415}, frame},                         // 475 − 60
		{1.25, kit.Size{W: 534.4, H: 302}, kit.Size{W: 650, H: 330}, kit.Size{W: 14.4, H: 37.6}}, // the system frame: 18 × 47 px
	} {
		r := newRig(t, running(nil), nil, rigOptions{scale: c.scale, frame: &c.wantFrame})
		got := r.s().hostClient(c.out, c.wantFrame)
		px := kit.Size{W: kit.DIP(math.Round(float64(got.W * c.scale))), H: kit.DIP(math.Round(float64(got.H * c.scale)))}
		if px != c.wantPx {
			t.Errorf("%vx, %v with frame %v: client %v (%v px), want %v px", c.scale, c.out, c.wantFrame, got, px, c.wantPx)
		}
	}

	// The fitted window gets that client area, and the fit takes it as the size it asked for:
	// no second ask for the same height.
	r := newRig(t, running(func(d *downexv1.Download) { d.State, d.Received, d.ConnectionInfo = state.StateCompleted, 100*MiB, nil }),
		nil, rigOptions{scale: 1.25, frame: &frame})
	asked := r.lastSize()
	want := r.s().hostClient(asked, frame)
	if got := r.pw.Kit.Size(); !sameSize(got, want) {
		t.Fatalf("client %v, want %v for %v", got, want, asked)
	}
	n := len(r.sizes())
	r.s().Relayout()
	r.settle()
	r.s().queueFit(false)
	r.settle()
	if len(r.sizes()) != n || r.s().outer() != asked {
		t.Fatalf("asked again: %v (outer %v)", r.sizes(), r.s().outer())
	}
}

// Today's page is 520 CSS px wide at every scaling (owner-compare 28: the address was cut
// more in a 506 DIP client area inside a 520 DIP window). With the system's frame (18 × 47 px
// at 125 %, 22 × 55 px at 150 %, 26 × 63 px at 175 %), every view's window has a 520 DIP wide
// client area, opens with one, and keeps a width the user gave it; the heights stay the
// window's (300 and 560 px, and the complete view's 260-720 px).
func TestProgressWindowClientWidthIsTodaysPage(t *testing.T) {
	for _, c := range []struct {
		scale  float32
		px     kit.Size // the frame in device pixels
		client float32  // the client width in device pixels
	}{
		{1, kit.Size{W: 14, H: 39}, 520},
		{1.25, kit.Size{W: 18, H: 47}, 650},
		{1.5, kit.Size{W: 22, H: 55}, 780},
		{1.75, kit.Size{W: 26, H: 63}, 910},
	} {
		frame := kit.Size{W: c.px.W / c.scale, H: c.px.H / c.scale}
		if got := InitialSize(frame); got.W != 520 || !same(float64(got.H), float64(300-frame.H)) {
			t.Fatalf("%vx: opens at %v", c.scale, got)
		}
		check := func(what string, r *rig, wantH kit.DIP) {
			t.Helper()
			s := r.s().outer()
			if !same(float64(s.W), float64(520+frame.W)) || !same(float64(s.H), float64(wantH)) {
				t.Fatalf("%vx %s: asked for %v, want %v × %v", c.scale, what, s, 520+frame.W, wantH)
			}
			if px := math.Round(float64(r.pw.Kit.Size().W * c.scale)); px != float64(c.client) {
				t.Fatalf("%vx %s: client %v (%v px), want %v px", c.scale, what, r.pw.Kit.Size(), px, c.client)
			}
		}
		o := rigOptions{scale: c.scale, frame: &frame, size: InitialSize(frame)}
		done := newRig(t, completed(nil), nil, o)
		h := done.s().neededHeight(progress.ViewComplete)
		wantH := kit.DIP(progress.FitWindowHeight(progress.ViewComplete, h, float64(frame.H)))
		if wantH < progress.MinHeight || wantH > progress.MaxHeight {
			t.Fatalf("%vx: complete view %v outside 260-720", c.scale, wantH)
		}
		check("complete", done, wantH)

		r := newRig(t, running(nil), nil, o)
		collapsed := kit.DIP(progress.FitWindowHeight(progress.ViewCollapsed, r.s().neededHeight(progress.ViewCollapsed), float64(frame.H)))
		if collapsed < progress.CollapsedHeight {
			t.Fatalf("%vx: collapsed %v below 300", c.scale, collapsed)
		}
		check("collapsed", r, collapsed)
		r.click(r.s().actions.toggle)
		check("details", r, progress.ExpandedHeight)

		// A window the user made wider keeps its width.
		wide := newRig(t, running(nil), nil, rigOptions{scale: c.scale, frame: &frame, size: kit.Size{W: 600, H: InitialSize(frame).H}})
		wide.click(wide.s().actions.toggle)
		if s := wide.s().outer(); !same(float64(s.W), float64(600+frame.W)) {
			t.Fatalf("%vx: a 600 px page asked for %v", c.scale, s)
		}
	}
}
