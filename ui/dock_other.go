//go:build (desktop || dev || bindings) && !(darwin && cgo)

package main

// hideFromDock: only macOS has a Dock (dock_darwin.go, which needs cgo, as the macOS
// window does); elsewhere a progress window needs nothing.
func hideFromDock() {}
