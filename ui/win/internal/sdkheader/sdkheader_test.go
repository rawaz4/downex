//go:build windows

package sdkheader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sdkOrSkip(t *testing.T) string {
	t.Helper()
	dir, ok := Dir()
	if !ok {
		t.Skipf(`Windows SDK headers not found at %s (set DOWNEX_SDK_INCLUDE to the SDK's Include\<version>\um folder)`, dir)
	}
	return dir
}

// MIDL headers declare each interface twice, in C++ and as a C vtable struct. The C++
// reading with its bases must give exactly the C vtable's method list: this checks the
// parser (inheritance, comments, overload handling) on real declarations.
func TestParserAgreesWithCVtables(t *testing.T) {
	dir := sdkOrSkip(t)
	s, err := Parse(dir, "objidlbase.h", "ocidl.h", "wincodec.h")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range []string{"IStream", "IWICImagingFactory", "IWICBitmap", "IWICBitmapLock", "IWICBitmapDecoder",
		"IWICBitmapFrameDecode", "IWICFormatConverter", "IWICBitmapEncoder", "IWICBitmapFrameEncode", "IWICPalette",
		"IWICBitmapScaler", "IWICComponentInfo", "IWICMetadataQueryReader"} {
		file := "wincodec.h"
		if name == "IStream" {
			file = "objidlbase.h"
		}
		want, err := CVtable(dir, file, name)
		if err != nil {
			t.Error(err)
			continue
		}
		got, err := s.AllMethods(name)
		if err != nil {
			t.Error(err)
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: C++ reading\n%v\nC vtable\n%v", name, got, want)
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d interfaces compared", checked)
	}
}

func TestMSVCOverloadOrder(t *testing.T) {
	got, over := msvcOrder([]string{"f", "g", "f", "h", "f"})
	if want := []string{"f", "f", "f", "g", "h"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if !over["f"] || over["g"] {
		t.Fatalf("overloaded %v", over)
	}
}

func TestStripComments(t *testing.T) {
	in := "a // b { \n c /* d { } */ e \"// kept\" f"
	got := stripComments(in)
	if strings.Contains(got, "b") || strings.Contains(got, "d") || !strings.Contains(got, `"// kept"`) || strings.Count(got, "\n") != 1 {
		t.Fatalf("stripComments = %q", got)
	}
}

// Check must catch a wrong slot, a wrong base and a wrong GUID: a binding written with
// mistakes is fed to it.
func TestCheckFindsMistakes(t *testing.T) {
	dir := sdkOrSkip(t)
	pkg := t.TempDir()
	set, err := Parse(dir, "d2d1.h")
	if err != nil {
		t.Fatal(err)
	}
	slot := func(m string) int {
		n, err := set.Slot("ID2D1RenderTarget", m)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// DrawLine and GetSize are right (GetSize returns a structure: a hidden pointer after
	// the interface pointer); DrawRectangle forgets its stroke style.
	src := fmt.Sprintf(`package fake

import (
	"syscall"

	"downex/ui/win/internal/com"
)

var IID_ID2D1Factory = com.MustGUID("06152247-6f50-465a-9245-118bfd3b6008")

type Resource struct{ com.Object }
type Brush struct{ Resource }
type RenderTarget struct{ Brush }

func (t *RenderTarget) FillRectangle() { syscall.SyscallN(t.Method(16), 0, 0, 0) }
func (t *RenderTarget) DrawLine()      { syscall.SyscallN(t.Method(15), 0, 0, 0, 0, 0, 0) }
func (t *RenderTarget) DrawRectangle() { syscall.SyscallN(t.Method(%d), 0, 0, 0, 0) }
func (t *RenderTarget) GetSize()       { syscall.SyscallN(t.Method(%d), 0, 0) }
func (t *RenderTarget) Flush()         { _ = t.Method(%d) }
`, slot("DrawRectangle"), slot("GetSize"), slot("Flush"))
	if err := os.WriteFile(filepath.Join(pkg, "fake.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	found, errs := Check(dir, Binding{Dir: pkg, Prefix: "ID2D1", Headers: []string{"d2d1.h"}})
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"RenderTarget.FillRectangle uses slot 16, but ID2D1RenderTarget::FillRectangle is slot 17",
		"type RenderTarget embeds Brush, but ID2D1Brush is not a base of ID2D1RenderTarget",
		"var IID_ID2D1Factory = 06152247-6f50-465a-9245-118bfd3b6008, headers say 06152247-6f50-465a-9245-118bfd3b6007",
		"RenderTarget.DrawRectangle passes 4 words, but ID2D1RenderTarget::DrawRectangle takes 5",
		"RenderTarget.Flush does not call syscall.SyscallN(X.Method(",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("Check did not report %q; it reported:\n%s", want, all)
		}
	}
	checked := map[string]bool{}
	for _, f := range found {
		checked[f.Where] = true
	}
	if !checked["RenderTarget.DrawLine"] || !checked["RenderTarget.GetSize"] {
		t.Errorf("the correct DrawLine and GetSize were not reported as checked: %v", found)
	}
}

func TestSlotRefusesOverloads(t *testing.T) {
	dir := sdkOrSkip(t)
	s, err := Parse(dir, "dwrite.h", "dwrite_1.h", "dwrite_2.h", "dwrite_3.h")
	if err != nil {
		t.Fatal(err)
	}
	// IDWriteFontFace4 declares GetGlyphImageFormats twice.
	if _, err := s.Slot("IDWriteFontFace4", "GetGlyphImageFormats"); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("Slot of an overload: %v", err)
	}
	if n, err := s.Slot("IDWriteTextFormat3", "SetFontAxisValues"); err != nil || n != 38 {
		t.Fatalf("IDWriteTextFormat3::SetFontAxisValues = %d, %v; the spike found 38", n, err)
	}
	it, _ := s.Interface("IDWriteTextLayout1")
	if !reflect.DeepEqual(it.Methods, []string{"SetPairKerning", "GetPairKerning", "SetCharacterSpacing", "GetCharacterSpacing"}) {
		t.Fatalf("IDWriteTextLayout1 methods %v", it.Methods)
	}
}
