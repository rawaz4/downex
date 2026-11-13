//go:build !desktop && !dev && !bindings

package main

import (
	"fmt"
	"os"
)

// main of a plain "go build": the window needs the build tags that the Wails CLI sets
// (and the frontend it builds and embeds), so this build only says how to make it.
// It keeps "go build ./..." and "go vet ./..." working on every platform without
// the frontend, cgo or WebKitGTK.
func main() {
	fmt.Fprintf(os.Stderr, "Downex %s: this build has no window.\n"+
		"Build the desktop app with the Wails CLI (\"wails build\" in ui/) or scripts/build-windows.ps1; see README.md.\n",
		version)
	os.Exit(2)
}
