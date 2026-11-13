//go:build !windows && !darwin

package platform

// StartApp starts the desktop application exe with args detached from the caller. Here
// (Linux and other systems) it is StartDetached: windows are never hidden there.
func StartApp(exe string, args ...string) error {
	return StartDetached(exe, args...)
}
