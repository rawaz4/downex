//go:build !windows

package main

// handForeground does nothing here: no foreground lock to hand over (macOS raises the
// window itself, docs/DESKTOP-SPEC.md §7).
func handForeground() bool { return true }
