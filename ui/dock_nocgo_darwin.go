//go:build darwin && !(cgo && (desktop || dev || bindings))

package main

// Without cgo, or without the window's build tags (tests, "go vet ./..."), a darwin
// build has no AppKit (dock_darwin.go): there is no activation to hand over and no
// system request to quit to answer.

func yieldActivation(...string) {}

func installAppHooks(func()) {}
