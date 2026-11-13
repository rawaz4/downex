package widgets

import (
	"errors"
	"fmt"
	"time"

	"downex/ui/kit"
	"downex/ui/kit/tokens"
)

// ErrNoPopups: the host cannot open popup windows (kit.Popups), so menus and Select lists
// do not open.
var ErrNoPopups = fmt.Errorf("widgets: the host has no popup windows: %w", kit.ErrUnsupported)

// popup is one popup window the widgets opened: a kit.Window whose content is a frame that
// leaves its box's shadow room around it and plays the box's rise (opacity 0 and 4 px lower
// to its place, --dur-3, --ease-out, as app.css's @keyframes rise), over a host popup.
type popup struct {
	owner  *kit.Window
	win    *kit.Window
	frame  *popupFrame
	host   kit.Popup
	box    kit.Rect // the box in the owner's window DIPs
	closed bool
}

// popupFrame is a popup window's content: the box at the shadow margins.
type popupFrame struct {
	Scope
	margins kit.Insets
	rise    float32 // 0 at the start of the rise, 1 at its end
	tween   *kit.Tween
	dir     kit.Direction
	pushed  bool
}

// Paint is kit.Painter's: the rise's opacity and offset over the box (popped in PaintOver).
func (f *popupFrame) Paint(p *kit.PaintContext) {
	f.pushed = f.rise < 1
	if f.pushed {
		p.Canvas.PushOpacity(f.rise)
		p.Canvas.PushTransform(kit.Translation(0, 4*(1-f.rise)))
	}
}

// PaintOver is kit.OverPainter's.
func (f *popupFrame) PaintOver(p *kit.PaintContext) {
	if f.pushed {
		p.Canvas.Pop()
		p.Canvas.Pop()
		f.pushed = false
	}
}

// shadowRoom is how far a box's shadows reach past it on each side (offset, blur and spread),
// rounded up, plus a pixel of anti-aliasing.
func shadowRoom(sh []kit.Shadow) kit.Insets {
	var in kit.Insets
	for _, s := range sh {
		if s.Inset {
			continue
		}
		r := s.Blur + s.Spread + 1
		in.Top = max(in.Top, r-s.Offset.Y)
		in.Bottom = max(in.Bottom, r+s.Offset.Y)
		in.Left = max(in.Left, r-s.Offset.X)
		in.Right = max(in.Right, r+s.Offset.X)
	}
	ceil := func(v kit.DIP) kit.DIP { return kit.DIP(int(v + 0.999)) }
	return kit.Insets{Top: ceil(max(in.Top, 0)), Right: ceil(max(in.Right, 0)), Bottom: ceil(max(in.Bottom, 0)), Left: ceil(max(in.Left, 0))}
}

// popupRoom is the room the --shadow-lg of a menu or list needs, the larger of the two themes
// (a theme change does not move the window).
var popupRoom = func() kit.Insets {
	a, b := shadowRoom(tokens.Light.ShadowLg), shadowRoom(tokens.Dark.ShadowLg)
	return kit.Insets{Top: max(a.Top, b.Top), Right: max(a.Right, b.Right), Bottom: max(a.Bottom, b.Bottom), Left: max(a.Left, b.Left)}
}()

// measure lays box out in a window of the owner's size and environment, without showing it,
// and returns its border-box size (what a fixed-positioned box takes in the viewport).
func measure(owner *kit.Window, theme Theme, box kit.Widget) kit.Size {
	sc := NewScope(theme, box)
	box.Base().Style.AlignSelf = kit.AlignStart
	w := kit.NewWindow(sc, kit.WindowOptions{Now: owner.Now})
	h := &measureHost{size: owner.Size(), env: owner.Environment(), res: owner.Host().Resources()}
	w.Attach(h)
	s := box.Base().Size()
	w.Detach()
	sc.Remove(box)
	return s
}

// openPopup opens box (laid out at size, placed at r in the owner's window DIPs) in a popup
// window above owner's window.
func openPopup(owner kit.Widget, theme Theme, box kit.Widget, r kit.Rect, name string, onDismiss func(kit.Dismiss)) (*popup, error) {
	ow := owner.Base().Window()
	if ow == nil || ow.Host() == nil {
		return nil, fmt.Errorf("widgets: open a popup: %w", kit.ErrClosed)
	}
	pops, ok := ow.Host().(kit.Popups)
	if !ok {
		return nil, ErrNoPopups
	}
	m := popupRoom
	f := &popupFrame{margins: m}
	f.Theme = theme
	f.Style = kit.Style{Padding: kit.Edges{Top: kit.Px(m.Top), End: kit.Px(m.Right), Bottom: kit.Px(m.Bottom), Start: kit.Px(m.Left)}}
	f.dir = ow.Direction()
	b := box.Base()
	b.Style.AlignSelf = kit.AlignStart
	b.Style.Width, b.Style.Height = kit.Px(r.Width()), kit.Px(r.Height())
	f.Add(box)
	pal := func(env kit.Environment) kit.Palette {
		p := theme.Tokens(env).Palette()
		p.Ground = kit.Color{} // the shadow's room shows what is under the popup
		return p
	}
	pw := kit.NewWindow(f, kit.WindowOptions{Title: name, Palette: pal, Now: ow.Now, Direction: f.dir})
	p := &popup{owner: ow, win: pw, frame: f, box: r}
	bounds := kit.R(r.Min.X-m.Left, r.Min.Y-m.Top, r.Max.X+m.Right, r.Max.Y+m.Bottom)
	hit := kit.RectAt(kit.Pt(m.Left, m.Top), r.Size())
	f.rise = 1
	if ow.MotionOK() {
		f.rise = 0
	}
	host, err := pops.OpenPopup(pw, kit.PopupOptions{Kind: kit.PopupMenu, Bounds: bounds, Hit: hit, Name: name, OnDismiss: onDismiss})
	if err != nil {
		f.Remove(box)
		return nil, fmt.Errorf("widgets: open a popup: %w", err)
	}
	p.host = host
	if f.rise < 1 {
		f.tween = &kit.Tween{From: 0, To: 1, Duration: tokens.Dur3, Ease: tokens.EaseOut.Easing(), Set: func(v float32) {
			f.rise = v
			pw.InvalidateRect(kit.RectAt(kit.Point{}, pw.Size()))
		}}
		pw.Animate(f.tween)
	}
	return p, nil
}

// deliver gives the popup's root an event (the owner's keys).
func (p *popup) deliver(e kit.Event) bool {
	if p == nil || p.closed {
		return false
	}
	return p.host.Deliver(e)
}

// close closes the popup window.
func (p *popup) close() {
	if p == nil || p.closed {
		return
	}
	p.closed = true
	p.host.Close()
}

// moveTo places the popup's box at r (owner window DIPs), resizing the window.
func (p *popup) moveTo(r kit.Rect) {
	if p == nil || p.closed || r == p.box {
		return
	}
	p.box = r
	m := p.frame.margins
	b := p.frame.Children()[0]
	if b.Style.Width != kit.Px(r.Width()) || b.Style.Height != kit.Px(r.Height()) {
		b.Style.Width, b.Style.Height = kit.Px(r.Width()), kit.Px(r.Height())
		b.Relayout()
	}
	p.host.SetBounds(kit.R(r.Min.X-m.Left, r.Min.Y-m.Top, r.Max.X+m.Right, r.Max.Y+m.Bottom))
	p.host.SetHit(kit.RectAt(kit.Pt(m.Left, m.Top), r.Size()))
}

// keyProxy is what holds the owner's keyboard focus while a popup that takes keys is open (a
// menu): a focusable element in a layer of its own that lets the pointer through, exempt from
// a dialog's focus trap, handing every key and typed text on to the popup. Its semantic node
// stands for the popup's active item, so the owner's focus reads as that item.
type keyProxy struct {
	kit.Element
	layer   *kit.Element
	deliver func(kit.Event) bool
}

func newKeyProxy(deliver func(kit.Event) bool) *keyProxy {
	k := &keyProxy{deliver: deliver}
	k.Focus = kit.FocusClick
	k.Ring = kit.Ring{None: true}
	k.Style = kit.Style{Width: kit.Px(0), Height: kit.Px(0)}
	k.layer = &kit.Element{}
	k.layer.PassThrough = true
	k.layer.Exempt = kit.ExemptKeep
	k.layer.Add(k)
	return k
}

// HandleKey is kit.KeyHandler's.
func (k *keyProxy) HandleKey(e kit.KeyEvent) bool { return k.deliver(e) }

// HandleText is kit.TextHandler's.
func (k *keyProxy) HandleText(e kit.TextEvent) bool { return k.deliver(e) }

// show pushes the proxy's layer and focuses it.
func (k *keyProxy) show(w *kit.Window) {
	w.PushLayer(k.layer)
	w.Focus(k)
}

// hide takes the layer away.
func (k *keyProxy) hide(w *kit.Window) {
	if w != nil {
		w.RemoveLayer(k.layer)
	}
}

// measureHost is the kit.Host of a window that only measures (measure): the owner's size,
// environment and resources, and nothing else.
type measureHost struct {
	size kit.Size
	env  kit.Environment
	res  kit.Resources
}

var errMeasuring = errors.New("widgets: a measuring window runs nothing")

func (h *measureHost) Environment() kit.Environment      { return h.env }
func (h *measureHost) Size() kit.Size                    { return h.size }
func (h *measureHost) WindowState() kit.WindowState      { return kit.WindowState{} }
func (h *measureHost) Resources() kit.Resources          { return h.res }
func (h *measureHost) Invalidate(kit.Rect)               {}
func (h *measureHost) RequestFrame()                     {}
func (h *measureHost) SetCursor(kit.Cursor)              {}
func (h *measureHost) CapturePointer()                   {}
func (h *measureHost) ReleasePointer()                   {}
func (h *measureHost) SetTextInput(kit.TextInput)        {}
func (h *measureHost) ClipboardText() (string, error)    { return "", errMeasuring }
func (h *measureHost) SetClipboardText(string) error     { return errMeasuring }
func (h *measureHost) SetTitle(string)                   {}
func (h *measureHost) SetMinSize(kit.Size)               {}
func (h *measureHost) Resize(kit.Size)                   {}
func (h *measureHost) SetFrameColors(kit.FrameColors)    {}
func (h *measureHost) Command(kit.WindowCommand)         {}
func (h *measureHost) Close()                            {}
func (h *measureHost) Post(func()) error                 { return errMeasuring }
func (h *measureHost) NotifySemantics(kit.SemanticEvent) {}

var _ kit.Host = (*measureHost)(nil)

// clampf is v kept within lo and hi (lo wins when they cross, as CSS's clamp()).
func clampf(v, lo, hi kit.DIP) kit.DIP { return max(lo, min(v, hi)) }

// popupNow is the clock popups stamp their type-ahead with (a test's clock through the owner).
func popupNow(w *kit.Window) time.Time {
	if w == nil {
		return time.Now()
	}
	return w.Now()
}
