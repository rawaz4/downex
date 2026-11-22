//go:build windows

package update

import "downex/internal/platform"

// startDetachedCommandLine is the default System.StartDetachedCommandLine.
var startDetachedCommandLine = platform.StartDetachedCommandLine
