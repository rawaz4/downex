package tray

import "downex/internal/brand"

// The tray icon is drawn in code at the size the notification area asks for, so it is
// sharp at every display scale: the bare Downex mark (internal/brand), a white glyph
// with a translucent dark contour that reads on light and dark taskbars.

// iconResource encodes the tray glyph at size pixels as an icon resource image, the
// input of CreateIconFromResourceEx.
func iconResource(size int) []byte { return brand.IconImage(brand.Tray(size)) }
