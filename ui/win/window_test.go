//go:build windows

package win

import (
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

// testRoot records the calls the host makes and fails on any that nests in another.
type testRoot struct {
	t        *testing.T
	host     kit.Host
	in       string
	calls    []string
	events   []kit.Event
	paints   []kit.Rect
	frames   int
	color    kit.Color
	onEvent  func(e kit.Event) bool
	onFrame  func()
	frameHit func(p kit.Point) kit.FramePart
	label    kit.TextLayout
}

func runtimeLock(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
}

func (r *testRoot) enter(name string) {
	if r.in != "" {
		r.t.Errorf("%s called inside %s: calls into the root nested", name, r.in)
	}
	r.in = name
	r.calls = append(r.calls, name)
}
func (r *testRoot) leave() { r.in = "" }

func (r *testRoot) Attach(h kit.Host) {
	r.enter("Attach")
	defer r.leave()
	r.host = h
	r.color = kit.RGB(0x1b7f4c)
	h.SetTitle("Downex backend test")
	h.SetFrameColors(kit.FrameColors{Background: kit.RGB(0xeef2f0), Caption: kit.RGB(0xffffff), Text: kit.RGB(0x0f1613),
		InactiveText: kit.RGB(0x56625c), Border: kit.RGB(0xcfd8d3), InactiveBorder: kit.RGB(0xe4e9e6)})
	h.SetMinSize(kit.Size{W: 200, H: 120})
	r.label, _ = h.Resources().LayoutText("Downex", body, kit.TextOptions{})
}
func (r *testRoot) Detach() {
	r.enter("Detach")
	defer r.leave()
	if r.label != nil {
		r.label.Release()
	}
}
func (r *testRoot) Frame(time.Time) {
	r.enter("Frame")
	defer r.leave()
	r.frames++
	if r.onFrame != nil {
		r.onFrame()
	}
}
func (r *testRoot) Paint(c kit.Canvas, dirty kit.Rect) {
	r.enter("Paint")
	defer r.leave()
	r.paints = append(r.paints, dirty)
	c.FillRect(dirty, kit.RGB(0xeef2f0))
	c.FillRect(kit.R(10, 10, 60, 60), r.color)
	c.DrawText(r.label, kit.Pt(70, 20), kit.RGB(0x0f1613))
}
func (r *testRoot) Event(e kit.Event) bool {
	r.enter("Event")
	defer r.leave()
	r.events = append(r.events, e)
	if r.onEvent != nil {
		return r.onEvent(e)
	}
	return false
}
func (r *testRoot) FrameHit(p kit.Point) kit.FramePart {
	if r.frameHit != nil {
		return r.frameHit(p)
	}
	return kit.FrameClient
}
func (r *testRoot) Semantics() kit.SemanticTree { return nil }

// skipOrRequire skips a test whose setup this PC lacks (a second monitor of another DPI),
// saying loudly what stays unverified; with DOWNEX_WIN_REQUIRE_ALL set (a gate run) it
// fails instead, so that a skip never passes for a verified gate.
func skipOrRequire(t *testing.T, why string) {
	t.Helper()
	if os.Getenv("DOWNEX_WIN_REQUIRE_ALL") != "" {
		t.Fatal(why)
	}
	t.Log("UNVERIFIED: " + why)
	t.Skip(why)
}

// Heap memory for the structures tests hand to the window procedure, which writes through
// them while it runs on the test goroutine's stack (package w32's rule: never a local).
var (
	heapMMI   = new(w32.MINMAXINFO)
	heapPoint = new(w32.Point)
	heapText  = new([64]uint16)
)

// openTest makes an App and a window for root on the test's locked thread, at the primary
// monitor's top-left corner and without taking the focus.
func openTest(t *testing.T, root *testRoot, opts WindowOptions) (*App, *Window) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	a, err := NewApp(AppOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Close()
		if l := a.res.live; l.layouts != 0 || l.paths != 0 || l.images != 0 {
			t.Errorf("leaked %+v", l)
		}
	})
	root.t = t
	opts.NoActivate = true
	if !opts.Placed {
		mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
		mi, _ := w32.GetMonitorInfo(mon)
		opts.At, opts.Placed = w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, true
	}
	if opts.Size == (kit.Size{}) {
		opts.Size = kit.Size{W: 320, H: 200}
	}
	w, err := a.NewWindow(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	return a, w
}

// pump runs the loop until cond holds, or fails the test after timeout.
func pump(t *testing.T, a *App, w *Window, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	if !pumpFor(t, a, w, timeout, cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// pumpFor runs the loop until cond holds or timeout passes and reports whether it held. A
// goroutine posts WM_NULL to wake the loop so that cond is looked at regularly.
func pumpFor(t *testing.T, a *App, w *Window, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	stop := make(chan struct{})
	defer close(stop)
	h := w.hwnd
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				w32.PostMessage(h, 0, 0, 0)
			}
		}
	}()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		if r := w32.GetMessage(&a.msg); r <= 0 {
			return cond()
		}
		a.dispatch(&a.msg)
		if a.panicked != nil {
			t.Fatal(a.panicked)
		}
	}
	return true
}

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procSendMessage = user32.NewProc("SendMessageW")
)

// send delivers a message to our own window synchronously, as the system would.
func send(w *Window, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := syscall.SyscallN(procSendMessage.Addr(), uintptr(w.hwnd), uintptr(msg), wp, lp)
	return r
}

func lparam(x, y int) uintptr { return uintptr(uint16(int16(x))) | uintptr(uint16(int16(y)))<<16 }

func TestWindowLifecycle(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Size: kit.Size{W: 320, H: 200}})
	if root.calls[0] != "Attach" {
		t.Fatalf("first call %s", root.calls[0])
	}
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return len(root.paints) > 0 })
	if got := root.paints[0]; got != kit.R(0, 0, 320, 200) {
		t.Errorf("first paint of %v, want the whole client area", got)
	}
	if w.Size() != (kit.Size{W: 320, H: 200}) {
		t.Errorf("size %v", w.Size())
	}
	t.Logf("renderer %s, scale %v, dark %v, text %v", w.Renderer(), w.Environment().Scale.PixelsPerDIP, w.Environment().Dark, w.Environment().Scale.Text)

	// Idle: no frames at all.
	frames := root.frames
	pumpFor(t, a, w, 600*time.Millisecond, func() bool { return false })
	if root.frames != frames {
		t.Errorf("an idle window made %d frames in 600 ms", root.frames-frames)
	}

	// RequestFrame: exactly one Frame, and no Paint while nothing is invalid.
	paints := len(root.paints)
	w.RequestFrame()
	w.RequestFrame()
	pump(t, a, w, "the requested frame", time.Second, func() bool { return root.frames == frames+1 })
	pumpFor(t, a, w, 200*time.Millisecond, func() bool { return false })
	if root.frames != frames+1 || len(root.paints) != paints {
		t.Errorf("RequestFrame made %d frames and %d paints, want 1 and 0", root.frames-frames, len(root.paints)-paints)
	}

	// An animation asks again from Frame: frames keep coming at the display's pace.
	n := 0
	root.onFrame = func() {
		if n++; n < 30 {
			w.RequestFrame()
		}
	}
	start := time.Now()
	w.RequestFrame()
	pump(t, a, w, "30 animation frames", 3*time.Second, func() bool { return n >= 30 })
	el := time.Since(start)
	root.onFrame = nil
	t.Logf("30 animation frames in %v (%.1f/s)", el, 30/el.Seconds())
	// Paced by the compositor: no faster than a 240 Hz display (this PC's panel runs at
	// 239 Hz); a loop that is not paced would make 30 frames in a few milliseconds.
	if el < 30*time.Second/240*9/10 {
		t.Errorf("30 frames in %v: not paced by the display", el)
	}

	// Invalidate: Frame then Paint of the area, rounded out to device pixels.
	paints = len(root.paints)
	w.Invalidate(kit.R(10.2, 10.2, 20.6, 20.6))
	pump(t, a, w, "the invalidated paint", time.Second, func() bool { return len(root.paints) > paints })
	got := root.paints[len(root.paints)-1]
	if !got.Intersect(kit.R(10.2, 10.2, 20.6, 20.6)).Empty() && (got.Min.X > 10.2 || got.Max.X < 20.6) {
		t.Errorf("painted %v for an invalid 10.2..20.6", got)
	}

	// A burst of invalidations (hover over many cells) becomes one frame at the display's pace.
	paints = len(root.paints)
	frames = root.frames
	for i := 0; i < 50; i++ {
		w.Invalidate(kit.R(float32(i), 0, float32(i+1), 1))
	}
	pump(t, a, w, "the coalesced paint", time.Second, func() bool { return len(root.paints) > paints })
	pumpFor(t, a, w, 150*time.Millisecond, func() bool { return false })
	if len(root.paints) != paints+1 || root.frames != frames+1 {
		t.Errorf("50 invalidations made %d frames and %d paints, want 1 and 1", root.frames-frames, len(root.paints)-paints)
	} else if got := root.paints[paints]; got.Min.X > 0 || got.Max.X < 50 {
		t.Errorf("the coalesced paint covers %v, want 0..50", got)
	}

	// Post from another goroutine runs on the UI thread, in order.
	var ran atomic.Int32
	var order []int
	thread := a.thread
	go func() {
		for i := 0; i < 3; i++ {
			i := i
			w.Post(func() {
				if id := currentThread(); id != thread {
					t.Errorf("posted function ran on thread %d, not %d", id, thread)
				}
				order = append(order, i)
				ran.Add(1)
			})
		}
	}()
	pump(t, a, w, "the posted functions", time.Second, func() bool { return ran.Load() == 3 })
	if fmt.Sprint(order) != "[0 1 2]" {
		t.Errorf("posts ran in order %v", order)
	}

	// Close: Detach is the last call, Post fails afterwards.
	w.Close()
	if !w.closed || root.calls[len(root.calls)-1] != "Detach" {
		t.Fatalf("after Close: closed=%v, last call %s", w.closed, root.calls[len(root.calls)-1])
	}
	if err := w.Post(func() {}); !errors.Is(err, kit.ErrClosed) {
		t.Errorf("Post after Close: %v", err)
	}
	if err := a.Run(); err != nil { // no windows: returns at once
		t.Fatal(err)
	}
}

func currentThread() uint32 {
	r, _, _ := syscall.SyscallN(syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId").Addr())
	return uint32(r)
}

// Input messages become kit events; what a Host method causes during a call into the root
// arrives after that call.
func TestWindowInput(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return len(root.paints) > 0 })
	ppd := w.ppd()
	px := func(d float32) int { return int(d * ppd) }

	root.events = nil
	send(w, w32.WM_MOUSEMOVE, 0, lparam(px(30), px(40)))
	send(w, w32.WM_LBUTTONDOWN, w32.MK_LBUTTON, lparam(px(30), px(40)))
	send(w, w32.WM_LBUTTONUP, 0, lparam(px(30), px(40)))
	send(w, w32.WM_LBUTTONDOWN, w32.MK_LBUTTON, lparam(px(30), px(40)))
	send(w, w32.WM_MOUSEWHEEL, uintptr(uint16(0xFF88))<<16, lparam(0, 0)) // one notch towards the user
	var kinds []string
	for _, e := range root.events {
		switch e := e.(type) {
		case kit.PointerEvent:
			kinds = append(kinds, fmt.Sprintf("pointer %d clicks %d at %.0f,%.0f", e.Action, e.Clicks, e.Pos.X, e.Pos.Y))
		case kit.WheelEvent:
			kinds = append(kinds, fmt.Sprintf("wheel %v mode %d", e.Delta.Y, e.Mode))
		}
	}
	got := strings.Join(kinds, "; ")
	want := "pointer 4 clicks 0 at 30,40; pointer 1 clicks 0 at 30,40; pointer 2 clicks 1 at 30,40; pointer 3 clicks 0 at 30,40; pointer 2 clicks 2 at 30,40; wheel"
	if !strings.HasPrefix(got, want) {
		t.Errorf("events:\n%s\nwant to start with\n%s", got, want)
	}
	if we, ok := root.events[len(root.events)-1].(kit.WheelEvent); !ok || we.Mode != kit.WheelLines || we.Delta.Y != float32(a.wheelLines)*100/3 {
		t.Errorf("wheel event %+v (lines per notch %d)", root.events[len(root.events)-1], a.wheelLines)
	}
	send(w, w32.WM_LBUTTONUP, 0, lparam(px(30), px(40)))

	// Keys: a KeyDown the root uses types nothing; one it does not use types its character.
	root.events = nil
	root.onEvent = func(e kit.Event) bool {
		k, ok := e.(kit.KeyEvent)
		return ok && k.Action == kit.KeyDown && k.Key == kit.KeySpace
	}
	mapVK := user32.NewProc("MapVirtualKeyW")
	for _, vk := range []uintptr{' ', 'A'} {
		scan, _, _ := mapVK.Call(vk, 0) // MAPVK_VK_TO_VSC: TranslateMessage needs the scan code
		w32.PostMessage(w.hwnd, w32.WM_KEYDOWN, vk, 1|scan<<16)
		w32.PostMessage(w.hwnd, w32.WM_KEYUP, vk, 1|scan<<16|3<<30)
	}
	pumpFor(t, a, w, time.Second, func() bool {
		for _, e := range root.events {
			if _, ok := e.(kit.TextEvent); ok {
				return true
			}
		}
		return false
	})
	pumpFor(t, a, w, 100*time.Millisecond, func() bool { return false }) // a Space typed would come now
	var texts []string
	for _, e := range root.events {
		if te, ok := e.(kit.TextEvent); ok {
			texts = append(texts, te.Text)
		}
	}
	if len(texts) != 1 || !strings.EqualFold(texts[0], "a") {
		t.Errorf("typed %q, want only the A (the root used Space); events %v", texts, root.events)
	}
	root.onEvent = nil

	// A surrogate pair arrives as one character.
	root.events = nil
	send(w, w32.WM_CHAR, 0xD83D, 0)
	send(w, w32.WM_CHAR, 0xDE80, 0)
	if len(root.events) != 1 || root.events[0] != (kit.TextEvent{Text: "🚀"}) {
		t.Errorf("surrogate pair gave %v", root.events)
	}

	// Never nest: Resize inside Event sends WM_SIZE at once, but the SizeEvent comes after.
	root.events = nil
	root.onEvent = func(e kit.Event) bool {
		if _, ok := e.(kit.TextEvent); ok {
			w.Resize(kit.Size{W: 360, H: 220})
			if w.Size() != (kit.Size{W: 360, H: 220}) {
				t.Errorf("size after Resize inside the call: %v", w.Size())
			}
			if len(root.events) != 1 {
				t.Errorf("events during the call: %v", root.events)
			}
		}
		return true
	}
	send(w, w32.WM_CHAR, 'x', 0)
	root.onEvent = nil
	if len(root.events) < 2 || root.events[1] != (kit.SizeEvent{Size: kit.Size{W: 360, H: 220}}) {
		t.Errorf("events after Resize: %v", root.events)
	}

	// Close from inside a call closes once it has returned; a CloseEvent the root does not
	// use closes the window.
	root.onEvent = func(e kit.Event) bool {
		if _, ok := e.(kit.CloseEvent); ok {
			return true // keep the window
		}
		return false
	}
	send(w, w32.WM_CLOSE, 0, 0)
	if w.closed {
		t.Fatal("a CloseEvent the root used closed the window")
	}
	root.onEvent = func(e kit.Event) bool {
		w.Close()
		if w.closed {
			t.Error("Close inside a call closed at once")
		}
		return true
	}
	send(w, w32.WM_CHAR, 'y', 0)
	if !w.closed || root.calls[len(root.calls)-1] != "Detach" {
		t.Fatalf("after Close in a call: closed=%v, last call %s", w.closed, root.calls[len(root.calls)-1])
	}
}

// A lost device in the window (D2DERR_RECREATE_TARGET, simulated) is recovered: the target
// is made again, the next frame paints the whole window, and it shows the scene.
func TestWindowDeviceLoss(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return len(root.paints) > 0 && w.painted })
	if w.target.dev.targetsMade != 1 {
		t.Fatalf("targets made %d", w.target.dev.targetsMade)
	}
	w.target.dev.simulateLoss = true
	root.color = kit.RGB(0xd03030)
	paints := len(root.paints)
	w.Invalidate(kit.R(0, 0, 5, 5))
	if !pumpFor(t, a, w, 2*time.Second, func() bool {
		return len(root.paints) >= paints+2 && w.target.dev.targetsMade == 2 && w.target.dev.rt != nil
	}) {
		t.Fatalf("no recovery: paints %d (was %d), targets %d, rt %v, last error %v", len(root.paints), paints, w.target.dev.targetsMade, w.target.dev.rt != nil, w.target.lastErr)
	}
	if !errors.Is(w.target.lastErr, kit.ErrDeviceLost) {
		t.Errorf("last frame error %v", w.target.lastErr)
	}
	if got := root.paints[len(root.paints)-1]; got != kit.R(0, 0, 320, 200) {
		t.Errorf("after the loss the window painted %v, not all of it", got)
	}
	t.Logf("recovered: %d targets, %d losses, renderer %s", w.target.dev.targetsMade, w.target.dev.losses, w.Renderer())
	img, err := capture(w)
	if err != nil {
		t.Skipf("cannot capture the window to check its pixels: %v", err)
	}
	ppd := w.ppd()
	c := img.RGBAAt(int(30*ppd), int(30*ppd))
	if c.R < 180 || c.G > 90 || c.B > 90 {
		t.Errorf("after the recovery the square shows %v, want the new red", c)
	}
	bg := img.RGBAAt(int(200*ppd), int(150*ppd))
	if bg.R < 220 || bg.G < 230 {
		t.Errorf("after the recovery the ground shows %v, want #eef2f0", bg)
	}
}

func TestWindowSoftwareRenderer(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Software: true})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	if w.Renderer() != "software" {
		t.Errorf("renderer %q, want software", w.Renderer())
	}
	img, err := capture(w)
	if err != nil {
		t.Skipf("cannot capture: %v", err)
	}
	if c := img.RGBAAt(int(30*w.ppd()), int(30*w.ppd())); c.G < 100 || c.R > 60 {
		t.Errorf("software frame shows %v at the square", c)
	}
}

// The x64 build is required; other architectures get an explanation that wraps
// kit.ErrUnsupported (the windows/arm64 build compiles and fails this way at run time).
func TestArchitectureRefused(t *testing.T) {
	err := archErr("arm64")
	if !errors.Is(err, kit.ErrUnsupported) || !strings.Contains(err.Error(), "needs the x64 build") || !strings.Contains(err.Error(), "windows/arm64") {
		t.Fatalf("archErr(arm64) = %v", err)
	}
	if archErr("amd64") != nil {
		t.Fatal("amd64 refused")
	}
}

// capture copies the window's client area: through PrintWindow(PW_RENDERFULLCONTENT), which
// reads Direct2D's GPU frames, or, when that gives nothing (the software renderer draws with
// GDI), by copying the window's own device context.
func capture(w *Window) (*stdimage.RGBA, error) {
	img, err := captureWith(w, true)
	if err == nil && !allBlack(img) {
		return img, nil
	}
	return captureWith(w, false)
}

func allBlack(img *stdimage.RGBA) bool {
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 || img.Pix[i+1] != 0 || img.Pix[i+2] != 0 {
			return false
		}
	}
	return true
}

func captureWith(w *Window, print bool) (*stdimage.RGBA, error) {
	gdi := syscall.NewLazyDLL("gdi32.dll")
	getDC, releaseDC := user32.NewProc("GetDC"), user32.NewProc("ReleaseDC")
	printWindow := user32.NewProc("PrintWindow")
	createDC, deleteDC := gdi.NewProc("CreateCompatibleDC"), gdi.NewProc("DeleteDC")
	createBmp, selectObj, deleteObj := gdi.NewProc("CreateCompatibleBitmap"), gdi.NewProc("SelectObject"), gdi.NewProc("DeleteObject")
	bitBlt, getDIBits := gdi.NewProc("BitBlt"), gdi.NewProc("GetDIBits")
	rc := w32.GetClientRect(w.hwnd)
	wr := w32.GetWindowRect(w.hwnd)
	origin := w32.ClientToScreen(w.hwnd, w32.Point{})
	cw, ch := int(rc.Right), int(rc.Bottom)
	bw, bh := int(wr.Right-wr.Left), int(wr.Bottom-wr.Top)
	ox, oy := int(origin.X-wr.Left), int(origin.Y-wr.Top)
	if !print {
		bw, bh, ox, oy = cw, ch, 0, 0
	}
	screen, _, _ := getDC.Call(0)
	defer releaseDC.Call(0, screen)
	mem, _, _ := createDC.Call(screen)
	defer deleteDC.Call(mem)
	bmp, _, _ := createBmp.Call(screen, uintptr(bw), uintptr(bh))
	defer deleteObj.Call(bmp)
	old, _, _ := selectObj.Call(mem, bmp)
	var ok uintptr
	if print {
		ok, _, _ = printWindow.Call(uintptr(w.hwnd), mem, 2 /* PW_RENDERFULLCONTENT */)
	} else {
		wdc, _, _ := getDC.Call(uintptr(w.hwnd))
		ok, _, _ = bitBlt.Call(mem, 0, 0, uintptr(cw), uintptr(ch), wdc, 0, 0, 0x00CC0020 /* SRCCOPY */)
		releaseDC.Call(uintptr(w.hwnd), wdc)
	}
	selectObj.Call(mem, old)
	if ok == 0 {
		return nil, fmt.Errorf("PrintWindow/BitBlt failed")
	}
	type bitmapInfoHeader struct {
		Size                          uint32
		Width, Height                 int32
		Planes, BitCount              uint16
		Compression, SizeImage        uint32
		XPels, YPels, ClrUsed, ClrImp int32
	}
	bi := bitmapInfoHeader{Width: int32(bw), Height: -int32(bh), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, bw*bh*4)
	if n, _, _ := getDIBits.Call(mem, bmp, 0, uintptr(bh), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); n == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	img := stdimage.NewRGBA(stdimage.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			i := ((y+oy)*bw + x + ox) * 4
			if i+3 >= len(buf) {
				continue
			}
			img.SetRGBA(x, y, color.RGBA{buf[i+2], buf[i+1], buf[i], 255})
		}
	}
	return img, nil
}

// On an architecture other than amd64 every constructor refuses with the same explanation
// (goarch stands in for runtime.GOARCH, so this runs on the x64 build too).
func TestConstructorsRefuseOtherArchitectures(t *testing.T) {
	r := newRes(t) // made before the architecture changes
	prev := goarch
	goarch = "arm64"
	defer func() { goarch = prev }()
	_, err1 := NewResources()
	_, err2 := NewOffscreen(r, 8, 8, 1, OffscreenOptions{})
	_, err3 := NewApp(AppOptions{Resources: r})
	for i, err := range []error{err1, err2, err3} {
		if !errors.Is(err, kit.ErrUnsupported) || !strings.Contains(err.Error(), "native window needs the x64 build of Downex (this is windows/arm64)") {
			t.Errorf("constructor %d: %v", i, err)
		}
	}
}

// The Host's window settings reach the window.
func TestWindowHostSettings(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })

	getText := user32.NewProc("GetWindowTextW")
	w.SetTitle("Downex — 3 downloading")
	buf := heapText[:] // GetWindowText of our own window runs its procedure: heap memory
	n, _, _ := getText.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if got := syscall.UTF16ToString(buf[:n]); got != "Downex — 3 downloading" {
		t.Errorf("title %q", got)
	}

	// The minimum size follows the DPI: WM_GETMINMAXINFO asks for it in pixels, with the
	// system's frame of the style around it (computed here, not with outerSize).
	w.SetMinSize(kit.Size{W: 300, H: 180})
	mmi := heapMMI
	*mmi = w32.MINMAXINFO{}
	w.proc(w32.WM_GETMINMAXINFO, 0, uintptr(unsafe.Pointer(mmi)))
	ppd := w.ppd()
	want := w32.Rect{Right: int32(300*ppd + 0.5), Bottom: int32(180*ppd + 0.5)}
	w32.AdjustWindowRectExForDpi(&want, w32.WS_OVERLAPPEDWINDOW|w32.WS_CLIPCHILDREN, w32.WS_EX_NOACTIVATE, w32.GetDpiForWindow(w.hwnd))
	if ow, oh := want.Right-want.Left, want.Bottom-want.Top; mmi.MinTrackSize.X != ow || mmi.MinTrackSize.Y != oh {
		t.Errorf("min track size %v, want %d×%d", mmi.MinTrackSize, ow, oh)
	}

	if env := w.Environment(); env != readEnvironment(w.dpi) || env.Scale.PixelsPerDIP != float32(w32.GetDpiForWindow(w.hwnd))/96 {
		t.Errorf("environment %+v", env)
	}
	// A setting broadcast that changes nothing sends no EnvironmentEvent.
	root.events = nil
	send(w, w32.WM_SETTINGCHANGE, 0, 0)
	for _, e := range root.events {
		if _, ok := e.(kit.EnvironmentEvent); ok {
			t.Errorf("EnvironmentEvent without a change: %+v", e)
		}
	}

	// Pointer capture ends by itself with the last button; losing it is a PointerCancel.
	px := func(d float32) int { return int(d * ppd) }
	root.onEvent = func(e kit.Event) bool {
		if p, ok := e.(kit.PointerEvent); ok && p.Action == kit.PointerDown {
			w.CapturePointer()
		}
		return true
	}
	send(w, w32.WM_LBUTTONDOWN, w32.MK_LBUTTON, lparam(px(20), px(20)))
	if !w.captured || w32.GetCapture() != w.hwnd {
		t.Fatal("CapturePointer did not capture")
	}
	send(w, w32.WM_MOUSEMOVE, w32.MK_LBUTTON, lparam(-px(30), px(20))) // outside the window, still ours
	send(w, w32.WM_LBUTTONUP, 0, lparam(-px(30), px(20)))
	if w.captured || w32.GetCapture() == w.hwnd {
		t.Error("capture did not end with the last button")
	}
	root.events = nil
	send(w, w32.WM_LBUTTONDOWN, w32.MK_LBUTTON, lparam(px(20), px(20)))
	send(w, w32.WM_CAPTURECHANGED, 0, 0) // something else took it
	root.onEvent = nil
	var cancel bool
	for _, e := range root.events {
		if p, ok := e.(kit.PointerEvent); ok && p.Action == kit.PointerCancel {
			cancel = true
		}
		if p, ok := e.(kit.PointerEvent); ok && p.Action == kit.PointerMove && p.Pos.X >= 0 {
			t.Errorf("a move outside while captured came at %v", p.Pos)
		}
	}
	if !cancel {
		t.Errorf("no PointerCancel after losing the capture: %v", root.events)
	}
	w32.ReleaseCapture()

	// The cursor the root sets is the one shown over the client area.
	w.SetCursor(kit.CursorText)
	if w.cursor != kit.CursorText || a.cursor(kit.CursorText) == a.cursor(kit.CursorDefault) {
		t.Error("SetCursor")
	}
	if send(w, w32.WM_SETCURSOR, uintptr(w.hwnd), w32.HTCLIENT) != 1 {
		t.Error("WM_SETCURSOR over the client area was not answered")
	}
	w.SetTextInput(kit.TextInput{Active: true, Caret: kit.R(20, 20, 21, 38)})
	w.SetTextInput(kit.TextInput{})
}

// A panic in a call into the root does not unwind through Win32: the window procedure
// returns and the loop raises it on the Go side.
func TestPanicInRootIsRaisedByTheLoop(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	root.onEvent = func(e kit.Event) bool {
		if _, ok := e.(kit.TextEvent); ok {
			panic("root bug")
		}
		return false
	}
	w32.PostMessage(w.hwnd, w32.WM_CHAR, 'z', 0)
	defer func() {
		v := recover()
		err, ok := v.(error)
		if !ok || !strings.Contains(err.Error(), "panic in a window procedure: root bug") {
			t.Fatalf("Run raised %v", v)
		}
		root.onEvent = nil
		if w.depth != 0 {
			t.Errorf("call depth %d after the panic", w.depth)
		}
	}()
	a.Run()
	t.Fatal("Run returned without raising the panic")
}

// Moving the window to a monitor of another DPI (this PC: 125% primary, 100% second at
// negative coordinates) keeps its size in DIPs and tells the root the new scale. The window
// is moved with SetWindowPos, without activation; the test skips without a second monitor
// of another DPI.
func TestWindowMovesBetweenMonitorDPIs(t *testing.T) {
	root := &testRoot{}
	a, w := openTest(t, root, WindowOptions{Size: kit.Size{W: 300, H: 180}})
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	primary := uintptr(w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY))
	var other uintptr
	for x := int32(-4000); x < 4000 && other == 0; x += 250 {
		for y := int32(-3000); y < 3000 && other == 0; y += 250 {
			if x >= 0 && y >= 0 {
				continue // the other monitor is at negative coordinates on this PC
			}
			if m := w32.MonitorFromPoint(w32.Point{X: x, Y: y}, 0 /* MONITOR_DEFAULTTONULL */); m != 0 && uintptr(m) != primary {
				other = uintptr(m)
			}
		}
	}
	if other == 0 {
		skipOrRequire(t, "no second monitor at negative coordinates: the DPI move is NOT verified")
	}
	mi, ok := w32.GetMonitorInfo(windows.Handle(other))
	if !ok {
		t.Skip("GetMonitorInfo failed")
	}
	dpi0, size0 := w.dpi, w.Size()
	wr := w32.GetWindowRect(w.hwnd)
	root.events = nil
	w32.SetWindowPos(w.hwnd, mi.Work.Left+40, mi.Work.Top+40, wr.Right-wr.Left, wr.Bottom-wr.Top, w32.SWP_NOZORDER|w32.SWP_NOACTIVATE)
	pumpFor(t, a, w, 500*time.Millisecond, func() bool { return w.dpi != dpi0 })
	if w.dpi == dpi0 {
		skipOrRequire(t, fmt.Sprintf("the monitor left of the primary has the same DPI (%d): the DPI move is NOT verified", dpi0))
	}
	t.Logf("moved from DPI %d to %d: size %v -> %v", dpi0, w.dpi, size0, w.Size())
	if w.Size() != size0 {
		t.Errorf("size in DIPs changed from %v to %v", size0, w.Size())
	}
	var env bool
	for _, e := range root.events {
		if ee, ok := e.(kit.EnvironmentEvent); ok && ee.Env.Scale.PixelsPerDIP == float32(w.dpi)/96 {
			env = true
		}
		if _, ok := e.(kit.SizeEvent); ok {
			t.Errorf("a SizeEvent for a DPI change that kept the size: %v", e)
		}
	}
	if !env {
		t.Errorf("no EnvironmentEvent with the new scale: %v", root.events)
	}
	paints := len(root.paints)
	pump(t, a, w, "the repaint at the new DPI", time.Second, func() bool { return len(root.paints) > paints })
	// And back.
	w32.SetWindowPos(w.hwnd, wr.Left, wr.Top, 0, 0, w32.SWP_NOZORDER|w32.SWP_NOACTIVATE|w32.SWP_NOSIZE)
	pumpFor(t, a, w, 500*time.Millisecond, func() bool { return w.dpi == dpi0 })
	if w.dpi != dpi0 || w.Size() != size0 {
		t.Errorf("back on the first monitor: DPI %d, size %v", w.dpi, w.Size())
	}
}
