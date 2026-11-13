//go:build windows

package downloads

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"google.golang.org/protobuf/proto"

	downexv1 "downex/internal/rpc/downex/v1"
	. "downex/ui/core/format/formattest"
	"downex/ui/core/query"
	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/state/statetest"
	"downex/ui/views"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// The plan's real-window gates for step 11, in a Win32 window drawn with Direct2D and
// DirectWrite (not offscreen), with the wall clock: the grid and the pane over the fixture
// scene in live mode. They open windows on the desktop without activating them, so they run
// only with DOWNEX_DL_REAL=1 (one gate per process: DOWNEX_DL_REAL=perf or =memory).

func realGate(t *testing.T, which string) {
	t.Helper()
	if v := os.Getenv("DOWNEX_DL_REAL"); v != which {
		t.Skipf("set DOWNEX_DL_REAL=%s to open a real window", which)
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
}

// realScreen is the main window's composition reduced to this lane's part: an App over the
// stores, the grid where the shell puts it at 1200 × 760, and the pane under it.
type realScreen struct {
	app    *views.App
	main   *state.Main
	dl     *Downloads
	kw     *kit.Window
	root   *widgets.Scope
	screen *placed
}

func newRealScreen(t *testing.T, daemon host.Daemon, events state.Events, files state.FileActions, plat views.Platform, copyText func(context.Context, string) error) *realScreen {
	t.Helper()
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	main := state.NewMain(state.MainOptions{
		Daemon: daemon, Events: events, Files: files, CopyText: copyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock}, Call: call,
	})
	storage := statetest.NewMemory(nil)
	theme := state.NewTheme(state.ThemeOptions{Storage: storage})
	theme.Set(host.ThemeLight)
	density := state.NewDensity(storage)
	var app *views.App
	root := widgets.NewScope(nil)
	root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	kw := kit.NewWindow(root, kit.WindowOptions{
		Title: "Downex grid gate",
		Palette: func(env kit.Environment) kit.Palette {
			if app == nil {
				return tokens.For(tokens.ChoiceLight, env).Palette()
			}
			return app.WidgetTheme.Palette(env)
		},
	})
	light := false
	app, err := views.NewApp(views.Options{
		Window: kw, Daemon: daemon, Platform: plat, Main: main, Theme: theme, Density: density,
		Storage: storage, Clock: state.SystemClock, Call: call, SystemDark: &light, StatusBarHeight: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	root.Theme = app.WidgetTheme
	dl, err := New(app)
	if err != nil {
		t.Fatal(err)
	}
	s := &placed{boxes: []kit.Rect{kit.R(225, 103, 1200, 501), kit.R(225, 501, 1200, 727)}}
	s.Style = kit.Style{Width: kit.Pct(100), Height: kit.Pct(100)}
	s.Add(dl.Grid(), dl.Details())
	root.Add(s)
	return &realScreen{app: app, main: main, dl: dl, kw: kw, root: root, screen: s}
}

// onUI runs f on the window's thread and waits for it.
func (s *realScreen) onUI(f func()) {
	done := make(chan struct{})
	if s.kw.Post(func() { f(); close(done) }) != nil {
		return
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// run opens the window (not activated), runs script on its own goroutine and closes the
// window when it returns.
func (s *realScreen) run(t *testing.T, script func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a, err := win.NewApp(win.AppOptions{ClassName: "DownexDownloadsLaneGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.NewWindow(s.kw, win.WindowOptions{Title: "Downex grid gate", Size: kit.Size{W: 1200, H: 760}, NoActivate: true}); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer func() { _ = s.kw.Post(s.kw.Close) }()
		script()
	}()
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	s.dl.Dispose()
	s.app.Dispose()
	s.main.Dispose()
}

func (s *realScreen) stats() (st kit.Stats) {
	s.onUI(func() { st = s.kw.Stats() })
	return
}

// 10,000 rows of the fixture scene in live mode (4 event batches a second, the simulation's
// own timer): at most 8 layouts a second, and no paint, frame or layout once it stops.
func TestRealWindowTenThousandRowsLive(t *testing.T) {
	realGate(t, "perf")
	mock, err := scene.New(scene.Options{Scene: scene.Random, Count: 10_000, AutoTick: true, TickMs: 250})
	if err != nil {
		t.Fatal(err)
	}
	sp := &viewstest.ScenePlatform{Mock: mock}
	s := newRealScreen(t, mock, mock, sp, sp, sp.CopyText)
	var report []string
	s.run(t, func() {
		s.main.Start()
		s.main.Downloads.RefreshStatus()
		time.Sleep(3 * time.Second) // the first read and the first frames
		var rows int
		var changes atomic.Int64
		stopCount := s.main.Downloads.Changes.Subscribe(func() { changes.Add(1) })
		defer stopCount()
		s.onUI(func() { rows = s.dl.grid.table.model.Len() })
		// The running rows are the oldest (at the end in the default order, Added newest
		// first): round 1 shows them scrolled to the end, rounds 2 and 3 sorted by speed
		// (the order changes with every batch).
		for round := 1; round <= 3; round++ {
			s.onUI(func() {
				t := s.dl.grid.table
				switch round {
				case 1:
					t.ScrollTo(kit.DIP(t.model.Len()) * t.RowHeightNow())
				case 2:
					t.ScrollTo(0)
					s.main.Downloads.SortBy(query.SortKey("speed"))
				}
			})
			time.Sleep(time.Second)
			a := s.stats()
			c0 := changes.Load()
			const secs = 10
			time.Sleep(secs * time.Second)
			b := s.stats()
			lps := float64(b.Layouts-a.Layouts) / secs
			var running int
			s.onUI(func() {
				for _, d := range s.main.Downloads.All() {
					if d.GetState() == state.StateDownloading {
						running++
					}
				}
			})
			report = append(report, fmt.Sprintf("live round %d: %d running, %d store changes; %d rows, %.1f layouts/s, %.1f paints/s, %.1f frames/s, %.0f DIP² painted/s",
				round, running, changes.Load()-c0, rows, lps, float64(b.Paints-a.Paints)/secs, float64(b.Frames-a.Frames)/secs, (b.PaintedArea-a.PaintedArea)/secs))
			if lps > 8 {
				t.Errorf("round %d: %.1f layouts a second, the gate is 8", round, lps)
			}
		}
		mock.Close() // the simulation stops: nothing changes any more
		time.Sleep(2 * time.Second)
		a := s.stats()
		time.Sleep(10 * time.Second)
		b := s.stats()
		report = append(report, fmt.Sprintf("idle 10 s: %d paints, %d frames, %d layouts, %d frame requests",
			b.Paints-a.Paints, b.Frames-a.Frames, b.Layouts-a.Layouts, b.FrameRequests-a.FrameRequests))
		if b.Paints != a.Paints || b.Frames != a.Frames || b.Layouts != a.Layouts {
			t.Errorf("idle window worked: %d paints, %d frames, %d layouts", b.Paints-a.Paints, b.Frames-a.Frames, b.Layouts-a.Layouts)
		}
	})
	for _, l := range report {
		t.Log(l)
	}
}

// processMemoryCountersEx2 is psapi.h's PROCESS_MEMORY_COUNTERS_EX2.
type processMemoryCountersEx2 struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
	privateUsage               uintptr
	privateWorkingSetSize      uintptr
	sharedCommitUsage          uint64
}

var procGetProcessMemoryInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// privateWorkingSet is the process's private working set in bytes (Task Manager's Memory
// column).
func privateWorkingSet() (uint64, error) {
	var c processMemoryCountersEx2
	c.cb = uint32(unsafe.Sizeof(c))
	self, _ := syscall.GetCurrentProcess()
	r, _, err := procGetProcessMemoryInfo.Call(uintptr(self), uintptr(unsafe.Pointer(&c)), uintptr(c.cb))
	if r == 0 {
		return 0, err
	}
	return uint64(c.privateWorkingSetSize), nil
}

// The grid and the pane of the main window, 40 downloads with one selected: 25 MB or less of
// private working set idle and 30 MB or less while 20 rows download (4 reports a second),
// measured 3 times each.
func TestRealWindowMemory(t *testing.T) {
	realGate(t, "memory")
	var list []*downexv1.Download
	for i := int64(1); i <= 40; i++ {
		list = append(list, Download(i, func(d *downexv1.Download) {
			d.Filename = "file-" + strconv.FormatInt(i, 10) + ".iso"
			d.Size = 800 * MB
			switch {
			case i <= 20:
				d.State, d.Received, d.SpeedBps, d.Connections = state.StateCompleted, 800*MB, 0, 0
			default:
				d.State, d.Received = state.StatePaused, int64(i)*10*MB
			}
		}))
	}
	daemon := statetest.NewDaemon(list...)
	fp := &viewstest.FakePlatform{}
	s := newRealScreen(t, daemon, &daemon.Events, fp, fp, fp.CopyText)
	var report []string
	measure := func(label string, limit float64) {
		for i := 1; i <= 3; i++ {
			time.Sleep(4 * time.Second)
			debug.FreeOSMemory()
			time.Sleep(time.Second)
			b, err := privateWorkingSet()
			if err != nil {
				t.Fatal(err)
			}
			mb := float64(b) / (1 << 20)
			report = append(report, fmt.Sprintf("%s, reading %d: private working set %.1f MB", label, i, mb))
			if mb > limit {
				t.Errorf("%s: %.1f MB, the gate is %.0f MB", label, mb, limit)
			}
		}
	}
	s.run(t, func() {
		s.main.Start()
		s.main.Downloads.RefreshStatus()
		time.Sleep(2 * time.Second)
		s.onUI(func() { s.main.Downloads.SelectOnly(25) })
		measure("idle", 25)
		// 20 rows download: 4 reports a second, each changing every running row
		stop := make(chan struct{})
		go func() {
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			n := 0
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				n++
				var evs []*downexv1.Event
				for i := int64(21); i <= 40; i++ {
					// a new message per report, as the wire gives: the stores never change one
					d := proto.Clone(list[i-1]).(*downexv1.Download)
					d.State = state.StateDownloading
					d.Received = min(d.Size, d.Received+int64(n)*256*KiB)
					d.SpeedBps = float64(1+(n+int(i))%9) * MB
					d.Connections = 4
					d.ConnectionInfo = []*downexv1.ConnectionInfo{conn(1, nil), conn(2, nil), conn(3, nil), conn(4, nil)}
					evs = append(evs, &downexv1.Event{Kind: &downexv1.Event_Updated{Updated: d}})
				}
				daemon.Emit(evs...)
			}
		}()
		measure("20 rows downloading", 30)
		close(stop)
	})
	for _, l := range report {
		t.Log(l)
	}
}
