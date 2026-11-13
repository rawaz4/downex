package platform

import "golang.org/x/sys/windows"

// TrimWorkingSet empties this process's working set: Windows takes back every page the
// process has in memory, and the pages it still uses come back (from memory, not the disk)
// as it touches them. What it gives back are the pages start-up and finished work touched
// and no longer use: code, read-only data, DLL pages and freed Go memory.
func TrimWorkingSet() error {
	return windows.SetProcessWorkingSetSizeEx(windows.CurrentProcess(), ^uintptr(0), ^uintptr(0), 0)
}
