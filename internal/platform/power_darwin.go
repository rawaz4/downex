package platform

import (
	"fmt"
	"os/exec"
)

// runPowerCommand runs a power command and returns its combined output.
var runPowerCommand = func(exe string, args ...string) ([]byte, error) {
	return exec.Command(exe, args...).CombinedOutput()
}

// Sleep puts the Mac to sleep (pmset sleepnow).
func Sleep() error { return macPower("sleep") }

// Hibernate is not offered on macOS (hibernation there is a power-management mode,
// not an action): it fails with ErrPowerUnsupported, and validation refuses it.
func Hibernate() error {
	return fmt.Errorf("platform: hibernate: %w", ErrPowerUnsupported)
}

// Shutdown asks System Events to shut down, which lets every application save or
// refuse (the first time, macOS asks the user to allow controlling System Events).
func Shutdown() error { return macPower("shutdown") }

// CanHibernate is false on macOS.
func CanHibernate() bool { return false }

func macPower(action string) error {
	exe, args, ok := macPowerCommand(action)
	if !ok {
		return fmt.Errorf("platform: %s: %w", action, ErrPowerUnsupported)
	}
	if out, err := runPowerCommand(exe, args...); err != nil {
		return fmt.Errorf("platform: %s: %w (%s)", action, err, trimOutput(out))
	}
	return nil
}

// trimOutput returns at most a short line of a command's output for an error message.
func trimOutput(out []byte) string {
	const limit = 200
	if len(out) > limit {
		out = out[:limit]
	}
	return string(out)
}
