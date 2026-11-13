//go:build windows

package platform

import "golang.org/x/sys/windows"

// freeSpace returns the bytes available to this user (quotas included) on the volume
// of the existing folder dir.
func freeSpace(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}
