//go:build linux

package client

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindDaemonResolvesSymlink checks that a client reached through a symbolic link
// (as from /usr/local/bin) finds the downexd installed next to the real executable.
func TestFindDaemonResolvesSymlink(t *testing.T) {
	realDir, linkDir := t.TempDir(), t.TempDir()
	exe := filepath.Join(realDir, "downex")
	for _, p := range []string{exe, filepath.Join(realDir, "downexd")} {
		if err := os.WriteFile(p, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(linkDir, "downex")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FindDaemon(func(string) string { return "" }, func() (string, error) { return link, nil })
	if want := filepath.Join(resolved, "downexd"); err != nil || got != want {
		t.Errorf("FindDaemon = %q, %v; want %q", got, err, want)
	}
}
