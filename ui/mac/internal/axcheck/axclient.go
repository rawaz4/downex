//go:build darwin && cgo

package axcheck

// [ax] A real AXUIElement client of this process (the HIServices API VoiceOver's side
// uses), for the checks the in-process NSAccessibility client cannot make: what the
// accessibility server itself answers. It reads its own process, which needs no
// Accessibility permission for most requests; called off the main thread, each request
// goes to the main thread's run loop as a cross-process one does.

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <ApplicationServices/ApplicationServices.h>

static int axTrusted(void) { return AXIsProcessTrusted(); }

static void *axApp(void) {
	AXUIElementRef a = AXUIElementCreateApplication(getpid());
	AXUIElementSetMessagingTimeout(a, 5.0);
	return (void *)a;
}

static void axRelease(void *p) { if (p) CFRelease((CFTypeRef)p); }

static char *axStr(CFStringRef s) {
	if (!s) return NULL;
	CFIndex n = CFStringGetMaximumSizeForEncoding(CFStringGetLength(s), kCFStringEncodingUTF8) + 1;
	char *b = malloc(n);
	if (!CFStringGetCString(s, b, n, kCFStringEncodingUTF8)) b[0] = 0;
	return b;
}

// axString reads a string attribute (malloc'd, or NULL); *err is the AXError.
static char *axString(void *e, const char *name, int *err) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFTypeRef v = NULL;
	*err = AXUIElementCopyAttributeValue((AXUIElementRef)e, n, &v);
	CFRelease(n);
	if (*err || !v) return NULL;
	char *r = NULL;
	if (CFGetTypeID(v) == CFStringGetTypeID()) r = axStr((CFStringRef)v);
	CFRelease(v);
	return r;
}

// axNumber reads a number attribute; *err is the AXError.
static double axNumber(void *e, const char *name, int *err) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFTypeRef v = NULL;
	*err = AXUIElementCopyAttributeValue((AXUIElementRef)e, n, &v);
	CFRelease(n);
	if (*err || !v) return 0;
	double d = 0;
	if (CFGetTypeID(v) == CFNumberGetTypeID()) CFNumberGetValue((CFNumberRef)v, kCFNumberDoubleType, &d);
	else if (CFGetTypeID(v) == CFBooleanGetTypeID()) d = CFBooleanGetValue((CFBooleanRef)v);
	else *err = -1;
	CFRelease(v);
	return d;
}

// axElement reads an element attribute (+1, or NULL).
static void *axElement(void *e, const char *name, int *err) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFTypeRef v = NULL;
	*err = AXUIElementCopyAttributeValue((AXUIElementRef)e, n, &v);
	CFRelease(n);
	if (*err || !v) return NULL;
	if (CFGetTypeID(v) != AXUIElementGetTypeID()) {
		CFRelease(v);
		return NULL;
	}
	return (void *)v;
}

// axList reads an array attribute: *count elements (+1 each) in a malloc'd array.
static void **axList(void *e, const char *name, int *count, int *err) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFTypeRef v = NULL;
	*count = 0;
	*err = AXUIElementCopyAttributeValue((AXUIElementRef)e, n, &v);
	CFRelease(n);
	if (*err || !v) return NULL;
	if (CFGetTypeID(v) != CFArrayGetTypeID()) {
		CFRelease(v);
		return NULL;
	}
	CFIndex c = CFArrayGetCount((CFArrayRef)v);
	void **out = malloc(sizeof(void *) * (c + 1));
	for (CFIndex i = 0; i < c; i++) {
		CFTypeRef o = CFArrayGetValueAtIndex((CFArrayRef)v, i);
		CFRetain(o);
		out[i] = (void *)o;
	}
	*count = (int)c;
	CFRelease(v);
	return out;
}

// axCount is AXUIElementGetAttributeValueCount.
static long axCount(void *e, const char *name, int *err) {
	CFStringRef n = CFStringCreateWithCString(NULL, name, kCFStringEncodingUTF8);
	CFIndex c = 0;
	*err = AXUIElementGetAttributeValueCount((AXUIElementRef)e, n, &c);
	CFRelease(n);
	return c;
}

// axNames is the element's attribute names joined by newlines (malloc'd).
static char *axNames(void *e, int *err) {
	CFArrayRef a = NULL;
	*err = AXUIElementCopyAttributeNames((AXUIElementRef)e, &a);
	if (*err || !a) return NULL;
	CFStringRef j = CFStringCreateByCombiningStrings(NULL, a, CFSTR("\n"));
	char *r = axStr(j);
	CFRelease(j);
	CFRelease(a);
	return r;
}

static int axSame(void *a, void *b) { return a && b && CFEqual((CFTypeRef)a, (CFTypeRef)b); }
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

// axuiTrusted reports whether this process may read other processes (it may always read
// its own).
func axuiTrusted() bool { return C.axTrusted() != 0 }

// axui is an AXUIElementRef the client holds (+1).
type axui struct{ p unsafe.Pointer }

func axuiApp() *axui { return &axui{C.axApp()} }

func (e *axui) release() {
	if e != nil && e.p != nil {
		C.axRelease(e.p)
		e.p = nil
	}
}

func cstr(s string) *C.char { return C.CString(s) }

// str reads a string attribute and its AXError.
func (e *axui) str(name string) (string, int) {
	n := cstr(name)
	defer C.free(unsafe.Pointer(n))
	var err C.int
	s := C.axString(e.p, n, &err)
	if s == nil {
		return "", int(err)
	}
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s), int(err)
}

// num reads a number (or boolean) attribute and its AXError.
func (e *axui) num(name string) (float64, int) {
	n := cstr(name)
	defer C.free(unsafe.Pointer(n))
	var err C.int
	v := C.axNumber(e.p, n, &err)
	return float64(v), int(err)
}

func (e *axui) role() string { s, _ := e.str("AXRole"); return s }

// elem reads an element attribute.
func (e *axui) elem(name string) (*axui, int) {
	n := cstr(name)
	defer C.free(unsafe.Pointer(n))
	var err C.int
	p := C.axElement(e.p, n, &err)
	if p == nil {
		return nil, int(err)
	}
	return &axui{p}, int(err)
}

// list reads an array attribute.
func (e *axui) list(name string) ([]*axui, int) {
	n := cstr(name)
	defer C.free(unsafe.Pointer(n))
	var err, count C.int
	p := C.axList(e.p, n, &count, &err)
	if p == nil {
		return nil, int(err)
	}
	defer C.free(unsafe.Pointer(p))
	out := make([]*axui, 0, int(count))
	for _, q := range unsafe.Slice((*unsafe.Pointer)(unsafe.Pointer(p)), int(count)) {
		out = append(out, &axui{q})
	}
	return out, int(err)
}

func (e *axui) count(name string) (int, int) {
	n := cstr(name)
	defer C.free(unsafe.Pointer(n))
	var err C.int
	c := C.axCount(e.p, n, &err)
	return int(c), int(err)
}

func (e *axui) names() ([]string, int) {
	var err C.int
	s := C.axNames(e.p, &err)
	if s == nil {
		return nil, int(err)
	}
	defer C.free(unsafe.Pointer(s))
	return strings.Split(C.GoString(s), "\n"), int(err)
}

func (e *axui) same(o *axui) bool { return e != nil && o != nil && C.axSame(e.p, o.p) != 0 }

func releaseAll(es []*axui) {
	for _, e := range es {
		e.release()
	}
}

// axuiWindow is this process's window titled title, as the server lists it.
func axuiWindow(title string) (*axui, error) {
	app := axuiApp()
	defer app.release()
	wins, err := app.list("AXWindows")
	if err != 0 {
		return nil, fmt.Errorf("AXWindows: AXError %d", err)
	}
	var found *axui
	var seen []string
	for _, w := range wins {
		t, _ := w.str("AXTitle")
		seen = append(seen, t)
		if found == nil && t == title {
			found = w
			continue
		}
		w.release()
	}
	if found == nil {
		return nil, fmt.Errorf("no window %q among %q", title, seen)
	}
	return found, nil
}

// axuiDump is e's subtree as role lines, at most depth levels, with each error met.
func axuiDump(e *axui, depth int) string {
	var b strings.Builder
	var walk func(e *axui, d int)
	walk = func(e *axui, d int) {
		r, rerr := e.str("AXRole")
		desc, _ := e.str("AXDescription")
		fmt.Fprintf(&b, "%s%s", strings.Repeat("  ", d), r)
		if rerr != 0 {
			fmt.Fprintf(&b, " [role err %d]", rerr)
		}
		if desc != "" {
			fmt.Fprintf(&b, " %q", desc)
		}
		if d >= depth {
			b.WriteString("\n")
			return
		}
		kids, err := e.list("AXChildren")
		if err != 0 {
			fmt.Fprintf(&b, " [children err %d]\n", err)
			return
		}
		b.WriteString("\n")
		for _, k := range kids {
			walk(k, d+1)
		}
		releaseAll(kids)
	}
	walk(e, 0)
	return b.String()
}
