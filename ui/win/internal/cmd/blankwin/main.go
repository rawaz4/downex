//go:build windows

// Command blankwin opens one native window whose root paints only its ground, to measure
// what the backend costs (memory, CPU, first frame) without any UI on top.
//
//	blankwin [-software] [-text] [-seconds n] [-gc percent]
//
// -text also draws one line of text, so DirectWrite and its fonts are loaded.
//
// It prints its process id, window handle and renderer on stdout, and exits when the window
// is closed or after -seconds.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"downex/ui/kit"
	"downex/ui/win"
)

func init() { runtime.LockOSThread() } // the UI thread is the main goroutine's

func main() {
	software := flag.Bool("software", false, "draw with Direct2D's software renderer")
	seconds := flag.Int("seconds", 0, "close the window after this many seconds (0: when closed)")
	gc := flag.Int("gc", 0, "GOGC percent (0 leaves Go's default)")
	text := flag.Bool("text", false, "draw a line of text too (loads DirectWrite)")
	flag.Parse()
	if *gc > 0 {
		debug.SetGCPercent(*gc)
	}
	start := time.Now()
	app, err := win.NewApp(win.AppOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blankwin:", err)
		os.Exit(1)
	}
	defer app.Close()
	root := &blank{text: *text}
	w, err := app.NewWindow(root, win.WindowOptions{
		Title:      "Downex native window (blank)",
		Size:       kit.Size{W: 900, H: 600},
		Software:   *software,
		NoActivate: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "blankwin:", err)
		os.Exit(1)
	}
	fmt.Printf("pid %d hwnd %#x renderer %s first frame %d ms\n", os.Getpid(), uintptr(w.HWND()), w.Renderer(), time.Since(start).Milliseconds())
	if *seconds > 0 {
		go func() {
			time.Sleep(time.Duration(*seconds) * time.Second)
			w.Post(w.Close)
		}()
	}
	if err := app.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "blankwin:", err)
		os.Exit(1)
	}
	fmt.Printf("frames %d\n", root.frames)
}

// blank paints the main window's ground (app.css --chrome, light), and with text a title in
// the page title's style.
type blank struct {
	host   kit.Host
	frames int
	text   bool
	title  kit.TextLayout
}

var chrome = kit.RGB(0xeef2f0)

func (b *blank) Attach(h kit.Host) {
	b.host = h
	h.SetFrameColors(kit.FrameColors{Background: chrome})
	if b.text {
		b.title, _ = h.Resources().LayoutText("All downloads", kit.TextStyle{
			Family: "'Segoe UI Variable Display', 'Segoe UI Variable Text', 'Segoe UI', system-ui, sans-serif",
			Size:   20, Weight: 650, LetterSpacing: -0.02, LineHeight: 1.2, Tabular: true,
		}, kit.TextOptions{})
	}
}

func (b *blank) Detach() {
	if b.title != nil {
		b.title.Release()
	}
}
func (b *blank) Frame(time.Time) { b.frames++ }
func (b *blank) Paint(c kit.Canvas, dirty kit.Rect) {
	c.FillRect(dirty, chrome)
	if b.title != nil {
		c.DrawText(b.title, kit.Pt(20, 16), kit.RGB(0x0f1613))
	}
}
func (b *blank) Event(kit.Event) bool             { return false }
func (b *blank) FrameHit(kit.Point) kit.FramePart { return kit.FrameClient }
func (b *blank) Semantics() kit.SemanticTree      { return nil }
