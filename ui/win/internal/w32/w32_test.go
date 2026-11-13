//go:build windows

package w32

import (
	"errors"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The structures' sizes on x64, from WinUser.h, WinGDI.h, ShellAPI.h, UxTheme.h and imm.h:
// a structure of the wrong size is refused by the function that checks its length field, or
// silently overruns or truncates what the system writes.
func TestStructSizes(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"MSG", unsafe.Sizeof(MSG{}), 48},
		{"WNDCLASSEXW", unsafe.Sizeof(WNDCLASSEXW{}), 80},
		{"CREATESTRUCTW", unsafe.Sizeof(CREATESTRUCTW{}), 80},
		{"PAINTSTRUCT", unsafe.Sizeof(PAINTSTRUCT{}), 72},
		{"MINMAXINFO", unsafe.Sizeof(MINMAXINFO{}), 40},
		{"MONITORINFO", unsafe.Sizeof(MONITORINFO{}), 40},
		{"TRACKMOUSEEVENT", unsafe.Sizeof(TRACKMOUSEEVENT{}), 24},
		{"WINDOWPLACEMENT", unsafe.Sizeof(WINDOWPLACEMENT{}), 44},
		{"HIGHCONTRASTW", unsafe.Sizeof(HIGHCONTRAST{}), 16},
		{"COMPOSITIONFORM", unsafe.Sizeof(COMPOSITIONFORM{}), 28},
		{"CANDIDATEFORM", unsafe.Sizeof(CANDIDATEFORM{}), 32},
		{"NCCALCSIZE_PARAMS", unsafe.Sizeof(NCCALCSIZE_PARAMS{}), 56},
		{"MARGINS", unsafe.Sizeof(MARGINS{}), 16},
		{"APPBARDATA", unsafe.Sizeof(APPBARDATA{}), 48},
		{"TEXTMETRICW", unsafe.Sizeof(TEXTMETRICW{}), 60},
	} {
		if c.got != c.want {
			t.Errorf("%s is %d bytes, want %d", c.name, c.got, c.want)
		}
	}
}

// Get and SetWindowPlacement accept the structure (SetWindowPlacement refuses a wrong
// length with ERROR_INVALID_PARAMETER), and a new restored size takes effect. The window is
// a hidden one of the system's static class, never shown. A window belongs to the thread that
// made it: the goroutine stays on that thread until the window is destroyed, or the calls
// below go to it from another thread (SetWindowPlacement and DestroyWindow then send to a
// thread nobody pumps, and wait for ever).
func TestWindowPlacementRoundTrip(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	class, _ := windows.UTF16PtrFromString("Static")
	h, err := CreateWindowEx(0, class, nil, WS_OVERLAPPEDWINDOW, 100, 100, 300, 200, 0, GetModuleHandle(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer DestroyWindow(h)
	p := new(WINDOWPLACEMENT)
	if err := GetWindowPlacement(h, p); err != nil {
		t.Fatal(err)
	}
	p.NormalPosition.Right = p.NormalPosition.Left + 420
	if err := SetWindowPlacement(h, p); err != nil {
		t.Fatalf("SetWindowPlacement: %v", err)
	}
	if wr := GetWindowRect(h); wr.Right-wr.Left != 420 {
		t.Errorf("width after SetWindowPlacement %d, want 420", wr.Right-wr.Left)
	}
	if err := SetWindowPlacement(0, p); !errors.Is(err, windows.ERROR_INVALID_WINDOW_HANDLE) {
		t.Errorf("SetWindowPlacement of no window: %v", err)
	}
}

// The calls that take a POINT by value pass it as one word on a 64-bit target and as two on
// 386 (stage 1's re-check: the packed word was wrong there). A point in the primary
// monitor's work area finds that monitor, a point far outside every monitor finds none
// (MONITOR_DEFAULTTONULL), and the window under a point outside every monitor is none: with
// the halves swapped or dropped these would differ.
func TestPointArguments(t *testing.T) {
	primary := MonitorFromPoint(Point{}, MONITOR_DEFAULTTOPRIMARY)
	mi, ok := GetMonitorInfo(primary)
	if primary == 0 || !ok {
		t.Fatal("no primary monitor")
	}
	in := Point{X: mi.Work.Right - 3, Y: mi.Work.Bottom - 2}
	if got := MonitorFromPoint(in, 0); got != primary {
		t.Errorf("MonitorFromPoint(%+v) = %#x, want the primary %#x", in, got, primary)
	}
	far := Point{X: 30000, Y: -30000}
	if got := MonitorFromPoint(far, 0); got != 0 {
		t.Errorf("MonitorFromPoint(%+v) = %#x, want none", far, got)
	}
	if got := MonitorFromPoint(Point{X: mi.Work.Left + 1, Y: -30000}, 0); got != 0 {
		t.Errorf("a point above every monitor found %#x", got)
	}
	if got := WindowFromPoint(far); got != 0 {
		t.Errorf("WindowFromPoint(%+v) = %#x, want none", far, got)
	}
}
