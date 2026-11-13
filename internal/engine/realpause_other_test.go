//go:build !windows

package engine

// realPause does nothing: only Windows holds a file another process scans (see
// realpause_windows_test.go).
func realPause() {}
