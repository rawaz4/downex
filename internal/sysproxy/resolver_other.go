//go:build !windows

package sysproxy

import (
	"context"
	"errors"
	"net/url"

	"downex/internal/transport"
)

// AutoResolver is unavailable off Windows: WPAD auto-detect via WinHttpGetProxyForUrl is
// a Windows facility. Callers fall back to the static Settings (NewStaticResolver). The
// type and its methods exist so cross-platform code type-checks.
type AutoResolver struct{}

// NewAutoResolver reports that auto-detection is unsupported on this platform.
func NewAutoResolver(string) (*AutoResolver, error) {
	return nil, errors.New("sysproxy: WPAD auto-detection needs Windows")
}

// ResolveProxy always returns direct (this stub is never constructed successfully).
func (r *AutoResolver) ResolveProxy(context.Context, *url.URL) ([]transport.Proxy, error) {
	return nil, nil
}

// Close is a no-op.
func (r *AutoResolver) Close() {}
