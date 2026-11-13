//go:build windows

package wic

import (
	"testing"

	"downex/ui/win/internal/sdkheader"
)

// TestSlots checks every vtable slot, embedded base and GUID of this package against the
// Windows SDK's wincodec.h and objidlbase.h.
func TestSlots(t *testing.T) {
	sdk, ok := sdkheader.Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\<version>\um folder)`, sdk)
	}
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{
		Dir:     ".",
		Prefix:  "IWIC",
		Names:   map[string]string{"Stream": "IStream"},
		Headers: []string{"objidlbase.h", "wincodec.h"},
	})
	for _, err := range errs {
		t.Error(err)
	}
	for _, f := range found {
		t.Logf("%-40s %s", f.Where, f.What)
	}
	if len(found) < 25 {
		t.Errorf("only %d bindings checked; the source scan found too little", len(found))
	}
}
