package platform

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// checkSize rejects a negative preallocation size before any OS call is made.
func checkSize(f *os.File, size int64) error {
	if size < 0 {
		return fmt.Errorf("platform: preallocate %s: negative size %d", f.Name(), size)
	}
	return nil
}

// ensureDir creates the absolute directory dir and its missing parents with perm (an
// existing directory keeps its mode) and returns it cleaned. It fails when dir is
// relative or names something that is not a directory.
func ensureDir(dir string, perm fs.FileMode) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("platform: %q is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, perm); err != nil {
		return "", fmt.Errorf("platform: create %s: %w", dir, err)
	}
	return dir, nil
}
