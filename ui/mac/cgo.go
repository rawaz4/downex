//go:build darwin && cgo

package mac

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -mmacosx-version-min=13.0
#cgo LDFLAGS: -framework Cocoa -framework CoreText -framework CoreGraphics -framework ImageIO -framework QuartzCore -framework Accelerate -mmacosx-version-min=13.0
*/
import "C"
