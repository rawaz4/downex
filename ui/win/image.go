//go:build windows

package win

import (
	"fmt"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/wic"
)

// image is a kit.Image: decoded premultiplied BGRA pixels in a WIC bitmap (system memory),
// and the bitmap each device made of them when it first drew the image.
type image struct {
	res  *Resources
	bmp  *wic.Bitmap
	w, h int
	devs map[*device]*d2d.Bitmap
}

// PixelSize implements kit.Image.
func (im *image) PixelSize() (int, int) {
	if im.bmp == nil {
		return 0, 0
	}
	return im.w, im.h
}

// Release implements kit.Image.
func (im *image) Release() {
	if im.bmp == nil {
		return
	}
	for dev, b := range im.devs {
		b.Release()
		delete(dev.images, im)
	}
	im.devs = nil
	com.Release(&im.bmp)
	im.res.live.images--
}

// bitmap is dev's copy of the image, made on first use.
func (im *image) bitmap(dev *device) *d2d.Bitmap {
	if im.bmp == nil {
		return nil
	}
	if b, ok := im.devs[dev]; ok {
		return b
	}
	b, hr := dev.rt.CreateBitmapFromWicBitmap(&im.bmp.Object, nil)
	if hr.Failed() {
		return nil
	}
	if im.devs == nil {
		im.devs = map[*device]*d2d.Bitmap{}
	}
	im.devs[dev] = b
	dev.images[im] = struct{}{}
	return b
}

// NewImage implements kit.Resources: PNG, JPEG, GIF (the first frame), BMP, TIFF, ICO, and
// WebP where its codec is installed.
func (r *Resources) NewImage(encoded []byte) (kit.Image, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	if len(encoded) == 0 {
		return nil, errInvalid("empty image")
	}
	f, err := r.imaging()
	if err != nil {
		return nil, err
	}
	stream, err := wic.NewMemStream(encoded)
	if err != nil {
		return nil, fmt.Errorf("win: image: %w", err)
	}
	defer stream.Release()
	dec, hr := f.CreateDecoderFromStream(stream, wic.DecodeMetadataCacheOnDemand)
	if hr.Failed() {
		if known := sniffImage(encoded); known != "" && known != "WebP" && (hr == com.WINCODEC_ERR_COMPONENTNOTFOUND || hr == com.WINCODEC_ERR_UNKNOWNIMAGEFORMAT) {
			// WIC finds no decoder for a PNG, JPEG, GIF, BMP, TIFF or ICO it cannot read
			// the header of: the data is broken, the format is not unknown.
			return nil, fmt.Errorf("win: %s data: %w: %w", known, com.Check("IWICImagingFactory::CreateDecoderFromStream", hr), kit.ErrInvalid)
		}
		return nil, imageError("IWICImagingFactory::CreateDecoderFromStream", hr)
	}
	defer dec.Release()
	frame, hr := dec.GetFrame(0)
	if hr.Failed() {
		return nil, imageError("IWICBitmapDecoder::GetFrame", hr)
	}
	defer frame.Release()
	conv, hr := f.CreateFormatConverter()
	if hr.Failed() {
		return nil, imageError("IWICImagingFactory::CreateFormatConverter", hr)
	}
	defer conv.Release()
	if hr := conv.Initialize(&frame.BitmapSource, &wic.GUID_WICPixelFormat32bppPBGRA); hr.Failed() {
		return nil, imageError("IWICFormatConverter::Initialize", hr)
	}
	bmp, hr := f.CreateBitmapFromSource(&conv.BitmapSource, wic.BitmapCacheOnLoad)
	if hr.Failed() {
		return nil, imageError("IWICImagingFactory::CreateBitmapFromSource", hr)
	}
	w, h, hr := bmp.GetSize()
	if hr.Failed() || w == 0 || h == 0 {
		bmp.Release()
		return nil, imageError("IWICBitmap::GetSize", hr)
	}
	r.live.images++
	return &image{res: r, bmp: bmp, w: int(w), h: int(h)}, nil
}

// sniffImage names the image format data starts with, "" when it is none WIC ships.
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

// imageError sorts WIC's failures into the kit's: no codec for the format is
// ErrUnsupported, anything else wrong with the data ErrInvalid.
func imageError(op string, hr com.HRESULT) error {
	err := com.Check(op, hr)
	if err == nil {
		err = &com.Error{Op: op, HRESULT: com.E_FAIL}
	}
	switch hr {
	case com.WINCODEC_ERR_COMPONENTNOTFOUND, com.WINCODEC_ERR_UNKNOWNIMAGEFORMAT:
		return fmt.Errorf("win: image format: %w: %w", err, kit.ErrUnsupported)
	case com.E_OUTOFMEMORY:
		return fmt.Errorf("win: image: %w", err)
	}
	return fmt.Errorf("win: image data: %w: %w", err, kit.ErrInvalid)
}

var _ kit.Image = (*image)(nil)
