//go:build windows

package win

import (
	"fmt"
	"runtime"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"downex/ui/kit"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/dwrite"
	"downex/ui/win/internal/wic"

	"golang.org/x/sys/windows"
)

// Resources is the process's device-independent drawing resources: the Direct2D and
// DirectWrite factories, the system fonts and the text formats made from them, stroke
// styles, and WIC for images. It implements kit.Resources; every window and offscreen
// target of the process draws what it makes. It belongs to one goroutine at a time (the UI
// thread once there are windows). Close releases it; layouts, paths and images it made must
// be released first, and draw nothing afterwards.
type Resources struct {
	d2d     *d2d.Factory
	dw      *dwrite.Factory
	dw6     *dwrite.Factory6       // nil before Windows 10 1903
	fonts   *dwrite.FontCollection // the WWS model: the families CSS names
	typo    *dwrite.FontCollection // the typographic model, made on first use
	textErr error                  // why DirectWrite could not start
	tnum    *dwrite.Typography     // 'tnum' on, for TextStyle.Tabular
	wic     *wic.ImagingFactory

	formats  map[kit.TextStyle]*textFormat
	families map[string]string // CSS font-family list -> the installed family set in it
	faces    map[faceKey]*fontFace
	strokes  map[strokeKey]*d2d.StrokeStyle
	rrects   map[rrectKey]*path // rounded rectangles whose corners differ, at (0, 0)
	rings    map[ringKey]*path  // sharp shadows, at (0, 0)
	locale   *uint16

	// Scratch buffers for the UI thread's calls, so that laying out and hit-testing text
	// allocate nothing once they have grown.
	u16      []uint16
	lines    []dwrite.LineMetrics
	clusters []dwrite.ClusterMetrics
	hits     []dwrite.HitTestMetrics

	closed bool
	// live counts what was made and not yet released (tests check for leaks).
	live struct{ layouts, paths, images int }
}

// goarch is the architecture the constructors check (a variable so that tests can see the
// refusal on amd64).
var goarch = runtime.GOARCH

// archErr says why this build cannot draw; nil on amd64.
func archErr(goarch string) error {
	if err := com.Supported(goarch); err != nil {
		return fmt.Errorf("win: the native window needs the x64 build of Downex (this is windows/%s): %w: %w", goarch, err, kit.ErrUnsupported)
	}
	return nil
}

var mtaOnce = sync.OnceValue(com.KeepMTA)

// NewResources makes the process's resources. It needs no window. DirectWrite is started
// by the first text layout or font metrics, so a target that draws no text never loads it.
func NewResources() (*Resources, error) {
	if err := archErr(goarch); err != nil {
		return nil, err
	}
	r := &Resources{
		formats:  map[kit.TextStyle]*textFormat{},
		families: map[string]string{},
		faces:    map[faceKey]*fontFace{},
		strokes:  map[strokeKey]*d2d.StrokeStyle{},
		rrects:   map[rrectKey]*path{},
		rings:    map[ringKey]*path{},
	}
	var err error
	if r.d2d, err = d2d.CreateFactory(); err != nil {
		return nil, fmt.Errorf("win: Direct2D: %w", err)
	}
	r.locale, _ = windows.UTF16PtrFromString("en-us")
	return r, nil
}

// text starts DirectWrite on first use: its factory, the system fonts in both family models
// and the tabular-figures typography. A failure is kept and returned again.
func (r *Resources) text() error {
	if r.dw != nil || r.textErr != nil {
		return r.textErr
	}
	fail := func(err error) error {
		com.Release(&r.tnum)
		com.Release(&r.fonts)
		com.Release(&r.dw6)
		com.Release(&r.dw)
		r.textErr = err
		return err
	}
	dw, err := dwrite.CreateFactory()
	if err != nil {
		return fail(fmt.Errorf("win: DirectWrite: %w", err))
	}
	r.dw = dw
	var hr com.HRESULT
	if r.fonts, hr = r.dw.GetSystemFontCollection(); hr.Failed() {
		return fail(fmt.Errorf("win: %w", com.Check("IDWriteFactory::GetSystemFontCollection", hr)))
	}
	r.dw6, _ = com.Query[dwrite.Factory6](&r.dw.Object, &dwrite.IID_IDWriteFactory6)
	if r.tnum, hr = r.dw.CreateTypography(); hr.Failed() {
		return fail(fmt.Errorf("win: %w", com.Check("IDWriteFactory::CreateTypography", hr)))
	}
	// A typography replaces DirectWrite's default features, so the defaults CSS keeps on
	// (kerning, standard and contextual ligatures) are named next to tabular figures.
	for _, feat := range []string{"tnum", "kern", "liga", "clig", "calt"} {
		if hr = r.tnum.AddFontFeature(dwrite.FourCC(feat), 1); hr.Failed() {
			return fail(fmt.Errorf("win: %w", com.Check("IDWriteTypography::AddFontFeature", hr)))
		}
	}
	return nil
}

// Close releases everything r holds. It is idempotent.
func (r *Resources) Close() {
	if r.closed {
		return
	}
	r.closed = true
	for k, f := range r.formats {
		f.release()
		delete(r.formats, k)
	}
	for k, f := range r.faces {
		f.release()
		delete(r.faces, k)
	}
	for k, s := range r.strokes {
		s.Release()
		delete(r.strokes, k)
	}
	for k, p := range r.rrects {
		p.Release()
		delete(r.rrects, k)
	}
	for k, p := range r.rings {
		p.Release()
		delete(r.rings, k)
	}
	com.Release(&r.tnum)
	com.Release(&r.typo)
	com.Release(&r.fonts)
	com.Release(&r.dw6)
	com.Release(&r.wic)
	com.Release(&r.dw)
	com.Release(&r.d2d)
}

var errClosed = fmt.Errorf("win: resources closed: %w", kit.ErrClosed)

func (r *Resources) usable() error {
	if r == nil || r.closed {
		return errClosed
	}
	return nil
}

// imaging is the WIC factory, made on first use.
func (r *Resources) imaging() (*wic.ImagingFactory, error) {
	if r.wic != nil {
		return r.wic, nil
	}
	if err := mtaOnce(); err != nil {
		return nil, fmt.Errorf("win: COM: %w", err)
	}
	f, err := wic.NewImagingFactory()
	if err != nil {
		return nil, fmt.Errorf("win: WIC: %w", err)
	}
	r.wic = f
	return f, nil
}

// utf16Of converts s into r.u16 (reused) and returns it with a terminating NUL that the
// returned length excludes. Invalid UTF-8 becomes U+FFFD, one per byte, as Go reads it.
func (r *Resources) utf16Of(s string) ([]uint16, int) {
	b := r.u16[:0]
	for _, c := range s {
		b = utf16.AppendRune(b, c)
	}
	n := len(b)
	b = append(b, 0)
	r.u16 = b
	return b, n
}

// utf16Len is the number of UTF-16 units of s[:end], end moved back to a rune start.
func utf16Len(s string, end int) int {
	end = min(max(end, 0), len(s))
	for end > 0 && end < len(s) && !utf8.RuneStart(s[end]) {
		end--
	}
	n := 0
	for _, c := range s[:end] {
		if c >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf8Offset is the byte offset in s of UTF-16 index i (clamped to s).
func utf8Offset(s string, i int) int {
	n := 0
	for off, c := range s {
		if n >= i {
			return off
		}
		if c >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(s)
}

// strokeKey is a stroke style: caps, join and dashes in multiples of the stroke width
// (Direct2D's unit for dashes).
type strokeKey struct {
	cap, join uint32
	on, off   float32
}

// strokeStyle is the cached style of s at width w.
func (r *Resources) strokeStyle(s kit.Stroke, w float32) *d2d.StrokeStyle {
	k := strokeKey{cap: d2d.CAP_STYLE_FLAT, join: d2d.LINE_JOIN_MITER_OR_BEVEL}
	switch s.Cap {
	case kit.CapRound:
		k.cap = d2d.CAP_STYLE_ROUND
	case kit.CapSquare:
		k.cap = d2d.CAP_STYLE_SQUARE
	}
	switch s.Join {
	case kit.JoinRound:
		k.join = d2d.LINE_JOIN_ROUND
	case kit.JoinBevel:
		k.join = d2d.LINE_JOIN_BEVEL
	}
	if s.Dash.Off > 0 && s.Dash.On >= 0 && w > 0 && finite(s.Dash.On) && finite(s.Dash.Off) {
		k.on, k.off = s.Dash.On/w, s.Dash.Off/w
	}
	if st, ok := r.strokes[k]; ok {
		return st
	}
	// SVG's miter limit is 4 stroke widths; Direct2D's is in half widths.
	p := d2d.StrokeStyleProperties{StartCap: k.cap, EndCap: k.cap, DashCap: k.cap, LineJoin: k.join, MiterLimit: 8}
	var st *d2d.StrokeStyle
	var hr com.HRESULT
	if k.off > 0 {
		p.DashStyle = d2d.DASH_STYLE_CUSTOM
		dashes := [2]float32{k.on, k.off}
		st, hr = r.d2d.CreateStrokeStyle(&p, &dashes[0], 2)
	} else {
		st, hr = r.d2d.CreateStrokeStyle(&p, nil, 0)
	}
	if hr.Failed() {
		return nil
	}
	if len(r.strokes) >= 256 { // widths that keep changing: start over rather than grow
		for kk, old := range r.strokes {
			old.Release()
			delete(r.strokes, kk)
		}
	}
	r.strokes[k] = st
	return st
}

func finite(v float32) bool { return v-v == 0 }

func finitePt(p kit.Point) bool { return finite(p.X) && finite(p.Y) }

func finiteRect(r kit.Rect) bool { return finitePt(r.Min) && finitePt(r.Max) }

// errInvalid wraps kit.ErrInvalid with a message.
func errInvalid(format string, args ...any) error {
	return fmt.Errorf("win: "+format+": %w", append(args, kit.ErrInvalid)...)
}

// isNotSufficient reports DirectWrite's "buffer too small", which asks for a retry.
func isNotSufficient(hr com.HRESULT) bool { return hr == com.E_NOT_SUFFICIENT_BUFFER }

var _ kit.Resources = (*Resources)(nil)
