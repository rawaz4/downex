//go:build !windows

package update

import (
	"errors"
	"fmt"
)

// startDetachedCommandLine is the default System.StartDetachedCommandLine: only Windows
// passes a command line as one string (the Setup runs nowhere else).
func startDetachedCommandLine(exe, _ string) error {
	return fmt.Errorf("update: start %s: %w", exe, errors.ErrUnsupported)
}
