//go:build windows

package add

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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

// TestRealWindowURLResizer shows Add files in a real Win32 window (Direct2D, the URL list a
// native multi-line field, shown without taking the focus) at 96 and 120 DPI, light and dark,
// and checks the resizer as today's WebView2 draws it (owner-compare 08: a dark stroke with a
// light one under it, ending 2.5 px inside the padding box), then drags it with posted mouse
// messages (no real input): press on it, move 40 px down, release. The list is 40 px taller,
// its native field follows, and the dialog grew. It opens windows on the desktop, so it runs
// only with DOWNEX_ADD_REAL=1.
func TestRealWindowURLResizer(t *testing.T) {
	if os.Getenv("DOWNEX_ADD_REAL") != "1" {
		t.Skip("set DOWNEX_ADD_REAL=1 to open a real window")
	}
	if err := viewstest.CanRender(); err != nil {
		t.Skip(err)
	}
	for _, dpi := range []uint32{96, 120} {
		for _, theme := range []string{"light", "dark"} {
			t.Run(fmt.Sprintf("%d-dpi-%s", dpi, theme), func(t *testing.T) { realResizer(t, dpi, theme) })
		}
	}
}

func realResizer(t *testing.T, dpi uint32, themeName string) {
	fx, ok := scene.Fixture("add-files")
	if !ok {
		fx, _ = scene.Fixture("")
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
	kw := kit.NewWindow(root, kit.WindowOptions{Title: "Downex resizer gate", Palette: func(env kit.Environment) kit.Palette {
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
	wa, err := win.NewApp(win.AppOptions{ClassName: "DownexAddResizerGate"})
	if err != nil {
		t.Fatal(err)
	}
	defer wa.Close()
	ww, err := wa.NewWindow(kw, win.WindowOptions{Title: "Downex resizer gate", Size: kit.Size{W: 1200, H: 760}, NoActivate: true, CustomFrame: true, ForceDPI: dpi})
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
		var add *Add
		onUI(func() {
			add = New(a, views.AddRequest{}, Options{Defaults: &Defaults{Dir: `C:\Users\you\Downloads`, MaxConnections: 32}})
			a.Dialogs.Show(add)
		})
		time.Sleep(1500 * time.Millisecond) // the open motion
		var box, field, card kit.Rect
		native := false
		onUI(func() {
			box, card = add.urlsArea.WindowBounds(), add.Card().WindowBounds()
			field = add.urlsArea.ContentRect().Add(box.Min)
			native = add.urlsArea.Native() != nil
		})
		if !native {
			t.Error("the URL list has no native field")
		}
		img, err := printWindow(hwnd)
		if err != nil {
			t.Error(err)
			return
		}
		// The resizer: the padding box's right column and bottom row (device pixels), the
		// strokes' right column 2.5 px and bottom row 2.5 px inside them.
		snap := func(d kit.DIP) int { return int(d*kit.DIP(scale) + 0.5) }
		right, bottom := snap(box.Max.X)-max(1, int(scale)), snap(box.Max.Y)-max(1, int(scale))
		col, row := int(float32(right)-2.5*scale), int(float32(bottom)-2.5*scale)
		ground := img.RGBAAt(col-px(6), row-px(6)) // the field, above the long stroke's middle
		ink := func(x, y int) int {
			c := img.RGBAAt(x, y)
			return (int(c.R) + int(c.G) + int(c.B) - int(ground.R) - int(ground.G) - int(ground.B)) / 3
		}
		// the dark stroke's bottom-right end (light: darker than the ground); the light stroke
		// under the long stroke's middle (dark: lighter)
		long := int(6*scale + 0.5)
		if themeName == "light" {
			if d := -maxInk(func(x, y int) int { return -ink(x, y) }, col-long/2, row-long/2, 2); d > -60 {
				t.Errorf("no dark stroke at %d,%d (%d against the ground %v)", col-long/2, row-long/2, d, ground)
			}
		} else if d := maxInk(ink, col-long/2, row-long/2, 3); d < 60 {
			t.Errorf("no light stroke near %d,%d (%d against the ground %v)", col-long/2, row-long/2, d, ground)
		}
		crop := img.SubImage(image.Rect(right-px(24), bottom-px(24), right+px(6), bottom+px(6)))
		_ = viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), viewstest.NativeName("add", "realwindow-resizer", themeName, 1200, float64(scale))), crop)

		// the drag, by posted messages: press on the resizer, move 40 px down, release
		gx, gy := px(box.Max.X-6), px(box.Max.Y-5)
		procPostMessage.Call(hwnd, wmMouseMove, 0, lparam(gx, gy))
		procPostMessage.Call(hwnd, wmLButtonDown, mkLButton, lparam(gx, gy))
		for i := 1; i <= 4; i++ {
			procPostMessage.Call(hwnd, wmMouseMove, mkLButton, lparam(gx, gy+px(kit.DIP(10*i))))
		}
		procPostMessage.Call(hwnd, wmLButtonUp, 0, lparam(gx, gy+px(40)))
		time.Sleep(600 * time.Millisecond)
		var box2, field2, card2 kit.Rect
		var placed kit.Rect
		onUI(func() {
			box2, card2 = add.urlsArea.WindowBounds(), add.Card().WindowBounds()
			field2 = add.urlsArea.ContentRect().Add(box2.Min)
			placed = field2
		})
		grown := box2.Height() - box.Height()
		if grown < 39 || grown > 41 {
			t.Errorf("dragged 40 px: the list grew %.2f px (%v → %v)", grown, box, box2)
		}
		if g := card2.Height() - card.Height(); g < grown-1 || g > grown+1 {
			t.Errorf("the dialog grew %.2f px around a list %.2f px taller", g, grown)
		}
		if field2.Height()-field.Height() < grown-1 {
			t.Errorf("the native field did not follow: %v → %v", field, field2)
		}
		after, err := printWindow(hwnd)
		if err == nil {
			b := image.Rect(px(box2.Min.X)-px(8), px(box2.Min.Y)-px(8), px(box2.Max.X)+px(8), px(box2.Max.Y)+px(8))
			_ = viewstest.WritePNG(filepath.Join(viewstest.ShotDir(), viewstest.NativeName("add", "realwindow-resized", themeName, 1200, float64(scale))), after.SubImage(b))
		}
		report = append(report, fmt.Sprintf("%d dpi %s: list %.2f → %.2f px, dialog %.2f → %.2f, field %v", dpi, themeName, box.Height(), box2.Height(), card.Height(), card2.Height(), placed))
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

// maxInk is the largest ink in the square of radius r around x, y.
func maxInk(ink func(x, y int) int, x, y, r int) int {
	best := -1 << 30
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			best = max(best, ink(x+dx, y+dy))
		}
	}
	return best
}
