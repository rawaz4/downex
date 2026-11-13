//go:build windows

package queues

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"

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

// TestRealWindow shows the dialog in a real Win32 window drawn with Direct2D and DirectWrite
// (a custom frame, shown without taking the focus, at 96 and 120 DPI), over the queues fixture
// scene with the wall clock: an order arrow and a queue row are clicked with posted mouse
// messages, the window's pixels (PrintWindow) are compared with the reference of the same
// state, and an idle dialog must not paint. It opens windows on the desktop, so it runs only
// with DOWNEX_QUEUES_REAL=1.
func TestRealWindow(t *testing.T) {
	if os.Getenv("DOWNEX_QUEUES_REAL") != "1" {
		t.Skip("set DOWNEX_QUEUES_REAL=1 to open a real window")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, dpi := range []uint32{96, 120} {
		for _, theme := range []string{"light", "dark"} {
			t.Run(fmt.Sprintf("%d-dpi-%s", dpi, theme), func(t *testing.T) { realWindow(t, dpi, theme) })
		}
	}
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	gdi32            = syscall.NewLazyDLL("gdi32.dll")
	procPostMessage  = user32.NewProc("PostMessageW")
	procPrintWindow  = user32.NewProc("PrintWindow")
	procGetDC        = user32.NewProc("GetDC")
	procReleaseDC    = user32.NewProc("ReleaseDC")
	procGetClient    = user32.NewProc("GetClientRect")
	procCreateDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelect       = gdi32.NewProc("SelectObject")
	procGetDIBits    = gdi32.NewProc("GetDIBits")
	procDeleteObject = gdi32.NewProc("DeleteObject")
	procDeleteDC     = gdi32.NewProc("DeleteDC")
)

const (
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	mkLButton     = 0x0001
)

// postClick posts a left click at a client point (physical pixels) to the window: no real
// input, no focus taken.
func postClick(hwnd uintptr, x, y int) {
	lp := uintptr(uint32(y)<<16 | uint32(x)&0xffff)
	procPostMessage.Call(hwnd, wmMouseMove, 0, lp)
	procPostMessage.Call(hwnd, wmLButtonDown, mkLButton, lp)
	procPostMessage.Call(hwnd, wmLButtonUp, 0, lp)
}

// printWindow is the window's client area as the compositor has it (PW_CLIENTONLY |
// PW_RENDERFULLCONTENT).
func printWindow(hwnd uintptr) (*image.RGBA, error) {
	var rc struct{ L, T, R, B int32 }
	procGetClient.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.R-rc.L), int(rc.B-rc.T)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("client %d × %d", w, h)
	}
	sdc, _, _ := procGetDC.Call(hwnd)
	defer procReleaseDC.Call(hwnd, sdc)
	mdc, _, _ := procCreateDC.Call(sdc)
	defer procDeleteDC.Call(mdc)
	bmp, _, _ := procCreateBitmap.Call(sdc, uintptr(w), uintptr(h))
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelect.Call(mdc, bmp)
	ok, _, err := procPrintWindow.Call(hwnd, mdc, 1|2)
	procSelect.Call(mdc, old)
	if ok == 0 {
		return nil, fmt.Errorf("PrintWindow: %v", err)
	}
	type bitmapInfoHeader struct {
		Size                         uint32
		Width, Height                int32
		Planes, BitCount             uint16
		Compression, SizeImage       uint32
		XPels, YPels, ClrUsed, ClrIm int32
	}
	bi := bitmapInfoHeader{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	buf := make([]byte, w*h*4)
	if n, _, err := procGetDIBits.Call(mdc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); n == 0 {
		return nil, fmt.Errorf("GetDIBits: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = buf[4*i+2], buf[4*i+1], buf[4*i], 255
	}
	return img, nil
}

func realWindow(t *testing.T, dpi uint32, themeName string) {
	fx, ok := scene.Fixture("queues-main")
	if !ok {
		t.Fatal("no fixture queues-main")
	}
	so := fx.Options()
	mock, err := scene.New(so)
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	sp := &viewstest.ScenePlatform{Mock: mock}
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}
	main := state.NewMain(state.MainOptions{Daemon: mock, Events: mock, Files: sp, CopyText: sp.CopyText,
		Clock: state.SystemClock, Frames: state.TimerFrames{Clock: state.SystemClock}, Call: call})
	storage := statetest.NewMemory(nil)
	theme := state.NewTheme(state.ThemeOptions{Storage: storage})
	choice := host.ThemeLight
	if themeName == "dark" {
		choice = host.ThemeDark
	}
	theme.Set(choice)
	var a *views.App
	root := widgets.NewScope(nil)
	root.Style = kit.Style{Display: kit.DisplayBlock, Width: kit.Pct(100), Height: kit.Pct(100)}
	kw := kit.NewWindow(root, kit.WindowOptions{Title: "Downex queues gate", Palette: func(env kit.Environment) kit.Palette {
		if a == nil {
			return tokens.For(tokens.ChoiceLight, env).Palette()
		}
		return a.WidgetTheme.Palette(env)
	}})
	light := false
	a, err = views.NewApp(views.Options{Window: kw, Daemon: mock, Platform: sp, Main: main, Theme: theme, Density: state.NewDensity(storage),
		Storage: storage, Clock: state.SystemClock, Call: call, SystemDark: &light, StatusBarHeight: 32,
		Caps: host.Capabilities{CustomTitleBar: true, OpenWith: true, Hibernate: true, WakeTimers: true}, Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	root.Theme = a.WidgetTheme

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wa, err := win.NewApp(win.AppOptions{ClassName: "DownexQueuesLaneGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	ww, err := wa.NewWindow(kw, win.WindowOptions{Title: "Downex queues gate", Size: kit.Size{W: 1200, H: 760}, NoActivate: true, CustomFrame: true, ForceDPI: dpi})
	if err != nil {
		t.Fatal(err)
	}
	hwnd := uintptr(ww.HWND())
	onUI := func(f func()) {
		done := make(chan struct{})
		if kw.Post(func() { f(); close(done) }) != nil {
			return
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	scale := float32(dpi) / 96
	center := func(e *kit.Element) (int, int) {
		var p kit.Point
		onUI(func() { p = e.WindowBounds().Center() })
		return int(p.X * kit.DIP(scale)), int(p.Y * kit.DIP(scale))
	}
	var report []string
	go func() {
		defer func() { _ = kw.Post(kw.Close) }()
		main.Start()
		main.Downloads.RefreshStatus()
		main.Queues.Load()
		main.LoadSettings()
		time.Sleep(time.Second)
		var d *Dialog
		onUI(func() {
			d = New(a, views.QueuesRequest{})
			a.Dialogs.Show(d)
		})
		time.Sleep(1500 * time.Millisecond) // the open motion

		// an order arrow, clicked with posted messages, reaches the daemon (the scene)
		var before []int64
		var second *orderRow
		onUI(func() {
			before = append([]int64(nil), d.order.ids...)
			if len(before) > 1 {
				second = d.order.rows[before[1]]
			}
		})
		if second == nil {
			t.Error("no second row in the order")
			return
		}
		x, y := center(&second.up.Element)
		postClick(hwnd, x, y)
		time.Sleep(time.Second)
		var after []int64
		onUI(func() {
			if q, ok := main.Queues.Get(1); ok {
				after = q.GetDownloadIds()
			}
		})
		if len(after) < 2 || after[0] != before[1] || !slices.Contains(after, before[0]) {
			t.Errorf("after the Up arrow, the main queue is %v (it was %v)", after, before)
		}
		report = append(report, fmt.Sprintf("Up arrow posted at (%d, %d): the main queue starts %v (was %v)", x, y, after[:min(3, len(after))], before[:min(3, len(before))]))

		// the Night row, clicked: the queues-night state
		var night *queueRow
		onUI(func() {
			for _, r := range d.qlist.rows {
				if r.name.Text() == "Night" {
					night = r
				}
			}
		})
		x, y = center(&night.Element)
		postClick(hwnd, x, y)
		postClick(hwnd, 2, 400) // the pointer away from the rows (the reference has no hover)
		time.Sleep(1500 * time.Millisecond)
		var heading string
		var card kit.Rect
		onUI(func() { heading, card = d.head.h3.Text(), d.Card().WindowBounds() })
		if heading != "Night" {
			t.Errorf("heading %q after the Night row was clicked", heading)
		}
		img, err := printWindow(hwnd)
		if err != nil {
			t.Error(err)
			return
		}
		refs, err := viewstest.LoadRefs()
		if err != nil {
			report = append(report, "no references: "+err.Error())
		} else if shot, ok := refs.Find("state", "queues-night", 1200, 760, float64(scale), 16, themeName); ok {
			ref, err := refs.Image(shot)
			if err != nil {
				t.Error(err)
				return
			}
			delta := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})[0]
			if delta.Max() > 1 {
				t.Errorf("real window: %v", delta)
			}
			c := viewstest.Compare(ref, img, delta.Ref, card.Min, scale)
			gr, gn := grayOf(ref), grayOf(img)
			px := func(v kit.DIP) int { return int(v * kit.DIP(scale)) }
			var parts []string
			onUI(func() {
				for _, pr := range regionProbes(d) {
					if pr.e.Style.Hidden {
						continue
					}
					b := pr.e.WindowBounds().Intersect(card)
					if b.Height() < 16 {
						continue
					}
					dx, dy, _ := shiftOf(gr, gn, image.Rect(px(b.Min.X), px(b.Min.Y), px(b.Max.X), px(b.Max.Y)), 4)
					parts = append(parts, fmt.Sprintf("%s %+.1f,%+.1f", pr.name, float64(dx)/float64(scale), float64(dy)/float64(scale)))
				}
			})
			name := viewstest.NativeName("queues", "night-realwindow", themeName, 1200, float64(scale))
			if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), name), c.Sheet); err != nil {
				t.Error(err)
			}
			report = append(report, fmt.Sprintf("queues-night in the real window: %v; mean %.2f, %.1f%% differ; parts (DIP) %v; sheet %s", delta, c.Mean, 100*c.Differing, parts, name))
		}

		var s0, s1 kit.Stats
		onUI(func() { s0 = kw.Stats() })
		time.Sleep(5 * time.Second)
		onUI(func() { s1 = kw.Stats() })
		report = append(report, fmt.Sprintf("idle 5 s: %d paints, %d frames, %d layouts", s1.Paints-s0.Paints, s1.Frames-s0.Frames, s1.Layouts-s0.Layouts))
		if s1.Paints != s0.Paints || s1.Layouts != s0.Layouts {
			t.Errorf("an idle dialog worked: %d paints, %d layouts", s1.Paints-s0.Paints, s1.Layouts-s0.Layouts)
		}
	}()
	if err := wa.Run(); err != nil {
		t.Fatal(err)
	}
	a.Dispose()
	main.Dispose()
	for _, l := range report {
		t.Log(l)
	}
}
