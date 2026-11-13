package kit

import (
	"slices"
	"sync/atomic"
	"time"
)

// Window is the content of one native window: the kit's Root. It holds the widget tree in
// layers (the content, then dialogs and other overlays above it), lays it out, paints it,
// sends it input, keeps its focus and its semantic tree, and runs its motion clock. A
// backend drives it through the Root methods; widgets reach it through their elements.
//
// Everything about it belongs to the UI thread; only Post may be called elsewhere.
type Window struct {
	opts    WindowOptions
	host    Host
	poster  atomic.Pointer[hostRef] // the host for Post, which any goroutine may call
	root    *Element
	content Widget
	size    Size
	env     Environment
	state   WindowState
	palette Palette
	active  bool // the window has the keyboard focus
	closed  bool
	sched   Scheduler

	nextID  NodeID
	nodes   map[NodeID]*Element
	virtual map[NodeID]virtualNodes
	sem     semQueue
	tree    semTree
	semUsed bool    // assistive technology has read the tree
	lists   []*List // the lists in the window, which give rows lasting ids once it has
	// followers are the attached LayoutFollowers (the native fields), told after every
	// layout pass.
	followers []LayoutFollower

	focused      *Element
	focusVisible bool
	traps        []*FocusTrap
	textInput    TextInput

	hover     *Element // the deepest element under the pointer
	capture   *Element
	cursor    Cursor
	pointerAt Point

	frameAsked  bool
	layoutAsked bool
	inLayout    bool
	fullLayout  bool
	postLayout  []func()
	pass        uint32
	layoutCtx   LayoutContext // the layout passes' context, reused
	paintCtx    PaintContext  // the paints' context, reused
	anims       []Animation
	stepping    []Animation
	timers      map[*Timer]struct{}
	deferred    []func()
	depth       int
	lastFrame   time.Time
	stats       Stats
}

// WindowOptions configure a Window.
type WindowOptions struct {
	// Title is the window's title (and the semantic tree's root name).
	Title string
	// MinSize is the smallest client size the user can resize the window to.
	MinSize Size
	// Direction is the layout's direction: DirRTL mirrors it (DirAuto counts as DirLTR).
	Direction Direction
	// Palette gives the kit's colours for an environment (light, dark, forced colours).
	Palette func(env Environment) Palette
	// FrameColors gives the frame's colours for an environment; the window sets them at
	// Attach and whenever the environment changes.
	FrameColors func(env Environment) FrameColors
	// OnKey gets the key events the focused element and its ancestors did not use, before
	// the window's own Tab handling: the window's shortcuts.
	OnKey func(e KeyEvent) bool
	// OnText gets the typed text no element used.
	OnText func(e TextEvent) bool
	// OnClose is asked when the user or the system asks to close the window; it returns
	// true to keep the window open (it may close it later with Close).
	OnClose func() bool
	// OnEnvironment hears every change of the environment, after the window took it in.
	OnEnvironment func(env Environment)
	// OnWindowState hears that the window was maximised, minimised or restored.
	OnWindowState func(s WindowState)
	// Now is the clock that input is stamped with; time.Now when nil.
	Now func() time.Time
	// Scheduler runs the window's timers; real timers when nil.
	Scheduler Scheduler
	// NoMotion turns every animation off, as reduced motion does.
	NoMotion bool
}

// Stats are a window's counters since it was made (or since ResetStats): the native
// guards that replace the browser's (check-idle-render) count with them.
type Stats struct {
	Frames        int     // Frame calls
	Layouts       int     // layout passes that laid something out
	BoxLayouts    int     // elements whose content was laid out
	Measures      int     // intrinsic sizes worked out (not from the cache)
	Paints        int     // Paint calls
	PaintedArea   float64 // the area painted, in square DIPs
	Invalidations int     // areas the window asked the host to paint
	FrameRequests int     // frames the window asked the host for
	Animations    int     // animations started
	Timers        int     // timers started
	SemEvents     int     // semantic events sent to the host
}

// hostRef holds a Host for an atomic pointer.
type hostRef struct{ h Host }

// layerRoot is the window's own root element: it lays every layer out over the whole
// window.
type layerRoot struct {
	Element
}

func (l *layerRoot) ContentWidths(*LayoutContext) (min, max DIP) { return 0, 0 }
func (l *layerRoot) ContentHeight(*LayoutContext, DIP) DIP       { return 0 }
func (l *layerRoot) LayoutContent(ctx *LayoutContext, box Rect) {
	for _, c := range l.kids {
		ctx.place(c, box, box.Width())
	}
}

// NewWindow makes a window whose content is content.
func NewWindow(content Widget, opts WindowOptions) *Window {
	w := &Window{
		opts:    opts,
		content: content,
		nodes:   map[NodeID]*Element{},
		virtual: map[NodeID]virtualNodes{},
		timers:  map[*Timer]struct{}{},
		sched:   opts.Scheduler,
		active:  true,
	}
	if w.sched == nil {
		w.sched = realScheduler{}
	}
	w.tree.w = w
	r := &layerRoot{}
	r.Semantics.Role = RoleWindow
	r.self = r
	r.flags |= isBoundary
	w.root = &r.Element
	if content != nil {
		r.Add(content)
	}
	return w
}

// Content is the window's content widget.
func (w *Window) Content() Widget { return w.content }

// Host is the window's host, or nil before Attach and after Detach.
func (w *Window) Host() Host { return w.host }

// Size is the window's client size.
func (w *Window) Size() Size { return w.size }

// Environment is the window's environment.
func (w *Window) Environment() Environment { return w.env }

// WindowState is whether the window is maximised or minimised.
func (w *Window) WindowState() WindowState { return w.state }

// Stats are the window's counters.
func (w *Window) Stats() Stats { return w.stats }

// ResetStats sets the counters back to 0.
func (w *Window) ResetStats() { w.stats = Stats{} }

// Palette is the kit's colours in effect.
func (w *Window) Palette() Palette { return w.palette }

// SetPalette sets the kit's colours and paints the window again.
func (w *Window) SetPalette(p Palette) {
	if p == w.palette {
		return
	}
	w.palette = p
	w.invalidateAll()
}

func (w *Window) scale() Scale { return w.env.Scale }

func (w *Window) rtl() bool { return w.opts.Direction == DirRTL }

// Direction is the layout's direction (WindowOptions.Direction): DirRTL mirrors it, DirAuto
// and DirLTR do not.
func (w *Window) Direction() Direction { return w.opts.Direction }

// PushLayer puts a layer above the others (a dialog with its backdrop, the toasts): it is
// laid out over the whole window, painted on top and hit first.
func (w *Window) PushLayer(l Widget) {
	w.root.Add(l)
}

// RaiseLayer moves a layer above every other layer without taking it out of the window: its
// elements stay attached, keep their state, focus and running motion (CSS's z-index changing
// on a positioned box). The content never moves; a widget that is not a layer is left alone.
func (w *Window) RaiseLayer(l Widget) {
	e := l.Base()
	if e == nil || e.parent != w.root || (w.content != nil && e == w.content.Base()) {
		return
	}
	kids := w.root.kids
	i := slices.Index(kids, e)
	if i < 0 || i == len(kids)-1 {
		return
	}
	copy(kids[i:], kids[i+1:])
	kids[len(kids)-1] = e
	w.invalidateElement(e)
	w.semChildren(w.root)
}

// Layers are the window's layers above its content, bottom first.
func (w *Window) Layers() []Widget {
	var out []Widget
	for _, k := range w.root.kids {
		if w.content != nil && k == w.content.Base() {
			continue
		}
		out = append(out, k.self)
	}
	return out
}

// RemoveLayer takes a layer away.
func (w *Window) RemoveLayer(l Widget) {
	if w.content == nil || l.Base() != w.content.Base() {
		w.root.Remove(l)
	}
}

// Post runs f on the UI thread after the calls in progress there (any goroutine may call
// it); it fails with ErrClosed once the window is closed.
func (w *Window) Post(f func()) error {
	ref := w.poster.Load()
	if ref == nil {
		return ErrClosed
	}
	return ref.h.Post(func() {
		if w.closed {
			return
		}
		w.enter()
		defer w.leave()
		f()
	})
}

// Defer runs f once the call into the window in progress (an event, a frame, a Post) has
// finished: what HTML does in a microtask, as when a closed dialog gives the focus back.
func (w *Window) Defer(f func()) {
	if w.depth == 0 {
		f()
		return
	}
	w.deferred = append(w.deferred, f)
}

// Close closes the window (see Host.Close).
func (w *Window) Close() {
	if w.host != nil {
		w.host.Close()
	}
}

func (w *Window) enter() { w.depth++ }

func (w *Window) leave() {
	if w.depth == 1 {
		for len(w.deferred) > 0 {
			d := w.deferred
			w.deferred = nil
			for _, f := range d {
				f()
			}
		}
		w.flushSemantics()
	}
	w.depth--
}

// ---- Root ---------------------------------------------------------------------------------

// Attach is Root's: it lays the window out for its host and sets the window's title,
// minimum size and frame colours.
func (w *Window) Attach(h Host) {
	w.enter()
	defer w.leave()
	w.host = h
	w.poster.Store(&hostRef{h})
	w.closed = false
	w.size = h.Size()
	w.env = h.Environment()
	w.state = h.WindowState()
	if w.opts.Palette != nil {
		w.palette = w.opts.Palette(w.env)
	}
	if w.opts.Title != "" {
		h.SetTitle(w.opts.Title)
	}
	if w.opts.MinSize != (Size{}) {
		h.SetMinSize(w.opts.MinSize)
	}
	if w.opts.FrameColors != nil {
		h.SetFrameColors(w.opts.FrameColors(w.env))
	}
	w.attach(w.root)
	w.relayoutAll()
	w.layout()
	w.invalidateAll()
}

// Detach is Root's: the window is closed, and every widget releases what it holds.
func (w *Window) Detach() {
	w.enter()
	for t := range w.timers {
		t.Stop()
	}
	w.anims = nil
	w.closed = true
	w.detach(w.root)
	w.deferred = nil
	w.sem = semQueue{}
	w.depth--
	w.host = nil
	w.poster.Store(nil)
}

// Frame is Root's: it advances the motion clock to now and lays out what changed.
func (w *Window) Frame(now time.Time) {
	w.enter()
	defer w.leave()
	w.stats.Frames++
	w.frameAsked = false
	w.lastFrame = now
	w.stepAnimations(now)
	if w.layoutAsked {
		w.layout()
	}
	w.fixFocus()
	if len(w.anims) > 0 {
		w.requestFrame()
	}
}

// fixFocus is HTML's focus fixup, run as a browser runs it when it updates the rendering: a
// focused element that has become disabled (Disabled without DisabledFocusable: a button
// disabled while busy) loses the focus to the window's body, and no ring shows for it. The
// next Tab starts again from the start, as a page's does.
func (w *Window) fixFocus() {
	if f := w.focused; f != nil && f.Disabled && !f.DisabledFocusable {
		w.setFocus(nil)
	}
}

// Paint is Root's: it paints the tree inside dirty.
func (w *Window) Paint(c Canvas, dirty Rect) {
	w.stats.Paints++
	w.stats.PaintedArea += float64(dirty.Width()) * float64(dirty.Height())
	if w.palette.Ground.A > 0 {
		c.FillRect(dirty, w.palette.Ground)
	}
	// The window's paint context is kept, so that a paint allocates nothing; it holds the
	// canvas only while the paint lasts.
	p := &w.paintCtx
	*p = PaintContext{Canvas: c, Scale: w.scale(), Env: w.env, Palette: w.palette, Dirty: dirty, w: w}
	w.paintTree(p, w.root, Point{})
	*p = PaintContext{}
}

// Event is Root's.
func (w *Window) Event(e Event) bool {
	w.enter()
	defer w.leave()
	switch e := e.(type) {
	case PointerEvent:
		return w.pointerEvent(e)
	case WheelEvent:
		return w.wheelEvent(e)
	case KeyEvent:
		return w.keyEvent(e)
	case TextEvent:
		return w.textEvent(e)
	case FocusEvent:
		w.active = e.In
		if w.focused != nil {
			w.invalidateElement(w.focused) // its ring shows only while the window is active
			if r, ok := w.focused.self.(FocusRestorer); ok && e.In {
				r.RestoreFocus() // a native field takes the system's focus back
			}
		}
	case SizeEvent:
		if e.Size != w.size {
			w.size = e.Size
			w.relayoutAll()
		}
	case EnvironmentEvent:
		w.setEnvironment(e.Env)
	case WindowStateEvent:
		w.state = e.State
		if w.opts.OnWindowState != nil {
			w.opts.OnWindowState(e.State)
		}
	case CloseEvent:
		return w.opts.OnClose != nil && w.opts.OnClose()
	}
	return false
}

// FrameHit is Root's: the frame part that the deepest element under the pointer at p, or
// its nearest ancestor, declares. An element's own part comes first, so the window buttons,
// which are buttons, are still the frame's; an interactive element that declares none is
// client (the search field and the other buttons in the title bar). An element that lets
// the pointer through (PassThrough: the search field's magnifier) is not hit.
func (w *Window) FrameHit(p Point) FramePart {
	for e := w.hitTest(p, true); e != nil; e = e.parent {
		if e.Frame != FrameClient {
			return e.Frame
		}
		if e.interactive() {
			return FrameClient
		}
	}
	return FrameClient
}

// Semantics is Root's: the window's semantic tree.
func (w *Window) Semantics() SemanticTree { return &w.tree }

// ---- environment, layout, invalidation -------------------------------------------------

func (w *Window) setEnvironment(env Environment) {
	if env == w.env {
		return
	}
	old := w.env
	w.env = env
	if w.opts.Palette != nil {
		w.palette = w.opts.Palette(env)
	}
	if w.opts.FrameColors != nil && w.host != nil {
		w.host.SetFrameColors(w.opts.FrameColors(env))
	}
	if env.ReducedMotion && !old.ReducedMotion {
		run := w.anims
		w.anims = nil
		for _, a := range run {
			a.Finish()
		}
	}
	w.relayoutAll()
	if w.opts.OnEnvironment != nil {
		w.opts.OnEnvironment(env)
	}
}

// relayoutAll lays the whole tree out again (a new size, scale or text size).
func (w *Window) relayoutAll() {
	var walk func(*Element)
	walk = func(e *Element) {
		e.flags |= needsLayout
		e.cache = measureCache{}
		e.stylePass = 0
		if b, ok := e.self.(*Scrollbar); ok { // [mac scroll] a new scroll bar style
			b.follow(w.env)
		}
		for _, c := range e.kids {
			walk(c)
		}
	}
	walk(w.root)
	w.fullLayout = true
	w.layoutWanted()
}

func (w *Window) layoutWanted() {
	if w.inLayout {
		return // the pass in progress takes it; afterLayout asks again if it did not
	}
	if !w.layoutAsked {
		w.layoutAsked = true
		w.requestFrame()
	}
}

func (w *Window) requestFrame() {
	if w.frameAsked || w.host == nil || w.closed {
		return
	}
	w.frameAsked = true
	w.stats.FrameRequests++
	w.host.RequestFrame()
}

// layout runs a layout pass over what asked for one, and a second one when what waited for
// the first (AfterLayout: a list keeping its cursor in view) changed the layout again.
func (w *Window) layout() {
	w.layoutAsked = false
	w.inLayout = true
	for range 2 {
		dirty := w.root.flags&(needsLayout|childNeedsLayout) != 0
		if !dirty && len(w.postLayout) == 0 {
			break
		}
		if dirty {
			w.pass++
			// The window's context is kept, with its flex items' memory, from pass to pass.
			ctx := &w.layoutCtx
			*ctx = LayoutContext{Scale: w.scale(), Env: w.env, RTL: w.rtl(), win: w, pass: w.pass, full: w.fullLayout,
				containers: ctx.containers[:0], flexMem: ctx.flexMem}
			if w.host != nil {
				ctx.Text = w.host.Resources()
			}
			w.stats.Layouts++
			ctx.place(w.root, RectAt(Point{}, w.size), w.size.W)
			// Between passes the context holds no element, and not the host's resources.
			clear(ctx.containers[:cap(ctx.containers)])
			ctx.Text = nil
			if w.fullLayout {
				w.fullLayout = false
				w.invalidateAll()
			}
		}
		run := w.postLayout
		w.postLayout = nil
		for _, f := range run {
			f()
		}
	}
	w.inLayout = false
	for _, f := range w.followers {
		f.LaidOut() // native fields follow their boxes, and hide with them
	}
	if w.root.flags&(needsLayout|childNeedsLayout) != 0 || len(w.postLayout) > 0 {
		w.layoutWanted()
	}
}

// LayoutFollower is a widget that follows its box after every layout pass of its window: a
// native control the host places over the box (a text field, a multi-line field). The window
// calls LaidOut for every attached follower once each pass is over (after the AfterLayout
// functions), also when the pass laid out something else and the follower's box only moved
// along with it, as a scrolled pane moves what it holds.
type LayoutFollower interface {
	LaidOut()
}

// AfterLayout runs f once the next layout pass is done.
func (w *Window) AfterLayout(f func()) {
	w.postLayout = append(w.postLayout, f)
	w.layoutWanted()
}

func (w *Window) invalidate(r Rect) {
	if w.host == nil || r.Empty() {
		return
	}
	r = r.Intersect(RectAt(Point{}, w.size))
	if r.Empty() {
		return
	}
	w.stats.Invalidations++
	w.host.Invalidate(r)
}

func (w *Window) invalidateAll() { w.invalidate(RectAt(Point{}, w.size)) }

func (w *Window) invalidateElement(e *Element) {
	if r, ok := w.repaintRect(e); ok {
		w.invalidate(r)
	}
}

// repaintRect is the area that invalidating e repaints: its visual rect within its
// ancestors' clips (with a pixel of room for antialiasing); false when none of it shows.
func (w *Window) repaintRect(e *Element) (Rect, bool) {
	if w.host == nil || !e.visible() {
		return Rect{}, false
	}
	r := w.visualRect(e)
	for p := e.parent; p != nil; p = p.parent {
		if p.clips() {
			r = r.Intersect(p.clipRect().Inset(UniformInsets(-1)))
			if r.Empty() {
				return Rect{}, false
			}
		}
	}
	return r, true
}

// paintedNow reports that e is changed while the window paints an area that holds all of
// what e shows: e paints in this pass (after the parent that changed it), so it needs no
// frame of its own.
func (w *Window) paintedNow(e *Element) bool {
	if w.paintCtx.Canvas == nil {
		return false
	}
	r, ok := w.repaintRect(e)
	if !ok {
		return true
	}
	d := w.paintCtx.Dirty
	return r.Min.X >= d.Min.X && r.Min.Y >= d.Min.Y && r.Max.X <= d.Max.X && r.Max.Y <= d.Max.Y
}

// InvalidateRect asks for r (window coordinates) to be painted again.
func (w *Window) InvalidateRect(r Rect) { w.invalidate(r) }

// ---- the tree's attachment ---------------------------------------------------------------

func (w *Window) newID() NodeID {
	w.nextID++
	return w.nextID
}

func (w *Window) attach(e *Element) {
	e.win = w
	if e.self == nil {
		e.self = e
	}
	if e.id == 0 {
		e.id = w.newID()
	}
	w.nodes[e.NodeID()] = e
	e.flags |= needsLayout
	e.cache = measureCache{}
	for _, c := range e.kids {
		w.attach(c)
	}
	if f, ok := e.self.(LayoutFollower); ok {
		w.followers = append(w.followers, f)
	}
	if a, ok := e.self.(Attacher); ok {
		a.Attached()
	}
}

func (w *Window) detach(e *Element) {
	for _, c := range e.kids {
		w.detach(c)
	}
	if f, ok := e.self.(LayoutFollower); ok {
		if i := slices.Index(w.followers, f); i >= 0 {
			w.followers = slices.Delete(w.followers, i, i+1)
		}
	}
	if d, ok := e.self.(Detacher); ok {
		d.Detached()
	}
	if w.focused == e {
		w.focused = nil
		if !w.closed {
			w.semFocus()
		}
	}
	if w.hover == e {
		w.hover = nil
	}
	if w.capture == e {
		w.capture = nil
		if w.host != nil {
			w.host.ReleasePointer()
		}
	}
	if w.nodes[e.NodeID()] == e {
		delete(w.nodes, e.NodeID())
	}
	e.flags &^= isHovered
	e.win = nil
}

// ---- painting -------------------------------------------------------------------------------

func (w *Window) paintTree(p *PaintContext, e *Element, parent Point) {
	if e.hidden() || e.flags&isParked != 0 {
		return
	}
	abs := e.bounds.Add(parent)
	vis := abs
	if o := w.overflow(e); o != (Insets{}) {
		vis = outset(vis, o, 0)
	}
	culled := vis.Intersect(p.Dirty).Empty()
	clips := e.clips()
	if culled && (clips || len(e.kids) == 0) {
		return
	}
	st := e.used
	if st == nil {
		st = &e.Style
	}
	if !culled {
		p.e, p.Origin = e, abs.Min
		w.paintBox(p, st)
		if pt, ok := e.self.(Painter); ok {
			pt.Paint(p)
		}
	}
	if len(e.kids) > 0 {
		var cr Rect
		if clips {
			cr = e.clipRect()
		}
		if !clips || !cr.Intersect(p.Dirty).Empty() {
			if clips {
				w.pushClip(p, e, st, abs, cr)
			}
			for _, c := range e.kids {
				w.paintTree(p, c, abs.Min)
			}
			if clips {
				p.Canvas.Pop()
			}
		}
	}
	p.e, p.Origin = e, abs.Min
	if op, ok := e.self.(OverPainter); ok && !culled {
		op.PaintOver(p)
	}
	if e == w.focused && w.RingShown() {
		p.FocusRing(RRect{Rect: e.bounds.Size().rect(), Radii: w.radii(e, st)}, e.Ring)
	}
}

// pushClip clips e's children: to its padding box, rounded as CSS rounds an overflow clip
// (the outer radii less the border), or to the area a clipper widget names.
func (w *Window) pushClip(p *PaintContext, e *Element, st *Style, abs, cr Rect) {
	rad := w.radii(e, st)
	if _, ok := e.self.(clipper); ok || rad == (Radii{}) || st.Overflow == OverflowClipInline {
		p.Canvas.PushClip(cr)
		return
	}
	outer := RRect{Rect: abs, Radii: rad}.Normalized()
	b := e.border
	inner := RRect{Rect: cr, Radii: Radii{
		TopLeft:     max(outer.Radii.TopLeft-max(b.Left, b.Top), 0),
		TopRight:    max(outer.Radii.TopRight-max(b.Right, b.Top), 0),
		BottomRight: max(outer.Radii.BottomRight-max(b.Right, b.Bottom), 0),
		BottomLeft:  max(outer.Radii.BottomLeft-max(b.Left, b.Bottom), 0),
	}}
	p.Canvas.PushRoundedClip(inner)
}

// RingShown reports whether the focused element shows its focus ring now: the window has
// the keyboard focus and the focus came from the keyboard (CSS :focus-visible).
func (w *Window) RingShown() bool { return w.active && w.focusVisible }

// ---- hit testing ----------------------------------------------------------------------------

// hitTest is the deepest element at p that takes the pointer (pointer) or any (not).
func (w *Window) hitTest(p Point, pointer bool) *Element {
	return w.hit(w.root, p, Point{}, pointer)
}

func (w *Window) hit(e *Element, p, parent Point, pointer bool) *Element {
	if e.hidden() || e.flags&isParked != 0 || e.Inert {
		return nil
	}
	abs := e.bounds.Add(parent)
	if e.clips() && !e.clipRect().Contains(p) {
		if abs.Contains(p) && !(pointer && e.PassThrough) {
			return e
		}
		return nil
	}
	for i := len(e.kids) - 1; i >= 0; i-- {
		if h := w.hit(e.kids[i], p, abs.Min, pointer); h != nil {
			return h
		}
	}
	if abs.Contains(p) && !(pointer && e.PassThrough) && e != w.root {
		return e
	}
	return nil
}

// ElementAt is the deepest element under p that takes the pointer, or nil.
func (w *Window) ElementAt(p Point) *Element { return w.hitTest(p, true) }

// TooltipAt is the tooltip of what is at p: the nearest Tooltip, or the text a truncated
// label cuts, with the area of the element it belongs to (window coordinates).
func (w *Window) TooltipAt(p Point) (text string, area Rect, ok bool) {
	for e := w.hitTest(p, false); e != nil; e = e.parent {
		if e.Tooltip != "" {
			return e.Tooltip, e.WindowBounds(), true
		}
		if t, ok := e.self.(TooltipSource); ok {
			if s := t.TooltipText(); s != "" {
				return s, e.WindowBounds(), true
			}
		}
	}
	return "", Rect{}, false
}

// ---- pointer input --------------------------------------------------------------------------

func (w *Window) pointerEvent(ev PointerEvent) bool {
	w.pointerAt = ev.Pos
	switch ev.Action {
	case PointerLeave:
		if w.capture == nil {
			w.setHover(nil)
		}
		return false
	case PointerDown:
		w.focusVisible = false
	}
	target := w.capture
	if target == nil {
		target = w.hitTest(ev.Pos, true)
	}
	if w.capture == nil && ev.Action != PointerCancel {
		w.setHover(target)
	}
	used := w.dispatchPointer(target, ev)
	switch ev.Action {
	case PointerDown:
		w.focusOnPress(target)
	case PointerUp:
		if ev.Buttons == 0 && w.capture != nil {
			w.capture = nil // the host ends a capture at the last button up by itself
			w.setHover(w.hitTest(ev.Pos, true))
		}
	case PointerCancel:
		w.capture = nil
		w.setHover(w.hitTest(ev.Pos, true))
	}
	return used
}

func (w *Window) dispatchPointer(target *Element, ev PointerEvent) bool {
	in := &PointerInput{PointerEvent: ev, Target: target}
	for e := target; e != nil; e = e.parent {
		if h, ok := e.self.(PointerHandler); ok && e.enabled() {
			in.Local = e.WindowToLocal(ev.Pos)
			if h.HandlePointer(in) {
				return true
			}
		}
	}
	return false
}

// Capture sends every pointer event to e until the last button goes up, or until
// ReleaseCapture (a dragged thumb or column edge).
func (w *Window) Capture(e *Element) {
	if e == nil || e.win != w {
		return
	}
	w.capture = e
	if w.host != nil {
		w.host.CapturePointer()
	}
}

// ReleaseCapture ends Capture.
func (w *Window) ReleaseCapture() {
	if w.capture == nil {
		return
	}
	w.capture = nil
	if w.host != nil {
		w.host.ReleasePointer()
	}
	w.setHover(w.hitTest(w.pointerAt, true))
}

// Captured is the element that has the pointer capture, or nil.
func (w *Window) Captured() *Element { return w.capture }

// PointerPos is the pointer's last position in window coordinates.
func (w *Window) PointerPos() Point { return w.pointerAt }

// setHover makes target and its ancestors the hovered chain (CSS :hover).
func (w *Window) setHover(target *Element) {
	if target == w.hover {
		w.updateCursor()
		return
	}
	in := map[*Element]bool{}
	for e := target; e != nil; e = e.parent {
		in[e] = true
	}
	for e := w.hover; e != nil; e = e.parent {
		if !in[e] && e.flags&isHovered != 0 {
			e.flags &^= isHovered
			if h, ok := e.self.(HoverHandler); ok {
				h.HoverChanged(false)
			}
		}
	}
	for e := target; e != nil; e = e.parent {
		if e.flags&isHovered == 0 {
			e.flags |= isHovered
			if h, ok := e.self.(HoverHandler); ok {
				h.HoverChanged(true)
			}
		}
	}
	w.hover = target
	w.updateCursor()
}

func (w *Window) updateCursor() {
	c := CursorDefault
	for e := w.hover; e != nil; e = e.parent {
		if e.Cursor != CursorDefault {
			c = e.Cursor
			break
		}
	}
	if w.capture != nil && w.capture.Cursor != CursorDefault {
		c = w.capture.Cursor
	}
	if c != w.cursor && w.host != nil {
		w.cursor = c
		w.host.SetCursor(c)
	}
}

// UpdateCursor sets the pointer's shape again after an element changed its Cursor.
func (w *Window) UpdateCursor() { w.updateCursor() }

func (w *Window) wheelEvent(ev WheelEvent) bool {
	target := w.hitTest(ev.Pos, true)
	in := &WheelInput{WheelEvent: ev, Target: target}
	for e := target; e != nil; e = e.parent {
		if h, ok := e.self.(WheelHandler); ok && e.enabled() {
			in.Local = e.WindowToLocal(ev.Pos)
			if h.HandleWheel(in) {
				return true
			}
		}
	}
	return false
}

// ---- keys and text ----------------------------------------------------------------------------

func isModifier(k Key) bool {
	return k == KeyShift || k == KeyControl || k == KeyAlt || k == KeyMeta
}

func (w *Window) keyEvent(ev KeyEvent) bool {
	if ev.Action == KeyDown && !isModifier(ev.Key) && ev.Mods&(ModCtrl|ModAlt|ModMeta) == 0 {
		if !w.focusVisible {
			w.focusVisible = true
			if w.focused != nil {
				w.invalidateElement(w.focused)
			}
		}
	}
	trap := w.topTrap()
	for e := w.focused; e != nil; e = e.parent {
		if h, ok := e.self.(KeyHandler); ok && e.enabled() {
			if h.HandleKey(ev) {
				return true
			}
		}
		if trap != nil && e == trap.node && ev.Action == KeyDown && trap.keyInside(ev) {
			return true
		}
	}
	if trap != nil && ev.Action == KeyDown && trap.keyOutside(ev) {
		return true
	}
	if w.opts.OnKey != nil && w.opts.OnKey(ev) {
		return true
	}
	if ev.Action == KeyDown && ev.Key == KeyTab && ev.Mods&(ModCtrl|ModAlt|ModMeta) == 0 {
		w.tab(ev.Mods&ModShift != 0)
		return true
	}
	return false
}

func (w *Window) textEvent(ev TextEvent) bool {
	for e := w.focused; e != nil; e = e.parent {
		if h, ok := e.self.(TextHandler); ok && e.enabled() {
			if h.HandleText(ev) {
				return true
			}
		}
	}
	return w.opts.OnText != nil && w.opts.OnText(ev)
}
