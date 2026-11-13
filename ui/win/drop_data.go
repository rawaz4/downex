//go:build windows

package win

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"downex/ui/win/internal/com"
	"downex/ui/win/internal/w32"
)

// DropData is what a drag over the window carries, read from its OLE data object as the
// WebView2 page of today's window saw it (the DataTransfer's types and getData): browsers
// offer a dragged link as a URL format (UniformResourceLocatorW from Chrome and Edge,
// text/x-moz-url from Firefox) beside its text and HTML, and Explorer offers files.
type DropData struct {
	// URIList is getData('text/uri-list'): the URL of a dragged link (text/x-moz-url's
	// first line, else UniformResourceLocatorW's, else the ANSI UniformResourceLocator's),
	// or the URL in a dropped Internet shortcut (.url); "" when there is none or it does not
	// parse as an absolute URL.
	URIList string
	// Text is getData('text/plain'): CF_UNICODETEXT, else CF_TEXT, else the URL.
	Text string
	// HTML is getData('text/html'): the fragment of "HTML Format" (CF_HTML), else the
	// text/html format.
	HTML string
	// Files reports the type "Files": files (CF_HDROP) or virtual files (a file group
	// descriptor) are dropped.
	Files bool
	// hasText and hasHTML: the formats are offered (their text may be empty).
	hasText, hasHTML bool
}

// The DataTransfer types DropData reports.
const (
	TypeURIList = "text/uri-list"
	TypePlain   = "text/plain"
	TypeHTML    = "text/html"
	TypeFiles   = "Files"
)

// Types is the DataTransfer's types: text/plain, text/uri-list, text/html and Files, those
// that are there.
func (d *DropData) Types() []string {
	var t []string
	if d.hasText || d.Text != "" {
		t = append(t, TypePlain)
	}
	if d.URIList != "" {
		t = append(t, TypeURIList)
	}
	if d.hasHTML || d.HTML != "" {
		t = append(t, TypeHTML)
	}
	if d.Files {
		t = append(t, TypeFiles)
	}
	return t
}

// Has reports one of Types.
func (d *DropData) Has(typ string) bool {
	switch typ {
	case TypePlain:
		return d.hasText || d.Text != ""
	case TypeURIList:
		return d.URIList != ""
	case TypeHTML:
		return d.hasHTML || d.HTML != ""
	case TypeFiles:
		return d.Files
	}
	return false
}

// maxDropFormat bounds one format's bytes; a larger one is taken as not offered.
const maxDropFormat = 8 << 20

// Clipboard formats (WinUser.h and the shell's registered names).
const (
	cfText        = 1
	cfUnicodeText = 13
	cfHDROP       = 15
)

var dropFormats struct {
	once                                 sync.Once
	urlW, urlA, mozURL, cfHTML, textHTML uint16
	groupW, groupA                       uint16
}

func registerDropFormats() {
	dropFormats.once.Do(func() {
		f := &dropFormats
		f.urlW = registerClipboardFormat("UniformResourceLocatorW")
		f.urlA = registerClipboardFormat("UniformResourceLocator")
		f.mozURL = registerClipboardFormat("text/x-moz-url")
		f.cfHTML = registerClipboardFormat("HTML Format")
		f.textHTML = registerClipboardFormat("text/html")
		f.groupW = registerClipboardFormat("FileGroupDescriptorW")
		f.groupA = registerClipboardFormat("FileGroupDescriptor")
	})
}

// formatEtc is FORMATETC; stgMedium is STGMEDIUM (ObjIdl.h).
type formatEtc struct {
	cf     uint16
	ptd    uintptr
	aspect uint32
	lindex int32
	tymed  uint32
}

type stgMedium struct {
	tymed   uint32
	handle  uintptr // hGlobal for TYMED_HGLOBAL
	release uintptr // pUnkForRelease
}

const (
	dvaspectContent = 1
	tymedHGlobal    = 1
	slotDOGetData   = 3
	slotDOQueryData = 5
)

// dataReader reads one data object's formats. Its FORMATETC and STGMEDIUM live on the
// heap (in the drop target): a call into another process's data object runs this thread's
// message loop, so Go code may run on this goroutine meanwhile.
type dataReader struct {
	obj *com.Object
	fe  *formatEtc
	med *stgMedium
	// shortcut is the Internet shortcut read in this drag (nil: none kept): the file is read
	// once, at DragEnter, and Drop takes it from here (a share that answers slowly blocks
	// the UI thread and OLE's drag loop once, not twice).
	shortcut *shortcutRead
}

// shortcutRead is an Internet shortcut's path and the URL read from it.
type shortcutRead struct {
	path, url string
	ok        bool
}

// shortcutURL is the URL of the .url file at path, read once per drag.
func (r dataReader) shortcutURL(path string) string {
	if c := r.shortcut; c != nil {
		if c.ok && c.path == path {
			return c.url
		}
		u := shortcutURL(path)
		*c = shortcutRead{path: path, url: u, ok: true}
		return u
	}
	return shortcutURL(path)
}

func (r dataReader) query(cf uint16) bool {
	if cf == 0 {
		return false
	}
	*r.fe = formatEtc{cf: cf, aspect: dvaspectContent, lindex: -1, tymed: tymedHGlobal}
	hr, _, _ := syscall.SyscallN(r.obj.Method(slotDOQueryData), uintptr(unsafe.Pointer(r.obj)), uintptr(unsafe.Pointer(r.fe)))
	return com.HRESULT(hr) == 0
}

// get calls f with the format's global memory, or reports false.
func (r dataReader) get(cf uint16, f func(mem uintptr, size uintptr)) bool {
	if cf == 0 {
		return false
	}
	*r.fe = formatEtc{cf: cf, aspect: dvaspectContent, lindex: -1, tymed: tymedHGlobal}
	*r.med = stgMedium{}
	hr, _, _ := syscall.SyscallN(r.obj.Method(slotDOGetData), uintptr(unsafe.Pointer(r.obj)), uintptr(unsafe.Pointer(r.fe)), uintptr(unsafe.Pointer(r.med)))
	if com.HRESULT(hr).Failed() {
		return false
	}
	defer syscall.SyscallN(procReleaseStgMedium.Addr(), uintptr(unsafe.Pointer(r.med)))
	if r.med.tymed != tymedHGlobal || r.med.handle == 0 {
		return false
	}
	size := w32.GlobalSize(r.med.handle)
	if size > maxDropFormat {
		return false
	}
	p := w32.GlobalLock(r.med.handle)
	if p == 0 {
		return false
	}
	defer w32.GlobalUnlock(r.med.handle)
	f(p, size)
	return true
}

// bytes is a format's bytes.
func (r dataReader) bytes(cf uint16) ([]byte, bool) {
	var out []byte
	ok := r.get(cf, func(p, n uintptr) {
		out = bytes.Clone(unsafe.Slice((*byte)(com.Addr(p)), n))
	})
	return out, ok
}

// wide is a format's UTF-16 text up to its first NUL.
func (r dataReader) wide(cf uint16) (string, bool) {
	var out string
	ok := r.get(cf, func(p, n uintptr) { out = decodeWide(unsafe.Slice((*uint16)(com.Addr(p)), n/2)) })
	return out, ok
}

// ansi is a format's text in the system's ANSI code page, up to its first NUL.
func (r dataReader) ansi(cf uint16) (string, bool) {
	b, ok := r.bytes(cf)
	if !ok {
		return "", false
	}
	return decodeANSI(b), true
}

func decodeWide(units []uint16) string {
	for i, u := range units {
		if u == 0 {
			units = units[:i]
			break
		}
	}
	return string(utf16.Decode(units))
}

func decodeANSI(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	if len(b) == 0 {
		return ""
	}
	n, err := windows.MultiByteToWideChar(0 /* CP_ACP */, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n <= 0 {
		return string(b)
	}
	u := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(0, 0, &b[0], int32(len(b)), &u[0], n); err != nil {
		return string(b)
	}
	return string(utf16.Decode(u))
}

// read reads what the drag carries.
func (r dataReader) read() DropData {
	registerDropFormats()
	f := &dropFormats
	var d DropData
	hdrop := r.query(cfHDROP)
	d.Files = hdrop || r.query(f.groupW) || r.query(f.groupA)

	// The URL: an Internet shortcut among the files first, then the browsers' formats; the
	// first format there decides, as Chromium's drop data takes it.
	if hdrop {
		if path, ok := r.firstFile(); ok && strings.EqualFold(filepath.Ext(path), ".url") {
			d.URIList = r.shortcutURL(path)
		}
	}
	if d.URIList == "" {
		switch {
		case r.query(f.mozURL):
			s, _ := r.wide(f.mozURL)
			d.URIList = validURL(firstLine(s))
		case r.query(f.urlW):
			s, _ := r.wide(f.urlW)
			d.URIList = validURL(firstLine(s))
		case r.query(f.urlA):
			s, _ := r.ansi(f.urlA)
			d.URIList = validURL(firstLine(s))
		}
	}
	if s, ok := r.wide(cfUnicodeText); ok {
		d.Text, d.hasText = s, true
	} else if s, ok := r.ansi(cfText); ok {
		d.Text, d.hasText = s, true
	} else if d.URIList != "" {
		d.Text = d.URIList
	}
	if b, ok := r.bytes(f.cfHTML); ok {
		d.HTML, d.hasHTML = cfHTMLFragment(b), true
	} else if s, ok := r.wide(f.textHTML); ok {
		d.HTML, d.hasHTML = s, true
	}
	return d
}

// firstFile is the first path of the drop's CF_HDROP.
func (r dataReader) firstFile() (string, bool) {
	var path string
	ok := r.get(cfHDROP, func(p, _ uintptr) {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n, _, _ := syscall.SyscallN(procDragQueryFileW.Addr(), r.med.handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && int(n) < len(buf) {
			path = string(utf16.Decode(buf[:n]))
		}
	})
	return path, ok && path != ""
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// validURL is s when it parses as an absolute URL, else "".
func validURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") {
		return ""
	}
	return s
}

// maxShortcut bounds the Internet shortcut file read.
const maxShortcut = 64 << 10

// shortcutURL is the URL of an Internet shortcut (a .url file: its [InternetShortcut]
// section's URL=), or "".
func shortcutURL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, maxShortcut)
	n, _ := f.Read(b)
	text := string(b[:n])
	if len(text) >= 2 && text[0] == 0xff && text[1] == 0xfe { // UTF-16 LE
		u := make([]uint16, 0, n/2)
		for i := 2; i+1 < n; i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		text = string(utf16.Decode(u))
	}
	text = strings.TrimPrefix(text, string(rune(0xfeff))) // a UTF-8 byte order mark
	in := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(line, "[InternetShortcut]")
			continue
		}
		if !in {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(k), "URL") {
			return validURL(strings.TrimSpace(v))
		}
	}
	return ""
}

// cfHTMLFragment is the fragment of a CF_HTML clip (UTF-8 with a header of byte offsets):
// StartFragment to EndFragment, else StartHTML to EndHTML, else everything after the header.
func cfHTMLFragment(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	off := map[string]int{}
	header := b
	if i := bytes.IndexByte(b, '<'); i >= 0 {
		header = b[:i]
	}
	for _, line := range strings.Split(string(header), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			off[k] = n
		}
	}
	span := func(a, z string) (string, bool) {
		s, ok1 := off[a]
		e, ok2 := off[z]
		if !ok1 || !ok2 || s < 0 || e < s || e > len(b) {
			return "", false
		}
		return string(b[s:e]), true
	}
	if s, ok := span("StartFragment", "EndFragment"); ok {
		return s
	}
	if s, ok := span("StartHTML", "EndHTML"); ok {
		return s
	}
	return string(b[len(header):])
}
