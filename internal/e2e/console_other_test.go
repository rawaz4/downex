//go:build !windows

package e2e

// consoleProbe is the console helper of the Windows tests (console_windows_test.go);
// there is none elsewhere.
func consoleProbe() (code int, ok bool) { return 0, false }
