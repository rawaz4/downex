package tray

import (
	"image"

	"downex/internal/brand"
)

// The macOS menu-bar icon is a template image (NSImage.template): black with
// coverage as alpha, which the menu bar tints for light and dark menu bars and for
// the highlighted (open menu) state. It is the bare Downex mark (internal/brand)
// without the application icon's tile, drawn in code like the Windows icon.

// templatePoints is the image's size in points; the status item is square
// (NSSquareStatusItemLength) on a menu bar 22 or 24 points high.
const templatePoints = 18

// templateIcon draws the template glyph at px×px pixels.
func templateIcon(px int) *image.NRGBA { return brand.Template(px) }

// templatePNG encodes templateIcon(px) as PNG, the form NSBitmapImageRep reads.
func templatePNG(px int) []byte { return brand.PNG(templateIcon(px)) }
