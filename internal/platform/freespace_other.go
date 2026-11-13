//go:build !windows && !linux && !darwin

package platform

import "errors"

// freeSpace is unknown on this system: callers skip their space checks.
func freeSpace(string) (uint64, error) { return 0, errors.ErrUnsupported }
