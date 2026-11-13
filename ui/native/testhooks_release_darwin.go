//go:build darwin && cgo && !e2etest

package main

import (
	"downex/ui/mac"
	"downex/ui/native/glue"
)

// e2eConfig is the e2etest build's hook (testhooks_e2e_darwin.go); the product has none.
func e2eConfig(*glue.Config) {}

// e2eListen is the e2etest build's remote control (testhooks_e2ectl_darwin.go).
func e2eListen(*mac.App) {}
