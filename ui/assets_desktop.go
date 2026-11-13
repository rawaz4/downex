//go:build desktop

package main

import (
	"embed"
	"io/fs"
)

// embedded is the built frontend ("pnpm build" in frontend/, run by "wails build").
//
//go:embed all:frontend/dist
var embedded embed.FS

// frontendAssets returns the frontend built into the executable.
func frontendAssets() (fs.FS, error) {
	return fs.Sub(embedded, "frontend/dist")
}
