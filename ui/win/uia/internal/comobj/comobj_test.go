//go:build windows

package comobj

import (
	"slices"
	"syscall"
	"testing"
	"unsafe"
)

// A handle names its entry only: a removed one finds nothing, also once its slot holds
// another entry, and the count follows.
func TestTable(t *testing.T) {
	var tb Table[string]
	a, b := tb.Add("a"), tb.Add("b")
	if a == 0 || b == 0 || a == b || tb.Len() != 2 {
		t.Fatalf("handles %v %v, len %d", a, b, tb.Len())
	}
	if v, ok := tb.Get(a); !ok || v != "a" {
		t.Fatalf("Get(a) = %q, %v", v, ok)
	}
	if !tb.Remove(a) || tb.Remove(a) || tb.Len() != 1 {
		t.Fatalf("Remove twice: len %d", tb.Len())
	}
	c := tb.Add("c") // takes a's slot
	if uint32(c) != uint32(a) || c == a {
		t.Fatalf("c %#x reuses a's slot %#x with another generation", c, a)
	}
	if _, ok := tb.Get(a); ok {
		t.Fatal("a stale handle found the slot's new entry")
	}
	if v, _ := tb.Get(c); v != "c" {
		t.Fatalf("Get(c) = %q", v)
	}
	for _, h := range []Handle{0, Handle(1<<32 | 99)} {
		if _, ok := tb.Get(h); ok || tb.Remove(h) {
			t.Fatalf("handle %#x found an entry", h)
		}
	}
}

// An object's interface pointers lead back to it, its count to the class's rules, and its
// handle is cleared when it is freed.
func TestClass(t *testing.T) {
	cb := syscall.NewCallback(func(this uintptr) uintptr { return this })
	v1, err := Vtable([]uintptr{cb, cb, cb})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := Vtable([]uintptr{cb, cb, cb, cb})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClass([]uintptr{v1, 0, v2})
	obj, err := c.New(42)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, -1, 2} {
		o, k, ok := c.Object(c.Iface(obj, i))
		if want < 0 {
			if ok {
				t.Errorf("interface %d (none) was found", i)
			}
			continue
		}
		if !ok || o != obj || k != want {
			t.Errorf("Object(Iface(%d)) = %#x, %d, %v", i, o, k, ok)
		}
	}
	if c.Handle(obj) != 42 || c.Refs(obj) != 1 || c.AddRef(obj) != 2 || c.Release(obj) != 1 || c.Release(obj) != 0 {
		t.Error("references")
	}
	c.Free(obj)
}

// VARIANT is 24 bytes on x64 with its value at 8; a BSTR keeps its text.
func TestVariantAndBSTR(t *testing.T) {
	if s, off := unsafe.Sizeof(Variant{}), unsafe.Offsetof(Variant{}.Val); s != 24 || off != 8 {
		t.Fatalf("VARIANT size %d, value at %d", s, off)
	}
	var v Variant
	v.SetR8(-2.5)
	if v.VT != VT_R8 || v.R8() != -2.5 {
		t.Errorf("R8 %+v", v)
	}
	v.SetBool(true)
	if v.VT != VT_BOOL || !v.Bool() || uint16(v.Val) != 0xFFFF {
		t.Errorf("BOOL %+v", v)
	}
	v.SetI4(-25)
	if v.I4() != -25 {
		t.Errorf("I4 %+v", v)
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	var buf []uint16
	for _, s := range []string{"", "Downex", "日本語の説明書.pdf", "📦 a"} {
		b := NewBSTR(&buf, s)
		if b == 0 {
			t.Fatal("SysAllocStringLen failed")
		}
		if got := BSTRString(b); got != s {
			t.Errorf("BSTR %q became %q", s, got)
		}
		FreeBSTR(b)
	}
	psa, err := NewI4Array([]int32{3, 7, -1})
	if err != nil {
		t.Fatal(err)
	}
	if got := I4Values(psa); !slices.Equal(got, []int32{3, 7, -1}) {
		t.Errorf("SAFEARRAY %v", got)
	}
	DestroyArray(psa)
}
