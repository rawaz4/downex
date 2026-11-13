//go:build windows

package dwrite

import (
	"testing"
	"unsafe"

	"downex/ui/win/internal/sdkheader"
)

// TestSlots checks every vtable slot, embedded base and IID of this package against the
// Windows SDK's dwrite*.h.
func TestSlots(t *testing.T) {
	sdk, ok := sdkheader.Dir()
	if !ok {
		t.Skipf("Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\\<version>\\um folder)", sdk)
	}
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{
		Dir:     ".",
		Prefix:  "IDWrite",
		Headers: []string{"dwrite.h", "dwrite_1.h", "dwrite_2.h", "dwrite_3.h"},
	})
	for _, err := range errs {
		t.Error(err)
	}
	for _, f := range found {
		t.Logf("%-40s %s", f.Where, f.What)
	}
	if len(found) < 30 {
		t.Errorf("only %d bindings checked; the source scan found too little", len(found))
	}
}

func TestStructSizes(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uintptr
	}{
		{"DWRITE_FONT_METRICS", unsafe.Sizeof(FontMetrics{}), 20},
		{"DWRITE_TEXT_METRICS", unsafe.Sizeof(TextMetrics{}), 36},
		{"DWRITE_LINE_METRICS", unsafe.Sizeof(LineMetrics{}), 24},
		{"DWRITE_CLUSTER_METRICS", unsafe.Sizeof(ClusterMetrics{}), 8},
		{"DWRITE_HIT_TEST_METRICS", unsafe.Sizeof(HitTestMetrics{}), 36},
		{"DWRITE_TRIMMING", unsafe.Sizeof(Trimming{}), 12},
		{"DWRITE_FONT_AXIS_VALUE", unsafe.Sizeof(FontAxisValue{}), 8},
	} {
		if c.got != c.want {
			t.Errorf("%s: Go size %d, header size %d", c.name, c.got, c.want)
		}
	}
	if FourCC("wght") != 0x74686777 {
		t.Errorf("FourCC(wght) = %#x, DWRITE_FONT_AXIS_TAG_WEIGHT is 0x74686777", FourCC("wght"))
	}
}
