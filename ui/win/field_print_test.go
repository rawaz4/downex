//go:build windows

package win

import (
	"syscall"
	"testing"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/win/internal/w32"
)

// printClient has the EDIT h draw itself into a memory bitmap through WM_PRINTCLIENT, as
// AnimateWindow and PrintWindow without the compositor have a window drawn, and counts the
// pixels of the placeholder's pure red.
func printClient(t *testing.T, h w32.HWND) int {
	t.Helper()
	gdi := syscall.NewLazyDLL("gdi32.dll")
	createDC, deleteDC := gdi.NewProc("CreateCompatibleDC"), gdi.NewProc("DeleteDC")
	createBmp, selectObj, deleteObj := gdi.NewProc("CreateCompatibleBitmap"), gdi.NewProc("SelectObject"), gdi.NewProc("DeleteObject")
	getDIBits := gdi.NewProc("GetDIBits")
	getDC, releaseDC := user32.NewProc("GetDC"), user32.NewProc("ReleaseDC")
	rc := w32.GetClientRect(h)
	cw, ch := int(rc.Right), int(rc.Bottom)
	if cw <= 0 || ch <= 0 {
		t.Fatalf("EDIT client %v", rc)
	}
	screen, _, _ := getDC.Call(0)
	defer releaseDC.Call(0, screen)
	mem, _, _ := createDC.Call(screen)
	defer deleteDC.Call(mem)
	bmp, _, _ := createBmp.Call(screen, uintptr(cw), uintptr(ch))
	defer deleteObj.Call(bmp)
	old, _, _ := selectObj.Call(mem, bmp)
	w32.SendMessage(h, wmPrintClient, mem, prfClient|prfErase)
	selectObj.Call(mem, old)
	type bitmapInfoHeader struct {
		Size                          uint32
		Width, Height                 int32
		Planes, BitCount              uint16
		Compression, SizeImage        uint32
		XPels, YPels, ClrUsed, ClrImp int32
	}
	bi := bitmapInfoHeader{Width: int32(cw), Height: -int32(ch), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, cw*ch*4)
	if n, _, _ := getDIBits.Call(mem, bmp, 0, uintptr(ch), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0); n == 0 {
		t.Fatal("GetDIBits failed")
	}
	n := 0
	for i := 0; i+3 < len(buf); i += 4 {
		if b, g, r := buf[i], buf[i+1], buf[i+2]; r > 200 && g < 70 && b < 70 {
			n++
		}
	}
	return n
}

// The placeholder is part of the field however the field is drawn: through WM_PAINT, and
// through WM_PRINTCLIENT, which AnimateWindow and PrintWindow without the compositor use.
// Drawn only after WM_PAINT, an empty field printed as an empty box.
func TestNativeFieldPrintsItsPlaceholder(t *testing.T) {
	r := newFieldRoot()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexFieldPrintTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(r.win, WindowOptions{Size: kit.Size{W: 480, H: 120}, NoActivate: true, Placed: true,
		At: w32.Point{X: mi.Work.Left + 60, Y: mi.Work.Top + 60}})
	if err != nil {
		t.Fatal(err)
	}
	w.overWindow = func() bool { return false }
	pump(t, a, w, "the first frame", 2*time.Second, func() bool { return w.painted })
	f := w.fields[0]
	if n := printClient(t, f.hwnd); n < 20 {
		t.Errorf("the empty field prints %d placeholder pixels", n)
	}
	r.field.SetText("kernel.tar")
	if n := printClient(t, f.hwnd); n != 0 {
		t.Errorf("a field with text prints %d placeholder pixels", n)
	}
}
