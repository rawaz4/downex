//go:build !windows && !darwin

package sysproxy

import (
	"context"
	"os"
)

// Discover reads the proxy settings from the environment (the Linux/other SYSTEM
// behaviour).
func Discover(context.Context) (Settings, error) {
	return EnvSettings(os.Getenv), nil
}
