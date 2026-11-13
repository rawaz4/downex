//go:build linux

package engine

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace atomically renames src to dst with RENAME_NOREPLACE, failing with an
// error that matches fs.ErrExist when dst exists. File systems or kernels without that
// flag fall back to linkRename.
func renameNoReplace(src, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS):
		return linkRename(src, dst)
	}
	return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
}
