//go:build windows

package webstorage

import (
	"os"

	"golang.org/x/sys/windows"
)

// openShared opens a file for reading while letting Chromium write, rename and delete it
// meanwhile (FILE_SHARE_DELETE, which os.Open does not give): reading never gets in the
// way of a running window's compactions.
func openShared(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
