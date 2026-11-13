//go:build !windows && !linux && !darwin && !freebsd

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
)

// Endpoint returns the EnvEndpoint override; there is no default endpoint on this OS.
func Endpoint() (string, error) {
	if e := os.Getenv(EnvEndpoint); e != "" {
		return e, nil
	}
	return "", fmt.Errorf("ipc: no default endpoint on this platform: %w", errors.ErrUnsupported)
}

// Listen is not supported on this OS.
func Listen(endpoint string) (net.Listener, error) {
	return nil, fmt.Errorf("ipc: listen on %s: %w", endpoint, errors.ErrUnsupported)
}

// Dial is not supported on this OS.
func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return nil, fmt.Errorf("ipc: dial %s: %w", endpoint, errors.ErrUnsupported)
}

// AcquireInstanceLock is not supported on this OS.
func AcquireInstanceLock(endpoint string) (release func(), err error) {
	return nil, fmt.Errorf("ipc: instance lock for %s: %w", endpoint, errors.ErrUnsupported)
}
