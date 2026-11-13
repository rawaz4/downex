//go:build windows

package win

import (
	"runtime"
	"runtime/debug"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/widgets"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/w32"

	"golang.org/x/sys/windows"
)

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

// privateBytes is the process's commit (PROCESS_MEMORY_COUNTERS_EX.PrivateUsage), after Go
// gave back what it freed.
func privateBytes() int64 {
	runtime.GC()
	debug.FreeOSMemory()
	var pm struct {
		cb                                                                       uint32
		faults                                                                   uint32
		peakWS, ws, quotaPeakPaged, quotaPaged, quotaPeakNonPaged, quotaNonPaged uintptr
		pagefile, peakPagefile, private                                          uintptr
	}
	pm.cb = uint32(unsafe.Sizeof(pm))
	procGetProcessMemoryInfo.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&pm)), uintptr(pm.cb))
	return int64(pm.private)
}

// A popup of a window that draws on the GPU draws on the GPU too, and its memory goes with it.
// Its targets were Direct2D's software renderer (WARP), which kept 10-13 MB of commit once the
// first menu had closed (the main window at 33.9 MB after one walk through the dialogs and
// menus, 13.2 MB fresh): checked here on the real targets and the process's real commit.
func TestPopupMemoryGoesWithIt(t *testing.T) {
	trigger := widgets.NewButton("Unlimited", "gauge", widgets.Secondary, widgets.SizeMd, nil)
	mb := widgets.NewMenuButton(trigger, "Global speed limit", []widgets.MenuItem{
		{ID: "0", Label: "Unlimited", Kind: widgets.ItemRadio, Checked: true, Heading: "Global speed limit"},
		{ID: "20", Label: "20 MiB/s", Kind: widgets.ItemRadio},
		{ID: "10", Label: "10 MiB/s", Kind: widgets.ItemRadio},
		{ID: "custom", Label: "Custom…", Separated: true},
	}, func(string) {})
	mb.Footnote = "Per-download and queue limits still apply."
	ww := openWidgets(t, "DownexPopupMemoryTest", false, kit.Size{W: 480, H: 420}, mb)
	a, w := ww.a, ww.w
	if w.Renderer() != "hardware" {
		t.Skipf("the window draws with the %q renderer: its popups do too", w.Renderer())
	}
	open := func() *popupWindow {
		w.call(func() { mb.OpenMenu(widgets.FocusFirst) })
		pump(t, a, w, "the menu's frames", 2*time.Second, func() bool { ps := popupsOf[w]; return len(ps) == 1 && ps[0].paints > 0 })
		p := ww.popup(t)
		settle(t, a, w, p)
		return p
	}
	shut := func() {
		w.call(func() { mb.CloseMenu(false) })
		pump(t, a, w, "the menu's close", 2*time.Second, func() bool { return len(popupsOf[w]) == 0 })
	}

	before := privateBytes()
	p := open()
	hw := d2d.RenderTargetProperties{Type: d2d.RENDER_TARGET_TYPE_HARDWARE}
	if p.dcrt == nil || !p.dcrt.IsSupported(&hw) {
		t.Error("the popup's target is not drawn on the GPU")
	}
	if p.ctRT == nil || !p.ctRT.IsSupported(&hw) {
		t.Error("the popup's ClearType pass is not drawn on the GPU")
	}
	// the box still draws: opaque --raised inside it
	box := mb.Menu().Bounds()
	at := box.Min.Sub(p.bounds.Min)
	if c := p.dibAt(int((at.X+8)*p.ppd), int((at.Y+4)*p.ppd)); c.A != 255 || c.R != 0xff || c.G != 0xff || c.B != 0xff {
		t.Errorf("inside the box %v, want opaque #ffffff (--raised)", c)
	}
	shut()
	for range 2 {
		open()
		shut()
	}
	after := privateBytes()
	grown := float64(after-before) / (1 << 20)
	t.Logf("commit %.1f MB before the menus, %.1f MB after three (%+.1f MB)", float64(before)/(1<<20), float64(after)/(1<<20), grown)
	// GPU targets: +4.7 MB here, the Direct3D runtime's own once per process (none in the
	// app, whose window made it first); the software targets: +12.2 MB.
	if grown > 8 {
		t.Errorf("the menus kept %.1f MB after they closed, want under 8", grown)
	}
}

// A menu that opens under the pointer takes it from the owner at once: the row under a
// right-click loses its hover (and so its hover actions) while the context menu is open, as
// the WebView's menu over the row takes :hover. A menu the pointer is not over (it stays on
// the trigger) leaves the owner's hover alone.
func TestPopupUnderPointerTakesHover(t *testing.T) {
	trigger := widgets.NewButton("Unlimited", "gauge", widgets.Secondary, widgets.SizeMd, nil)
	mb := widgets.NewMenuButton(trigger, "Global speed limit", []widgets.MenuItem{
		{ID: "0", Label: "Unlimited", Kind: widgets.ItemRadio, Checked: true},
		{ID: "20", Label: "20 MiB/s", Kind: widgets.ItemRadio},
	}, func(string) {})
	ww := openWidgets(t, "DownexPopupHoverTest", false, kit.Size{W: 480, H: 420}, mb)
	a, w := ww.a, ww.w
	for _, under := range []bool{true, false} {
		// where the system's pointer is: over the window, then over the menu once it shows
		// there (under), or still over the window (the trigger)
		w.overWindow = func() bool {
			ps := popupsOf[w]
			return !under || len(ps) == 0 || !ps[0].shown
		}
		c := trigger.Base().WindowBounds().Center()
		w32.SendMessage(w.hwnd, 0x0200 /* WM_MOUSEMOVE */, 0, lparam(int(c.X*w.ppd()), int(c.Y*w.ppd())))
		pump(t, a, w, "the hover", time.Second, func() bool { return trigger.Base().Hovered() })
		// The system's pointer is elsewhere, so its WM_MOUSELEAVE comes; while the window says
		// the pointer is over it, the hover stays (the moment before the menu shows).
		if !pumpFor(t, a, w, 3*time.Second, func() bool { return !w.tracking }) {
			t.Skip("the system's pointer is over the test window")
		}
		if !trigger.Base().Hovered() {
			t.Fatal("the hover went before the menu opened")
		}
		w.call(func() { mb.OpenMenu(widgets.FocusFirst) })
		pump(t, a, w, "the menu's first frame", 2*time.Second, func() bool { ps := popupsOf[w]; return len(ps) == 1 && ps[0].shown })
		pumpFor(t, a, w, 50*time.Millisecond, func() bool { return false })
		if got := trigger.Base().Hovered(); got == under {
			t.Errorf("pointer under the menu %v: the owner's element hovered %v", under, got)
		}
		w.call(func() { mb.CloseMenu(false) })
		pump(t, a, w, "the close", time.Second, func() bool { return len(popupsOf[w]) == 0 })
	}
}
