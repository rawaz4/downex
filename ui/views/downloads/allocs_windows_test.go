//go:build windows

package downloads

import (
	"math"
	"testing"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// A steady repaint (nothing changed since the last frame) of the grid and the pane allocates
// nothing: a frame and the whole window painted with Direct2D and DirectWrite, the Studio
// scene's rows (nothing selected), a running download with its pane, the strip, and Compact,
// at 100 and 125 %, light and dark.
func TestSteadyRepaintAllocatesNothing(t *testing.T) {
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	running := Download(1, func(d *downexv1.Download) {
		d.State, d.Size, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 80*MB, 32*MB, 2*MB, 2
		d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, nil), conn(2, nil)}
		d.Filename = "وزە بۆ کردنی کارێک ؟.mp4"
	})
	paused := Download(2, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MB })
	done := Download(3, func(d *downexv1.Download) { d.State, d.Received = state.StateCompleted, 10*MiB })
	cases := []struct {
		name string
		make func(t *testing.T, o viewstest.Options) *rig
	}{
		{"studio", func(t *testing.T, o viewstest.Options) *rig { return newRig(t, o, gridBox, 0) }},
		{"pane", func(t *testing.T, o viewstest.Options) *rig {
			o.Daemon = statetest.NewDaemon(running, paused, done)
			r := newRig(t, o, gridBox, 226)
			r.Main.Downloads.SelectOnly(1)
			r.refresh()
			return r
		}},
		{"strip", func(t *testing.T, o viewstest.Options) *rig {
			o.Daemon = statetest.NewDaemon(running, paused, done)
			r := newRig(t, o, gridBox, 226)
			r.Main.Downloads.SelectAll()
			r.refresh()
			return r
		}},
		{"compact", func(t *testing.T, o viewstest.Options) *rig {
			return newRigPrep(t, o, gridBox, 0, func(h *viewstest.Harness) { h.App.Density.Set(state.Compact) })
		}},
	}
	res, _, err := viewstest.RealResources()
	if err != nil {
		t.Skip(err)
	}
	for _, c := range cases {
		for _, scale := range []float32{1, 1.25} {
			for _, theme := range []string{"light", "dark"} {
				r := c.make(t, viewstest.Options{Scale: scale, Theme: theme, Real: true})
				size := r.Window.Size()
				px := func(v kit.DIP) int { return int(math.Ceil(float64(v * scale))) }
				o, err := win.NewOffscreen(res.(*win.Resources), px(size.W), px(size.H), scale, win.OffscreenOptions{})
				if err != nil {
					t.Fatal(err)
				}
				full := kit.RectAt(kit.Point{}, size)
				paint := func() {
					r.Window.Frame(r.Clock.Now())
					cv, err := o.BeginFrame(full)
					if err != nil {
						t.Fatal(err)
					}
					r.Window.Paint(cv, full)
					if err := o.EndFrame(); err != nil {
						t.Fatal(err)
					}
				}
				paint() // the first paint makes the layouts, paths and brushes
				paint()
				if n := testing.AllocsPerRun(50, paint); n != 0 {
					t.Errorf("%s %vx %s: %v allocations per steady repaint", c.name, scale, theme, n)
				} else {
					t.Logf("%s %vx %s: 0 allocations per steady repaint", c.name, scale, theme)
				}
				o.Close()
			}
		}
	}
}
