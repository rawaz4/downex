//go:build windows

// Package wic binds the parts of the Windows Imaging Component (wincodec.h) and of the COM
// stream API (objidlbase.h, ole32, shlwapi) that ui/win decodes images, keeps offscreen
// bitmaps and encodes PNG files with. Types are the interfaces without their IWIC prefix;
// each method's slot is written in its body and checked against the SDK headers by
// TestSlots. See package com for the calling rules.
package wic

import (
	"syscall"
	"unsafe"

	"downex/ui/win/internal/com"

	"golang.org/x/sys/windows"
)

// The GUIDs used, checked against the headers by TestSlots.
var (
	CLSID_WICImagingFactory       = com.MustGUID("cacaf262-9370-4615-a13b-9f5539da4c0a")
	IID_IWICImagingFactory        = com.MustGUID("ec5ec8a9-c395-4314-9c77-54d7a935ff70")
	GUID_ContainerFormatPng       = com.MustGUID("1b7cfaf4-713f-473c-bbcd-6137425faeaf")
	GUID_WICPixelFormat32bppPBGRA = com.MustGUID("6fddc324-4e03-4bfe-b185-3d77768dc910")
	GUID_WICPixelFormat32bppBGRA  = com.MustGUID("6fddc324-4e03-4bfe-b185-3d77768dc90f")
	GUID_WICPixelFormat32bppBGR   = com.MustGUID("6fddc324-4e03-4bfe-b185-3d77768dc90e")
	GUID_WICPixelFormat24bppBGR   = com.MustGUID("6fddc324-4e03-4bfe-b185-3d77768dc90c")
)

// Enumerations (wincodec.h).
const (
	DecodeMetadataCacheOnDemand = 0

	BitmapNoCache     = 0
	BitmapCacheOnLoad = 2

	BitmapEncoderNoCache = 2

	BitmapDitherTypeNone       = 0
	BitmapPaletteTypeMedianCut = 1

	BitmapLockRead  = 1
	BitmapLockWrite = 2
)

// Rect is WICRect.
type Rect struct{ X, Y, W, H int32 }

// ImagingFactory is IWICImagingFactory.
type ImagingFactory struct{ com.Object }

// NewImagingFactory makes a WIC factory. The calling thread must be in a COM apartment
// (com.KeepMTA makes every thread one).
func NewImagingFactory() (*ImagingFactory, error) {
	return com.CreateInstance[ImagingFactory]("WICImagingFactory", &CLSID_WICImagingFactory, &IID_IWICImagingFactory)
}

// CreateDecoderFromStream finds a decoder for the image in stream.
func (f *ImagingFactory) CreateDecoderFromStream(stream *Stream, options uint32) (*BitmapDecoder, com.HRESULT) {
	var d *BitmapDecoder
	r, _, _ := syscall.SyscallN(f.Method(4), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(stream)), 0, uintptr(options),
		uintptr(unsafe.Pointer(&d)))
	return d, com.HRESULT(r)
}

// CreateEncoder makes an encoder of the container format.
func (f *ImagingFactory) CreateEncoder(format *com.GUID) (*BitmapEncoder, com.HRESULT) {
	var e *BitmapEncoder
	r, _, _ := syscall.SyscallN(f.Method(8), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(format)), 0, uintptr(unsafe.Pointer(&e)))
	return e, com.HRESULT(r)
}

// CreateFormatConverter makes a pixel format converter.
func (f *ImagingFactory) CreateFormatConverter() (*FormatConverter, com.HRESULT) {
	var c *FormatConverter
	r, _, _ := syscall.SyscallN(f.Method(10), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&c)))
	return c, com.HRESULT(r)
}

// CreateBitmap makes a w × h bitmap of the pixel format.
func (f *ImagingFactory) CreateBitmap(w, h uint32, format *com.GUID, cache uint32) (*Bitmap, com.HRESULT) {
	var b *Bitmap
	r, _, _ := syscall.SyscallN(f.Method(17), uintptr(unsafe.Pointer(f)), uintptr(w), uintptr(h), uintptr(unsafe.Pointer(format)),
		uintptr(cache), uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// CreateBitmapFromSource makes a bitmap holding source's pixels.
func (f *ImagingFactory) CreateBitmapFromSource(source *BitmapSource, cache uint32) (*Bitmap, com.HRESULT) {
	var b *Bitmap
	r, _, _ := syscall.SyscallN(f.Method(18), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(source)), uintptr(cache),
		uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// BitmapSource is IWICBitmapSource.
type BitmapSource struct{ com.Object }

// GetSize is the source's size in pixels.
func (s *BitmapSource) GetSize() (w, h uint32, hr com.HRESULT) {
	r, _, _ := syscall.SyscallN(s.Method(3), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&w)), uintptr(unsafe.Pointer(&h)))
	return w, h, com.HRESULT(r)
}

// Bitmap is IWICBitmap.
type Bitmap struct{ BitmapSource }

// Lock gives direct access to the rc part of the bitmap's memory.
func (b *Bitmap) Lock(rc *Rect, flags uint32) (*BitmapLock, com.HRESULT) {
	var l *BitmapLock
	r, _, _ := syscall.SyscallN(b.Method(8), uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(rc)), uintptr(flags), uintptr(unsafe.Pointer(&l)))
	return l, com.HRESULT(r)
}

// BitmapLock is IWICBitmapLock.
type BitmapLock struct{ com.Object }

// GetStride is the distance between rows in bytes.
func (l *BitmapLock) GetStride() (uint32, com.HRESULT) {
	var s uint32
	r, _, _ := syscall.SyscallN(l.Method(4), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(&s)))
	return s, com.HRESULT(r)
}

// GetDataPointer is the locked memory and its size.
func (l *BitmapLock) GetDataPointer() (unsafe.Pointer, uint32, com.HRESULT) {
	var size uint32
	var p unsafe.Pointer
	r, _, _ := syscall.SyscallN(l.Method(5), uintptr(unsafe.Pointer(l)), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&p)))
	return p, size, com.HRESULT(r)
}

// BitmapDecoder is IWICBitmapDecoder.
type BitmapDecoder struct{ com.Object }

// GetFrame is the image's frame at index.
func (d *BitmapDecoder) GetFrame(index uint32) (*BitmapFrameDecode, com.HRESULT) {
	var f *BitmapFrameDecode
	r, _, _ := syscall.SyscallN(d.Method(13), uintptr(unsafe.Pointer(d)), uintptr(index), uintptr(unsafe.Pointer(&f)))
	return f, com.HRESULT(r)
}

// BitmapFrameDecode is IWICBitmapFrameDecode.
type BitmapFrameDecode struct{ BitmapSource }

// FormatConverter is IWICFormatConverter.
type FormatConverter struct{ BitmapSource }

// Initialize converts source to the pixel format.
func (c *FormatConverter) Initialize(source *BitmapSource, format *com.GUID) com.HRESULT {
	r, _, _ := syscall.SyscallN(c.Method(8), uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(format)),
		BitmapDitherTypeNone, 0, 0 /* alpha threshold 0.0 */, BitmapPaletteTypeMedianCut)
	return com.HRESULT(r)
}

// BitmapEncoder is IWICBitmapEncoder.
type BitmapEncoder struct{ com.Object }

// Initialize starts encoding into stream.
func (e *BitmapEncoder) Initialize(stream *Stream, cache uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(e.Method(3), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(stream)), uintptr(cache))
	return com.HRESULT(r)
}

// CreateNewFrame adds a frame; props are its encoder options (release them).
func (e *BitmapEncoder) CreateNewFrame() (*BitmapFrameEncode, *com.Object, com.HRESULT) {
	var f *BitmapFrameEncode
	var props *com.Object
	r, _, _ := syscall.SyscallN(e.Method(10), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(&f)), uintptr(unsafe.Pointer(&props)))
	return f, props, com.HRESULT(r)
}

// Commit finishes the image.
func (e *BitmapEncoder) Commit() com.HRESULT {
	r, _, _ := syscall.SyscallN(e.Method(11), uintptr(unsafe.Pointer(e)))
	return com.HRESULT(r)
}

// BitmapFrameEncode is IWICBitmapFrameEncode.
type BitmapFrameEncode struct{ com.Object }

// Initialize starts the frame with its options (may be nil).
func (f *BitmapFrameEncode) Initialize(props *com.Object) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(3), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(props)))
	return com.HRESULT(r)
}

// SetSize sets the frame's size in pixels.
func (f *BitmapFrameEncode) SetSize(w, h uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(4), uintptr(unsafe.Pointer(f)), uintptr(w), uintptr(h))
	return com.HRESULT(r)
}

// SetPixelFormat asks for a pixel format; the encoder writes back the nearest it supports.
func (f *BitmapFrameEncode) SetPixelFormat(format *com.GUID) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(6), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(format)))
	return com.HRESULT(r)
}

// WriteSource encodes source's pixels (converted to the frame's format).
func (f *BitmapFrameEncode) WriteSource(source *BitmapSource) com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(11), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(source)), 0)
	return com.HRESULT(r)
}

// Commit finishes the frame.
func (f *BitmapFrameEncode) Commit() com.HRESULT {
	r, _, _ := syscall.SyscallN(f.Method(12), uintptr(unsafe.Pointer(f)))
	return com.HRESULT(r)
}

// Stream is IStream (objidlbase.h), read here through its HGLOBAL.
type Stream struct{ com.Object }

// StatStg is STATSTG.
type StatStg struct {
	Name                 *uint16
	Type                 uint32
	Size                 uint64
	MTime, CTime, ATime  windows.Filetime
	Mode, LocksSupported uint32
	Clsid                com.GUID
	StateBits            uint32
	reserved             uint32
}

// STATFLAG_NONAME leaves StatStg.Name nil, so there is nothing to free.
const STATFLAG_NONAME = 1

// Stat describes the stream: its size, without its name.
func (s *Stream) Stat(st *StatStg) com.HRESULT {
	r, _, _ := syscall.SyscallN(s.Method(12), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(st)), STATFLAG_NONAME)
	return com.HRESULT(r)
}

var (
	modShlwapi                = windows.NewLazySystemDLL("shlwapi.dll")
	procSHCreateMemStream     = modShlwapi.NewProc("SHCreateMemStream")
	modOle32                  = windows.NewLazySystemDLL("ole32.dll")
	procCreateStreamOnHGlobal = modOle32.NewProc("CreateStreamOnHGlobal")
	procGetHGlobalFromStream  = modOle32.NewProc("GetHGlobalFromStream")
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalLock            = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock          = modKernel32.NewProc("GlobalUnlock")
)

// NewMemStream is a read-only stream over a copy of data: the stream does not keep data
// (SHCreateMemStream copies it), so Go memory is never held by COM.
func NewMemStream(data []byte) (*Stream, error) {
	if err := procSHCreateMemStream.Find(); err != nil {
		return nil, err
	}
	if len(data) == 0 || uint64(len(data)) > 1<<31 {
		return nil, &com.Error{Op: "SHCreateMemStream", HRESULT: com.E_INVALIDARG}
	}
	r, _, _ := syscall.SyscallN(procSHCreateMemStream.Addr(), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if r == 0 {
		return nil, &com.Error{Op: "SHCreateMemStream", HRESULT: com.E_OUTOFMEMORY}
	}
	return (*Stream)(com.Addr(r)), nil
}

// NewGrowingStream is an empty stream in a growing HGLOBAL, freed with the stream.
func NewGrowingStream() (*Stream, error) {
	if err := procCreateStreamOnHGlobal.Find(); err != nil {
		return nil, err
	}
	var s *Stream
	r, _, _ := syscall.SyscallN(procCreateStreamOnHGlobal.Addr(), 0, 1, uintptr(unsafe.Pointer(&s)))
	if err := com.Check("CreateStreamOnHGlobal", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return s, nil
}

// Bytes copies the content of a stream made by NewGrowingStream.
func (s *Stream) Bytes() ([]byte, error) {
	var st StatStg
	if err := com.Check("IStream::Stat", s.Stat(&st)); err != nil {
		return nil, err
	}
	if st.Size > 1<<31 {
		return nil, &com.Error{Op: "IStream::Stat", HRESULT: com.E_OUTOFMEMORY}
	}
	n := int(st.Size)
	if err := procGetHGlobalFromStream.Find(); err != nil {
		return nil, err
	}
	var h uintptr
	r, _, _ := syscall.SyscallN(procGetHGlobalFromStream.Addr(), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&h)))
	if err := com.Check("GetHGlobalFromStream", com.HRESULT(r)); err != nil {
		return nil, err
	}
	p, _, e := syscall.SyscallN(procGlobalLock.Addr(), h)
	if p == 0 {
		return nil, &com.Error{Op: "GlobalLock", HRESULT: com.HRESULT(0x80070000 | uint32(e))}
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(com.Addr(p)), n))
	syscall.SyscallN(procGlobalUnlock.Addr(), h)
	return out, nil
}
