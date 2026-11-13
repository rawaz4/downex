//go:build windows

package add

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
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
	"downex/ui/views/viewstest"
	"downex/ui/win"
)

// TestRealWindowAddress shows the Confirm dialog of a web page's video in a real Win32 window
// drawn with Direct2D and DirectWrite (a custom frame, shown without taking the focus, at 96
// and 120 DPI, light and dark), and compares the card's page address (after "YouTube ·") with
// the reference of the same state: its box, that its text is drawn, and the pixels of the
// address. Then posted mouse messages (no real input) put the caret with a press and select a
// word with a double press, and a posted End key moves the caret. It opens windows on the
// desktop, so it runs only with DOWNEX_ADD_REAL=1.
func TestRealWindowAddress(t *testing.T) {
	if os.Getenv("DOWNEX_ADD_REAL") != "1" {
		t.Skip("set DOWNEX_ADD_REAL=1 to open a real window")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, st := range []string{"confirm-video-1", "confirm-video-6-longurl"} {
		for _, dpi := range []uint32{96, 120} {
			for _, theme := range []string{"light", "dark"} {
				t.Run(fmt.Sprintf("%s-%d-dpi-%s", st, dpi, theme), func(t *testing.T) { realAddress(t, st, dpi, theme) })
			}
		}
	}
}

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	gdi32           = syscall.NewLazyDLL("gdi32.dll")
	procPostMessage = user32.NewProc("PostMessageW")
	procPrintWindow = user32.NewProc("PrintWindow")
	procGetDC       = user32.NewProc("GetDC")
	procReleaseDC   = user32.NewProc("ReleaseDC")
	procGetClient   = user32.NewProc("GetClientRect")
	procCreateDC    = gdi32.NewProc("CreateCompatibleDC")
	procCreateBmp   = gdi32.NewProc("CreateCompatibleBitmap")
	procSelect      = gdi32.NewProc("SelectObject")
	procGetDIBits   = gdi32.NewProc("GetDIBits")
	procDeleteObj   = gdi32.NewProc("DeleteObject")
	procDeleteDC    = gdi32.NewProc("DeleteDC")
)

const (
	wmKeyDown       = 0x0100
	wmKeyUp         = 0x0101
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	mkLButton       = 0x0001
	vkEnd           = 0x23
)

func lparam(x, y int) uintptr { return uintptr(uint32(y)<<16 | uint32(x)&0xffff) }

// postPress posts a left press and release at a client point (physical pixels); double
// follows it with the system's double-click message, as a second press in time does.
func postPress(hwnd uintptr, x, y int, double bool) {
	lp := lparam(x, y)
	procPostMessage.Call(hwnd, wmMouseMove, 0, lp)
	procPostMessage.Call(hwnd, wmLButtonDown, mkLButton, lp)
	procPostMessage.Call(hwnd, wmLButtonUp, 0, lp)
	if double {
		procPostMessage.Call(hwnd, wmLButtonDblClk, mkLButton, lp)
		procPostMessage.Call(hwnd, wmLButtonUp, 0, lp)
	}
}

func postKey(hwnd uintptr, vk uintptr) {
	procPostMessage.Call(hwnd, wmKeyDown, vk, 1)
	procPostMessage.Call(hwnd, wmKeyUp, vk, 1|3<<30)
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
	bmp, _, _ := procCreateBmp.Call(sdc, uintptr(w), uintptr(h))
	defer procDeleteObj.Call(bmp)
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

// inkColumns counts the columns of r (physical pixels) holding a pixel that differs from the
// row's first by more than 40 in a channel: how much text is drawn there.
func inkColumns(img *image.RGBA, r image.Rectangle) int {
	r = r.Intersect(img.Bounds())
	n := 0
	for x := r.Min.X; x < r.Max.X; x++ {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			g, b := img.RGBAAt(r.Min.X, y), img.RGBAAt(x, y)
			if absInt(int(g.R)-int(b.R)) > 40 || absInt(int(g.G)-int(b.G)) > 40 || absInt(int(g.B)-int(b.B)) > 40 {
				n++
				break
			}
		}
	}
	return n
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func realAddress(t *testing.T, name string, dpi uint32, themeName string) {
	fx, ok := scene.Fixture(name)
	if !ok {
		t.Fatalf("no fixture %s", name)
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
	kw := kit.NewWindow(root, kit.WindowOptions{Title: "Downex add gate", Palette: func(env kit.Environment) kit.Palette {
		if a == nil {
			return tokens.For(tokens.ChoiceLight, env).Palette()
		}
		return a.WidgetTheme.Palette(env)
	}})
	light := false
	a, err = views.NewApp(views.Options{Window: kw, Daemon: mock, Platform: sp, Main: main, Theme: theme, Density: state.NewDensity(storage),
		Storage: storage, Clock: state.SystemClock, Call: call, SystemDark: &light, StatusBarHeight: 32,
		Caps: host.Capabilities{CustomTitleBar: true}, Demo: true})
	if err != nil {
		t.Fatal(err)
	}
	root.Theme = a.WidgetTheme

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wa, err := win.NewApp(win.AppOptions{ClassName: "DownexAddLaneGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	ww, err := wa.NewWindow(kw, win.WindowOptions{Title: "Downex add gate", Size: kit.Size{W: 1200, H: 760}, NoActivate: true, CustomFrame: true, ForceDPI: dpi})
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
	px := func(v kit.DIP) int { return int(v * kit.DIP(scale)) }
	var report []string
	go func() {
		defer func() { _ = kw.Post(kw.Close) }()
		main.Start()
		main.Downloads.RefreshStatus()
		main.Queues.Load()
		main.LoadSettings()
		time.Sleep(time.Second)
		var c *Confirm
		onUI(func() {
			awaiting := main.Downloads.Awaiting()
			if len(awaiting) == 0 {
				return
			}
			c = NewConfirm(a, awaiting[0], ConfirmOptions{Waiting: len(awaiting)})
			a.Dialogs.Show(c)
		})
		if c == nil {
			t.Error("nothing awaits confirmation")
			return
		}
		time.Sleep(1500 * time.Millisecond) // the open motion and the guard
		var box, card kit.Rect
		var value string
		onUI(func() {
			box, card, value = c.Video.Address.WindowBounds(), c.Card().WindowBounds(), c.Video.Address.Value()
		})
		img, err := printWindow(hwnd)
		if err != nil {
			t.Error(err)
			return
		}
		// the address's text is drawn (an EDIT drew nothing in a WIC render)
		inner := image.Rect(px(box.Min.X)+px(3), px(box.Min.Y)+1, px(box.Max.X)-px(3), px(box.Max.Y)-1)
		ink := inkColumns(img, inner)
		if ink < inner.Dx()/4 {
			t.Errorf("the address shows %d inked columns of %d", ink, inner.Dx())
		}
		line := fmt.Sprintf("%s: address %q at %.2f,%.2f %.2fx%.2f DIP, %d of %d columns inked", name, value, box.Min.X, box.Min.Y, box.Width(), box.Height(), ink, inner.Dx())

		refs, rerr := viewstest.LoadRefs()
		shots, berr := loadRefBoxes()
		if rerr != nil || berr != nil {
			report = append(report, line, fmt.Sprintf("no references: %v %v", rerr, berr))
		} else if shot, ok := refs.Find("state", name, 1200, 760, float64(scale), 16, themeName); ok {
			var refBox kit.Rect
			found := false
			for _, s := range shots {
				if s.Name != name || s.CSS.W != 1200 || s.CSS.H != 760 || s.Scale != scale || s.Font != 16 || s.Layout == nil {
					continue
				}
				for _, n := range s.Layout.Nodes {
					if n.tag() == "input" && n.has("address") {
						refBox, found = n.rect(), true
					}
				}
			}
			ref, err := refs.Image(shot)
			if err != nil || !found {
				t.Errorf("reference: %v (address box found %v)", err, found)
				return
			}
			dialog := viewstest.BoxDeltas(shot, []viewstest.Probe{{Name: "dialog", Dialog: true, Native: card}})[0]
			d := boxDelta{what: "address", ref: refBox, native: box}
			if d.worst() > 1 || dialog.Max() > 1 {
				t.Errorf("real window: %v; %v", d, dialog)
			}
			area := kit.R(refBox.Min.X-6, refBox.Min.Y-4, refBox.Max.X+6, refBox.Max.Y+4)
			at := kit.Pt(box.Min.X-6, box.Min.Y-4)
			cmp := viewstest.Compare(ref, img, area, at, scale)
			dx, dy, best, unshifted := viewstest.BestShift(ref, img, area, at, scale, 3)
			sheet := viewstest.NativeName("add", "realwindow-address-"+name, themeName, 1200, float64(scale))
			if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), sheet), cmp.Sheet); err != nil {
				t.Error(err)
			}
			whole := viewstest.Compare(ref, img, dialog.Ref, card.Min, scale)
			wsheet := viewstest.NativeName("add", "realwindow-"+name, themeName, 1200, float64(scale))
			if err := viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), wsheet), whole.Sheet); err != nil {
				t.Error(err)
			}
			report = append(report, line, fmt.Sprintf("  %v; dialog %v; address pixels mean %.2f, %.1f%% differ, best shift %d,%d px (%.2f vs %.2f unshifted); sheets %s, %s (dialog mean %.2f)",
				d, dialog, cmp.Mean, 100*cmp.Differing, dx, dy, best, unshifted, sheet, wsheet, whole.Mean))
		}

		// a press puts the caret, a double press selects a word, End moves the caret
		cx, cy := px(box.Min.X+40), px(box.Center().Y)
		postPress(hwnd, cx, cy, false)
		time.Sleep(300 * time.Millisecond)
		var focused bool
		var lo, hi int
		onUI(func() { focused = c.Video.Address.Focused(); lo, hi = c.Video.Address.Selection() })
		if !focused || lo != hi {
			t.Errorf("after a press: focused %v, selection %d..%d", focused, lo, hi)
		}
		time.Sleep(time.Second) // past the double-click time: the next press is a first one
		postPress(hwnd, cx, cy, true)
		time.Sleep(300 * time.Millisecond)
		var word string
		onUI(func() { word = c.Video.Address.SelectedText() })
		if word == "" {
			t.Error("a double press selected nothing")
		}
		sel, err := printWindow(hwnd)
		if err == nil {
			crop := sel.SubImage(image.Rect(px(box.Min.X)-8, px(box.Min.Y)-6, px(box.Max.X)+8, px(box.Max.Y)+6))
			_ = viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), viewstest.NativeName("add", "realwindow-address-selected-"+name, themeName, 1200, float64(scale))), crop)
		}
		postKey(hwnd, vkEnd)
		time.Sleep(300 * time.Millisecond)
		onUI(func() { lo, hi = c.Video.Address.Selection() })
		if lo != len(value) || hi != len(value) {
			t.Errorf("after End: selection %d..%d, want the end %d", lo, hi, len(value))
		}
		report = append(report, fmt.Sprintf("  posted press: focused %v; double press selected %q; End put the caret at %d of %d", focused, word, hi, len(value)))

		var s0, s1 kit.Stats
		onUI(kw.Blur)
		time.Sleep(300 * time.Millisecond)
		onUI(func() { s0 = kw.Stats() })
		time.Sleep(3 * time.Second)
		onUI(func() { s1 = kw.Stats() })
		report = append(report, fmt.Sprintf("  idle 3 s: %d paints, %d layouts", s1.Paints-s0.Paints, s1.Layouts-s0.Layouts))
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
