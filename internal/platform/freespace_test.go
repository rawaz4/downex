package platform

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFreeSpace: an existing folder has room on its volume, and a folder that does not
// exist yet is measured at its nearest existing parent.
func TestFreeSpace(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeSpace(dir)
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
		if err != nil || free <= 0 {
			t.Fatalf("FreeSpace(%s) = %d, %v; want room", dir, free, err)
		}
	default:
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("FreeSpace on %s = %d, %v; want ErrUnsupported", runtime.GOOS, free, err)
		}
		return
	}
	missing := filepath.Join(dir, "video.mp4.dxparts", "video-137")
	got, err := FreeSpace(missing)
	if err != nil || got <= 0 {
		t.Fatalf("FreeSpace(%s) = %d, %v; want its parent's free space", missing, got, err)
	}
}
