//go:build !windows

package sitejob

import (
	"os"
	"testing"
)

// checkPrivate checks that only the user can read or write path.
func checkPrivate(t *testing.T, path string) {
	t.Helper()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: mode %v, %v; want 0600", path, fi.Mode().Perm(), err)
	}
}
