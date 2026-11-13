//go:build !windows

package pac

import "os/exec"

// hideHelperWindow does nothing: the helper has no window outside Windows.
func hideHelperWindow(*exec.Cmd) {}

// limitHelperProcess does nothing here: an address-space limit (RLIMIT_AS) would also
// cap the reservations of the Go runtime and of race-detector builds. An allocation the
// system refuses ends the helper; one it grants lazily is caught by the heap watchdog.
func limitHelperProcess(uint64) {}
