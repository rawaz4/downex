// Package hostcaptest fakes the popup, tooltip and field-kind capabilities (kit.Popups,
// kit.Tooltips, kit.NativeFieldKinds) on top of kittest's host, so that the
// widgets that open popups, use native fields of every kind or turn tooltips on are tested
// headless on every GOOS. A Host is a kittest.Host with popups (each a root of its own,
// driven by its own kittest.Loop), fields that have a caret and a selection, and a tooltip
// source a test can query.
package hostcaptest

import (
	"time"
	"unicode/utf16"

	"downex/ui/kit"
	"downex/ui/kit/kittest"
)

// Host is a kittest.Host with the popup, tooltip and field-kind capabilities.
type Host struct {
	*kittest.Host
	// Popups are the popups opened, in order (closed ones stay, with Closed set).
	Popups []*Popup
	// Fields are the native fields made, of every kind.
	Fields []*Field
	// TooltipSource is what the root gave SetTooltipSource.
	TooltipSource func(p kit.Point) (string, kit.Rect, bool)

	clock *kittest.Clock
}

var (
	_ kit.Host             = (*Host)(nil)
	_ kit.Popups           = (*Host)(nil)
	_ kit.NativeFieldKinds = (*Host)(nil)
	_ kit.Tooltips         = (*Host)(nil)
	_ kit.NativeFields     = (*Host)(nil)
)

// NewHost makes a host of a window size and environment; clock drives the popups' loops.
func NewHost(size kit.Size, env kit.Environment, clock *kittest.Clock) *Host {
	if clock == nil {
		clock = kittest.NewClock()
	}
	return &Host{Host: kittest.NewHost(size, env), clock: clock}
}

// NewLoop attaches root to a new Host and returns the loop that drives it (kittest.NewLoop
// with this package's host).
func NewLoop(root kit.Root, clock *kittest.Clock, size kit.Size, env kit.Environment) (*kittest.Loop, *Host) {
	if clock == nil {
		clock = kittest.NewClock()
	}
	h := NewHost(size, env, clock)
	l := &kittest.Loop{Host: h.Host, Root: root, Canvas: &kittest.Canvas{}, Clock: clock, Refresh: time.Second / 60}
	root.Attach(h)
	return l, h
}

// Open are the popups still open.
func (h *Host) Open() []*Popup {
	var out []*Popup
	for _, p := range h.Popups {
		if !p.Closed {
			out = append(out, p)
		}
	}
	return out
}

// Top is the popup opened last that is still open, or nil.
func (h *Host) Top() *Popup {
	o := h.Open()
	if len(o) == 0 {
		return nil
	}
	return o[len(o)-1]
}

// Press stands in for a mouse button going down at p (owner window DIPs) outside every
// popup: each open popup's OnDismiss hears it, as the real host's mouse hook tells them;
// the press itself is the test's to send to the owner's root afterwards, as it goes on there.
func (h *Host) Press(p kit.Point) {
	for _, pp := range h.Open() {
		pp.dismiss(kit.Dismiss{Reason: kit.DismissPress, Pos: p, Inside: true})
	}
	h.RunPosted()
}

// Deactivate stands in for the owner window losing the activation.
func (h *Host) Deactivate() {
	for _, pp := range h.Open() {
		pp.dismiss(kit.Dismiss{Reason: kit.DismissDeactivate})
	}
	h.RunPosted()
}

// OpenPopup is kit.Popups'.
func (h *Host) OpenPopup(root kit.Root, o kit.PopupOptions) (kit.Popup, error) {
	if h.Closed {
		return nil, kit.ErrClosed
	}
	p := &Popup{Opts: o, Bounds: o.Bounds, Hit: o.Hit, Root: root, owner: h}
	p.Host = kittest.NewHost(o.Bounds.Size(), h.Env)
	p.Host.Res = h.Res
	p.Loop = &kittest.Loop{Host: p.Host, Root: root, Canvas: &kittest.Canvas{}, Clock: h.clock, Refresh: time.Second / 60}
	h.Popups = append(h.Popups, p)
	p.call(func() { root.Attach(p.Host) })
	return p, nil
}

// SetTooltipSource is kit.Tooltips'.
func (h *Host) SetTooltipSource(f func(p kit.Point) (string, kit.Rect, bool)) { h.TooltipSource = f }

// NewTextField is kit.NativeFields': a FieldText field with a caret and a selection.
func (h *Host) NewTextField(o kit.TextFieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	return h.NewField(o, kit.FieldOptions{}, cb)
}

// NewField is kit.NativeFieldKinds'.
func (h *Host) NewField(o kit.TextFieldOptions, k kit.FieldOptions, cb kit.TextFieldCallbacks) (kit.NativeField, error) {
	if h.Closed {
		return nil, kit.ErrClosed
	}
	f := &Field{Opts: o, Kind: k, cb: cb, host: h}
	h.Fields = append(h.Fields, f)
	return f, nil
}

// Popup is a fake popup window: its root is driven by Loop over its own Host (whose size is
// the popup's), and Click and Move stand in for the pointer over it.
type Popup struct {
	Opts   kit.PopupOptions
	Bounds kit.Rect
	Hit    kit.Rect
	Root   kit.Root
	Host   *kittest.Host
	Loop   *kittest.Loop
	Closed bool
	// Delivered are the events the owner handed on, in order.
	Delivered []kit.Event

	owner   *Host
	depth   int
	closing bool
}

var _ kit.Popup = (*Popup)(nil)

// call runs f as a call into the popup's root: a Close it asks for waits until it returns.
func (p *Popup) call(f func()) {
	p.depth++
	f()
	p.depth--
	if p.depth == 0 && p.closing && !p.Closed {
		p.Closed = true
		p.Root.Detach()
	}
	p.Host.RunPosted()
	p.owner.RunPosted()
}

func (p *Popup) dismiss(d kit.Dismiss) {
	if p.Closed || p.Opts.OnDismiss == nil {
		return
	}
	p.Opts.OnDismiss(d)
}

// SetBounds is kit.Popup's: a new size reaches the root as a SizeEvent.
func (p *Popup) SetBounds(r kit.Rect) {
	if p.Closed {
		return
	}
	old := p.Bounds.Size()
	p.Bounds = r
	if r.Size() != old {
		p.Host.SizeV = r.Size()
		p.call(func() { p.Root.Event(kit.SizeEvent{Size: r.Size()}) })
	}
}

// SetHit is kit.Popup's.
func (p *Popup) SetHit(r kit.Rect) { p.Hit = r }

// Deliver is kit.Popup's.
func (p *Popup) Deliver(e kit.Event) bool {
	if p.Closed {
		return false
	}
	p.Delivered = append(p.Delivered, e)
	var used bool
	p.call(func() { used = p.Root.Event(e) })
	return used
}

// Close is kit.Popup's.
func (p *Popup) Close() {
	if p.Closed {
		return
	}
	if p.depth > 0 {
		p.closing = true
		return
	}
	p.Closed = true
	p.Root.Detach()
}

// Send gives the popup's root an event as the pointer over it would (popup DIPs).
func (p *Popup) Send(e kit.Event) bool {
	if p.Closed {
		return false
	}
	var used bool
	p.call(func() { used = p.Root.Event(e) })
	return used
}

// Move stands in for the pointer moving to at (popup DIPs).
func (p *Popup) Move(at kit.Point) {
	p.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
}

// Click stands in for a left click at at (popup DIPs).
func (p *Popup) Click(at kit.Point) {
	p.Send(kit.PointerEvent{Action: kit.PointerMove, Pos: at})
	p.Send(kit.PointerEvent{Action: kit.PointerDown, Pos: at, Button: kit.ButtonLeft, Buttons: kit.ButtonLeft, Clicks: 1})
	p.Send(kit.PointerEvent{Action: kit.PointerUp, Pos: at, Button: kit.ButtonLeft})
}

// Field is a fake native field of any kind with a caret and a selection (UTF-16 offsets);
// Type, Click and the selection calls stand in for the user.
type Field struct {
	Opts     kit.TextFieldOptions
	Kind     kit.FieldOptions
	Bounds   kit.Rect
	Value    string
	HasFocus bool
	Released bool
	// Start and End are the selection.
	Start, End int
	// SetBoundsCalls counts the moves; Reclaims counts Reclaim calls.
	SetBoundsCalls, Reclaims int

	cb   kit.TextFieldCallbacks
	host *Host
}

var (
	_ kit.NativeField    = (*Field)(nil)
	_ kit.FieldControl   = (*Field)(nil)
	_ kit.FieldReclaimer = (*Field)(nil)
)

func u16len(s string) int { return len(utf16.Encode([]rune(s))) }

func (f *Field) SetBounds(r kit.Rect) {
	f.Bounds = r
	f.SetBoundsCalls++
}
func (f *Field) SetOptions(o kit.TextFieldOptions) { f.Opts = o }
func (f *Field) Text() string                      { return f.Value }

// SetText is kit.NativeField's: the caret goes to the end.
func (f *Field) SetText(s string) {
	f.Value = s
	n := u16len(s)
	f.Start, f.End = n, n
}
func (f *Field) Focus()     { f.HasFocus = true }
func (f *Field) Blur()      { f.HasFocus = false }
func (f *Field) SelectAll() { f.Start, f.End = 0, u16len(f.Value) }
func (f *Field) Release()   { f.Released = true }
func (f *Field) Reclaim()   { f.Reclaims++ }

// Shown reports whether the field is placed on screen.
func (f *Field) Shown() bool { return !f.Released && !f.Bounds.Empty() }

// Selection is kit.FieldControl's.
func (f *Field) Selection() (int, int) { return f.Start, f.End }

// SetSelection is kit.FieldControl's.
func (f *Field) SetSelection(start, end int) {
	n := u16len(f.Value)
	f.Start, f.End = min(max(start, 0), n), min(max(end, 0), n)
	if f.End < f.Start {
		f.Start, f.End = f.End, f.Start
	}
}

// SetMaxLength is kit.FieldControl's.
func (f *Field) SetMaxLength(n int) { f.Kind.MaxLength = n }

// SetPassword is kit.FieldControl's.
func (f *Field) SetPassword(on bool) {
	f.Kind.Kind = kit.FieldText
	if on {
		f.Kind.Kind = kit.FieldPassword
	}
}

// ReplaceSelection is kit.FieldControl's: as typing s over the selection.
func (f *Field) ReplaceSelection(s string) { f.Type(s) }

// Type stands in for the user typing s over the selection (at the caret): the field keeps
// to its MaxLength and tells the root through its Changed callback.
func (f *Field) Type(s string) {
	u := utf16.Encode([]rune(f.Value))
	ins := utf16.Encode([]rune(s))
	start, end := min(f.Start, len(u)), min(f.End, len(u))
	if m := f.Kind.MaxLength; m > 0 {
		room := m - (len(u) - (end - start))
		if room < len(ins) {
			ins = ins[:max(room, 0)]
		}
	}
	out := append(append(append([]uint16{}, u[:start]...), ins...), u[end:]...)
	f.Value = string(utf16.Decode(out))
	f.Start, f.End = start+len(ins), start+len(ins)
	f.changed()
}

// Replace stands in for the user replacing the whole text (HTML's fireEvent.input).
func (f *Field) Replace(s string) {
	f.SetText(s)
	f.changed()
}

func (f *Field) changed() {
	if f.cb.Changed != nil {
		f.cb.Changed(f.Value)
	}
	f.host.RunPosted()
}

// Click stands in for a click into the field: it takes the system's focus and tells the
// root.
func (f *Field) Click() {
	f.HasFocus = true
	if f.cb.Focused != nil {
		f.cb.Focused(true)
	}
	f.host.RunPosted()
}

// LoseFocus stands in for the system moving its focus elsewhere.
func (f *Field) LoseFocus() {
	f.HasFocus = false
	if f.cb.Focused != nil {
		f.cb.Focused(false)
	}
	f.host.RunPosted()
}
