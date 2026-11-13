//go:build darwin && cgo

// Command macspike is the native macOS window's first measurement (the port's step 2):
// the real main window of ui/views (shell, download list, details, status bar) drawn by the
// ui/mac backend over the simulated daemon of ui/state/scene, so that memory, text and
// resizing can be compared with today's WebKit window before the full port. It is a
// development tool, not shipped.
//
//	macspike [-scene studio|random] [-size WxH] [-theme system|light|dark] [-live]
//	         [-exit-after D] [-stats FILE] [-shot FILE]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"downex/ui/host"
	"downex/ui/kit"
	"downex/ui/kit/tokens"
	"downex/ui/kit/widgets"
	"downex/ui/mac"
	"downex/ui/state"
	"downex/ui/state/scene"
	"downex/ui/views"
	"downex/ui/views/app"
	"downex/ui/views/fixture"
)

func init() { runtime.LockOSThread() } // AppKit: the main goroutine stays on the main thread

func main() {
	sceneName := flag.String("scene", "studio", "the simulation: studio or random")
	size := flag.String("size", "", "client size WxH in points (default: the main window's)")
	theme := flag.String("theme", "", "force a theme: system, light or dark")
	live := flag.Bool("live", false, "move the simulation on 4 times a second")
	exitAfter := flag.Duration("exit-after", 0, "close the window after this long")
	stats := flag.String("stats", "", "write the process's memory once a second (JSON lines)")
	shot := flag.String("shot", "", "write what the window drew as PNG 3 s after it opened")
	// [ax]
	flag.StringVar(&axDumpFile, "axdump", "", "write the window's accessibility tree, as VoiceOver reads it, 3 s after it opened")
	// [/ax]
	flag.Parse()
	if err := run(*sceneName, *size, *theme, *live, *exitAfter, *stats, *shot); err != nil {
		fmt.Fprintln(os.Stderr, "macspike:", err)
		os.Exit(1)
	}
}

func run(sceneName, size, theme string, live bool, exitAfter time.Duration, stats, shot string) error {
	start := time.Now()
	so := scene.Options{Scene: scene.Studio, AutoTick: live, TickMs: 250}
	if sceneName == "random" {
		so.Scene, so.Count = scene.Random, 10000
	}
	mock, err := scene.New(so)
	if err != nil {
		return err
	}
	defer mock.Close()
	view := host.ViewOf(nil)
	spec := view.Window()
	fp := &fixture.Platform{Mock: mock}
	themeState := state.NewTheme(state.ThemeOptions{})
	defer themeState.Close()
	var fixed *tokens.Choice
	if c, ok := tokens.ParseChoice(theme); ok {
		fixed = &c
	}

	a, err := mac.NewApp(mac.AppOptions{Regular: true})
	if err != nil {
		return err
	}
	defer a.Close()

	vt := &views.Theme{State: themeState, Fixed: fixed}
	var va *views.App
	var m *app.Main
	widgetTheme := func() *views.Theme {
		if va != nil {
			return va.WidgetTheme
		}
		return vt
	}
	density := state.NewDensity(nil)
	defer density.Close()
	stores := state.NewMain(state.MainOptions{
		Daemon: mock, Events: mock, Files: fp, CopyText: fp.CopyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock},
		Collator: state.UseUserLocale(),
	})
	holder := widgets.NewScope(vt)
	holder.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	minSize := kit.Size{W: float32(spec.MinWidth), H: float32(spec.MinHeight)}
	kw := kit.NewWindow(holder, kit.WindowOptions{
		Title:       spec.Title,
		MinSize:     minSize,
		Palette:     func(env kit.Environment) kit.Palette { return widgetTheme().Palette(env) },
		FrameColors: func(env kit.Environment) kit.FrameColors { return widgetTheme().MainFrame(env) },
		OnKey:       func(e kit.KeyEvent) bool { return m != nil && m.HandleKey(e) },
	})
	va, err = views.NewApp(views.Options{
		Window: kw, Daemon: mock, Platform: fp, Main: stores, Theme: themeState, Density: density,
		Clock: state.SystemClock, Caps: view.Capabilities(), Build: host.CurrentBuild("0.9.2-spike"), Demo: true,
		Log: slog.New(slog.DiscardHandler), FixedTheme: fixed,
	})
	if err != nil {
		return err
	}
	defer va.Dispose()
	holder.Theme = va.WidgetTheme
	m, err = app.New(va, app.Options{})
	if err != nil {
		return err
	}
	defer m.Dispose()
	holder.Add(m.Root())

	wsize := kit.Size{W: float32(spec.Width), H: float32(spec.Height)}
	if size != "" {
		var w, h float32
		if _, err := fmt.Sscanf(strings.ToLower(size), "%gx%g", &w, &h); err != nil || w <= 0 || h <= 0 {
			return fmt.Errorf("bad -size %q", size)
		}
		wsize = kit.Size{W: w, H: h}
	}
	w, err := a.NewWindow(kw, mac.WindowOptions{Title: spec.Title, Size: wsize, MinSize: minSize})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "macspike: first frame after %v\n", time.Since(start).Round(time.Millisecond))
	stores.Start()
	defer stores.Dispose()
	if stats != "" {
		go writeStats(stats)
	}
	if shot != "" {
		time.AfterFunc(3*time.Second, func() {
			_ = w.Post(func() {
				if err := w.WritePNG(shot); err != nil {
					fmt.Fprintln(os.Stderr, "macspike: shot:", err)
				}
			})
		})
	}
	// [ax]
	if axDumpFile != "" {
		time.AfterFunc(3*time.Second, func() { _ = w.Post(func() { writeAXDump(w, axDumpFile) }) })
	}
	// [/ax]
	if exitAfter > 0 {
		time.AfterFunc(exitAfter, func() { _ = w.Post(w.Close) })
	}
	return a.Run()
}

// writeStats appends the Go heap and the process's resident size once a second.
func writeStats(path string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for t := time.NewTicker(time.Second); ; <-t.C {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		_ = enc.Encode(map[string]any{"t": time.Now().Format(time.RFC3339), "heap_inuse": ms.HeapInuse, "sys": ms.Sys})
	}
}
