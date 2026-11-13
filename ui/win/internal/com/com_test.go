//go:build windows

package com

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"downex/ui/win/internal/sdkheader"
)

func TestSlots(t *testing.T) {
	sdk, ok := sdkheader.Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\<version>\um folder)`, sdk)
	}
	found, errs := sdkheader.Check(sdk, sdkheader.Binding{Dir: ".", Names: map[string]string{"Object": "IUnknown"}})
	for _, err := range errs {
		t.Error(err)
	}
	if len(found) != 3 {
		t.Errorf("checked %d IUnknown slots, want QueryInterface, AddRef and Release: %v", len(found), found)
	}
}

func TestGUID(t *testing.T) {
	const s = "06152247-6f50-465a-9245-118bfd3b6007"
	g := MustGUID(s)
	if g.Data1 != 0x06152247 || g.Data2 != 0x6f50 || g.Data3 != 0x465a || g.Data4 != [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07} {
		t.Fatalf("MustGUID(%s) = %+v", s, g)
	}
	if got := GUIDString(g); got != s {
		t.Fatalf("GUIDString = %s, want %s", got, s)
	}
	for _, bad := range []string{"", "06152247-6f50-465a-9245-118bfd3b600", "06152247x6f50-465a-9245-118bfd3b6007", "0615224g-6f50-465a-9245-118bfd3b6007"} {
		if _, err := ParseGUID(bad); err == nil {
			t.Errorf("ParseGUID(%q) succeeded", bad)
		}
	}
}

func TestErrors(t *testing.T) {
	if Check("x", S_OK) != nil || Check("x", S_FALSE) != nil {
		t.Fatal("success codes are errors")
	}
	err := Check("ID2D1RenderTarget::EndDraw", D2DERR_RECREATE_TARGET)
	if err == nil || err.Error() != "ID2D1RenderTarget::EndDraw: HRESULT 0x8899000c D2DERR_RECREATE_TARGET" {
		t.Fatalf("error %v", err)
	}
	wrapped := fmt.Errorf("frame: %w", err)
	if Code(wrapped) != D2DERR_RECREATE_TARGET {
		t.Fatalf("Code of a wrapped error = %v", Code(wrapped))
	}
	var e *Error
	if !errors.As(wrapped, &e) || e.Op != "ID2D1RenderTarget::EndDraw" {
		t.Fatalf("errors.As: %+v", e)
	}
	if s := HRESULT(0x80001234).String(); s != "HRESULT 0x80001234" {
		t.Fatalf("unknown code formats as %q", s)
	}
}

// The x64 build is the only one that can pass FLOATs by value; arm64 is refused with an
// explanation.
func TestSupported(t *testing.T) {
	if err := Supported("amd64"); err != nil {
		t.Fatalf("amd64 refused: %v", err)
	}
	for _, arch := range []string{"arm64", "386"} {
		err := Supported(arch)
		if err == nil || !strings.Contains(err.Error(), "x64 (amd64)") || !strings.Contains(err.Error(), arch) {
			t.Errorf("Supported(%s) = %v, want an error naming the x64 build", arch, err)
		}
	}
}

func TestPacking(t *testing.T) {
	// The one-word forms hold both halves on a 64-bit target; on 386 (Wide false) they
	// keep the first field alone, and callers pass two words there.
	wantF, wantU := uint64(0x40000000_3f800000), uint64(9)<<32|7
	if !Wide {
		wantF, wantU = 0x3f800000, 7
	}
	if got := Pack2F(1, 2); uint64(got) != wantF {
		t.Errorf("Pack2F(1, 2) = %#x, want %#x", got, wantF)
	}
	if got := Pack2U(7, 9); uint64(got) != wantU {
		t.Errorf("Pack2U(7, 9) = %#x, want %#x", got, wantU)
	}
	if Wide != (unsafe.Sizeof(uintptr(0)) == 8) {
		t.Error("Wide")
	}
	if F(1) != 0x3f800000 || Bool(true) != 1 || Bool(false) != 0 {
		t.Error("F or Bool")
	}
}
