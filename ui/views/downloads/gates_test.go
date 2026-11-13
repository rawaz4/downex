package downloads

import (
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/selection"
	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/statetest"
	"downex/ui/views/viewstest"
)

// idleFor lets the window run for d with nothing changing and reports its paints, frames and
// layouts.
func idleFor(r *rig, d time.Duration) (paints, frames, layouts int) {
	p, f, l := r.Loop.Paints, r.Loop.Frames, r.Window.Stats().Layouts
	r.Loop.Run(d)
	return r.Loop.Paints - p, r.Loop.Frames - f, r.Window.Stats().Layouts - l
}

// While nothing changes the grid and the pane neither paint, nor ask for frames, nor lay out:
// the Studio scene between two reports with nothing selected, a running download selected
// (its pane with the chart and the connection boxes), several selected (the strip), and the
// Compact density.
func TestIdleGridAndPaneDoNotPaint(t *testing.T) {
	running := Download(1, func(d *downexv1.Download) {
		d.State, d.Size, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 80*MB, 32*MB, 2*MB, 2
		d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, nil), conn(2, nil)}
	})
	paused := Download(2, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MB })
	done := Download(3, func(d *downexv1.Download) { d.State, d.Received = state.StateCompleted, 10*MiB })
	cases := []struct {
		name string
		rig  func(t *testing.T) *rig
	}{
		{"Studio scene, nothing selected", func(t *testing.T) *rig { return newRig(t, viewstest.Options{}, gridBox, 0) }},
		{"a running download selected", func(t *testing.T) *rig {
			r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(running, paused, done)}, gridBox, 226)
			r.Main.Downloads.SelectOnly(1)
			r.refresh()
			return r
		}},
		{"several selected", func(t *testing.T) *rig {
			r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(running, paused, done)}, gridBox, 226)
			r.Main.Downloads.SelectAll()
			r.refresh()
			return r
		}},
		{"Compact", func(t *testing.T) *rig {
			r := newRigPrep(t, viewstest.Options{}, gridBox, 0, func(h *viewstest.Harness) { h.App.Density.Set(state.Compact) })
			return r
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.rig(t)
			r.Loop.Run(2 * time.Second) // what showing started (an arrival, the pane opening) ends
			p, f, l := idleFor(r, 30*time.Second)
			t.Logf("idle 30 s: %d paints, %d frames, %d layouts", p, f, l)
			if p != 0 || f != 0 || l != 0 {
				t.Errorf("idle window worked: %d paints, %d frames, %d layouts", p, f, l)
			}
		})
	}
}

// A probing row's hatch drifts for as long as it probes (today's .probe animation), and
// stops with it; it repaints only the hatch's cells, never lays out.
func TestProbingHatchIsTheOnlyIdleMotion(t *testing.T) {
	probing := Download(1, func(d *downexv1.Download) { d.State, d.Size, d.Received = state.StateProbing, -1, 0 })
	r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(probing)}, gridBox, 0)
	r.Loop.Run(2 * time.Second)
	p, f, l := idleFor(r, 5*time.Second)
	if l != 0 {
		t.Errorf("the hatch laid out %d times", l)
	}
	if p == 0 || f == 0 {
		t.Errorf("the hatch did not drift: %d paints, %d frames", p, f)
	}
	t.Logf("probing: the hatch drifts: %d paints, %d frames, %d layouts in 5 s", p, f, l)
	_ = kit.ListKey(0)
}

// The pane at the references' window sizes with 16 and 20 px text (Real text on Windows):
// its body's content against its view. Today's pane scrolls in a focusable region when its
// content does not fit (DetailsPanel.svelte .content), so this measures and reports it.
func TestPaneFitAtReferenceSizes(t *testing.T) {
	running := Download(1, func(d *downexv1.Download) {
		d.State, d.Size, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 80*MB, 32*MB, 2*MB, 2
		d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, nil), conn(2, nil)}
	})
	for _, sz := range [][2]int{{1200, 760}, {1190, 752}, {960, 608}, {900, 600}, {1920, 1080}} {
		for _, font := range []int{16, 20} {
			o := viewstest.Options{Width: sz[0], Height: sz[1], Font: font, Daemon: statetest.NewDaemon(running), Real: viewstest.CanRender() == nil}
			side := kit.DIP(225)
			if font == 20 {
				side = 281
			}
			if sz[0] < 1000 {
				side = 57
			}
			r := newRigPrep(t, o, kit.R(side, 103, kit.DIP(sz[0]), kit.DIP(sz[1]-33)), 0, nil)
			r.Main.Downloads.SelectOnly(1)
			r.refresh()
			h := r.pane.height
			// the shell gives the pane its own height under the grid
			r.screen.boxes[0].Max.Y = kit.DIP(sz[1]-33) - h
			r.screen.boxes[1] = kit.R(side, kit.DIP(sz[1]-33)-h, kit.DIP(sz[0]), kit.DIP(sz[1]-33))
			r.screen.RelayoutContent()
			r.Settle()
			pane, ok := find[*widgets.ScrollPane](&r.pane.Element)
			if !ok {
				t.Fatal("no body")
			}
			off, content, view := pane.ScrollRange()
			t.Logf("%dx%d %d px: pane %.0f tall, body content %.1f in %.1f (offset %.0f)", sz[0], sz[1], font, h, content, view, off)
		}
	}
}

// Between reports downexd's status is read every 3 s (state.StatusInterval) and the stores
// tell their watchers; the pane runs its updaters then. With the stores' clock moving as well
// (idleFor moves only the window's), a selected download that does not change neither lays
// out nor paints: a paused, a completed, a failed and a running one (no new report), and the
// strip. Before, the chart and the connection boxes laid themselves out at every check (90
// boxes each 3 s, the shell's TestIdle).
func TestStatusChecksLeaveTheIdlePaneAlone(t *testing.T) {
	running := Download(1, func(d *downexv1.Download) {
		d.State, d.Size, d.Received, d.SpeedBps, d.Connections = state.StateDownloading, 80*MB, 32*MB, 2*MB, 2
		d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, nil), conn(2, nil)}
	})
	paused := Download(2, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MB })
	done := Download(3, func(d *downexv1.Download) { d.State, d.Received = state.StateCompleted, 10*MiB })
	failed := Download(4, func(d *downexv1.Download) { d.State, d.Error = state.StateFailed, "HTTP 500" })
	for _, c := range []struct {
		name string
		ids  []int64
	}{
		{"running", []int64{1}}, {"paused", []int64{2}}, {"completed", []int64{3}}, {"failed", []int64{4}},
		{"several selected", []int64{1, 2, 3, 4}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(running, paused, done, failed)}, gridBox, 226)
			r.Main.Downloads.SelectOnly(c.ids[0])
			for _, id := range c.ids[1:] {
				r.Main.Downloads.Click(id, selection.Modifiers{Toggle: true})
			}
			r.Settle()
			r.Advance(2 * time.Second)
			p, f, l := r.Loop.Paints, r.Loop.Frames, r.Window.Stats().Layouts
			r.Advance(30 * time.Second) // 10 status checks
			p, f, l = r.Loop.Paints-p, r.Loop.Frames-f, r.Window.Stats().Layouts-l
			t.Logf("30 s of status checks: %d paints, %d frames, %d layouts", p, f, l)
			if p != 0 || f != 0 || l != 0 {
				t.Errorf("the idle pane worked: %d paints, %d frames, %d layouts", p, f, l)
			}
		})
	}
}

// The inline speed limit list stays open across status checks: its choices are given again
// only when they change (Select.SetOptions closes the list).
func TestStatusChecksKeepTheLimitListOpen(t *testing.T) {
	paused := Download(2, func(d *downexv1.Download) { d.State, d.Received = state.StatePaused, MB })
	r := newRig(t, viewstest.Options{Daemon: statetest.NewDaemon(paused)}, gridBox, 226)
	r.Main.Downloads.SelectOnly(2)
	r.Settle()
	var sel *widgets.Select
	var walk func(e *kit.Element)
	walk = func(e *kit.Element) {
		if s, ok := e.Widget().(*widgets.Select); ok && s.Semantics.Name == txtLimitLabel {
			sel = s
		}
		for _, c := range e.Children() {
			walk(c)
		}
	}
	walk(&r.pane.Element)
	if sel == nil {
		t.Fatal("no limit select in the pane")
	}
	sel.Open(widgets.FocusFirst)
	r.Settle()
	if !sel.IsOpen() {
		t.Fatal("the list did not open")
	}
	r.Advance(10 * time.Second) // 3 status checks
	if !sel.IsOpen() {
		t.Error("a status check closed the open limit list")
	}
	sel.Close()
	r.Settle()
}
