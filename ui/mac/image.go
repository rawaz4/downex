//go:build darwin && cgo

package mac

/*
#include "draw.h"
*/
import "C"

import (
	"fmt"
	"unsafe"

	"downex/ui/kit"
)

// image is a kit.Image: a decoded CGImage (ImageIO), which is device-independent. It keeps
// the image's embedded colour profile, so drawing it into an sRGB target converts it as
// WebKit does.
type image struct {
	res  *Resources
	img  C.CGImageRef
	w, h int
}

// PixelSize implements kit.Image.
func (im *image) PixelSize() (int, int) {
	if im == nil || im.img == 0 {
		return 0, 0
	}
	return im.w, im.h
}

// Release implements kit.Image.
func (im *image) Release() {
	if im == nil || im.img == 0 {
		return
	}
	C.CGImageRelease(im.img)
	im.img = 0
	im.res.live.images--
}

// NewImage implements kit.Resources: PNG, JPEG, GIF (the first frame), BMP, TIFF, ICO,
// WebP and whatever else ImageIO decodes.
func (r *Resources) NewImage(encoded []byte) (kit.Image, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	if len(encoded) == 0 {
		return nil, errInvalid("empty image")
	}
	var status C.int
	img := C.dx_image_decode(unsafe.Pointer(&encoded[0]), C.long(len(encoded)), &status)
	if img == 0 {
		known := sniffImage(encoded)
		if status == 1 && known == "" {
			return nil, fmt.Errorf("mac: image format: no decoder: %w", kit.ErrUnsupported)
		}
		if known == "" {
			known = "image"
		}
		return nil, errInvalid("%s data cannot be decoded", known)
	}
	w, h := int(C.CGImageGetWidth(img)), int(C.CGImageGetHeight(img))
	if w <= 0 || h <= 0 {
		C.CGImageRelease(img)
		return nil, errInvalid("image of %d×%d pixels", w, h)
	}
	r.live.images++
	return &image{res: r, img: img, w: w, h: h}, nil
}

// sniffImage names the image format data starts with, "" when it is none of the common
// ones: a known signature with data ImageIO cannot read is broken, not unknown.
func sniffImage(b []byte) string {
	has := func(sig string) bool { return len(b) >= len(sig) && string(b[:len(sig)]) == sig }
	switch {
	case has("\x89PNG\r\n\x1a\n"):
		return "PNG"
	case has("\xff\xd8\xff"):
		return "JPEG"
	case has("GIF87a"), has("GIF89a"):
		return "GIF"
	case has("BM"):
		return "BMP"
	case has("II*\x00"), has("MM\x00*"):
		return "TIFF"
	case has("\x00\x00\x01\x00"):
		return "ICO"
	case has("RIFF") && len(b) >= 12 && string(b[8:12]) == "WEBP":
		return "WebP"
	}
	return ""
}

var _ kit.Image = (*image)(nil)
