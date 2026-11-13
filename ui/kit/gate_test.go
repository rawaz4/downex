package kit_test

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"

	"downex/ui/kit"
)

// The step-4 gates (native-ui/decision.json): the counters that replace check-idle-render.
// KIT_GATES_OUT names a file the measured values are appended to (JSON lines).

func record(t *testing.T, v map[string]any) {
	t.Helper()
	path := os.Getenv("KIT_GATES_OUT")
	if path == "" {
		return
	}
	v["test"] = t.Name()
	v["go"] = runtime.Version()
	v["goos"] = runtime.GOOS + "/" + runtime.GOARCH
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// TestGateLiveList: a 10,000-row list whose 30 running downloads update 4 times a second
// lays out at most 8 times a second (check-idle-render's MAX_LAYOUTS), runs no animation
// in most samples, repaints only the rows that changed, and once the updates stop makes
// no frame, layout or paint at all.
func TestGateLiveList(t *testing.T) {
	for _, c := range []struct {
		name string
		env  kit.Environment
	}{{"100%", env100}, {"125%", env125}} {
		t.Run(c.name, func(t *testing.T) {
			f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
			f.loop.Settle(20)
			if c.env != env100 {
				f.loop.Send(kit.EnvironmentEvent{Env: c.env})
				f.loop.Settle(20)
			}
			w, l, loop := f.w, f.list, f.loop
			w.ResetStats()
			frames0, paints0 := loop.Frames, loop.Paints
			const seconds = 10
			const running = 30
			busySamples, samples := 0, 0
			var area float64
			start := loop.Clock.Now()
			nextBatch := start
			nextSample := start
			for loop.Clock.Now().Before(start.Add(seconds * time.Second)) {
				now := loop.Clock.Now()
				if !now.Before(nextBatch) {
					// One Watch batch: every running download reports; the store patches
					// its sorted view once (one revision per batch).
					for k := kit.ListKey(1); k <= running; k++ {
						f.m.pct[k]++
						l.RowChanged(k)
					}
					l.RowsChanged()
					nextBatch = nextBatch.Add(250 * time.Millisecond)
				}
				if !now.Before(nextSample) {
					samples++
					if w.Animating() > 0 {
						busySamples++
					}
					nextSample = nextSample.Add(500 * time.Millisecond)
				}
				paints := loop.Paints
				loop.Step()
				if loop.Paints > paints {
					area += float64(loop.Painted.Width()) * float64(loop.Painted.Height())
				}
			}
			s := w.Stats()
			layoutsPerSec := float64(s.Layouts) / seconds
			paintsPerSec := float64(loop.Paints-paints0) / seconds
			framesPerSec := float64(loop.Frames-frames0) / seconds
			rowArea := float64(l.RowWidget(1).Base().Size().W * l.RowHeightNow())
			t.Logf("live: %.1f layouts/s, %.1f paints/s, %.1f frames/s, %.1f boxes laid out per batch, "+
				"%.1f rows painted per paint, animations in %d/%d samples",
				layoutsPerSec, paintsPerSec, framesPerSec, float64(s.BoxLayouts)/(seconds*4),
				area/float64(max(loop.Paints-paints0, 1))/rowArea, busySamples, samples)
			if layoutsPerSec > 8 {
				t.Errorf("%.1f layouts a second, the gate is 8", layoutsPerSec)
			}
			if busySamples*2 > samples {
				t.Errorf("an animation ran in %d of %d samples", busySamples, samples)
			}
			// Each batch repaints the realised running rows (16 rows in view, plus overscan
			// up to row 26 that is not drawn), not the window.
			if perPaint := area / float64(max(loop.Paints-paints0, 1)); perPaint > rowArea*running+1 {
				t.Errorf("a batch paints %.0f square DIPs, more than its %d rows (%.0f)", perPaint, running, rowArea*running)
			}

			// Idle: the downloads stop changing.
			idle0 := w.Stats()
			frames, paints, requests := loop.Frames, loop.Paints, loop.Host.FrameRequests
			loop.Run(5 * time.Second)
			idle := w.Stats()
			t.Logf("idle 5 s: %d frames, %d paints, %d layouts, %d frame requests",
				loop.Frames-frames, loop.Paints-paints, idle.Layouts-idle0.Layouts, loop.Host.FrameRequests-requests)
			if loop.Frames != frames || loop.Paints != paints || idle.Layouts != idle0.Layouts || loop.Host.FrameRequests != requests {
				t.Errorf("idle window worked: %d frames, %d paints, %d layouts",
					loop.Frames-frames, loop.Paints-paints, idle.Layouts-idle0.Layouts)
			}
			record(t, map[string]any{
				"rows": 10_000, "running": running, "batches_per_s": 4, "seconds": seconds,
				"layouts_per_s": layoutsPerSec, "paints_per_s": paintsPerSec, "frames_per_s": framesPerSec,
				"box_layouts_per_batch":  float64(s.BoxLayouts) / (seconds * 4),
				"rows_painted_per_paint": area / float64(max(loop.Paints-paints0, 1)) / rowArea,
				"animation_samples":      busySamples, "samples": samples,
				"idle_frames": loop.Frames - frames, "idle_paints": loop.Paints - paints,
				"idle_layouts": idle.Layouts - idle0.Layouts, "idle_frame_requests": loop.Host.FrameRequests - requests,
				"scale": c.env.Scale.PixelsPerDIP,
			})
		})
	}
}

// TestGateHeap: the Go heap after building and scrolling the 10,000-row list fits the
// window's 25 MB budget, with assistive technology reading the tree (every row visited
// keeps a lasting node id) and without. The fake text layouts are Go objects here (a
// real backend's are native DirectWrite memory), so this is an upper bound for the kit's
// own share.
func TestGateHeap(t *testing.T) {
	for _, reader := range []bool{false, true} {
		name := "no reader"
		if reader {
			name = "reader"
		}
		t.Run(name, func(t *testing.T) { heapGate(t, reader) })
	}
}

func heapGate(t *testing.T, reader bool) {
	var m0, m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	f := newGrid(t, 10_000, kit.Size{W: 1200, H: 760})
	// The fake host's and canvas's own logs are not the window's memory.
	f.loop.Host.Quiet, f.loop.Canvas.Quiet = true, true
	f.loop.Settle(20)
	if reader {
		tree := f.w.Semantics()
		tree.AppendChildren(nil, tree.Root())
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	// Scroll through every row with the keyboard, a page at a time, then back to the top.
	if !f.w.Focus(f.list) {
		t.Fatal("the list does not take the focus")
	}
	for i := 0; f.list.ScrollOffset() < 10_000*46+30-760; i++ {
		if i > 2000 {
			t.Fatalf("Page Down stopped at %v", f.list.ScrollOffset())
		}
		f.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyPageDown})
		f.loop.Settle(20)
	}
	f.loop.Send(kit.KeyEvent{Action: kit.KeyDown, Key: kit.KeyHome})
	f.loop.Settle(20)
	runtime.GC()
	runtime.ReadMemStats(&m2)
	const mb = 1 << 20
	s := f.list.Stats()
	t.Logf("Go heap: %.2f MB before, %.2f MB built, %.2f MB after scrolling (in use %.2f MB, from the OS %.2f MB); "+
		"%d row widgets, %d binds, %d node ids, %d live text layouts",
		float64(m0.HeapAlloc)/mb, float64(m1.HeapAlloc)/mb, float64(m2.HeapAlloc)/mb,
		float64(m2.HeapInuse)/mb, float64(m2.HeapSys)/mb, s.RowsMade, s.Binds, s.NodeIDs, f.loop.Host.Res.Live)
	if m2.HeapAlloc > 25*mb {
		t.Errorf("the Go heap holds %.1f MB after scrolling, over the 25 MB window budget", float64(m2.HeapAlloc)/mb)
	}
	if reader != (s.NodeIDs == 10_000) {
		t.Errorf("%d lasting node ids with a reader %v", s.NodeIDs, reader)
	}
	record(t, map[string]any{
		"reader":         reader,
		"heap_before_mb": float64(m0.HeapAlloc) / mb, "heap_built_mb": float64(m1.HeapAlloc) / mb,
		"heap_scrolled_mb": float64(m2.HeapAlloc) / mb, "heap_inuse_mb": float64(m2.HeapInuse) / mb,
		"heap_sys_mb": float64(m2.HeapSys) / mb, "sys_mb": float64(m2.Sys) / mb,
		"kit_delta_mb": (float64(m2.HeapAlloc) - float64(m0.HeapAlloc)) / mb,
		"row_widgets":  s.RowsMade, "binds": s.Binds, "node_ids": s.NodeIDs, "live_text_layouts": f.loop.Host.Res.Live,
	})
	runtime.KeepAlive(f)
}
