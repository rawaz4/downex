//go:build windows

// Package d2d binds the parts of Direct2D (d2d1.h, d2d1_1.h) that ui/win draws with. Types
// are the interfaces without their ID2D1 prefix and embed their base, so a *HwndRenderTarget
// has RenderTarget's methods. Each method's slot is written in its body (Method(n)) and
// checked against the SDK headers by TestSlots; see package com for the calling rules.
package d2d

import (
	"syscall"
	"unsafe"

	"downex/ui/win/internal/com"

	"golang.org/x/sys/windows"
)

// The IIDs used, checked against the headers by TestIIDs (name = "IID" + interface).
var (
	IID_ID2D1Factory       = com.MustGUID("06152247-6f50-465a-9245-118bfd3b6007")
	IID_ID2D1DeviceContext = com.MustGUID("e8f7fe7a-191c-466d-ad95-975678bda998")
)

// The built-in effects used (d2d1effects.h), checked against the headers by TestSlots.
var (
	CLSID_D2D1ColorMatrix  = com.MustGUID("921f03d6-641c-47df-852d-b4bb6153ae11")
	CLSID_D2D1Crop         = com.MustGUID("e23f7110-0e9a-4324-af47-6a2c0c46f35b")
	CLSID_D2D1GaussianBlur = com.MustGUID("1feb6d69-2fe6-4ac9-8c58-1d7f93e7a6a5")
)

// Effect properties and their types (d2d1_1.h, d2d1effects.h).
const (
	PROPERTY_TYPE_FLOAT   = 5
	PROPERTY_TYPE_VECTOR4 = 8
	PROPERTY_TYPE_ENUM    = 11

	PROPERTY_TYPE_MATRIX_5X4 = 17

	CROP_PROP_RECT = 0

	COLORMATRIX_PROP_COLOR_MATRIX   = 0
	COLORMATRIX_PROP_ALPHA_MODE     = 1
	COLORMATRIX_ALPHA_MODE_STRAIGHT = 2

	GAUSSIANBLUR_PROP_STANDARD_DEVIATION = 0
	GAUSSIANBLUR_PROP_OPTIMIZATION       = 1
	GAUSSIANBLUR_PROP_BORDER_MODE        = 2

	GAUSSIANBLUR_OPTIMIZATION_QUALITY = 2

	BORDER_MODE_SOFT = 0
	BORDER_MODE_HARD = 1

	INTERPOLATION_MODE_LINEAR  = 1
	COMPOSITE_MODE_SOURCE_OVER = 0
)

// Enumerations (d2d1.h, dcommon.h, d2d1_1.h).
const (
	FACTORY_TYPE_SINGLE_THREADED = 0

	RENDER_TARGET_TYPE_DEFAULT  = 0
	RENDER_TARGET_TYPE_SOFTWARE = 1
	RENDER_TARGET_TYPE_HARDWARE = 2

	ALPHA_MODE_UNKNOWN       = 0
	ALPHA_MODE_PREMULTIPLIED = 1
	ALPHA_MODE_STRAIGHT      = 2
	ALPHA_MODE_IGNORE        = 3

	DXGI_FORMAT_UNKNOWN        = 0
	DXGI_FORMAT_A8_UNORM       = 65
	DXGI_FORMAT_B8G8R8A8_UNORM = 87

	PRESENT_OPTIONS_NONE            = 0
	PRESENT_OPTIONS_RETAIN_CONTENTS = 1
	PRESENT_OPTIONS_IMMEDIATELY     = 2

	RENDER_TARGET_USAGE_NONE = 0

	ANTIALIAS_MODE_PER_PRIMITIVE = 0
	ANTIALIAS_MODE_ALIASED       = 1

	TEXT_ANTIALIAS_MODE_DEFAULT   = 0
	TEXT_ANTIALIAS_MODE_CLEARTYPE = 1
	TEXT_ANTIALIAS_MODE_GRAYSCALE = 2
	TEXT_ANTIALIAS_MODE_ALIASED   = 3

	DRAW_TEXT_OPTIONS_NONE              = 0
	DRAW_TEXT_OPTIONS_NO_SNAP           = 1
	DRAW_TEXT_OPTIONS_CLIP              = 2
	DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT = 4

	CAP_STYLE_FLAT   = 0
	CAP_STYLE_SQUARE = 1
	CAP_STYLE_ROUND  = 2

	LINE_JOIN_MITER          = 0
	LINE_JOIN_BEVEL          = 1
	LINE_JOIN_ROUND          = 2
	LINE_JOIN_MITER_OR_BEVEL = 3

	DASH_STYLE_SOLID  = 0
	DASH_STYLE_CUSTOM = 5

	FILL_MODE_ALTERNATE = 0
	FILL_MODE_WINDING   = 1

	FIGURE_BEGIN_FILLED = 0
	FIGURE_BEGIN_HOLLOW = 1
	FIGURE_END_OPEN     = 0
	FIGURE_END_CLOSED   = 1

	SWEEP_DIRECTION_COUNTER_CLOCKWISE = 0
	SWEEP_DIRECTION_CLOCKWISE         = 1
	ARC_SIZE_SMALL                    = 0
	ARC_SIZE_LARGE                    = 1

	COMBINE_MODE_UNION     = 0
	COMBINE_MODE_INTERSECT = 1
	COMBINE_MODE_XOR       = 2
	COMBINE_MODE_EXCLUDE   = 3

	EXTEND_MODE_CLAMP  = 0
	EXTEND_MODE_WRAP   = 1
	EXTEND_MODE_MIRROR = 2

	GAMMA_2_2 = 0

	COLOR_SPACE_SRGB = 1

	BUFFER_PRECISION_8BPC_UNORM = 1

	COLOR_INTERPOLATION_MODE_STRAIGHT      = 0
	COLOR_INTERPOLATION_MODE_PREMULTIPLIED = 1

	BITMAP_INTERPOLATION_MODE_NEAREST_NEIGHBOR = 0
	BITMAP_INTERPOLATION_MODE_LINEAR           = 1

	OPACITY_MASK_CONTENT_GRAPHICS = 0

	LAYER_OPTIONS_NONE = 0

	// D2D1_BITMAP_OPTIONS and D2D1_MAP_OPTIONS (d2d1_1.h).
	BITMAP_OPTIONS_CANNOT_DRAW = 2
	BITMAP_OPTIONS_CPU_READ    = 4
	MAP_OPTIONS_READ           = 1

	// D2D1_LAYER_OPTIONS1 (d2d1_1.h): a layer that starts from the pixels under it and has
	// no alpha of its own can take ClearType text.
	LAYER_OPTIONS1_INITIALIZE_FROM_BACKGROUND = 1
	LAYER_OPTIONS1_IGNORE_ALPHA               = 2

	DEFAULT_FLATTENING_TOLERANCE = 0.25
)

// Structures, laid out as the headers declare them.
type (
	// RECT is a GDI rectangle in pixels (windef.h), BindDC's.
	RECT   struct{ Left, Top, Right, Bottom int32 }
	PointF struct{ X, Y float32 }
	SizeU  struct{ W, H uint32 }
	PointU struct{ X, Y uint32 }
	RectU  struct{ Left, Top, Right, Bottom uint32 }
	RectF  struct{ Left, Top, Right, Bottom float32 }
	// ColorF is D2D1_COLOR_F: straight-alpha RGBA, the layout of kit.Color.
	ColorF      struct{ R, G, B, A float32 }
	Matrix3x2F  struct{ M11, M12, M21, M22, M31, M32 float32 }
	RoundedRect struct {
		Rect             RectF
		RadiusX, RadiusY float32
	}
	Ellipse struct {
		Point            PointF
		RadiusX, RadiusY float32
	}
	PixelFormat struct {
		Format    uint32 // DXGI_FORMAT
		AlphaMode uint32
	}
	RenderTargetProperties struct {
		Type        uint32
		PixelFormat PixelFormat
		DpiX, DpiY  float32
		Usage       uint32
		MinLevel    uint32
	}
	HwndRenderTargetProperties struct {
		Hwnd           windows.HWND
		PixelSize      SizeU
		PresentOptions uint32
	}
	BitmapProperties struct {
		PixelFormat PixelFormat
		DpiX, DpiY  float32
	}
	// BitmapProperties1 is D2D1_BITMAP_PROPERTIES1.
	BitmapProperties1 struct {
		PixelFormat  PixelFormat
		DpiX, DpiY   float32
		Options      uint32 // D2D1_BITMAP_OPTIONS
		ColorContext *Resource
	}
	// MappedRect is D2D1_MAPPED_RECT.
	MappedRect struct {
		Pitch uint32
		Bits  uintptr
	}
	StrokeStyleProperties struct {
		StartCap, EndCap, DashCap uint32
		LineJoin                  uint32
		MiterLimit                float32
		DashStyle                 uint32
		DashOffset                float32
	}
	GradientStop struct {
		Position float32
		Color    ColorF
	}
	LinearGradientBrushProperties struct {
		Start, End PointF
	}
	BrushProperties struct {
		Opacity   float32
		Transform Matrix3x2F
	}
	// LayerParameters is D2D1_LAYER_PARAMETERS, and D2D1_LAYER_PARAMETERS1 too: the two have
	// the same fields, and LayerOptions holds a D2D1_LAYER_OPTIONS or a D2D1_LAYER_OPTIONS1.
	LayerParameters struct {
		ContentBounds     RectF
		GeometricMask     *Geometry
		MaskAntialiasMode uint32
		MaskTransform     Matrix3x2F
		Opacity           float32
		OpacityBrush      *Brush
		LayerOptions      uint32
	}
	BezierSegment struct {
		P1, P2, P3 PointF
	}
	QuadraticBezierSegment struct {
		P1, P2 PointF
	}
	ArcSegment struct {
		Point          PointF
		Size           struct{ W, H float32 }
		RotationAngle  float32
		SweepDirection uint32
		ArcSize        uint32
	}
	FactoryOptions struct {
		DebugLevel uint32
	}
)

// Identity is the identity matrix.
var Identity = Matrix3x2F{M11: 1, M22: 1}

// InfiniteRect is D2D1::InfiniteRect(), a layer's unbounded content.
var InfiniteRect = RectF{-3.4028235e38, -3.4028235e38, 3.4028235e38, 3.4028235e38}

var (
	modD2D1               = windows.NewLazySystemDLL("d2d1.dll")
	procD2D1CreateFactory = modD2D1.NewProc("D2D1CreateFactory")
)

// CreateFactory makes a single-threaded Direct2D factory.
func CreateFactory() (*Factory, error) {
	if err := procD2D1CreateFactory.Find(); err != nil {
		return nil, err
	}
	var f *Factory
	opts := FactoryOptions{}
	r, _, _ := syscall.SyscallN(procD2D1CreateFactory.Addr(), FACTORY_TYPE_SINGLE_THREADED,
		uintptr(unsafe.Pointer(&IID_ID2D1Factory)), uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&f)))
	if err := com.Check("D2D1CreateFactory", com.HRESULT(r)); err != nil {
		return nil, err
	}
	return f, nil
}

// Factory is ID2D1Factory.
type Factory struct{ com.Object }

// CreatePathGeometry makes an empty path geometry.
func (f *Factory) CreatePathGeometry() (*PathGeometry, com.HRESULT) {
	var g *PathGeometry
	r, _, _ := syscall.SyscallN(f.Method(10), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(&g)))
	return g, com.HRESULT(r)
}

// CreateStrokeStyle makes a stroke style; dashes may be nil.
func (f *Factory) CreateStrokeStyle(p *StrokeStyleProperties, dashes *float32, count uint32) (*StrokeStyle, com.HRESULT) {
	var s *StrokeStyle
	r, _, _ := syscall.SyscallN(f.Method(11), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(dashes)),
		uintptr(count), uintptr(unsafe.Pointer(&s)))
	return s, com.HRESULT(r)
}

// CreateWicBitmapRenderTarget makes a render target that draws into a WIC bitmap
// (wic.Bitmap, passed as its Object).
func (f *Factory) CreateWicBitmapRenderTarget(bitmap *com.Object, p *RenderTargetProperties) (*RenderTarget, com.HRESULT) {
	var t *RenderTarget
	r, _, _ := syscall.SyscallN(f.Method(13), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(bitmap)), uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&t)))
	return t, com.HRESULT(r)
}

// CreateDCRenderTarget makes a render target that draws into GDI device contexts.
func (f *Factory) CreateDCRenderTarget(p *RenderTargetProperties) (*DCRenderTarget, com.HRESULT) {
	var t *DCRenderTarget
	r, _, _ := syscall.SyscallN(f.Method(16), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&t)))
	return t, com.HRESULT(r)
}

// CreateHwndRenderTarget makes a render target that draws into a window.
func (f *Factory) CreateHwndRenderTarget(p *RenderTargetProperties, hp *HwndRenderTargetProperties) (*HwndRenderTarget, com.HRESULT) {
	var t *HwndRenderTarget
	r, _, _ := syscall.SyscallN(f.Method(14), uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(hp)),
		uintptr(unsafe.Pointer(&t)))
	return t, com.HRESULT(r)
}

// Resource is ID2D1Resource.
type Resource struct{ com.Object }

// Brush is ID2D1Brush.
type Brush struct{ Resource }

// SolidColorBrush is ID2D1SolidColorBrush.
type SolidColorBrush struct{ Brush }

// SetColor sets the brush's colour.
func (b *SolidColorBrush) SetColor(c *ColorF) {
	syscall.SyscallN(b.Method(8), uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(c)))
}

// LinearGradientBrush is ID2D1LinearGradientBrush.
type LinearGradientBrush struct{ Brush }

// SetStartPoint moves the gradient's start.
func (b *LinearGradientBrush) SetStartPoint(x, y float32) {
	syscall.SyscallN(b.Method(8), uintptr(unsafe.Pointer(b)), com.Pack2F(x, y))
}

// SetEndPoint moves the gradient's end.
func (b *LinearGradientBrush) SetEndPoint(x, y float32) {
	syscall.SyscallN(b.Method(9), uintptr(unsafe.Pointer(b)), com.Pack2F(x, y))
}

// GradientStopCollection is ID2D1GradientStopCollection.
type GradientStopCollection struct{ Resource }

// StrokeStyle is ID2D1StrokeStyle.
type StrokeStyle struct{ Resource }

// Image is ID2D1Image (Direct2D 1.1): what DrawImage draws, a bitmap or an effect's output.
type Image struct{ Resource }

// Bitmap is ID2D1Bitmap.
type Bitmap struct{ Image }

// Properties is ID2D1Properties (Direct2D 1.1).
type Properties struct{ com.Object }

// SetValue sets the property index to the size bytes at data, of the PROPERTY_TYPE_ typ.
func (p *Properties) SetValue(index, typ uint32, data unsafe.Pointer, size uint32) com.HRESULT {
	r, _, _ := syscall.SyscallN(p.Method(9), uintptr(unsafe.Pointer(p)), uintptr(index), uintptr(typ), uintptr(data), uintptr(size))
	return com.HRESULT(r)
}

// Effect is ID2D1Effect (Direct2D 1.1): one built-in effect, made by
// DeviceContext.CreateEffect.
type Effect struct{ Properties }

// SetInput makes image (nil: none) the effect's input index.
func (e *Effect) SetInput(index uint32, image *Image, invalidate bool) {
	var inv uintptr
	if invalidate {
		inv = 1
	}
	syscall.SyscallN(e.Method(14), uintptr(unsafe.Pointer(e)), uintptr(index), uintptr(unsafe.Pointer(image)), inv)
}

// GetOutput is the effect's output image, with a reference the caller releases.
func (e *Effect) GetOutput() *Image {
	var im *Image
	syscall.SyscallN(e.Method(18), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(&im)))
	return im
}

// CopyFromRenderTarget copies src of target (pixels; it must have no clip or layer pushed)
// to dst in the bitmap, on the GPU.
func (b *Bitmap) CopyFromRenderTarget(dst *PointU, target *RenderTarget, src *RectU) com.HRESULT {
	r, _, _ := syscall.SyscallN(b.Method(9), uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(target)), uintptr(unsafe.Pointer(src)))
	return com.HRESULT(r)
}

// Bitmap1 is ID2D1Bitmap1 (Direct2D 1.1).
type Bitmap1 struct{ Bitmap }

// Map gives the CPU the pixels of a CPU_READ bitmap in m; it waits for the GPU's work on
// the bitmap. Unmap ends it.
func (b *Bitmap1) Map(options uint32, m *MappedRect) com.HRESULT {
	r, _, _ := syscall.SyscallN(b.Method(14), uintptr(unsafe.Pointer(b)), uintptr(options), uintptr(unsafe.Pointer(m)))
	return com.HRESULT(r)
}

// Unmap ends a Map.
func (b *Bitmap1) Unmap() com.HRESULT {
	r, _, _ := syscall.SyscallN(b.Method(15), uintptr(unsafe.Pointer(b)))
	return com.HRESULT(r)
}

// Geometry is ID2D1Geometry.
type Geometry struct{ Resource }

// GetBounds is the geometry's bounds after transform (nil: none).
func (g *Geometry) GetBounds(transform *Matrix3x2F) (RectF, com.HRESULT) {
	var b RectF
	r, _, _ := syscall.SyscallN(g.Method(4), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(transform)), uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// FillContainsPoint reports whether the filled geometry holds the point (x, y).
func (g *Geometry) FillContainsPoint(x, y float32, transform *Matrix3x2F, tolerance float32) (bool, com.HRESULT) {
	var in int32
	r, _, _ := syscall.SyscallN(g.Method(7), uintptr(unsafe.Pointer(g)), com.Pack2F(x, y), uintptr(unsafe.Pointer(transform)),
		com.F(tolerance), uintptr(unsafe.Pointer(&in)))
	return in != 0, com.HRESULT(r)
}

// CombineWithGeometry writes the combination of g and input (transformed) into sink.
func (g *Geometry) CombineWithGeometry(input *Geometry, mode uint32, transform *Matrix3x2F, tolerance float32, sink *SimplifiedGeometrySink) com.HRESULT {
	r, _, _ := syscall.SyscallN(g.Method(11), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(input)), uintptr(mode),
		uintptr(unsafe.Pointer(transform)), com.F(tolerance), uintptr(unsafe.Pointer(sink)))
	return com.HRESULT(r)
}

// PathGeometry is ID2D1PathGeometry.
type PathGeometry struct{ Geometry }

// Open starts writing the path's figures.
func (g *PathGeometry) Open() (*GeometrySink, com.HRESULT) {
	var s *GeometrySink
	r, _, _ := syscall.SyscallN(g.Method(17), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(&s)))
	return s, com.HRESULT(r)
}

// SimplifiedGeometrySink is ID2D1SimplifiedGeometrySink.
type SimplifiedGeometrySink struct{ com.Object }

// SetFillMode sets the fill rule of the figures.
func (s *SimplifiedGeometrySink) SetFillMode(mode uint32) {
	syscall.SyscallN(s.Method(3), uintptr(unsafe.Pointer(s)), uintptr(mode))
}

// BeginFigure starts a figure at (x, y).
func (s *SimplifiedGeometrySink) BeginFigure(x, y float32, begin uint32) {
	syscall.SyscallN(s.Method(5), uintptr(unsafe.Pointer(s)), com.Pack2F(x, y), uintptr(begin))
}

// EndFigure ends the figure, closed or open.
func (s *SimplifiedGeometrySink) EndFigure(end uint32) {
	syscall.SyscallN(s.Method(8), uintptr(unsafe.Pointer(s)), uintptr(end))
}

// Close finishes the geometry.
func (s *SimplifiedGeometrySink) Close() com.HRESULT {
	r, _, _ := syscall.SyscallN(s.Method(9), uintptr(unsafe.Pointer(s)))
	return com.HRESULT(r)
}

// GeometrySink is ID2D1GeometrySink.
type GeometrySink struct{ SimplifiedGeometrySink }

// AddLine adds a straight line to (x, y).
func (s *GeometrySink) AddLine(x, y float32) {
	syscall.SyscallN(s.Method(10), uintptr(unsafe.Pointer(s)), com.Pack2F(x, y))
}

// AddBezier adds a cubic Bézier curve.
func (s *GeometrySink) AddBezier(b *BezierSegment) {
	syscall.SyscallN(s.Method(11), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(b)))
}

// AddQuadraticBezier adds a quadratic Bézier curve.
func (s *GeometrySink) AddQuadraticBezier(b *QuadraticBezierSegment) {
	syscall.SyscallN(s.Method(12), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(b)))
}

// AddArc adds an elliptical arc.
func (s *GeometrySink) AddArc(a *ArcSegment) {
	syscall.SyscallN(s.Method(14), uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(a)))
}

// RenderTarget is ID2D1RenderTarget.
type RenderTarget struct{ Resource }

// CreateBitmap makes a bitmap of size from pixels (nil: uninitialised) with rows pitch bytes
// apart.
func (t *RenderTarget) CreateBitmap(w, h uint32, pixels unsafe.Pointer, pitch uint32, p *BitmapProperties) (*Bitmap, com.HRESULT) {
	var b *Bitmap
	r, _, _ := syscall.SyscallN(t.Method(4), uintptr(unsafe.Pointer(t)), com.Pack2U(w, h), uintptr(pixels), uintptr(pitch),
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// CreateBitmapFromWicBitmap makes a bitmap of a WIC bitmap source's pixels (p may be nil).
func (t *RenderTarget) CreateBitmapFromWicBitmap(source *com.Object, p *BitmapProperties) (*Bitmap, com.HRESULT) {
	var b *Bitmap
	r, _, _ := syscall.SyscallN(t.Method(5), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// CreateSolidColorBrush makes a brush of one colour.
func (t *RenderTarget) CreateSolidColorBrush(c *ColorF) (*SolidColorBrush, com.HRESULT) {
	var b *SolidColorBrush
	r, _, _ := syscall.SyscallN(t.Method(8), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0, uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// CreateGradientStopCollection makes Direct2D 1.0's gradient stops (straight alpha).
func (t *RenderTarget) CreateGradientStopCollection(stops *GradientStop, count uint32, extend uint32) (*GradientStopCollection, com.HRESULT) {
	var g *GradientStopCollection
	r, _, _ := syscall.SyscallN(t.Method(9), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(stops)), uintptr(count), GAMMA_2_2,
		uintptr(extend), uintptr(unsafe.Pointer(&g)))
	return g, com.HRESULT(r)
}

// CreateLinearGradientBrush makes a linear gradient brush over stops.
func (t *RenderTarget) CreateLinearGradientBrush(p *LinearGradientBrushProperties, stops *GradientStopCollection) (*LinearGradientBrush, com.HRESULT) {
	var b *LinearGradientBrush
	r, _, _ := syscall.SyscallN(t.Method(10), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(stops)),
		uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// DrawLine strokes the segment from (x0, y0) to (x1, y1).
func (t *RenderTarget) DrawLine(x0, y0, x1, y1 float32, b *Brush, width float32, style *StrokeStyle) {
	syscall.SyscallN(t.Method(15), uintptr(unsafe.Pointer(t)), com.Pack2F(x0, y0), com.Pack2F(x1, y1), uintptr(unsafe.Pointer(b)),
		com.F(width), uintptr(unsafe.Pointer(style)))
}

// DrawRectangle strokes a rectangle.
func (t *RenderTarget) DrawRectangle(rc *RectF, b *Brush, width float32, style *StrokeStyle) {
	syscall.SyscallN(t.Method(16), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(rc)), uintptr(unsafe.Pointer(b)), com.F(width),
		uintptr(unsafe.Pointer(style)))
}

// FillRectangle fills a rectangle.
func (t *RenderTarget) FillRectangle(rc *RectF, b *Brush) {
	syscall.SyscallN(t.Method(17), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(rc)), uintptr(unsafe.Pointer(b)))
}

// DrawRoundedRectangle strokes a rounded rectangle.
func (t *RenderTarget) DrawRoundedRectangle(rr *RoundedRect, b *Brush, width float32, style *StrokeStyle) {
	syscall.SyscallN(t.Method(18), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(rr)), uintptr(unsafe.Pointer(b)), com.F(width),
		uintptr(unsafe.Pointer(style)))
}

// FillRoundedRectangle fills a rounded rectangle.
func (t *RenderTarget) FillRoundedRectangle(rr *RoundedRect, b *Brush) {
	syscall.SyscallN(t.Method(19), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(rr)), uintptr(unsafe.Pointer(b)))
}

// DrawEllipse strokes an ellipse.
func (t *RenderTarget) DrawEllipse(e *Ellipse, b *Brush, width float32, style *StrokeStyle) {
	syscall.SyscallN(t.Method(20), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(b)), com.F(width),
		uintptr(unsafe.Pointer(style)))
}

// FillEllipse fills an ellipse.
func (t *RenderTarget) FillEllipse(e *Ellipse, b *Brush) {
	syscall.SyscallN(t.Method(21), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(b)))
}

// DrawGeometry strokes a geometry.
func (t *RenderTarget) DrawGeometry(g *Geometry, b *Brush, width float32, style *StrokeStyle) {
	syscall.SyscallN(t.Method(22), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(b)), com.F(width),
		uintptr(unsafe.Pointer(style)))
}

// FillGeometry fills a geometry.
func (t *RenderTarget) FillGeometry(g *Geometry, b *Brush) {
	syscall.SyscallN(t.Method(23), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(b)), 0)
}

// FillOpacityMask paints b through the alpha of mask's src part into dst. The target's
// antialias mode must be ANTIALIAS_MODE_ALIASED.
func (t *RenderTarget) FillOpacityMask(mask *Bitmap, b *Brush, dst, src *RectF) {
	syscall.SyscallN(t.Method(25), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(mask)), uintptr(unsafe.Pointer(b)),
		OPACITY_MASK_CONTENT_GRAPHICS, uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(src)))
}

// DrawBitmap draws the src part of bm (nil: all) into dst.
func (t *RenderTarget) DrawBitmap(bm *Bitmap, dst *RectF, opacity float32, interpolation uint32, src *RectF) {
	syscall.SyscallN(t.Method(26), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(bm)), uintptr(unsafe.Pointer(dst)),
		com.F(opacity), uintptr(interpolation), uintptr(unsafe.Pointer(src)))
}

// DrawTextLayout draws a DirectWrite text layout (dwrite.TextLayout, passed as its Object)
// with its top-left corner at (x, y).
func (t *RenderTarget) DrawTextLayout(x, y float32, layout *com.Object, b *Brush, options uint32) {
	syscall.SyscallN(t.Method(28), uintptr(unsafe.Pointer(t)), com.Pack2F(x, y), uintptr(unsafe.Pointer(layout)),
		uintptr(unsafe.Pointer(b)), uintptr(options))
}

// SetTransform sets the world transform.
func (t *RenderTarget) SetTransform(m *Matrix3x2F) {
	syscall.SyscallN(t.Method(30), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(m)))
}

// SetAntialiasMode sets how shapes are antialiased.
func (t *RenderTarget) SetAntialiasMode(mode uint32) {
	syscall.SyscallN(t.Method(32), uintptr(unsafe.Pointer(t)), uintptr(mode))
}

// SetTextAntialiasMode sets how text is antialiased.
func (t *RenderTarget) SetTextAntialiasMode(mode uint32) {
	syscall.SyscallN(t.Method(34), uintptr(unsafe.Pointer(t)), uintptr(mode))
}

// PushLayer starts a layer; layer may be nil (Windows 8 and later manage it).
func (t *RenderTarget) PushLayer(p *LayerParameters, layer *Resource) {
	syscall.SyscallN(t.Method(40), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(layer)))
}

// PopLayer ends the most recent layer.
func (t *RenderTarget) PopLayer() {
	syscall.SyscallN(t.Method(41), uintptr(unsafe.Pointer(t)))
}

// PushAxisAlignedClip clips to rc (in world coordinates; its bounds after a rotation).
func (t *RenderTarget) PushAxisAlignedClip(rc *RectF, mode uint32) {
	syscall.SyscallN(t.Method(45), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(rc)), uintptr(mode))
}

// PopAxisAlignedClip ends the most recent clip.
func (t *RenderTarget) PopAxisAlignedClip() {
	syscall.SyscallN(t.Method(46), uintptr(unsafe.Pointer(t)))
}

// Clear fills the whole target (inside the clip) with c.
func (t *RenderTarget) Clear(c *ColorF) {
	syscall.SyscallN(t.Method(47), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)))
}

// BeginDraw starts drawing.
func (t *RenderTarget) BeginDraw() {
	syscall.SyscallN(t.Method(48), uintptr(unsafe.Pointer(t)))
}

// EndDraw finishes drawing and presents (a window's target); D2DERR_RECREATE_TARGET means the
// device is lost.
func (t *RenderTarget) EndDraw() com.HRESULT {
	r, _, _ := syscall.SyscallN(t.Method(49), uintptr(unsafe.Pointer(t)), 0, 0)
	return com.HRESULT(r)
}

// SetDpi sets the DPI that maps DIPs to the target's pixels.
func (t *RenderTarget) SetDpi(dpiX, dpiY float32) {
	syscall.SyscallN(t.Method(51), uintptr(unsafe.Pointer(t)), com.F(dpiX), com.F(dpiY))
}

// IsSupported reports whether the target has the properties p (a Type of
// RENDER_TARGET_TYPE_HARDWARE asks whether it draws on the GPU).
func (t *RenderTarget) IsSupported(p *RenderTargetProperties) bool {
	r, _, _ := syscall.SyscallN(t.Method(56), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(p)))
	return int32(r) != 0
}

// HwndRenderTarget is ID2D1HwndRenderTarget.
type HwndRenderTarget struct{ RenderTarget }

// Resize changes the target's size in pixels.
func (t *HwndRenderTarget) Resize(size *SizeU) com.HRESULT {
	r, _, _ := syscall.SyscallN(t.Method(58), uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(size)))
	return com.HRESULT(r)
}

// DCRenderTarget is ID2D1DCRenderTarget: a target that draws into a GDI device context,
// which BindDC names before each frame.
type DCRenderTarget struct{ RenderTarget }

// BindDC binds the target to the device context dc, on its rectangle rc (pixels).
func (t *DCRenderTarget) BindDC(dc windows.Handle, rc *RECT) com.HRESULT {
	r, _, _ := syscall.SyscallN(t.Method(57), uintptr(unsafe.Pointer(t)), uintptr(dc), uintptr(unsafe.Pointer(rc)))
	return com.HRESULT(r)
}

// DeviceContext is ID2D1DeviceContext (Windows 8), which every render target of Windows 8
// and later also is.
type DeviceContext struct{ RenderTarget }

// CreateBitmap makes a Direct2D 1.1 bitmap of size pixels with props, from src (nil:
// uninitialised) rows pitch bytes apart.
func (dc *DeviceContext) CreateBitmap(size SizeU, src unsafe.Pointer, pitch uint32, props *BitmapProperties1) (*Bitmap1, com.HRESULT) {
	if !com.Wide {
		return createBitmap386(dc, size, src, pitch, props)
	}
	var b *Bitmap1
	// D2D1_SIZE_U by value: one word on a 64-bit target
	r, _, _ := syscall.SyscallN(dc.Method(57), uintptr(unsafe.Pointer(dc)), com.Pack2U(size.W, size.H), uintptr(src), uintptr(pitch),
		uintptr(unsafe.Pointer(props)), uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// createBitmap386 is CreateBitmap on 386, where the D2D1_SIZE_U passed by value is two
// words, its width first (the SDK checker counts the x64 form in the method).
func createBitmap386(dc *DeviceContext, size SizeU, src unsafe.Pointer, pitch uint32, props *BitmapProperties1) (*Bitmap1, com.HRESULT) {
	var b *Bitmap1
	r, _, _ := syscall.SyscallN(dc.Method(57), uintptr(unsafe.Pointer(dc)), uintptr(size.W), uintptr(size.H), uintptr(src), uintptr(pitch),
		uintptr(unsafe.Pointer(props)), uintptr(unsafe.Pointer(&b)))
	return b, com.HRESULT(r)
}

// CreateEffect makes the built-in effect clsid (CLSID_D2D1...).
func (dc *DeviceContext) CreateEffect(clsid *windows.GUID) (*Effect, com.HRESULT) {
	var e *Effect
	r, _, _ := syscall.SyscallN(dc.Method(63), uintptr(unsafe.Pointer(dc)), uintptr(unsafe.Pointer(clsid)), uintptr(unsafe.Pointer(&e)))
	return e, com.HRESULT(r)
}

// DrawImage draws image with its origin at offset (nil: the origin), the part rect of it
// (nil: all of it), through the current transform and clip.
func (dc *DeviceContext) DrawImage(image *Image, offset *PointF, rect *RectF, interpolation, composite uint32) {
	syscall.SyscallN(dc.Method(83), uintptr(unsafe.Pointer(dc)), uintptr(unsafe.Pointer(image)), uintptr(unsafe.Pointer(offset)),
		uintptr(unsafe.Pointer(rect)), uintptr(interpolation), uintptr(composite))
}

// PushLayer starts a layer with Direct2D 1.1's options (D2D1_LAYER_PARAMETERS1, whose
// layerOptions are D2D1_LAYER_OPTIONS1); layer may be nil.
func (dc *DeviceContext) PushLayer(p *LayerParameters, layer *Resource) {
	syscall.SyscallN(dc.Method(86), uintptr(unsafe.Pointer(dc)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(layer)))
}

// CreateGradientStopCollection makes gradient stops interpolated as asked (Direct2D 1.1).
func (dc *DeviceContext) CreateGradientStopCollection(stops *GradientStop, count uint32, extend, interpolation uint32) (*GradientStopCollection, com.HRESULT) {
	var g *GradientStopCollection
	r, _, _ := syscall.SyscallN(dc.Method(64), uintptr(unsafe.Pointer(dc)), uintptr(unsafe.Pointer(stops)), uintptr(count),
		COLOR_SPACE_SRGB, COLOR_SPACE_SRGB, BUFFER_PRECISION_8BPC_UNORM, uintptr(extend), uintptr(interpolation),
		uintptr(unsafe.Pointer(&g)))
	return g, com.HRESULT(r)
}
