package platform

import (
	"testing"

	"golang.org/x/sys/windows"
)

var procGetPriorityClass = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetPriorityClass")

func priorityClass(t *testing.T) uint32 {
	t.Helper()
	r, _, err := procGetPriorityClass.Call(uintptr(windows.CurrentProcess()))
	if r == 0 {
		t.Fatalf("GetPriorityClass: %v", err)
	}
	return uint32(r)
}

func TestRaiseDownloadPriority(t *testing.T) {
	before := priorityClass(t)
	t.Cleanup(func() {
		if err := windows.SetPriorityClass(windows.CurrentProcess(), before); err != nil {
			t.Errorf("restore the priority class: %v", err)
		}
	})
	if err := RaiseDownloadPriority(); err != nil {
		t.Fatal(err)
	}
	if got := priorityClass(t); got != windows.ABOVE_NORMAL_PRIORITY_CLASS {
		t.Fatalf("priority class %#x, want ABOVE_NORMAL (%#x)", got, windows.ABOVE_NORMAL_PRIORITY_CLASS)
	}
}
