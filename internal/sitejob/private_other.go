//go:build !windows

package sitejob

import "os"

// createPrivate creates a new file at path that only the user can read and write
// (mode 0600); an existing file is an error.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
