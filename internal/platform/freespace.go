package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
)

// FreeSpace returns the bytes available to the current user on the volume that holds
// dir (docs/SITE-MEDIA-SPEC.md §9 #10: a site download checks for room before it
// preallocates). A dir that does not exist yet is measured at its nearest existing
// parent, so that a part directory can be checked before it is created. Systems
// without a free-space query return an error wrapping errors.ErrUnsupported.
func FreeSpace(dir string) (int64, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return 0, fmt.Errorf("platform: free space of %s: %w", dir, err)
	}
	for {
		_, err := os.Stat(abs)
		if err == nil {
			break
		}
		parent := filepath.Dir(abs)
		if !errors.Is(err, fs.ErrNotExist) || parent == abs {
			return 0, fmt.Errorf("platform: free space of %s: %w", dir, err)
		}
		abs = parent
	}
	free, err := freeSpace(abs)
	if err != nil {
		return 0, fmt.Errorf("platform: free space of %s: %w", dir, err)
	}
	return int64(min(free, math.MaxInt64)), nil
}
