//go:build (dev || bindings) && !desktop

package main

import (
	"io/fs"
	"testing/fstest"
)

// frontendAssets returns a placeholder page. "wails dev" serves the frontend from the
// Vite dev server (frontend:dev:serverUrl in wails.json), and the bindings generator
// that "wails build" runs before it builds the frontend never serves pages, so
// neither needs frontend/dist to exist.
func frontendAssets() (fs.FS, error) {
	return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(devPage)}}, nil
}

const devPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Downex</title></head>
<body><p>The Downex frontend is served by its development server. Start the window with
<code>wails dev</code> in the ui directory.</p></body></html>
`
