//go:build linux || darwin

package dlock

import (
	"os"
	"path/filepath"
	"testing"
)

// The lock directory and files are accessible to the current user only.
func TestPrivateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db.locks")
	lk, err := Acquire(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Discard()
	for p, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "3.lock"): 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&^want != 0 {
			t.Errorf("%s mode = %v, want at most %v", p, perm, want)
		}
	}
}
