//go:build !windows

package mediajob

import "os"

// renameReplace atomically renames src to dst, replacing a file dst (rename(2); the
// caller syncs the folder). Callers check that dst is a regular file (replaceable).
func renameReplace(src, dst string) error { return os.Rename(src, dst) }

// isReparsePoint: only Windows has reparse points; os.Lstat reports links here.
func isReparsePoint(os.FileInfo) bool { return false }
