//go:build darwin && cgo

package mac

// [ax] An in-process accessibility client: it reads a window through AppKit's legacy
// NSAccessibility API (accessibilityAttributeValue: and friends), which is what the
// system's accessibility server calls when VoiceOver asks, so tests and macspike see what
// VoiceOver sees without the Accessibility permission an AXUIElement client needs.
// Everything here belongs to the main thread.

/*
#include <stdlib.h>
#include "ax_darwin.h"
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"

	"downex/ui/kit"
)

// AXElement is one accessibility object (an element, a native field, the window) as a
// client holds it: retained until Release.
type AXElement struct{ p unsafe.Pointer }

// AXRange is an NSRange value.
type AXRange struct{ Location, Length int }

// AXWindow is the window itself as an accessibility element (AXWindow).
func (w *Window) AXWindow() *AXElement {
	if w.closed || w.ns == nil {
		return nil
	}
	return &AXElement{C.dxaxWindowElement(w.ns)}
}

func newAXElement(p unsafe.Pointer) *AXElement {
	if p == nil {
		return nil
	}
	return &AXElement{p}
}

// Release drops the client's reference. It is idempotent.
func (e *AXElement) Release() {
	if e != nil && e.p != nil {
		C.dxaxRelease(e.p)
		e.p = nil
	}
}

// Object is the Objective-C object (unretained: valid while e is), for test tools.
func (e *AXElement) Object() unsafe.Pointer { return e.p }

// Same reports whether e and o are the same object.
func (e *AXElement) Same(o *AXElement) bool { return e != nil && o != nil && e.p == o.p }

// Class is the object's Objective-C class.
func (e *AXElement) Class() string {
	s := C.dxaxClassName(e.p)
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s)
}

// Attribute reads an attribute (with up to two integer parameters for a parameterized one,
// such as AXCellForColumnAndRow). The value is a string, a float64, an *AXElement, a
// []*AXElement, a kit.Rect (a frame: screen points, y up), a kit.Point, a kit.Size, an
// AXRange, or nil.
func (e *AXElement) Attribute(name string, params ...int) any {
	p0, p1 := 0, 0
	if len(params) > 0 {
		p0 = params[0]
	}
	if len(params) > 1 {
		p1 = params[1]
	}
	return e.attribute(name, len(params), p0, p1)
}

// LegacyAttribute reads an attribute through accessibilityAttributeValue: itself, for an
// element too (the in-process client otherwise calls an element's methods): the route by
// which the system's server reads WebKit's extra attributes (AXMenuItemMarkChar and the
// others) of an element. AppKit answers the protocol's own attributes there only to the
// server, not in-process.
func (e *AXElement) LegacyAttribute(name string) any { return e.attribute(name, -1, 0, 0) }

// LegacyAttributeNames is accessibilityAttributeNames itself (see LegacyAttribute).
func (e *AXElement) LegacyAttributeNames() []string { return e.names(3) }

func (e *AXElement) attribute(name string, nparam, p0, p1 int) any {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	var v C.dxaxVal
	C.dxaxAttr(e.p, cs, C.int(nparam), C.int(p0), C.int(p1), &v)
	switch v.kind {
	case 1:
		defer C.free(unsafe.Pointer(v.s))
		return C.GoString(v.s)
	case 2:
		return float64(v.n)
	case 3:
		return newAXElement(v.e)
	case 4:
		defer C.free(unsafe.Pointer(v.es))
		out := make([]*AXElement, 0, int(v.count))
		for _, p := range unsafe.Slice(v.es, int(v.count)) {
			out = append(out, newAXElement(p))
		}
		return out
	case 5:
		return kit.R(float32(v.x), float32(v.y), float32(v.x+v.w), float32(v.y+v.h))
	case 6:
		return kit.Pt(float32(v.x), float32(v.y))
	case 7:
		return kit.Size{W: float32(v.w), H: float32(v.h)}
	case 8:
		return AXRange{int(v.x), int(v.w)}
	}
	return nil
}

// String is a string attribute, "" when it is none.
func (e *AXElement) String(name string) string {
	s, _ := e.Attribute(name).(string)
	return s
}

// Number is a number attribute (booleans are 0 and 1), and whether it is one.
func (e *AXElement) Number(name string) (float64, bool) {
	n, ok := e.Attribute(name).(float64)
	return n, ok
}

// Element is an element attribute, or nil.
func (e *AXElement) Element(name string) *AXElement {
	el, _ := e.Attribute(name).(*AXElement)
	return el
}

// Elements is an array attribute's elements.
func (e *AXElement) Elements(name string) []*AXElement {
	es, _ := e.Attribute(name).([]*AXElement)
	return es
}

// Children are the element's AXChildren.
func (e *AXElement) Children() []*AXElement { return e.Elements("AXChildren") }

func (e *AXElement) names(what int) []string {
	s := C.dxaxNames(e.p, C.int(what))
	defer C.free(unsafe.Pointer(s))
	if *s == 0 {
		return nil
	}
	return strings.Split(C.GoString(s), "\n")
}

// AttributeNames, ActionNames and ParameterizedAttributeNames list what the object has.
func (e *AXElement) AttributeNames() []string              { return e.names(0) }
func (e *AXElement) ActionNames() []string                 { return e.names(1) }
func (e *AXElement) ParameterizedAttributeNames() []string { return e.names(2) }

// Settable reports whether an attribute can be set.
func (e *AXElement) Settable(name string) bool {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	return bool(C.dxaxSettable(e.p, cs))
}

// Set sets an attribute to a string, a float64, a bool or a []*AXElement.
func (e *AXElement) Set(name string, v any) error {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	switch v := v.(type) {
	case string:
		s := C.CString(v)
		defer C.free(unsafe.Pointer(s))
		C.dxaxSet(e.p, cs, 1, s, 0, nil, 0)
	case float64:
		C.dxaxSet(e.p, cs, 2, nil, C.double(v), nil, 0)
	case bool:
		n := 0.0
		if v {
			n = 1
		}
		C.dxaxSet(e.p, cs, 3, nil, C.double(n), nil, 0)
	case []*AXElement:
		ps := C.malloc(C.size_t(len(v)+1) * C.size_t(unsafe.Sizeof(uintptr(0))))
		defer C.free(ps)
		s := unsafe.Slice((*unsafe.Pointer)(ps), len(v)+1)
		for i, el := range v {
			s[i] = el.p
		}
		C.dxaxSet(e.p, cs, 4, nil, 0, (*unsafe.Pointer)(ps), C.int(len(v)))
	default:
		return fmt.Errorf("mac: cannot set %s to a %T", name, v)
	}
	return nil
}

// Perform performs an action (AXPress, AXIncrement, AXScrollToVisible, ...).
func (e *AXElement) Perform(action string) {
	cs := C.CString(action)
	defer C.free(unsafe.Pointer(cs))
	C.dxaxPerform(e.p, cs)
}

// HitTest is the element at a screen point (Cocoa's coordinates: y up), or nil.
func (e *AXElement) HitTest(p kit.Point) *AXElement {
	return newAXElement(C.dxaxHit(e.p, C.double(p.X), C.double(p.Y)))
}

// AXDump writes the tree under e, depth levels deep: role, subrole, label, value and
// identifier of each element, indented.
func AXDump(e *AXElement, depth int) string {
	var b strings.Builder
	var walk func(e *AXElement, level int)
	walk = func(e *AXElement, level int) {
		b.WriteString(strings.Repeat("  ", level))
		b.WriteString(e.String("AXRole"))
		if s := e.String("AXSubrole"); s != "" {
			b.WriteString("/" + s)
		}
		for _, a := range []string{"AXTitle", "AXDescription", "AXIdentifier"} {
			if s := e.String(a); s != "" {
				fmt.Fprintf(&b, " %s=%q", strings.TrimPrefix(a, "AX"), s)
			}
		}
		switch v := e.Attribute("AXValue").(type) {
		case string:
			if v != "" {
				fmt.Fprintf(&b, " Value=%q", v)
			}
		case float64:
			fmt.Fprintf(&b, " Value=%g", v)
		case *AXElement:
			v.Release()
		}
		b.WriteByte('\n')
		if level >= depth {
			return
		}
		for _, c := range e.Children() {
			walk(c, level+1)
			c.Release()
		}
	}
	walk(e, 0)
	return b.String()
}

// AXCounters are what the accessibility of every window of the process posted, for tests.
type AXCounters struct {
	// Posted counts the notifications by name.
	Posted map[string]int
	// Announcements counts the announcements; LastAnnouncement and LastPriority (1 medium,
	// 2 high) are the last one's.
	Announcements    int
	LastAnnouncement string
	LastPriority     int
	// LastTarget is the object the last announcement was posted about (compare it with
	// a window's AXWindow().Object()).
	LastTarget unsafe.Pointer
	// Refused counts the requests refused because they came off the main thread.
	Refused int
}

var axNotificationNames = [...]string{
	"AXUIElementDestroyed", "AXFocusedUIElementChanged", "AXValueChanged", "AXTitleChanged",
	"AXSelectedChildrenChanged", "AXSelectedRowsChanged", "AXRowCountChanged", "AXLayoutChanged", "AXExpandedChanged",
}

// AXStats reads the counters.
func AXStats() AXCounters {
	c := AXCounters{Posted: map[string]int{}, Refused: int(C.dxaxRefused())}
	for i, n := range axNotificationNames {
		c.Posted[n] = int(C.dxaxPosted(C.int(i)))
	}
	c.Announcements = int(C.dxaxPosted(C.DXAX_N_COUNT))
	var p C.int
	var target unsafe.Pointer
	if s := C.dxaxLastAnnouncement(&p, &target); s != nil {
		c.LastAnnouncement = C.GoString(s)
		C.free(unsafe.Pointer(s))
	}
	c.LastPriority, c.LastTarget = int(p), target
	return c
}

// AXRegistered is the number of hosts (windows, popups) whose accessibility is registered
// and not yet closed, for tests: a closed host is not kept.
func AXRegistered() int { return len(axByHost) }

// AXElements is the number of accessibility elements w's bridge keeps (0 while no client
// has read it).
func (w *Window) AXElements() int {
	if a := axByHost[w]; a != nil && a.b != nil {
		return a.b.Len()
	}
	return 0
}
