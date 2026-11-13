//go:build !windows && !darwin

package clipboard

import "log/slog"

// startPlatform: no clipboard monitor here yet (Linux: X11/Wayland later).
func startPlatform(func(string), *slog.Logger) (func() error, error) { return nil, ErrNotSupported }
