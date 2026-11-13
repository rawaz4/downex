//go:build !windows && !linux && !darwin

package platform

import "fmt"

// Sleep fails with ErrPowerUnsupported on this system.
func Sleep() error { return fmt.Errorf("platform: sleep: %w", ErrPowerUnsupported) }

// Hibernate fails with ErrPowerUnsupported on this system.
func Hibernate() error { return fmt.Errorf("platform: hibernate: %w", ErrPowerUnsupported) }

// Shutdown fails with ErrPowerUnsupported on this system.
func Shutdown() error { return fmt.Errorf("platform: shut down: %w", ErrPowerUnsupported) }

// CanHibernate is false on this system.
func CanHibernate() bool { return false }
