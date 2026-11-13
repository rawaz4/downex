package app_test

import (
	"bytes"
	"fmt"
	"reflect"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"

	downexv1 "downex/internal/rpc/downex/v1"
	"downex/ui/kit"
	"downex/ui/state"
	"downex/ui/state/statetest"
)

// nopCanvas draws nothing and records nothing, so that what allocates is the window's own.
type nopCanvas struct{}

func (nopCanvas) FillRect(kit.Rect, kit.Color)                       {}
func (nopCanvas) StrokeRect(kit.Rect, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillRoundedRect(kit.RRect, kit.Color)               {}
func (nopCanvas) StrokeRoundedRect(kit.RRect, kit.Stroke, kit.Color) {}
func (nopCanvas) FillEllipse(kit.Rect, kit.Color)                    {}
func (nopCanvas) StrokeEllipse(kit.Rect, kit.Stroke, kit.Color)      {}
func (nopCanvas) DrawLine(a, b kit.Point, s kit.Stroke, c kit.Color) {}
func (nopCanvas) FillPath(kit.Path, kit.Color)                       {}
func (nopCanvas) StrokePath(kit.Path, kit.Stroke, kit.Color)         {}
func (nopCanvas) FillGradient(kit.Rect, kit.LinearGradient)          {}
func (nopCanvas) DrawShadow(kit.RRect, kit.Shadow)                   {}
func (nopCanvas) DrawText(kit.TextLayout, kit.Point, kit.Color)      {}
func (nopCanvas) DrawImage(kit.Image, kit.Rect, kit.Rect)            {}
func (nopCanvas) PushClip(kit.Rect)                                  {}
func (nopCanvas) PushRoundedClip(kit.RRect)                          {}
func (nopCanvas) PushOpacity(float32)                                {}
func (nopCanvas) PushTransform(kit.Affine)                           {}
func (nopCanvas) Pop()                                               {}

// TestSteadyFrames: once the main window has settled, a repaint of the whole window (title
// bar, sidebar, page header, grid, details pane, status bar, banners, tools toast) allocates
// nothing, at both scales and text sizes, in both themes.
func TestSteadyFrames(t *testing.T) {
	states := []struct {
		name string
		edit func(f *fixture)
	}{
		{"main", func(*fixture) {}},
		{"selection and details pane", func(f *fixture) { f.sel(1) }},
		{"connection banner", func(f *fixture) { f.d.SetDown(true); f.advance(3 * time.Second) }},
		{"post-action banner", func(f *fixture) {
			f.d.Emit(statetest.PostAction(1, downexv1.PostAction_POST_ACTION_SLEEP, f.h.App.Clock.Now().UnixMilli()+60_000, false))
			f.h.Settle()
		}},
		{"tools toast", func(f *fixture) {
			f.d.Emit(statetest.ToolsEvent(statetest.Tools(func(s *downexv1.ToolsStatus) {
				s.Ready = false
				for _, x := range s.Tools {
					x.State = downexv1.ToolState_TOOL_STATE_INSTALLING
					x.Version = ""
					x.BytesDone, x.BytesTotal = 10*MiB, 40*MiB
				}
			})))
			f.h.Settle()
		}},
		{"empty list", func(f *fixture) { f.ds().SetSearch("nothing matches this"); f.h.Settle() }},
	}
	for _, st := range states {
		for _, theme := range []string{"light", "dark"} {
			t.Run(st.name+" "+theme, func(t *testing.T) {
				f := quiet(t, theme)
				st.edit(f)
				f.h.Advance(time.Second)
				full := kit.RectAt(kit.Point{}, f.h.Window.Size())
				var c nopCanvas
				// The logo (kit/icons' DrawBrand) is in: its bars' path data is made once.
				f.h.Window.Paint(c, full)
				if n := ownAllocsPerRun(50, func() { f.h.Window.Paint(c, full) }); n != 0 {
					t.Errorf("a repaint allocates %.1f times", n)
				}
			})
		}
	}
}

// ownAllocsPerRun is testing.AllocsPerRun counting only the allocations made under f
// (its call stack), not the whole process's.
//
// The flake it replaced: AllocsPerRun reads the process's malloc count, so whatever else
// allocated during the runs counted as the window's, and 50 such allocations in 50 runs
// report 1. With every allocation profiled, 50 repaints of the main window right after
// one warm-up counted 22 on an idle machine: about 20 were the runtime's type-assertion
// caches under the window's own calls (rebuilt at random, about 1 in 1,024 cache misses,
// until a call site has seen its types) and the rest the runtime's own: a new OS thread
// for the scheduler (allocm, malg, mcommoninit), a garbage collector mark worker, the
// finalizer and cleanup goroutine (which frees an earlier fixture's ICU handles,
// runtime.AddCleanup). A loaded machine starts more threads and collects more often; the
// failures (1 in about 7,000 subtests under a parallel go test -race) were too rare to
// profile. Warming f up for runs calls builds those caches; the profile leaves the rest out.
//
// Its first version read runtime.MemProfile, whose records keep only the innermost 32
// frames of a stack: an allocation deep in the paint tree (kit.(*Window).paintTree
// recursing through nested boxes) lost the measured frame and was silently not counted,
// which made it weaker than AllocsPerRun. allocsUnder reads runtime/pprof's allocs
// profile instead (stacks of up to 128 frames, GODEBUG profstackdepth) and also counts a
// stack the profile cut short inside downex/ui code. Like AllocsPerRun, it returns the
// average count rounded down.
func ownAllocsPerRun(runs int, f func()) float64 {
	for range runs {
		f()
	}
	old := runtime.MemProfileRate
	runtime.MemProfileRate = 1 // every allocation, from the next one on
	defer func() { runtime.MemProfileRate = old }()
	// A profile is complete up to two garbage collections ago.
	runtime.GC()
	runtime.GC()
	before := allocsUnder(measured)
	for range runs {
		measured(f)
	}
	runtime.GC()
	runtime.GC()
	return float64((allocsUnder(measured) - before) / int64(runs))
}

// measured calls f; its frame marks the allocations ownAllocsPerRun counts.
//
//go:noinline
func measured(f func()) { f() }

// allocsUnder returns the allocations so far in the allocs profile made under fn: those
// whose call stack passes through fn, and those whose stack the profile cut short (its
// root is not runtime.goexit, where every goroutine's stack ends) with a downex/ui frame
// in what it kept. A cut-short stack cannot show whether fn was below it; in a test of
// the window, deep downex/ui stacks are the window's own. A runtime stack on the system
// stack also ends elsewhere (runtime.systemstack, runtime.mstart), but has no downex/ui
// frame.
func allocsUnder(fn func(func())) int64 {
	name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	var buf bytes.Buffer
	if err := pprof.Lookup("allocs").WriteTo(&buf, 1); err != nil {
		panic(err)
	}
	names := map[uintptr]string{}
	nameOf := func(pc uintptr) string {
		n, ok := names[pc]
		if !ok {
			if f := runtime.FuncForPC(pc - 1); f != nil { // pc is a return address
				n = f.Name()
			}
			names[pc] = n
		}
		return n
	}
	var total int64
	// A record is "inuse objects: inuse bytes [alloc objects: alloc bytes] @ pc pc ...",
	// innermost frame first, then its symbolized frames as # lines.
	for line := range strings.Lines(buf.String()) {
		if strings.HasPrefix(line, "heap profile:") || strings.HasPrefix(line, "#") {
			continue
		}
		counts, stack, ok := strings.Cut(line, " @ ")
		if !ok {
			continue
		}
		_, counts, ok = strings.Cut(counts, "[")
		if !ok {
			continue
		}
		counts, _, ok = strings.Cut(counts, ":")
		objs, err := strconv.ParseInt(counts, 10, 64)
		if !ok || err != nil {
			panic(fmt.Sprintf("allocs profile record %q", line))
		}
		var under, ui bool
		var root string
		for f := range strings.FieldsSeq(stack) {
			pc, err := strconv.ParseUint(f, 0, 64)
			if err != nil {
				panic(fmt.Sprintf("allocs profile record %q", line))
			}
			root = nameOf(uintptr(pc))
			under = under || root == name
			ui = ui || strings.HasPrefix(root, "downex/ui/")
		}
		if under || ui && root != "runtime.goexit" {
			total += objs
		}
	}
	return total
}

// TestOwnAllocsPerRun: ownAllocsPerRun counts an allocation made right under f, 40
// frames below it (deeper than the 32 frames runtime.MemProfile keeps) and 200 frames
// below it (deeper than the allocs profile keeps at all), once per run each; and not an
// allocation another goroutine makes meanwhile, though f waits for it.
func TestOwnAllocsPerRun(t *testing.T) {
	for _, depth := range []int{0, 40, 200} {
		if n := ownAllocsPerRun(50, func() { allocBelow(depth) }); n != 1 {
			t.Errorf("one allocation %d frames below f: %.1f per run, want 1", depth, n)
		}
	}
	work, done := make(chan struct{}), make(chan struct{})
	go func() {
		for range work {
			sink = new([4]int)
			done <- struct{}{}
		}
	}()
	defer close(work)
	if n := ownAllocsPerRun(50, func() { work <- struct{}{}; <-done }); n != 0 {
		t.Errorf("another goroutine's allocation: %.1f per run, want 0", n)
	}
}

// sink keeps allocBelow's allocation on the heap.
var sink any

// allocBelow allocates one object depth frames below its caller.
//
//go:noinline
func allocBelow(depth int) {
	if depth == 0 {
		sink = new([4]int)
		return
	}
	allocBelow(depth - 1)
}

// TestIdle: a main window over a downexd where nothing moves paints and lays out nothing,
// across status checks (every 3 s) and the free-space reading (60 s).
func TestIdle(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			f := quiet(t, theme)
			f.h.Advance(2 * time.Second)
			reads := f.fp.freeReads()
			f.h.Window.ResetStats()
			f.h.Advance(65 * time.Second)
			if s := f.h.Window.Stats(); s.Paints != 0 || s.Layouts != 0 {
				t.Errorf("idle: %d paints, %d layouts", s.Paints, s.Layouts)
			}
			if f.fp.freeReads() <= reads {
				t.Errorf("the free space was not read again in 65 s")
			}
		})
	}
	// With a selection the details pane (the downloads lane's) lays itself out again at every
	// status check; listed in needs. Measured, not failed here.
	f := quiet(t, "light")
	f.sel(1)
	f.h.Advance(2 * time.Second)
	f.h.Window.ResetStats()
	f.h.Advance(9 * time.Second)
	s := f.h.Window.Stats()
	t.Logf("with a selection, 9 s idle: %d paints (%.0f DIP²), %d layouts (%d boxes): the downloads lane's details pane", s.Paints, s.PaintedArea, s.Layouts, s.BoxLayouts)
}

// quiet is the composed window with the lanes over downloads that do not move.
func quiet(t *testing.T, theme string) *fixture {
	t.Helper()
	f := lanes(t, setup{frameless: true},
		dl(1, state.StatePaused, func(d *downexv1.Download) { d.Filename = "paused.iso"; d.Received = MiB }),
		dl(2, state.StateCompleted, func(d *downexv1.Download) { d.Filename = "done.zip"; d.Received = 10 * MiB }),
		dl(3, state.StateFailed, func(d *downexv1.Download) { d.Filename = "failed.bin"; d.Error = "HTTP 500" }))
	if theme == "dark" {
		dark := true
		f.h.App.WidgetTheme.SystemDark = &dark
		f.h.Settle()
	}
	return f
}
