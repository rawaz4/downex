//go:build !windows

package platform

import (
	"fmt"
	"time"
)

// WakeTimer would wake the computer from sleep at a set time; only Windows has one
// (docs/M6-SPEC.md §1 wake_computer).
type WakeTimer struct{}

// NewWakeTimer fails with ErrPowerUnsupported off Windows.
func NewWakeTimer() (*WakeTimer, error) {
	return nil, fmt.Errorf("platform: wake timer: %w", ErrPowerUnsupported)
}

// WakeAt fails with ErrPowerUnsupported off Windows.
func (w *WakeTimer) WakeAt(time.Time) error {
	return fmt.Errorf("platform: wake timer: %w", ErrPowerUnsupported)
}

// Cancel does nothing off Windows.
func (w *WakeTimer) Cancel() error { return nil }

// Close does nothing off Windows.
func (w *WakeTimer) Close() error { return nil }
