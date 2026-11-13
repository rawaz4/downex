//go:build windows

package progress

import (
	"math"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/core/progress"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// The plan's gates for the progress window (step 12), with DirectWrite's text: no needless
// scroll bar at 100, 125 and 150 %, tabs that never shrink the window, no allocation in a
// steady repaint, and no paint while nothing changes.

// collapsedViews are the fixture states without the details (they fit their content).
var collapsedViews = []string{"pw-status", "pw-limits", "pw-site", "pw-paused", "pw-failed", "pw-complete", "pw-complete-media"}

// scrolling is the screen's scrolling part: the complete view or the progress panel.
func scrolling(s *Screen) *widgets.ScrollPane {
	if s.complete {
		return s.done.ScrollPane
	}
	return s.panel
}

// The collapsed and complete views show no scroll bar at 100, 125 and 150 % (with 16 and 20
// px text, in both themes), and they keep today's bounds: a collapsed window of at least
// 300 px, a complete window within 260-720 px.
func TestNoScrollbarAtScales(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, name := range collapsedViews {
		for _, scale := range []float32{1, 1.25, 1.5} {
			for _, font := range []int{16, 20} {
				for _, theme := range []string{"light", "dark"} {
					r, _ := sceneRig(t, name, scale, font, theme)
					s := r.s()
					pane := scrolling(s)
					off, content, view := pane.ScrollRange()
					_, bar := pane.Scrollbar().ThumbRect()
					outer := s.outer()
					t.Logf("%s %vx %dpx %s: outer %vx%v, scroller content %.2f in %.2f (offset %.2f), bar %v",
						name, scale, font, theme, outer.W, outer.H, content, view, off, bar)
					if bar || content > view+0.01 || off != 0 {
						t.Errorf("%s %vx %dpx %s: a scroll bar (content %.2f in %.2f, offset %.2f, bar %v)", name, scale, font, theme, content, view, off, bar)
					}
					if s.expanded {
						t.Errorf("%s: expanded", name)
					}
					switch {
					case s.complete && (outer.H < progress.MinHeight-0.01 || outer.H > progress.MaxHeight+0.01):
						t.Errorf("%s %vx %dpx: complete window %v tall, outside %v-%v", name, scale, font, outer.H, progress.MinHeight, progress.MaxHeight)
					case !s.complete && outer.H < progress.CollapsedHeight-0.01:
						t.Errorf("%s %vx %dpx: collapsed window %v tall, below %v", name, scale, font, outer.H, progress.CollapsedHeight)
					}
				}
			}
		}
	}
}

// Switching between the tabs, with DirectWrite's text, never makes the window smaller, at
// 100, 125 and 150 % with 16 and 20 px text.
func TestTabSwitchNeverShrinksReal(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, name := range []string{"pw-status", "pw-site", "pw-paused", "pw-failed", "pw-expanded"} {
		for _, scale := range []float32{1, 1.25, 1.5} {
			for _, font := range []int{16, 20} {
				r, _ := sceneRig(t, name, scale, font, "light")
				h := r.pw.Kit.Size().H
				for i := range 6 {
					tab := tabLimits
					if i%2 == 1 {
						tab = tabStatus
					}
					r.click(r.tab(tab))
					now := r.pw.Kit.Size().H
					if now < h-0.01 {
						t.Fatalf("%s %vx %dpx: %v → %v after %s", name, scale, font, h, now, tab)
					}
					if _, bar := scrolling(r.s()).Scrollbar().ThumbRect(); bar && !r.s().expanded {
						t.Errorf("%s %vx %dpx: a scroll bar on %s", name, scale, font, tab)
					}
					h = now
				}
			}
		}
	}
}

// A steady repaint of the window (nothing changed since the last frame) allocates nothing:
// a frame, and the whole window painted with Direct2D and DirectWrite, in every view.
func TestSteadyRepaintAllocatesNothing(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, name := range []string{"pw-status", "pw-limits", "pw-site", "pw-expanded", "pw-site-expanded", "pw-paused", "pw-failed", "pw-complete", "pw-complete-media"} {
		for _, scale := range []float32{1, 1.25} {
			r, _ := sceneRig(t, name, scale, 16, "light")
			res, _, err := viewstest.RealResources()
			if err != nil {
				t.Skip(err)
			}
			size := r.pw.Kit.Size()
			px := func(v kit.DIP) int { return int(math.Ceil(float64(v * scale))) }
			o, err := win.NewOffscreen(res.(*win.Resources), px(size.W), px(size.H), scale, win.OffscreenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			full := kit.RectAt(kit.Point{}, size)
			paint := func() {
				r.pw.Root.Frame(r.clock.Now())
				c, err := o.BeginFrame(full)
				if err != nil {
					t.Fatal(err)
				}
				r.pw.Kit.Paint(c, full)
				if err := o.EndFrame(); err != nil {
					t.Fatal(err)
				}
			}
			paint() // the first paint makes the layouts, paths and brushes
			paint()
			if n := testing.AllocsPerRun(50, paint); n != 0 {
				t.Errorf("%s %vx: %v allocations per steady repaint", name, scale, n)
			} else {
				t.Logf("%s %vx: 0 allocations per steady repaint", name, scale)
			}
			o.Close()
		}
	}
}

// While nothing changes the window neither paints nor asks for frames: a running download
// between two reports (with the 3 s status checks), with and without its details, a paused
// one and the complete view, for 30 s.
func TestIdleWindowDoesNotPaint(t *testing.T) {
	cases := []struct {
		name string
		rig  func(t *testing.T) *rig
	}{
		{"running", func(t *testing.T) *rig {
			return newRig(t, running(nil), nil, rigOptions{statusEvery: 3 * time.Second})
		}},
		{"running, details shown", func(t *testing.T) *rig {
			return newRig(t, running(nil), nil, rigOptions{statusEvery: 3 * time.Second, storage: map[string]string{"downex.progress.details": "1"}})
		}},
		{"paused", func(t *testing.T) *rig {
			return newRig(t, running(func(d *downexv1.Download) { d.State = downexv1.DownloadState_DOWNLOAD_STATE_PAUSED; d.SpeedBps = 0 }), nil, rigOptions{statusEvery: 3 * time.Second})
		}},
		{"complete", func(t *testing.T) *rig {
			return newRig(t, completed(nil), nil, rigOptions{statusEvery: 3 * time.Second})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.rig(t)
			// what opening started ends: the first reads, and the 4 s retry wait of running()'s
			// second connection, which the details count down once a second
			r.advance(10 * time.Second)
			paints, frames := r.loop.Paints, r.loop.Frames
			r.advance(30 * time.Second)
			if p, f := r.loop.Paints-paints, r.loop.Frames-frames; p != 0 || f != 0 {
				t.Errorf("%d paints and %d frames in 30 idle seconds", p, f)
			} else {
				t.Logf("0 paints and 0 frames in 30 idle seconds (%d status checks)", r.api.count("status"))
			}
		})
	}
}

// A move to a display of another scaling fits the window again (its content's height in
// DIPs changes with the rounding and the text): the collapsed and complete views still show
// no scroll bar after 100 % → 125 % → 150 % → 100 %, and the complete view keeps 260-720 px.
func TestRefitsAfterScaleChange(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, name := range []string{"pw-status", "pw-failed", "pw-complete", "pw-complete-media"} {
		r, _ := sceneRig(t, name, 1, 16, "light")
		for _, sc := range []kit.Scale{{PixelsPerDIP: 1.25, Text: 1}, {PixelsPerDIP: 1.5, Text: 1.25}, {PixelsPerDIP: 1, Text: 1}} {
			env := r.host.Env
			env.Scale = sc
			r.host.Env = env
			r.pw.Root.Event(kit.EnvironmentEvent{Env: env})
			r.settle()
			s := r.s()
			pane := scrolling(s)
			_, content, view := pane.ScrollRange()
			_, bar := pane.Scrollbar().ThumbRect()
			outer := s.outer()
			t.Logf("%s at %vx, text %vx: outer %vx%v, content %.2f in %.2f", name, sc.PixelsPerDIP, sc.Text, outer.W, outer.H, content, view)
			if bar || content > view+0.01 {
				t.Errorf("%s at %vx, text %vx: a scroll bar (content %.2f in %.2f)", name, sc.PixelsPerDIP, sc.Text, content, view)
			}
			if s.complete && (outer.H < progress.MinHeight-0.01 || outer.H > progress.MaxHeight+0.01) {
				t.Errorf("%s: complete window %v tall", name, outer.H)
			}
		}
	}
}
