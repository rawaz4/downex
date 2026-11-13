//go:build windows

package palette_test

import (
	"context"
	"fmt"
	"image"
	"os"
	"runtime"
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
	"downex/ui/views/palette"
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// TestRealWindowShrinkRepaint [e2e]: the palette in a real Win32 window drawn with Direct2D
// (shown without taking the focus, at 96 and 120 DPI, light and dark). The window keeps the
// pixels of its last frame and repaints only what was invalidated, so when the palette shrinks
// (every command, then "pause") the area its old card and shadow covered must be painted
// again: the window's pixels right after the shrink (copied from its device context) must equal
// those after a full repaint, outside the new card (the query field's caret blinks inside it).
// PrintWindow cannot show it: it makes the window repaint all of itself first (measured, and
// logged). It opens windows on the desktop, so it runs only with DOWNEX_PALETTE_REAL=1.
func TestRealWindowShrinkRepaint(t *testing.T) {
	if os.Getenv("DOWNEX_PALETTE_REAL") != "1" {
		t.Skip("set DOWNEX_PALETTE_REAL=1 to open a real window")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, dpi := range []uint32{96, 120} {
		for _, theme := range []string{"light", "dark"} {
			t.Run(fmt.Sprintf("%d-dpi-%s", dpi, theme), func(t *testing.T) { shrinkInRealWindow(t, dpi, theme) })
		}
	}
}

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	gdi32            = syscall.NewLazyDLL("gdi32.dll")
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
	procBitBlt       = gdi32.NewProc("BitBlt")
)

// grab is the window's client area: through PrintWindow (PW_CLIENTONLY |
// PW_RENDERFULLCONTENT) when print, which makes the window paint all of itself again first, or
// copied from the window's device context (BitBlt), the pixels it shows now.
func grab(hwnd uintptr, print bool) (*image.RGBA, error) {
	var rc struct{ L, T, R, B int32 }
	procGetClient.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := int(rc.R-rc.L), int(rc.B-rc.T)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("client %d x %d", w, h)
	}
	sdc, _, _ := procGetDC.Call(hwnd)
	defer procReleaseDC.Call(hwnd, sdc)
	mdc, _, _ := procCreateDC.Call(sdc)
	defer procDeleteDC.Call(mdc)
	bmp, _, _ := procCreateBitmap.Call(sdc, uintptr(w), uintptr(h))
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelect.Call(mdc, bmp)
	var ok uintptr
	var err error
	if print {
		ok, _, err = procPrintWindow.Call(hwnd, mdc, 1|2)
	} else {
		ok, _, err = procBitBlt.Call(mdc, 0, 0, uintptr(w), uintptr(h), sdc, 0, 0, 0x00CC0020) // SRCCOPY
	}
	procSelect.Call(mdc, old)
	if ok == 0 {
		return nil, fmt.Errorf("PrintWindow/BitBlt: %v", err)
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

func shrinkInRealWindow(t *testing.T, dpi uint32, themeName string) {
	fx, ok := scene.Fixture("palette")
	if !ok {
		t.Fatal("no fixture palette")
	}
	mock, err := scene.New(fx.Options())
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
	kw := kit.NewWindow(root, kit.WindowOptions{Title: "Downex palette gate", Palette: func(env kit.Environment) kit.Palette {
		if a == nil {
			return tokens.For(tokens.ChoiceLight, env).Palette()
		}
		return a.WidgetTheme.Palette(env)
	}})
	light := false
	a, err = views.NewApp(views.Options{Window: kw, Daemon: mock, Platform: sp, Main: main, Theme: theme, Density: state.NewDensity(storage),
		Storage: storage, Clock: state.SystemClock, Call: call, SystemDark: &light, StatusBarHeight: 32,
		Caps: host.Capabilities{CustomTitleBar: true, OpenWith: true}, Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	root.Theme = a.WidgetTheme

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wa, err := win.NewApp(win.AppOptions{ClassName: "DownexPaletteLaneGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	ww, err := wa.NewWindow(kw, win.WindowOptions{Title: "Downex palette gate", Size: kit.Size{W: 1000, H: 700}, NoActivate: true, CustomFrame: true, ForceDPI: dpi})
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
	var report []string
	go func() {
		defer func() { _ = kw.Post(kw.Close) }()
		main.Start()
		main.Downloads.RefreshStatus()
		main.LoadSettings()
		time.Sleep(time.Second)
		var p *palette.Palette
		onUI(func() {
			p = palette.New(a, palette.Options{})
			a.Dialogs.Show(p)
		})
		time.Sleep(1500 * time.Millisecond) // the open motion
		var before, after kit.Rect
		var reach kit.DIP
		onUI(func() {
			before = p.Card().WindowBounds()
			reach = kit.ShadowsOverflow(views.TokensOf(p.Card()).ShadowLg)
			p.SetQuery("pause")
		})
		time.Sleep(time.Second)
		onUI(func() { after = p.Card().WindowBounds() })
		if after.Height() >= before.Height() {
			t.Errorf("the palette did not shrink: %v -> %v", before, after)
			return
		}
		// What the window shows now; then PrintWindow, which repaints the whole window
		// before it copies it (so it cannot show what an incremental frame left behind),
		// and the window's pixels after that full repaint.
		got, err := grab(hwnd, false)
		if err != nil {
			t.Error(err)
			return
		}
		var s0, s1 kit.Stats
		var size kit.Size
		onUI(func() { s0, size = kw.Stats(), kw.Size() })
		printed, err := grab(hwnd, true)
		if err != nil {
			t.Error(err)
			return
		}
		time.Sleep(500 * time.Millisecond)
		onUI(func() { s1 = kw.Stats() })
		report = append(report, fmt.Sprintf("PrintWindow repainted %.0f of %.0f square DIPs", s1.PaintedArea-s0.PaintedArea, float64(size.W*size.H)))
		want, err := grab(hwnd, false)
		if err != nil {
			t.Error(err)
			return
		}
		px := func(v kit.DIP) int { return int(v * kit.DIP(scale)) }
		// The old card and its shadow, less the new card's box.
		area := before.Inset(kit.UniformInsets(-reach - 1)).Intersect(kit.RectAt(kit.Point{}, kit.Size{W: kit.DIP(got.Bounds().Dx()) / kit.DIP(scale), H: kit.DIP(got.Bounds().Dy()) / kit.DIP(scale)}))
		inner := image.Rect(px(after.Min.X)-1, px(after.Min.Y)-1, px(after.Max.X)+2, px(after.Max.Y)+2)
		n, worst, first := 0, 0, image.Point{-1, -1}
		for y := px(area.Min.Y); y < px(area.Max.Y); y++ {
			for x := px(area.Min.X); x < px(area.Max.X); x++ {
				if (image.Point{x, y}).In(inner) {
					continue
				}
				a, b := got.RGBAAt(x, y), want.RGBAAt(x, y)
				d := max(absDiff(a.R, b.R), absDiff(a.G, b.G), absDiff(a.B, b.B))
				if d > 2 {
					if n == 0 {
						first = image.Pt(x, y)
					}
					n++
					worst = max(worst, d)
				}
			}
		}
		report = append(report, fmt.Sprintf("palette %v -> %v (shadow reach %.0f DIP): %d pixels outside the new card differ from a full repaint, worst %d, first at %v", before, after, reach, n, worst, first))
		if n > 0 {
			t.Errorf("%d pixels outside the new card kept the old palette (worst %d levels, first at %v px): %v -> %v", n, worst, first, before, after)
		}
		if dir := os.Getenv("DOWNEX_PALETTE_SHOTS"); dir != "" {
			name := fmt.Sprintf("palette-shrink-%s-%d", themeName, dpi)
			_ = viewstest.WritePNG(dir+`\`+name+"-incremental.png", got)
			_ = viewstest.WritePNG(dir+`\`+name+"-full.png", want)
			_ = viewstest.WritePNG(dir+`\`+name+"-printwindow.png", printed)
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

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}
