//go:build windows

package main

import "golang.org/x/sys/windows"

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

// handForeground hands the browser's right to the foreground on (AllowSetForegroundWindow
// with ASFW_ANY) before the relay passes a download the user just started on: the browser
// started this process, so it may, and the Downex window that the add opens (its progress
// window, the confirmation) may then come to the front instead of only flashing its
// taskbar button (docs/DESKTOP-SPEC.md §7). It reports whether Windows allowed it.
func handForeground() bool {
	const asfwAny = 0xFFFFFFFF
	r, _, _ := procAllowSetForegroundWindow.Call(uintptr(asfwAny))
	return r != 0
}
