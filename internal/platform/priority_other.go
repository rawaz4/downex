//go:build !windows

package platform

// RaiseDownloadPriority does nothing on this system: raising a process's priority (a
// negative nice value) needs privileges an ordinary user does not have.
func RaiseDownloadPriority() error { return nil }
