//go:build !windows && !darwin

package platform

import "fmt"

// AwakeRequest would keep the computer from sleeping while idle; only Windows and macOS
// have one (Linux would take a logind inhibitor, which is not wired yet).
type AwakeRequest struct{}

// NewAwakeRequest fails with ErrPowerUnsupported on this system.
func NewAwakeRequest(string) (*AwakeRequest, error) {
	return nil, fmt.Errorf("platform: awake request: %w", ErrPowerUnsupported)
}

// Hold fails with ErrPowerUnsupported on this system.
func (*AwakeRequest) Hold() error {
	return fmt.Errorf("platform: awake request: %w", ErrPowerUnsupported)
}

// Release does nothing on this system.
func (*AwakeRequest) Release() error { return nil }

// Close does nothing on this system.
func (*AwakeRequest) Close() error { return nil }
