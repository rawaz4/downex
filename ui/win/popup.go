//go:build windows

package win

import (
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sync"
	"time"
	"unsafe"

	"downex/ui/kit"
	"downex/ui/kit/hostcap"
	"downex/ui/win/internal/com"
	"downex/ui/win/internal/d2d"
	"downex/ui/win/internal/w32"
	"downex/ui/win/uia"

	"golang.org/x/sys/windows"
)

// popupWindow is a popup window of a Window (hostcap.Popups): a top-level WS_POPUP window
// owned by the Window, so it stays above it and goes with it, that never takes the activation
// or the keyboard focus (WS_EX_NOACTIVATE). It is a layered window with per-pixel alpha, so
// that its root draws the box's shadow (--shadow-lg) around the box as the WebView window does:
// a Direct2D DC render target with premultiplied alpha draws into a DIB section, which
// UpdateLayeredWindow shows. Outside the root's Hit area the pointer goes to what is under the
// popup (HTTRANSPARENT). It implements kit.Host for its root and calls into it one call at a
// time, as Window does.
type popupWindow struct {
	owner *Window
	app   *App
	root  kit.Root
	hwnd  w32.HWND
	opts  hostcap.PopupOptions

	bounds kit.Rect // owner window DIPs
	hit    kit.Rect // popup DIPs
	ppd    float32
	size   kit.Size
	px     w32.Point // client size in pixels
	at     w32.Point // screen position in pixels
	env    kit.Environment

	// The drawing: a DIB section selected into a memory DC, and the device whose target draws
	// into it.
	memDC  windows.Handle
	dib    windows.Handle
	oldBmp windows.Handle
	bits   unsafe.Pointer
	dcrt   *d2d.DCRenderTarget
	dev    *device
	bind   d2d.RECT
	shown  bool
	// The ClearType pass: the same frame drawn on an opaque target (ALPHA_MODE_IGNORE), whose
	// pixels replace the first pass's inside the box wherever that is fully opaque, so that the
	// box's text keeps ClearType as the WebView window's menus have it (text on transparent
	// pixels can only be grayscale).
	ctDC   windows.Handle
	ctDIB  windows.Handle
	ctOld  windows.Handle
	ctBits unsafe.Pointer
	ctRT   *d2d.DCRenderTarget
	ctDev  *device

	pending     kit.Rect
	frameWanted bool

	depth    int
	queue    []queued
	closing  bool
	closed   bool
	attached bool

	postMu    sync.Mutex
	posts     []func()
	postWoken bool

	cursor    kit.Cursor
	tracking  bool
	captured  bool
	releasing bool
	buttons   kit.Buttons

	// Heap memory for the calls that run window procedures (package w32's rule).
	tme    w32.TRACKMOUSEEVENT
	ulwDst w32.Point
	ulwSrc w32.Point
	ulwSz  popSize
	blend  popBlend
	bi     popBitmapInfo
	title  *uint16

	// UI Automation (popup_uia.go): the provider, made at the first WM_GETOBJECT for it;
	// uiaDone once it was disconnected (none is made again).
	uia     *uia.Tree
	uiaDone bool

	frames, paints int // counts for tests
}

var (
	popupProcOnce sync.Once
	popupProcPtr  uintptr
	popupClass    = func() *uint16 { p, _ := windows.UTF16PtrFromString("DownexNativePopup"); return p }()
	popupTable    struct {
		sync.Mutex
		byHWND  map[w32.HWND]*popupWindow
		making  map[uintptr]*popupWindow
		next    uintptr
		service map[w32.HWND]*popupService
		classes map[windows.Handle]bool
	}
	// popupsOf are each Window's open popups (UI thread).
	popupsOf = map[*Window][]*popupWindow{}
)

var _ hostcap.Popups = (*Window)(nil)

// registerPopupClass registers the popup windows' class for the App's module, once.
func (a *App) registerPopupClass() error {
	popupProcOnce.Do(func() {
		popupProcPtr = w32.NewCallback(popupWndProc)
		popupTable.byHWND = map[w32.HWND]*popupWindow{}
		popupTable.making = map[uintptr]*popupWindow{}
		popupTable.service = map[w32.HWND]*popupService{}
		popupTable.classes = map[windows.Handle]bool{}
	})
	popupTable.Lock()
	done := popupTable.classes[a.inst]
	popupTable.Unlock()
	if done {
		return nil
	}
	wc := &w32.WNDCLASSEXW{WndProc: popupProcPtr, Instance: a.inst, Cursor: a.arrow, ClassName: popupClass}
	wc.Size = uint32(unsafe.Sizeof(*wc))
	if _, err := w32.RegisterClassEx(wc); err != nil && !errors.Is(err, windows.ERROR_CLASS_ALREADY_EXISTS) {
		return fmt.Errorf("win: popup class: %w", err)
	}
	popupTable.Lock()
	popupTable.classes[a.inst] = true
	popupTable.Unlock()
	return nil
}

// OpenPopup implements hostcap.Popups.
func (w *Window) OpenPopup(root kit.Root, o hostcap.PopupOptions) (hostcap.Popup, error) {
	if w.closed || w.hwnd == 0 {
		return nil, fmt.Errorf("win: popup: %w", kit.ErrClosed)
	}
	if root == nil {
		return nil, errInvalid("OpenPopup without a root")
	}
	a := w.app
	if err := a.onThread(); err != nil {
		return nil, err
	}
	if err := a.registerPopupClass(); err != nil {
		return nil, err
	}
	p := &popupWindow{owner: w, app: a, root: root, opts: o, bounds: o.Bounds, hit: o.Hit, ppd: w.ppd(), env: w.env}
	p.blend = popBlend{Op: popACSrcOver, SourceConstantAlpha: 255, AlphaFormat: popACSrcAlpha}
	p.place()
	if err := p.makeSurface(); err != nil {
		return nil, err
	}
	p.dev = newDevice(a.res, p.ppd, p.makeTarget, p.dropTarget)
	p.ctDev = newDevice(a.res, p.ppd, p.makeClearTypeTarget, func() { p.ctRT = nil })
	p.ctDev.clearTypeLayers = true
	p.title, _ = windows.UTF16PtrFromString(stripNUL(o.Name))
	popupTable.Lock()
	popupTable.next++
	id := popupTable.next
	popupTable.making[id] = p
	popupTable.Unlock()
	h, err := w32.CreateWindowEx(popWSExLayered|popWSExToolWin|w32.WS_EX_NOACTIVATE, popupClass, p.title, w32.WS_POPUP,
		p.at.X, p.at.Y, p.px.X, p.px.Y, w.hwnd, a.inst, id)
	popupTable.Lock()
	delete(popupTable.making, id)
	popupTable.Unlock()
	if err != nil {
		p.releaseSurface()
		return nil, fmt.Errorf("win: popup: %w", err)
	}
	p.hwnd = h
	popupsOf[w] = append(popupsOf[w], p)
	a.hooks().retain()
	p.attached = true
	p.depth++
	root.Attach(p)
	p.depth--
	p.pending = kit.RectAt(kit.Point{}, p.size)
	p.frame()
	p.drain()
	if p.closed {
		return nil, fmt.Errorf("win: the popup was closed while it opened: %w", kit.ErrClosed)
	}
	return p, nil
}

// place works out the popup's pixels from its bounds in the owner's DIPs: where it is on the
// screen and its size.
func (p *popupWindow) place() {
	b := p.bounds
	ppd := p.ppd
	x0 := int32(math.Round(float64(b.Min.X * ppd)))
	y0 := int32(math.Round(float64(b.Min.Y * ppd)))
	x1 := int32(math.Round(float64(b.Max.X * ppd)))
	y1 := int32(math.Round(float64(b.Max.Y * ppd)))
	p.px = w32.Point{X: max(x1-x0, 1), Y: max(y1-y0, 1)}
	p.at = w32.ClientToScreen(p.owner.hwnd, w32.Point{X: x0, Y: y0})
	p.size = kit.Size{W: float32(p.px.X) / ppd, H: float32(p.px.Y) / ppd}
}

// makeSurface makes the memory DC and the 32-bit top-down DIB section the target draws into.
func (p *popupWindow) makeSurface() error {
	if p.memDC == 0 {
		p.memDC = createCompatibleDC()
		if p.memDC == 0 {
			return fmt.Errorf("win: popup: CreateCompatibleDC failed")
		}
	}
	p.bi = popBitmapInfo{Width: p.px.X, Height: -p.px.Y, Planes: 1, BitCount: 32, Compression: popBIRGB}
	p.bi.Size = uint32(unsafe.Offsetof(p.bi.Colors))
	var bits unsafe.Pointer
	dib := createDIBSection(p.memDC, &p.bi, &bits)
	if dib == 0 {
		return fmt.Errorf("win: popup: CreateDIBSection failed for %d × %d", p.px.X, p.px.Y)
	}
	old := selectObject(p.memDC, dib)
	if p.dib != 0 {
		w32.DeleteObject(p.dib)
	} else {
		p.oldBmp = old
	}
	p.dib, p.bits = dib, bits
	p.bind = d2d.RECT{Right: p.px.X, Bottom: p.px.Y}
	if p.dcrt != nil {
		p.dcrt.BindDC(p.memDC, &p.bind)
	}
	if p.ctDC == 0 {
		p.ctDC = createCompatibleDC()
	}
	if p.ctDC != 0 {
		var cbits unsafe.Pointer
		if cdib := createDIBSection(p.ctDC, &p.bi, &cbits); cdib != 0 {
			old := selectObject(p.ctDC, cdib)
			if p.ctDIB != 0 {
				w32.DeleteObject(p.ctDIB)
			} else {
				p.ctOld = old
			}
			p.ctDIB, p.ctBits = cdib, cbits
			if p.ctRT != nil {
				p.ctRT.BindDC(p.ctDC, &p.bind)
			}
		}
	}
	return nil
}

func (p *popupWindow) releaseSurface() {
	if p.dev != nil {
		p.dev.drop()
	}
	if p.ctDev != nil {
		p.ctDev.drop()
	}
	if p.ctDC != 0 {
		if p.ctOld != 0 {
			selectObject(p.ctDC, p.ctOld)
		}
		deleteDC(p.ctDC)
		p.ctDC = 0
	}
	if p.ctDIB != 0 {
		w32.DeleteObject(p.ctDIB)
		p.ctDIB, p.ctBits = 0, nil
	}
	if p.memDC != 0 {
		if p.oldBmp != 0 {
			selectObject(p.memDC, p.oldBmp)
		}
		deleteDC(p.memDC)
		p.memDC = 0
	}
	if p.dib != 0 {
		w32.DeleteObject(p.dib)
		p.dib = 0
	}
}

// makeTarget is the device's target: a DC render target with premultiplied alpha bound to
// the DIB section.
func (p *popupWindow) makeTarget() (*d2d.RenderTarget, error) {
	if p.memDC == 0 {
		return nil, fmt.Errorf("win: popup closed: %w", kit.ErrClosed)
	}
	rt, err := p.dcTarget(d2d.ALPHA_MODE_PREMULTIPLIED, p.memDC)
	if err != nil {
		return nil, err
	}
	p.dcrt = rt
	return &rt.RenderTarget, nil
}

// dcTarget makes a DC render target bound to dc, drawn as the owner window draws: on the GPU
// (D2D copies each frame into the DIB section), or with the software renderer when the window
// uses it or the GPU target cannot be made. The software renderer (WARP) keeps about 7 MB of
// its own once a popup drew with it (measured 2026-11-05: 25.0 -> 32.4 MB private working set,
// +11 MB commit, after one menu, kept after it closed), where the GPU target's memory goes
// with the popup.
func (p *popupWindow) dcTarget(alpha uint32, dc windows.Handle) (*d2d.DCRenderTarget, error) {
	props := d2d.RenderTargetProperties{
		Type:        d2d.RENDER_TARGET_TYPE_HARDWARE,
		PixelFormat: d2d.PixelFormat{Format: d2d.DXGI_FORMAT_B8G8R8A8_UNORM, AlphaMode: alpha},
		DpiX:        96 * p.ppd, DpiY: 96 * p.ppd,
	}
	if p.owner.drawsInSoftware() {
		props.Type = d2d.RENDER_TARGET_TYPE_SOFTWARE
	}
	rt, hr := p.app.res.d2d.CreateDCRenderTarget(&props)
	if hr.Failed() && props.Type == d2d.RENDER_TARGET_TYPE_HARDWARE {
		props.Type = d2d.RENDER_TARGET_TYPE_SOFTWARE
		rt, hr = p.app.res.d2d.CreateDCRenderTarget(&props)
	}
	if hr.Failed() {
		return nil, fmt.Errorf("win: popup: %w", com.Check("ID2D1Factory::CreateDCRenderTarget", hr))
	}
	if hr := rt.BindDC(dc, &p.bind); hr.Failed() {
		rt.Release()
		return nil, fmt.Errorf("win: popup: %w", com.Check("ID2D1DCRenderTarget::BindDC", hr))
	}
	return rt, nil
}

// drawsInSoftware: the window draws with the software renderer (see hwndTarget.softwareNow).
func (w *Window) drawsInSoftware() bool { return w.target != nil && w.target.softwareNow() }

// dropTarget is the device's lost: it drops the popup's reference to the target.
func (p *popupWindow) dropTarget() { p.dcrt = nil }

// makeClearTypeTarget is the ClearType pass's target: opaque, bound to its own DIB.
func (p *popupWindow) makeClearTypeTarget() (*d2d.RenderTarget, error) {
	if p.ctDC == 0 || p.ctDIB == 0 {
		return nil, fmt.Errorf("win: popup: no ClearType surface: %w", kit.ErrUnsupported)
	}
	rt, err := p.dcTarget(d2d.ALPHA_MODE_IGNORE, p.ctDC)
	if err != nil {
		return nil, err
	}
	p.ctRT = rt
	return &rt.RenderTarget, nil
}

// clearTypePass draws the frame again on the opaque target and takes its pixels inside the
// box (the Hit area less its border and rounded corners) where the first pass is opaque.
func (p *popupWindow) clearTypePass(dirty kit.Rect) {
	if p.ctDev == nil || p.ctBits == nil || p.hit.Empty() {
		return
	}
	c, err := p.ctDev.begin(dirty)
	if err != nil {
		return
	}
	p.ctDev.rt.Clear(&transparent)
	p.depth++
	p.root.Paint(c, dirty)
	p.depth--
	if p.ctDev.end() != nil {
		return
	}
	ppd := p.ppd
	box := p.hit.Inset(kit.UniformInsets(1)) // inside the border
	r := float32(10) * ppd                   // the box's 11 px corners, less the border
	x0, y0 := int(math.Ceil(float64(max(box.Min.X, dirty.Min.X)*ppd))), int(math.Ceil(float64(max(box.Min.Y, dirty.Min.Y)*ppd)))
	x1, y1 := int(float64(min(box.Max.X, dirty.Max.X)*ppd)), int(float64(min(box.Max.Y, dirty.Max.Y)*ppd))
	bx0, by0, bx1, by1 := box.Min.X*ppd, box.Min.Y*ppd, box.Max.X*ppd, box.Max.Y*ppd
	n := int(p.px.X) * int(p.px.Y) * 4
	dst := unsafe.Slice((*byte)(p.bits), n)
	src := unsafe.Slice((*byte)(p.ctBits), n)
	stride := int(p.px.X) * 4
	for y := max(y0, 0); y < min(y1, int(p.px.Y)); y++ {
		fy := float32(y) + 0.5
		for x := max(x0, 0); x < min(x1, int(p.px.X)); x++ {
			fx := float32(x) + 0.5
			// outside the rounded corners: keep the first pass's
			cx, cy := float32(0), float32(0)
			switch {
			case fx < bx0+r:
				cx = bx0 + r - fx
			case fx > bx1-r:
				cx = fx - (bx1 - r)
			}
			switch {
			case fy < by0+r:
				cy = by0 + r - fy
			case fy > by1-r:
				cy = fy - (by1 - r)
			}
			if cx > 0 && cy > 0 && cx*cx+cy*cy > (r-1)*(r-1) {
				continue
			}
			i := y*stride + x*4
			if dst[i+3] != 255 {
				continue // a rise or fade is under way: not opaque
			}
			dst[i], dst[i+1], dst[i+2] = src[i], src[i+1], src[i+2]
		}
	}
}

var transparent = d2d.ColorF{}

// frame makes one frame: the root's Frame, then a paint of what is invalid, shown with
// UpdateLayeredWindow.
func (p *popupWindow) frame() {
	if p.closed {
		return
	}
	p.frames++
	p.depth++
	p.root.Frame(time.Now())
	p.depth--
	dirty := p.pending.Intersect(kit.RectAt(kit.Point{}, p.size))
	p.pending = kit.Rect{}
	if dirty.Empty() {
		return
	}
	dirty = roundOut(dirty, p.ppd)
	c, err := p.dev.begin(dirty)
	if err != nil {
		return
	}
	p.dev.rt.Clear(&transparent)
	p.paints++
	p.depth++
	p.root.Paint(c, dirty)
	p.depth--
	if err := p.dev.end(); err != nil {
		if errors.Is(err, kit.ErrDeviceLost) {
			p.Invalidate(kit.RectAt(kit.Point{}, p.size))
		}
		return
	}
	p.clearTypePass(dirty)
	p.show()
}

// show pushes the DIB to the screen.
func (p *popupWindow) show() {
	p.ulwDst, p.ulwSrc = p.at, w32.Point{}
	p.ulwSz = popSize{CX: p.px.X, CY: p.px.Y}
	updateLayeredWindow(p.hwnd, &p.ulwDst, &p.ulwSz, p.memDC, &p.ulwSrc, &p.blend)
	if !p.shown {
		p.shown = true
		w32.ShowWindow(p.hwnd, popSWShowNA)
		// A popup that opens under the pointer (a context menu at the right-click) takes it
		// from the owner at once, as an element over the row takes :hover in the WebView: the
		// row's hover actions go, without waiting for the pointer to move.
		p.owner.pointerMaybeLeft()
	}
}

// ---- calls into the root, one at a time ----

func (p *popupWindow) event(e kit.Event) bool {
	if p.closed || !p.attached {
		return false
	}
	if p.depth > 0 {
		p.queue = append(p.queue, queued{e: e})
		return false
	}
	p.depth++
	used := p.root.Event(e)
	p.depth--
	p.drain()
	return used
}

func (p *popupWindow) call(f func()) {
	if p.closed || !p.attached || f == nil {
		return
	}
	if p.depth > 0 {
		p.queue = append(p.queue, queued{f: f})
		return
	}
	p.depth++
	f()
	p.depth--
	p.drain()
}

func (p *popupWindow) drain() {
	for p.depth == 0 && !p.closed {
		if p.closing {
			p.destroy()
			return
		}
		if len(p.queue) == 0 {
			return
		}
		q := p.queue[0]
		p.queue[0] = queued{}
		p.queue = p.queue[1:]
		p.depth++
		if q.f != nil {
			q.f()
		} else {
			p.root.Event(q.e)
		}
		p.depth--
	}
}

// destroy closes the popup now: the root's last call, then the window and what it holds.
func (p *popupWindow) destroy() {
	if p.closed {
		return
	}
	// UI Automation's elements go first, while the popup and its root are whole.
	p.uiaDestroyed()
	p.closed = true
	p.queue = nil
	p.app.clock.cancel(p.hwnd)
	if p.attached {
		p.root.Detach()
	}
	if p.hwnd != 0 {
		w32.DestroyWindow(p.hwnd)
	}
	p.releaseSurface()
	p.forget()
}

// forget takes the popup out of its owner's list and releases the hooks' hold.
func (p *popupWindow) forget() {
	list := popupsOf[p.owner]
	for i, x := range list {
		if x == p {
			popupsOf[p.owner] = append(list[:i:i], list[i+1:]...)
			p.app.hooks().release()
			break
		}
	}
	if len(popupsOf[p.owner]) == 0 {
		delete(popupsOf, p.owner)
	}
}

// dismiss tells the owner's root why the popup should close, as a call into it.
func (p *popupWindow) dismiss(d hostcap.Dismiss) {
	if p.closed || p.opts.OnDismiss == nil {
		return
	}
	f := p.opts.OnDismiss
	p.owner.call(func() { f(d) })
}

// hitContains reports whether a screen point is over the popup's Hit area.
func (p *popupWindow) hitContains(pt w32.Point) bool {
	c := w32.ScreenToClient(p.hwnd, pt)
	d := kit.Pt(float32(c.X)/p.ppd, float32(c.Y)/p.ppd)
	if p.hit.Empty() {
		return kit.RectAt(kit.Point{}, p.size).Contains(d)
	}
	return p.hit.Contains(d)
}

// ---- hostcap.Popup ----

// SetBounds implements hostcap.Popup.
func (p *popupWindow) SetBounds(r kit.Rect) {
	if p.closed || r == p.bounds {
		return
	}
	old := p.px
	p.bounds = r
	p.place()
	if p.px != old {
		if err := p.makeSurface(); err != nil {
			return
		}
		p.event(kit.SizeEvent{Size: p.size})
		p.Invalidate(kit.RectAt(kit.Point{}, p.size))
		return
	}
	p.show()
}

// SetHit implements hostcap.Popup.
func (p *popupWindow) SetHit(r kit.Rect) { p.hit = r }

// Deliver implements hostcap.Popup.
func (p *popupWindow) Deliver(e kit.Event) bool { return p.event(e) }

// Close implements hostcap.Popup and kit.Host: the root gets Detach once the call in progress
// has returned.
func (p *popupWindow) Close() {
	if p.closed {
		return
	}
	if p.depth > 0 {
		p.closing = true
		return
	}
	p.destroy()
}

// ---- kit.Host ----

func (p *popupWindow) Environment() kit.Environment { return p.env }
func (p *popupWindow) Size() kit.Size               { return p.size }
func (p *popupWindow) WindowState() kit.WindowState { return kit.WindowState{} }
func (p *popupWindow) Resources() kit.Resources     { return p.app.res }

// Invalidate implements kit.Host.
func (p *popupWindow) Invalidate(r kit.Rect) {
	if p.closed || r.Empty() {
		return
	}
	p.pending = p.pending.Union(r)
	p.RequestFrame()
}

// RequestFrame implements kit.Host.
func (p *popupWindow) RequestFrame() {
	if p.closed || p.frameWanted || p.hwnd == 0 {
		return
	}
	p.frameWanted = true
	p.app.clock.request(p.hwnd)
}

// SetCursor implements kit.Host.
func (p *popupWindow) SetCursor(c kit.Cursor) {
	p.cursor = c
	if pt, ok := w32.GetCursorPos(); ok && w32.WindowFromPoint(pt) == p.hwnd {
		w32.SetCursor(p.app.cursor(c))
	}
}

// CapturePointer implements kit.Host.
func (p *popupWindow) CapturePointer() {
	if !p.closed && !p.captured {
		p.captured = true
		w32.SetCapture(p.hwnd)
	}
}

// ReleasePointer implements kit.Host.
func (p *popupWindow) ReleasePointer() {
	if p.captured {
		p.releasing = true
		w32.ReleaseCapture()
		p.releasing = false
		p.captured = false
	}
}

func (p *popupWindow) SetTextInput(kit.TextInput) {}

// ClipboardText implements kit.Host: the owner's clipboard.
func (p *popupWindow) ClipboardText() (string, error) { return p.owner.ClipboardText() }

// SetClipboardText implements kit.Host.
func (p *popupWindow) SetClipboardText(s string) error { return p.owner.SetClipboardText(s) }

// SetTitle implements kit.Host.
func (p *popupWindow) SetTitle(title string) {
	if p.closed {
		return
	}
	p.title, _ = windows.UTF16PtrFromString(stripNUL(title))
	w32.SetWindowText(p.hwnd, p.title)
}

func (p *popupWindow) SetMinSize(kit.Size)            {}
func (p *popupWindow) SetFrameColors(kit.FrameColors) {}
func (p *popupWindow) Command(kit.WindowCommand)      {}

// Resize implements kit.Host: the popup grows or shrinks from its top-left corner.
func (p *popupWindow) Resize(s kit.Size) {
	p.SetBounds(kit.RectAt(p.bounds.Min, s))
}

// Post implements kit.Host.
func (p *popupWindow) Post(f func()) error {
	p.postMu.Lock()
	if p.closed {
		p.postMu.Unlock()
		return fmt.Errorf("win: popup closed: %w", kit.ErrClosed)
	}
	p.posts = append(p.posts, f)
	wake := !p.postWoken
	p.postWoken = true
	p.postMu.Unlock()
	if wake {
		if err := w32.PostMessage(p.hwnd, msgPost, 0, 0); err != nil {
			return fmt.Errorf("win: popup post: %w", err)
		}
	}
	return nil
}

func (p *popupWindow) runPosts() {
	p.postMu.Lock()
	take := p.posts
	p.posts = nil
	p.postWoken = false
	p.postMu.Unlock()
	for _, f := range take {
		p.call(f)
	}
}

// NotifySemantics implements kit.Host: the popup's UI Automation events.
func (p *popupWindow) NotifySemantics(e kit.SemanticEvent) { p.uiaNotify(e) }

// The popup is its root's host; its Close is kit.Host's and hostcap.Popup's alike.
var (
	_ kit.Host      = (*popupWindow)(nil)
	_ hostcap.Popup = (*popupWindow)(nil)
)

// ---- the window procedure ----

func popupWndProc(h w32.HWND, msg uint32, wp, lp uintptr) uintptr {
	popupTable.Lock()
	p := popupTable.byHWND[h]
	if p == nil && msg == w32.WM_NCCREATE {
		cs := (*w32.CREATESTRUCTW)(com.Addr(lp))
		if p = popupTable.making[cs.CreateParams]; p != nil {
			p.hwnd = h
			popupTable.byHWND[h] = p
		}
	}
	svc := popupTable.service[h]
	popupTable.Unlock()
	switch {
	case p != nil:
		return p.proc(msg, wp, lp)
	case svc != nil:
		return svc.proc(h, msg, wp, lp)
	}
	return w32.DefWindowProc(h, msg, wp, lp)
}

func (p *popupWindow) dip(lp uintptr) kit.Point {
	return kit.Pt(float32(int16(lp))/p.ppd, float32(int16(lp>>16))/p.ppd)
}

func (p *popupWindow) proc(msg uint32, wp, lp uintptr) (ret uintptr) {
	defer func() {
		if v := recover(); v != nil {
			p.app.failIn(p.owner, v, debug.Stack())
			ret = 0
		}
	}()
	switch msg {
	case msgFrame:
		if p.frameWanted {
			p.frameWanted = false
			p.frame()
			p.drain()
		}
		return 0
	case msgPost:
		p.runPosts()
		return 0
	case popWMMouseActivate:
		return popMANoActivate
	case popWMNCHitTest:
		if p.hitContains(screenPoint(lp)) {
			return popHTClient
		}
		return popHTTransparent
	case popWMSetCursor:
		if lp&0xFFFF == popHTClient {
			w32.SetCursor(p.app.cursor(p.cursor))
			return 1
		}
	case popWMMouseMove:
		if !p.tracking {
			p.tme = w32.TRACKMOUSEEVENT{Flags: w32.TME_LEAVE, Track: p.hwnd}
			p.tme.Size = uint32(unsafe.Sizeof(p.tme))
			w32.TrackMouseEvent(&p.tme)
			p.tracking = true
			p.event(kit.PointerEvent{Action: kit.PointerEnter, Pos: p.dip(lp), Buttons: buttonsOf(wp), Mods: modsOf(wp)})
		}
		p.event(kit.PointerEvent{Action: kit.PointerMove, Pos: p.dip(lp), Buttons: buttonsOf(wp), Mods: modsOf(wp)})
		return 0
	case popWMMouseLeave:
		p.tracking = false
		p.event(kit.PointerEvent{Action: kit.PointerLeave, Mods: modsOf(0)})
		return 0
	case popWMLButtonDown, popWMRButtonDown, popWMMButtonDown, popWMXButtonDown:
		b := buttonOf(msg, wp)
		p.buttons = buttonsOf(wp)
		p.event(kit.PointerEvent{Action: kit.PointerDown, Pos: p.dip(lp), Button: b, Buttons: p.buttons, Mods: modsOf(wp), Clicks: 1})
		return 0
	case popWMLButtonUp, popWMRButtonUp, popWMMButtonUp, popWMXButtonUp:
		b := buttonOf(msg, wp)
		p.buttons = buttonsOf(wp)
		p.event(kit.PointerEvent{Action: kit.PointerUp, Pos: p.dip(lp), Button: b, Buttons: p.buttons, Mods: modsOf(wp)})
		if p.captured && p.buttons == 0 {
			p.ReleasePointer()
		}
		return 0
	case popWMCaptureChanged:
		if p.captured && !p.releasing {
			p.captured = false
			p.event(kit.PointerEvent{Action: kit.PointerCancel, Buttons: p.buttons, Mods: modsOf(0)})
		}
		return 0
	case popWMMouseWheel:
		pt := w32.ScreenToClient(p.hwnd, screenPoint(lp))
		notches := float32(int16(wp>>16)) / 120
		lines := float32(p.app.wheelLines)
		if lines == 0 {
			lines = 3
		}
		p.event(kit.WheelEvent{Pos: kit.Pt(float32(pt.X)/p.ppd, float32(pt.Y)/p.ppd), Delta: kit.Pt(0, -notches*lines*100/3), Mode: kit.WheelLines, Mods: modsOf(wp)})
		return 0
	case wmGetObject:
		if r, ok := p.uiaGetObject(wp, lp); ok {
			return r
		}
	case popWMDestroy:
		if !p.closed {
			// The owner went (its owned windows go with it): UI Automation's elements first,
			// then the root's last call.
			p.uiaDestroyed()
			p.closed = true
			if p.attached {
				p.root.Detach()
			}
			p.app.clock.cancel(p.hwnd)
		}
		return 0
	case popWMNCDestroy:
		popupTable.Lock()
		delete(popupTable.byHWND, p.hwnd)
		popupTable.Unlock()
		r := w32.DefWindowProc(p.hwnd, msg, wp, lp)
		if p.memDC != 0 {
			p.releaseSurface()
		}
		p.forget()
		p.hwnd = 0
		return r
	}
	return w32.DefWindowProc(p.hwnd, msg, wp, lp)
}
