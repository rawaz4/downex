package platform

import "fmt"

// Power actions on Linux belong to logind's D-Bus interface
// (org.freedesktop.login1.Manager Suspend, Hibernate, PowerOff); until that is wired
// they are unsupported.

// Sleep fails with ErrPowerUnsupported on Linux (logind is not wired yet).
func Sleep() error { return fmt.Errorf("platform: sleep: %w", ErrPowerUnsupported) }

// Hibernate fails with ErrPowerUnsupported on Linux (logind is not wired yet).
func Hibernate() error { return fmt.Errorf("platform: hibernate: %w", ErrPowerUnsupported) }

// Shutdown fails with ErrPowerUnsupported on Linux (logind is not wired yet).
func Shutdown() error { return fmt.Errorf("platform: shut down: %w", ErrPowerUnsupported) }

// CanHibernate is false on Linux until logind is wired.
func CanHibernate() bool { return false }
