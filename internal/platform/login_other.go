//go:build !windows && !linux && !darwin

package platform

import (
	"errors"
	"fmt"
)

// SetLaunchAtLogin is not implemented on this platform; its error matches
// errors.ErrUnsupported.
func SetLaunchAtLogin(enabled bool, exe string, args ...string) error {
	return fmt.Errorf("platform: launch at login: %w", errors.ErrUnsupported)
}
