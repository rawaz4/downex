//go:build darwin && !cgo

package clipboard

import "log/slog"

// startPlatform: the pure-Go downexd has no pasteboard access; the downexd-agent
// (cgo) monitors the clipboard.
func startPlatform(func(string), *slog.Logger) (func() error, error) { return nil, ErrNotSupported }
