//go:build windows

package win

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// A synthetic IDataObject, as a browser or Explorer offers its drag: a COM object whose
// GetData and QueryGetData answer the formats it holds in global memory. The tests call the
// window's IDropTarget methods with it directly, without DoDragDrop (which needs a real
// mouse).

type fakeData struct{ formats map[uint16][]byte }

var (
	fakeOnce sync.Once
	fakeVtbl uintptr
	fakes    sync.Map // object address → *fakeData
)

const dvEFormatEtc = 0x80040064

func fakeVtable() uintptr {
	fakeOnce.Do(func() {
		notImpl := syscall.NewCallback(func(this uintptr) uintptr { return 0x80004001 })
		m := make([]uintptr, 12)
		for i := range m {
			m[i] = notImpl
		}
		m[0] = syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			*(*uintptr)(com.Addr(ppv)) = this
			return 0
		})
		m[1] = syscall.NewCallback(func(this uintptr) uintptr { return 2 })
		m[2] = syscall.NewCallback(func(this uintptr) uintptr { return 1 })
		m[slotDOGetData] = syscall.NewCallback(func(this, pfe, pmed uintptr) uintptr {
			v, _ := fakes.Load(this)
			fe := (*formatEtc)(com.Addr(pfe))
			b, ok := v.(*fakeData).formats[fe.cf]
			if !ok || fe.tymed&tymedHGlobal == 0 {
				return dvEFormatEtc
			}
			mem, err := w32.GlobalAlloc(uintptr(len(b)))
			if err != nil {
				return 0x8007000E
			}
			p := w32.GlobalLock(mem)
			copy(unsafe.Slice((*byte)(com.Addr(p)), len(b)), b)
			w32.GlobalUnlock(mem)
			*(*stgMedium)(com.Addr(pmed)) = stgMedium{tymed: tymedHGlobal, handle: mem}
			return 0
		})
		m[slotDOQueryData] = syscall.NewCallback(func(this, pfe uintptr) uintptr {
			v, _ := fakes.Load(this)
			if _, ok := v.(*fakeData).formats[(*formatEtc)(com.Addr(pfe)).cf]; ok {
				return 0
			}
			return dvEFormatEtc
		})
		p, _ := windows.LocalAlloc(0x40, uint32(len(m))*uint32(unsafe.Sizeof(uintptr(0))))
		copy(unsafe.Slice((*uintptr)(com.Addr(p)), len(m)), m)
		fakeVtbl = p
	})
	return fakeVtbl
}

func newFakeData(t *testing.T, formats map[uint16][]byte) uintptr {
	obj, _ := windows.LocalAlloc(0x40, uint32(unsafe.Sizeof(uintptr(0))))
	*(*uintptr)(com.Addr(obj)) = fakeVtable()
	fakes.Store(obj, &fakeData{formats: formats})
	t.Cleanup(func() { fakes.Delete(obj); windows.LocalFree(windows.Handle(obj)) })
	return obj
}

func wideZ(s string) []byte {
	u := append(utf16.Encode([]rune(s)), 0)
	return unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*2)
}

// hdrop is a CF_HDROP block (DROPFILES, wide names, a double NUL).
func hdrop(paths ...string) []byte {
	b := []byte{20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0} // pFiles 20, fWide
	for _, p := range paths {
		b = append(b, wideZ(p)...)
	}
	return append(b, 0, 0)
}

func cfHTMLClip(fragment string) []byte {
	const head = "Version:0.9\r\nStartHTML:%08d\r\nEndHTML:%08d\r\nStartFragment:%08d\r\nEndFragment:%08d\r\n"
	pre, post := "<html><body>\r\n<!--StartFragment-->", "<!--EndFragment-->\r\n</body></html>"
	h := len(fmt.Sprintf(head, 0, 0, 0, 0))
	sf := h + len(pre)
	ef := sf + len(fragment)
	return []byte(fmt.Sprintf(head, h, ef+len(post), sf, ef) + pre + fragment + post)
}

// recorder is a DropHandler that answers as told and records what it was asked.
type recorder struct {
	over, drop DropAction
	events     []string
	last       DropEvent
	data       DropData
}

func (r *recorder) DragOver(e DropEvent) DropAction {
	r.events, r.last, r.data = append(r.events, "over"), e, *e.Data
	return r.over
}
func (r *recorder) Drop(e DropEvent) DropAction {
	r.events, r.last, r.data = append(r.events, "drop"), e, *e.Data
	return r.drop
}
func (r *recorder) DragLeave() { r.events = append(r.events, "leave") }

// effectMem is a DWORD outside the Go heap that the target writes its effect to.
var effectMem uintptr

func init() {
	effectMem, _ = windows.LocalAlloc(0x40, 4)
}

func effect() *uint32 { return (*uint32)(com.Addr(effectMem)) }

// callTarget calls method (DragEnter, DragOver, Drop) of the window's drop target at the
// window's client point p (DIPs), with the source allowing allowed.
func callTarget(t *testing.T, w *Window, method int, data uintptr, p kit.Point, allowed uint32) (uint32, uintptr) {
	t.Helper()
	obj := w.dropObject()
	if obj == 0 {
		t.Fatal("no drop target")
	}
	vt := unsafe.Slice((*uintptr)(com.Addr(*(*uintptr)(com.Addr(obj)))), dropMethods)
	scr := w32.ClientToScreen(w.hwnd, w32.Point{X: int32(p.X * w.ppd()), Y: int32(p.Y * w.ppd())})
	pt := uintptr(uint64(uint32(scr.X)) | uint64(uint32(scr.Y))<<32)
	*effect() = allowed
	var r uintptr
	switch method {
	case slotDTDragEnter, slotDTDrop:
		r, _, _ = syscall.SyscallN(vt[method], obj, data, 1 /* MK_LBUTTON */, pt, effectMem)
	case slotDTDragOver:
		r, _, _ = syscall.SyscallN(vt[method], obj, 1, pt, effectMem)
	case slotDTDragLeave:
		r, _, _ = syscall.SyscallN(vt[method], obj)
	}
	return *effect(), r
}

// What browsers and Explorer offer reads as today's WebView2 page read it.
func TestDropDataAsTheWebViewSawIt(t *testing.T) {
	registerDropFormats()
	f := &dropFormats
	dir := t.TempDir()
	shortcut := filepath.Join(dir, "Example.url")
	if err := os.WriteFile(shortcut, []byte("[{000214A0-0000-0000-C000-000000000046}]\r\nProp3=19,11\r\n[InternetShortcut]\r\nIDList=\r\nURL=https://example.com/b.iso\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := "https://example.com/a.zip"
	for _, c := range []struct {
		name    string
		formats map[uint16][]byte
		want    DropData
		types   []string
	}{
		{"a link from Chrome or Edge", map[uint16][]byte{
			f.urlW: wideZ(link), f.urlA: append([]byte(link), 0), cfUnicodeText: wideZ(link),
			f.cfHTML: cfHTMLClip(`<a href="` + link + `">a.zip</a>`), f.groupW: make([]byte, 600),
		}, DropData{URIList: link, Text: link, HTML: `<a href="` + link + `">a.zip</a>`, Files: true}, []string{TypePlain, TypeURIList, TypeHTML, TypeFiles}},
		{"a link from Firefox", map[uint16][]byte{
			f.mozURL: wideZ(link + "\nThe title"), f.urlW: wideZ("https://other.example/"), cfUnicodeText: wideZ(link),
			f.textHTML: wideZ(`<a href="` + link + `">t</a>`),
		}, DropData{URIList: link, Text: link, HTML: `<a href="` + link + `">t</a>`}, []string{TypePlain, TypeURIList, TypeHTML}},
		{"a URL without its text", map[uint16][]byte{f.urlW: wideZ(link)}, DropData{URIList: link, Text: link}, []string{TypePlain, TypeURIList}},
		{"an ANSI URL", map[uint16][]byte{f.urlA: append([]byte(link), 0)}, DropData{URIList: link, Text: link}, []string{TypePlain, TypeURIList}},
		{"selected text", map[uint16][]byte{cfUnicodeText: wideZ("iso"), f.cfHTML: cfHTMLClip("<b>iso</b>")},
			DropData{Text: "iso", HTML: "<b>iso</b>"}, []string{TypePlain, TypeHTML}},
		{"ANSI text", map[uint16][]byte{cfText: []byte("plain\x00")}, DropData{Text: "plain"}, []string{TypePlain}},
		{"files from Explorer", map[uint16][]byte{cfHDROP: hdrop(`C:\Users\me\notes.txt`)}, DropData{Files: true}, []string{TypeFiles}},
		{"an Internet shortcut", map[uint16][]byte{cfHDROP: hdrop(shortcut)},
			DropData{URIList: "https://example.com/b.iso", Text: "https://example.com/b.iso", Files: true}, []string{TypePlain, TypeURIList, TypeFiles}},
		{"a URL format that is no URL", map[uint16][]byte{f.urlW: wideZ("not a url")}, DropData{}, nil},
		{"nothing readable", map[uint16][]byte{}, DropData{}, nil},
	} {
		obj := newFakeData(t, c.formats)
		tgt := &dropTarget{}
		got := tgt.read(obj)
		if got.URIList != c.want.URIList || got.Text != c.want.Text || got.HTML != c.want.HTML || got.Files != c.want.Files {
			t.Errorf("%s: read %+v, want %+v", c.name, got, c.want)
		}
		if types := got.Types(); !slices.Equal(types, c.types) {
			t.Errorf("%s: types %q, want %q", c.name, types, c.types)
		}
	}
}

// A dropped Internet shortcut is read once per drag: the drop takes the URL DragEnter read
// (the file is gone here by then), and the next drag reads its own.
func TestDropReadsAShortcutOncePerDrag(t *testing.T) {
	registerDropFormats()
	dir := t.TempDir()
	path := filepath.Join(dir, "Example.url")
	if err := os.WriteFile(path, []byte("[InternetShortcut]"+crlf+"URL=https://example.com/c.iso"+crlf), 0o600); err != nil {
		t.Fatal(err)
	}
	obj := newFakeData(t, map[uint16][]byte{cfHDROP: hdrop(path)})
	tgt := &dropTarget{}
	if got := tgt.read(obj).URIList; got != "https://example.com/c.iso" {
		t.Fatalf("DragEnter read %q", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := tgt.read(obj).URIList; got != "https://example.com/c.iso" {
		t.Fatalf("the drop read %q, want the URL DragEnter read", got)
	}
	tgt.shortcut = shortcutRead{} // a new drag (DragEnter) reads again
	if got := tgt.read(obj).URIList; got != "" {
		t.Fatalf("a new drag of a gone shortcut read %q", got)
	}
}

// The target answers what the handler decides, at the point in DIPs, with copy only when
// the source allows it; a drop reads the data again; leaving tells the handler; a drop the
// handler leaves to the text field under the pointer goes into that field, as a browser's
// field takes it, and the field tells its root as typing does.
func TestDropTargetCalls(t *testing.T) {
	r := newFieldRoot()
	runtimeLock(t)
	a, err := NewApp(AppOptions{ClassName: "DownexDropTest"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	mon := w32.MonitorFromPoint(w32.Point{}, w32.MONITOR_DEFAULTTOPRIMARY)
	mi, _ := w32.GetMonitorInfo(mon)
	w, err := a.NewWindow(r.win, WindowOptions{Size: kit.Size{W: 420, H: 80}, NoActivate: true, Software: true,
		At: w32.Point{X: mi.Work.Left + 40, Y: mi.Work.Top + 40}, Placed: true})
	if err != nil {
		t.Fatal(err)
	}
	pump(t, a, w, "the field", 5*time.Second, func() bool { return len(w.fields) == 1 && windows.IsWindowVisible(windows.HWND(w.fields[0].hwnd)) })
	rec := &recorder{over: DropCopy, drop: DropCopy}
	if err := w.EnableDrop(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.EnableDrop(rec); err != nil { // a second call replaces the handler
		t.Fatal(err)
	}
	registerDropFormats() // the data object is made before a drag reads (and registers) them; run alone, urlW was 0
	f := &dropFormats
	data := newFakeData(t, map[uint16][]byte{f.urlW: wideZ("https://example.com/a.zip"), cfUnicodeText: wideZ("https://example.com/a.zip")})
	grid := kit.Pt(10, 40) // the page beside the field
	if eff, hr := callTarget(t, w, slotDTDragEnter, data, grid, 7); eff != dropEffectCopy || hr != 0 {
		t.Fatalf("DragEnter: effect %d, hr %#x", eff, hr)
	}
	if rec.last.Field || rec.data.URIList != "https://example.com/a.zip" || absf(rec.last.At.X-10) > 1 || absf(rec.last.At.Y-40) > 1 {
		t.Fatalf("DragEnter asked %+v with %+v", rec.last, rec.data)
	}
	if eff, _ := callTarget(t, w, slotDTDragOver, 0, grid, 4 /* LINK only */); eff != dropEffectNone {
		t.Fatalf("a source that allows no copy got effect %d", eff)
	}
	rec.over = DropRefuse
	if eff, _ := callTarget(t, w, slotDTDragOver, 0, grid, 7); eff != dropEffectNone {
		t.Fatalf("a refused drag got effect %d", eff)
	}
	callTarget(t, w, slotDTDragLeave, 0, grid, 0)
	if !slices.Equal(rec.events, []string{"over", "over", "over", "leave"}) {
		t.Fatalf("events %q", rec.events)
	}

	// Over the field: the handler hears Field; a drop it leaves to the field is inserted.
	fr := w32.GetWindowRect(w.fields[0].hwnd)
	c := w32.ScreenToClient(w.hwnd, w32.Point{X: (fr.Left + fr.Right) / 2, Y: (fr.Top + fr.Bottom) / 2})
	over := kit.Pt(float32(c.X)/w.ppd(), float32(c.Y)/w.ppd())
	text := newFakeData(t, map[uint16][]byte{cfUnicodeText: wideZ("iso\r\nimage")})
	rec.over, rec.drop = DropToField, DropToField
	if eff, _ := callTarget(t, w, slotDTDragEnter, text, over, 3); eff != dropEffectCopy || !rec.last.Field {
		t.Fatalf("over the field: effect %d, %+v", eff, rec.last)
	}
	if eff, _ := callTarget(t, w, slotDTDrop, text, over, 3); eff != dropEffectCopy {
		t.Fatalf("drop into the field: effect %d", eff)
	}
	pump(t, a, w, "the field's change", 5*time.Second, func() bool { return len(r.changes) > 0 })
	if got := r.changes[len(r.changes)-1]; got != "isoimage" {
		t.Fatalf("the field took %q, want the text without its line break", got)
	}
	// DropToField where there is no field is refused.
	if eff, _ := callTarget(t, w, slotDTDragEnter, text, grid, 3); eff != dropEffectNone {
		t.Fatalf("DropToField off a field: effect %d", eff)
	}
	callTarget(t, w, slotDTDragLeave, 0, grid, 0)

	// A drag delivered while the UI thread is inside a call into the root (OLE's loop
	// pumping in a modal one) is refused without asking the handler: no re-entrant call.
	rec.over, rec.drop = DropCopy, DropCopy
	asked := len(rec.events)
	w.depth = 1
	eff, _ := callTarget(t, w, slotDTDragEnter, data, grid, 7)
	w.depth = 0
	if eff != dropEffectNone || len(rec.events) != asked {
		t.Fatalf("a drag inside a call: effect %d, handler asked %d times", eff, len(rec.events)-asked)
	}
	callTarget(t, w, slotDTDragLeave, 0, grid, 0)

	// The handler's panic refuses the drop instead of unwinding through OLE, and is reported
	// as a window procedure's is (Run raises it again); the window is back in order.
	w.EnableDrop(panicky{})
	if eff, hr := callTarget(t, w, slotDTDragEnter, data, grid, 7); eff != dropEffectNone || hr != 0 {
		t.Fatalf("a panicking handler: effect %d hr %#x", eff, hr)
	}
	if a.panicked == nil || !strings.Contains(a.panicked.Error(), "handler bug") {
		t.Fatalf("the handler's panic was not reported: %v", a.panicked)
	}
	if w.depth != 0 {
		t.Fatalf("the window was left inside a call (depth %d)", w.depth)
	}
	a.panicked = nil // reported; the test goes on
	obj := w.dropObject()
	w.DisableDrop()
	if w.dropObject() != 0 || targetOf(obj) != nil {
		t.Fatal("DisableDrop left the target")
	}
	// A target registered when the window is destroyed is revoked with it.
	if err := w.EnableDrop(rec); err != nil {
		t.Fatal(err)
	}
	obj = w.dropObject()
	w.Close()
	if targetOf(obj) != nil {
		t.Fatal("the window's destruction left its drop target")
	}
}

type panicky struct{}

func (panicky) DragOver(DropEvent) DropAction { panic("handler bug") }
func (panicky) Drop(DropEvent) DropAction     { panic("handler bug") }
func (panicky) DragLeave()                    {}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// CF_HTML's fragment is cut by its byte offsets, which count UTF-8 bytes.
func TestCFHTMLFragment(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"fragment", string(cfHTMLClip(`<a href="https://x/é">é</a>`)), `<a href="https://x/é">é</a>`},
		{"no fragment offsets", "Version:0.9\r\nStartHTML:00000051\r\nEndHTML:00000065\r\n<html>x</html>", "<html>x</html>"},
		{"offsets past the end", "Version:0.9\r\nStartFragment:00000010\r\nEndFragment:00009999\r\n<b>y</b>", "<b>y</b>"},
	} {
		if got := cfHTMLFragment([]byte(c.in)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// crlf is a Windows line break, as a .url file has them.
const crlf = "\x0d\x0a"
