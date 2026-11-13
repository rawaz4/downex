//go:build darwin

package host

import (
	"strings"
	"testing"
)

// open is never given a relative path, which it could take for an option or a URL.
func TestRunOpenNeedsAnAbsolutePath(t *testing.T) {
	for _, p := range []string{"-R", "a.txt", "https://example.com/", ""} {
		if err := runOpen(p, p); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("runOpen(%q) = %v", p, err)
		}
	}
}
