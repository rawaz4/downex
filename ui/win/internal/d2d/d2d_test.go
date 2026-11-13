//go:build windows

package d2d

import (
	"testing"
	"unsafe"

	"downex/ui/win/internal/sdkheader"
)

// TestSlots checks every vtable slot, embedded base and IID of this package against the
// Windows SDK's d2d1*.h.
func TestSlots(t *testing.T) {
	sdk, ok := sdkheader.Dir()
	if !ok {
		t.Skipf("Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\\<version>\\um folder)", sdk)
	}
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{
		Dir:     ".",
		Prefix:  "ID2D1",
		Headers: []string{"d2d1.h", "d2d1_1.h", "d2d1effects.h"},
	})
	for _, err := range errs {
		t.Error(err)
	}
	for _, f := range found {
		t.Logf("%-45s %s", f.Where, f.What)
	}
	if len(found) < 40 {
		t.Errorf("only %d bindings checked; the source scan found too little", len(found))
	}
}

// The structures handed to Direct2D must have the headers' sizes (x64).
func TestStructSizes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"D2D1_RENDER_TARGET_PROPERTIES", unsafe.Sizeof(RenderTargetProperties{}), 28},
		{"D2D1_HWND_RENDER_TARGET_PROPERTIES", unsafe.Sizeof(HwndRenderTargetProperties{}), 24},
		{"D2D1_BITMAP_PROPERTIES", unsafe.Sizeof(BitmapProperties{}), 16},
		{"D2D1_STROKE_STYLE_PROPERTIES", unsafe.Sizeof(StrokeStyleProperties{}), 28},
		{"D2D1_GRADIENT_STOP", unsafe.Sizeof(GradientStop{}), 20},
		{"D2D1_LAYER_PARAMETERS", unsafe.Sizeof(LayerParameters{}), 72},
		{"D2D1_ARC_SEGMENT", unsafe.Sizeof(ArcSegment{}), 28},
		{"D2D1_ROUNDED_RECT", unsafe.Sizeof(RoundedRect{}), 24},
		{"D2D1_ELLIPSE", unsafe.Sizeof(Ellipse{}), 16},
		{"D2D1_MATRIX_3X2_F", unsafe.Sizeof(Matrix3x2F{}), 24},
		{"D2D1_BRUSH_PROPERTIES", unsafe.Sizeof(BrushProperties{}), 28},
		{"D2D1_BITMAP_PROPERTIES1", unsafe.Sizeof(BitmapProperties1{}), 32},
		{"D2D1_MAPPED_RECT", unsafe.Sizeof(MappedRect{}), 16},
		{"D2D1_POINT_2U", unsafe.Sizeof(PointU{}), 8},
		{"D2D1_RECT_U", unsafe.Sizeof(RectU{}), 16},
	} {
		if c.got != c.want {
			t.Errorf("%s: Go size %d, header size %d", c.name, c.got, c.want)
		}
	}
	if off := unsafe.Offsetof(LayerParameters{}.OpacityBrush); off != 56 {
		t.Errorf("D2D1_LAYER_PARAMETERS.opacityBrush at %d, want 56", off)
	}
}
