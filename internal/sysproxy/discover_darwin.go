//go:build darwin

package sysproxy

import (
	"context"
	"fmt"
	"os/exec"
)

// scutilPath is the absolute path of scutil (overridable in tests).
var scutilPath = "/usr/sbin/scutil"

// Discover reads the macOS system proxy configuration with `scutil --proxy` and parses
// it (no cgo). A failure to run scutil returns the error with empty settings.
func Discover(ctx context.Context) (Settings, error) {
	out, err := exec.CommandContext(ctx, scutilPath, "--proxy").Output()
	if err != nil {
		return Settings{}, fmt.Errorf("sysproxy: scutil --proxy: %w", err)
	}
	return ParseScutil(out), nil
}
