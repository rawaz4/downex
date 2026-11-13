//go:build windows

package mediajob

import (
	"os"

	"golang.org/x/sys/windows"
)

// renameNoReplace atomically renames src to dst, failing with an error that matches
// fs.ErrExist when dst exists: unlike os.Rename it omits MOVEFILE_REPLACE_EXISTING.
func renameNoReplace(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	if err := windows.MoveFileEx(from, to, 0); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}
