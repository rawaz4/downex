//go:build !windows && !(darwin && cgo)

// Command native is downex-ui, Downex's native window, which draws with Direct2D and
// DirectWrite: it runs on Windows, and on macOS over ui/mac with cgo (Linux keeps today's window). Elsewhere it
// builds (so that go build ./... works on every system) and says so.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "downex-ui: the native window runs on Windows only")
	os.Exit(1)
}
