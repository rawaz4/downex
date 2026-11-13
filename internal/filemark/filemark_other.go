//go:build !windows && !darwin && !linux

package filemark

// markPlatform: no file marks on this system.
func markPlatform(string, Origin) error { return ErrNotSupported }
