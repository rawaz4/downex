//go:build !windows && !(darwin && cgo)

package viewstest

import (
	"image"

	"downex/ui/kit"
)

// RealResources answers kit.ErrUnsupported: offscreen rendering is the Windows and macOS
// (cgo) backends'.
func RealResources() (kit.Resources, func(), error) { return nil, nil, kit.ErrUnsupported }

// CanRender answers kit.ErrUnsupported without a backend that renders.
func CanRender() error { return kit.ErrUnsupported }

// Render answers kit.ErrUnsupported without a backend that renders.
func (h *Harness) Render() (*image.RGBA, error) { return nil, kit.ErrUnsupported }
