//go:build !windows

package main

import (
	"context"
	"log/slog"
)

// startMemoryTrim does nothing here: WebView2's memory target is Windows-only, and
// WebKitGTK and WKWebView have no such switch.
func startMemoryTrim(context.Context, *slog.Logger) {}
