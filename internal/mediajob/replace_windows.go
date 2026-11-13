//go:build windows

package mediajob

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// renameReplace atomically renames src to dst, replacing a file dst
// (MOVEFILE_REPLACE_EXISTING; a folder dst makes it fail), and returns only once the move
// is on disk (MOVEFILE_WRITE_THROUGH). Callers check that dst is a regular file
// (replaceable), as the engine does.
func renameReplace(src, dst string) error {
	from, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	to, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}

// isReparsePoint reports a file with a reparse point (a link, a junction, a cloud
// placeholder, a deduplicated file): os.Lstat calls some of them regular files.
func isReparsePoint(fi os.FileInfo) bool {
	a, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && a.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
